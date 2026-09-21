package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// This file is the before/after evidence for Fix 5: nothing re-indexed an
// edited file, so the index kept whatever the file looked like when `index`
// last ran and the assistant reasoned about PRE-EDIT code on the very next
// turn. The review reproduced it by editing a constant from `pathPrefix =
// "FILE:"` to `pathPrefix = "path:"` and watching the follow-up turn still be
// served "FILE:". It poisons every subsequent turn in a session.
//
// Run against the pre-fix daemon, TestReindex_AfterApplyServesPostEditContent
// FAILS with the retrieved chunk still holding the old value.

// recordingStore is a VectorStore that keeps chunks in memory so a test can ask
// what the index actually holds, without a real chromem-go DB or embedder.
type recordingStore struct {
	chunks  []Chunk
	deletes []string
}

func (r *recordingStore) Upsert(_ context.Context, chunks []Chunk) error {
	r.chunks = append(r.chunks, chunks...)
	return nil
}

// AllIDs reports what this fake actually holds, rather than an empty list.
// A fake that claims to hold nothing would make pruneOrphanedChunks find no
// orphans and do nothing -- which is the bug it exists to fix, so the fake
// would quietly disable the code under test.
func (r *recordingStore) AllIDs(_ context.Context, _ int) ([]string, error) {
	ids := make([]string, 0, len(r.chunks))
	for _, c := range r.chunks {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

func (r *recordingStore) Query(_ context.Context, _ []float32, k int) ([]Chunk, error) {
	if k < len(r.chunks) {
		return r.chunks[:k], nil
	}
	return r.chunks, nil
}

func (r *recordingStore) DeleteByFilePath(_ context.Context, relPath string) error {
	r.deletes = append(r.deletes, relPath)
	kept := r.chunks[:0]
	for _, c := range r.chunks {
		if c.FilePath != relPath {
			kept = append(kept, c)
		}
	}
	r.chunks = kept
	return nil
}

func (r *recordingStore) Count() int { return len(r.chunks) }

// indexedContent joins everything the store currently holds for one file, which
// is what retrieval would be able to serve for it.
func (r *recordingStore) indexedContent(relPath string) string {
	var b strings.Builder
	for _, c := range r.chunks {
		if c.FilePath == relPath {
			b.WriteString(c.Content)
		}
	}
	return b.String()
}

// serverWithIndex stands up a daemon over root with an in-memory index already
// populated from the file's current content, exactly as `index` would leave it.
func serverWithIndex(t *testing.T, root, relPath string) (*Server, *recordingStore) {
	t.Helper()
	store := &recordingStore{}
	srv := &Server{
		logger:    discardLogger(),
		workspace: root,
		embedder:  NewPlaceholderEmbedder(embedDim),
		store:     store,
	}
	content := readFileString(t, filepath.Join(root, relPath))
	chunks := chunkContent([]byte(content), relPath)
	for i := range chunks {
		chunks[i].Vector = make([]float32, embedDim)
	}
	if err := store.Upsert(context.Background(), chunks); err != nil {
		t.Fatalf("seeding index: %v", err)
	}
	return srv, store
}

// TestReindex_AfterApplyServesPostEditContent is the review's repro.
func TestReindex_AfterApplyServesPostEditContent(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "consts.go", "package main\n\nconst pathPrefix = \"FILE:\"\n")
	srv, store := serverWithIndex(t, root, "consts.go")

	if !strings.Contains(store.indexedContent("consts.go"), `"FILE:"`) {
		t.Fatalf("test bug: index should start with the pre-edit value")
	}

	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{
			FilePath: "consts.go",
			Search:   `const pathPrefix = "FILE:"`,
			Replace:  `const pathPrefix = "path:"`,
		},
	})
	if !resp.Applied {
		t.Fatalf("apply = %+v, want Applied", resp)
	}

	indexed := store.indexedContent("consts.go")
	if strings.Contains(indexed, `"FILE:"`) {
		t.Errorf("STALE INDEX: still serving the pre-edit value; indexed content = %q", indexed)
	}
	if !strings.Contains(indexed, `"path:"`) {
		t.Errorf("indexed content = %q, want the post-edit value", indexed)
	}
}

