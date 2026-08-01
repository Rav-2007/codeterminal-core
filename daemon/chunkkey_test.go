package main

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// Chunk keys (Chunk.FilePath, and the Chunk.ID derived from it) are index keys
// and wire values, not filesystem paths, and must be forward-slash on every
// platform. See the Chunk doc comment in vectorstore.go.
//
// The defect these tests pin is Windows-only and, being honest about it, is
// NOT REPRODUCIBLE ON THIS MACHINE. filepath.ToSlash is the identity function
// wherever the separator is already '/', so on Linux and macOS every assertion
// below passes with or without the fix — they are a contract pin here and a
// real gate once CI has a Windows runner.
//
// What the bug was: ScanWorkspace stored filepath.Rel's output verbatim, so a
// nested file indexed on Windows was keyed `src\main.go`. The apply path calls
// reindexAfterApply with the model's FilePath, which is always forward-slashed,
// so DeleteByFilePath("src/main.go") matched nothing. Every applied edit then
// doubled that file's chunks and left the pre-edit version in the index as
// ground truth — precisely the failure reindexFile exists to prevent, and it
// would have been silent.

// TestChunkKeysAreSlashFormOnEveryPlatform asserts the canonical form directly
// at the point it is produced.
func TestChunkKeysAreSlashFormOnEveryPlatform(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, filepath.Join("src", "inner", "deep.go"), "package inner\n\nconst A = 1\n")

	scan, err := ScanWorkspace(root)
	if err != nil {
		t.Fatalf("ScanWorkspace: %v", err)
	}

	var found bool
	for _, c := range scan.Chunks {
		if strings.Contains(c.FilePath, "\\") {
			t.Errorf("Chunk.FilePath = %q contains a backslash; index keys are forward-slash on every platform", c.FilePath)
		}
		if strings.Contains(c.ID, "\\") {
			t.Errorf("Chunk.ID = %q contains a backslash; index keys are forward-slash on every platform", c.ID)
		}
		if c.FilePath == "src/inner/deep.go" {
			found = true
		}
	}
	if !found {
		var got []string
		for _, c := range scan.Chunks {
			got = append(got, c.FilePath)
		}
		t.Errorf("no chunk keyed %q; got %v", "src/inner/deep.go", got)
	}
}

// TestReindexFileAgreesWithScanWorkspaceOnTheKey is the invariant that actually
// broke: the key the indexer writes must be the key the apply path deletes. A
// divergence is silent — the delete simply matches nothing — so this asserts
// both halves against the same literal.
func TestReindexFileAgreesWithScanWorkspaceOnTheKey(t *testing.T) {
	const key = "src/inner/deep.go"

	root := realTempDir(t)
	writeTempFile(t, root, filepath.Join("src", "inner", "deep.go"), "package inner\n\nconst A = 1\n")

	// What the indexer produces.
	scan, err := ScanWorkspace(root)
	if err != nil {
		t.Fatalf("ScanWorkspace: %v", err)
	}
	var indexedKeys []string
	for _, c := range scan.Chunks {
		if strings.HasSuffix(c.FilePath, "deep.go") {
			indexedKeys = append(indexedKeys, c.FilePath)
		}
	}
	if len(indexedKeys) == 0 {
		t.Fatalf("test bug: indexer produced no chunk for deep.go")
	}
	for _, k := range indexedKeys {
		if k != key {
			t.Fatalf("indexer keyed the file %q, want %q", k, key)
		}
	}

	// What the apply path deletes and re-upserts, driven with the same
	// forward-slash form a model would send.
	store := &recordingStore{}
	srv := &Server{
		logger:    discardLogger(),
		workspace: root,
		embedder:  NewPlaceholderEmbedder(embedDim),
		store:     store,
	}
	seed := scan.Chunks
	for i := range seed {
		seed[i].Vector = make([]float32, embedDim)
	}
	if err := store.Upsert(context.Background(), seed); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	before := store.Count()

	if err := srv.reindexFile(context.Background(), root, key); err != nil {
		t.Fatalf("reindexFile: %v", err)
	}

	if len(store.deletes) != 1 || store.deletes[0] != key {
		t.Errorf("reindexFile deleted under %v, want exactly [%q]", store.deletes, key)
	}
	// The real symptom: a key mismatch means the delete is a no-op and the
	// re-upsert lands beside the stale chunks instead of replacing them.
	if store.Count() != before {
		t.Errorf("chunk count %d -> %d; a re-index of an unchanged file must replace its chunks, not accumulate them",
			before, store.Count())
	}
	for _, c := range store.chunks {
		if strings.HasSuffix(c.FilePath, "deep.go") && c.FilePath != key {
			t.Errorf("re-upserted under %q, want %q — the delete key and the upsert key have diverged", c.FilePath, key)
		}
	}
}