// TestReindex_FailedApplyDoesNotReindex is the trigger-placement requirement: a
// refused apply left the file untouched (Fix 1 guarantees it), so re-indexing
// there would be pure cost and would make a failure look like a change.
func TestReindex_FailedApplyDoesNotReindex(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "consts.go", "package main\n\nconst pathPrefix = \"FILE:\"\n")
	srv, store := serverWithIndex(t, root, "consts.go")

	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{
			FilePath: "consts.go",
			Search:   `const nothingLikeThis = "absent"`,
			Replace:  `x`,
		},
	})
	if resp.Applied {
		t.Fatalf("apply = %+v, want a refusal", resp)
	}
	if len(store.deletes) != 0 {
		t.Errorf("a refused apply triggered %d index invalidation(s): %v", len(store.deletes), store.deletes)
	}
	if !strings.Contains(store.indexedContent("consts.go"), `"FILE:"`) {
		t.Errorf("index was disturbed by a refused apply")
	}
}

// TestReindex_ShrinkingFileLeavesNoOrphanChunks is why invalidation has to
// delete by FILE rather than rely on upsert-by-ID. Chunk IDs encode their line
// range ("path:1-40"), so a file that shrinks past a chunk boundary would leave
// its former tail behind under an ID the new content never regenerates -- an
// orphan that keeps matching queries with code that no longer exists.
func TestReindex_ShrinkingFileLeavesNoOrphanChunks(t *testing.T) {
	root := realTempDir(t)
	var long strings.Builder
	long.WriteString("package main\n")
	for i := range 200 {
		long.WriteString("// filler line with a distinctive marker DELETEME\n")
		_ = i
	}
	long.WriteString("const keep = 1\n")
	writeTempFile(t, root, "big.go", long.String())

	srv, store := serverWithIndex(t, root, "big.go")
	if len(store.chunks) < 3 {
		t.Fatalf("test bug: expected several chunks, got %d", len(store.chunks))
	}

	// Collapse the file to a fraction of its size.
	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{
			FilePath: "big.go",
			Search:   strings.Repeat("// filler line with a distinctive marker DELETEME\n", 200),
			Replace:  "",
		},
	})
	if !resp.Applied {
		t.Fatalf("apply = %+v, want Applied", resp)
	}

	if got := store.indexedContent("big.go"); strings.Contains(got, "DELETEME") {
		t.Errorf("ORPHAN CHUNKS: deleted content still in the index: %q", got)
	}
	if !strings.Contains(store.indexedContent("big.go"), "const keep = 1") {
		t.Errorf("re-index dropped content that is still in the file")
	}
}

// TestReindex_CreatedFileIsIndexed pins the Fix 7 interaction: a file that did
// not exist until this apply is retrievable straight away, rather than waiting
// for the next full index run.
func TestReindex_CreatedFileIsIndexed(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "seed.go", "package main\n")
	srv, store := serverWithIndex(t, root, "seed.go")

	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "brand_new.go", Search: "", Replace: "package main\n\nfunc Fresh() {}\n"},
	})
	if !resp.Applied {
		t.Fatalf("apply = %+v, want Applied", resp)
	}
	if got := store.indexedContent("brand_new.go"); !strings.Contains(got, "func Fresh()") {
		t.Errorf("created file not indexed; indexed content = %q", got)
	}
}

// TestReindex_NoRetrievalConfiguredIsHarmless confirms a daemon with retrieval
// disabled still applies edits normally — the hook must be inert, not fatal.
func TestReindex_NoRetrievalConfiguredIsHarmless(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.go", "package main\n\nfunc old() {}\n")
	srv := &Server{logger: discardLogger(), workspace: root} // no embedder, no store

	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "f.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	if !resp.Applied {
		t.Fatalf("apply = %+v, want Applied even with retrieval off", resp)
	}
}

// TestReindex_FileThatBecameIneligibleIsDroppedFromIndex covers the delete-only
// path: a file the indexer would now skip must end up with NO chunks, not stale
// ones.
func TestReindex_FileThatBecameIneligibleIsDroppedFromIndex(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "notes.txt", "hello\n")
	srv, store := serverWithIndex(t, root, "notes.txt")
	if len(store.chunks) == 0 {
		t.Fatal("test bug: expected seeded chunks")
	}

	// Gitignore it, then edit it: the edit is legitimate, but the file is no
	// longer indexable, so its chunks must go.
	writeTempFile(t, root, ".gitignore", "notes.txt\n")

	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "notes.txt", Search: "hello", Replace: "goodbye"},
	})
	if !resp.Applied {
		t.Fatalf("apply = %+v, want Applied", resp)
	}
	if got := store.indexedContent("notes.txt"); got != "" {
		t.Errorf("newly-ineligible file still indexed: %q", got)
	}
}