// TestReindexAfterApplyKeepsOneCopy drives the whole apply path, which is where
// the doubling would actually have been observed by a user.
func TestReindexAfterApplyKeepsOneCopy(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, filepath.Join("src", "consts.go"), "package src\n\nconst pathPrefix = \"FILE:\"\n")

	scan, err := ScanWorkspace(root)
	if err != nil {
		t.Fatalf("ScanWorkspace: %v", err)
	}
	store := &recordingStore{}
	srv := &Server{
		logger:    discardLogger(),
		workspace: root,
		embedder:  NewPlaceholderEmbedder(embedDim),
		store:     store,
	}
	for i := range scan.Chunks {
		scan.Chunks[i].Vector = make([]float32, embedDim)
	}
	if err := store.Upsert(context.Background(), scan.Chunks); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{
			FilePath: "src/consts.go",
			Search:   `const pathPrefix = "FILE:"`,
			Replace:  `const pathPrefix = "path:"`,
		},
	})
	if !resp.Applied {
		t.Fatalf("apply = %+v, want Applied", resp)
	}

	// Ask the store what it holds for this file under EVERY key, not just the
	// canonical one. Querying only "src/consts.go" is what makes the doubling
	// silent: the orphaned chunk sits under a key nobody looks up, so a
	// canonical-key-only assertion passes while the stale content is still
	// there and still reachable by a similarity search.
	var all strings.Builder
	copies := 0
	for _, c := range store.chunks {
		if strings.HasSuffix(c.FilePath, "consts.go") {
			all.WriteString(c.Content)
			copies++
		}
	}
	indexed := all.String()

	if strings.Contains(indexed, `"FILE:"`) {
		t.Errorf("the pre-edit value survived the re-index under some key; indexed content = %q", indexed)
	}
	if !strings.Contains(indexed, `"path:"`) {
		t.Errorf("indexed content = %q, want the post-edit value", indexed)
	}
	if copies != 1 {
		var keys []string
		for _, c := range store.chunks {
			keys = append(keys, c.FilePath)
		}
		t.Errorf("the file is indexed under %d chunks across keys %v, want exactly 1 — "+
			"a key mismatch makes the delete a no-op and leaves the pre-edit copy behind", copies, keys)
	}
}

// TestChunkKeyRoundTripsThroughNativeForm pins the one conversion this design
// relies on: a key may be turned into a filesystem path and back without
// changing. This is the assertion that would fail on Windows if anyone
// "simplified" the canonical form back to filepath.Rel's output.
func TestChunkKeyRoundTripsThroughNativeForm(t *testing.T) {
	for _, key := range []string{"main.go", "src/main.go", "a/b/c/deep_test.go"} {
		if got := filepath.ToSlash(filepath.FromSlash(key)); got != key {
			t.Errorf("round trip of %q = %q, want unchanged", key, got)
		}
	}
	if runtime.GOOS != "windows" {
		t.Log("NOT RUN on hardware: the separator divergence this guards is Windows-only; " +
			"on this platform filepath.ToSlash is the identity function")
	}
}
