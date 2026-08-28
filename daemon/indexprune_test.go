package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// pruneFixture builds a workspace and a real pair of stores.
func pruneFixture(t *testing.T) (root string, store *ChromemStore, lex *FTSChunkStore, run func(t *testing.T)) {
	t.Helper()
	root = t.TempDir()
	idx := t.TempDir()
	var err error
	if store, err = NewChromemStore(idx); err != nil {
		t.Fatal(err)
	}
	if lex, err = NewFTSChunkStore(idx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lex.Close() })

	emb := &fakeEmbedder{dim: 8}
	lg := log.New(io.Discard, "", 0)
	run = func(t *testing.T) {
		t.Helper()
		// reuse=false: the most thorough rebuild a user can ask for. If the
		// orphans survive THIS, they survive everything.
		if _, err := buildIndex(context.Background(), root, emb, store, lex, lg, false); err != nil {
			t.Fatal(err)
		}
	}
	return root, store, lex, run
}

func writeLines(t *testing.T, path string, n int) {
	t.Helper()
	s := "package main\n"
	for i := 1; i <= n; i++ {
		s += fmt.Sprintf("// line %d, padded out far enough that windows actually form\n", i)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storeMatchesScan asserts both stores hold EXACTLY what the workspace now
// produces -- not a superset. A superset is the whole bug.
func storeMatchesScan(t *testing.T, root string, store *ChromemStore, lex *FTSChunkStore) {
	t.Helper()
	scan, err := ScanWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, c := range scan.Chunks {
		want[c.ID] = true
	}
	check := func(label string, ids []string) {
		var extra []string
		for _, id := range ids {
			if !want[id] {
				extra = append(extra, id)
			}
		}
		if len(extra) > 0 {
			t.Errorf("%s holds %d chunk(s) the workspace no longer produces: %v", label, len(extra), extra)
		}
		if len(ids) < len(want) {
			t.Errorf("%s holds %d chunk(s) but the workspace produces %d -- pruning removed too much", label, len(ids), len(want))
		}
	}
	vIDs, err := store.AllIDs(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	check("the vector store", vIDs)
	lIDs, err := lex.AllIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check("the lexical store", lIDs)
}

// MEASURED BEFORE THE FIX: the deleted file's 4 chunks all survived a full
// rebuild. This is the serious one, and not only a quality problem -- a user
// who deletes a file and re-indexes still had its full text in the store,
// retrievable, and eligible to be sent to the model.
func TestReindexingForgetsADeletedFile(t *testing.T) {
	root, store, lex, run := pruneFixture(t)
	writeLines(t, filepath.Join(root, "gone.go"), 120)
	writeLines(t, filepath.Join(root, "stays.go"), 50)
	run(t)
	if store.Count() == 0 {
		t.Fatal("nothing indexed, so this test would pass vacuously")
	}

	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	run(t)

	storeMatchesScan(t, root, store, lex)
	for _, id := range mustIDs(t, store) {
		if filePathOfChunkID(id) == "gone.go" {
			t.Errorf("a chunk of the deleted file is still stored and still retrievable: %s", id)
		}
	}
}

// MEASURED BEFORE THE FIX: 200 lines -> 30 lines took the store from 7 chunks
// to 8. It added the new chunk and kept all seven stale ones, whose IDs name
// line ranges the file no longer has.
func TestReindexingForgetsTheTailOfAShrunkFile(t *testing.T) {
	root, store, lex, run := pruneFixture(t)
	p := filepath.Join(root, "shrink.go")
	writeLines(t, p, 200)
	run(t)
	before := store.Count()

	writeLines(t, p, 30)
	run(t)

	if store.Count() >= before {
		t.Errorf("the file shrank from 200 lines to 30 and the store went %d -> %d; it must shrink too",
			before, store.Count())
	}
	storeMatchesScan(t, root, store, lex)
}

// The chunker-change case, simulated exactly: same file, same bytes, a chunk
// present under an ID this chunker never emits. This is what an AST-aware
// chunker would leave behind across every file at once.
func TestReindexingForgetsChunksTheChunkerNoLongerEmits(t *testing.T) {
	root, store, lex, run := pruneFixture(t)
	writeLines(t, filepath.Join(root, "big.go"), 120)
	run(t)
	produced := store.Count()

	ghost := Chunk{
		ID: "big.go:7-59", FilePath: "big.go", StartLine: 7, EndLine: 59,
		Content: "STALE CONTENT FROM AN OLDER CHUNKER", Class: FileClassCode,
		Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0},
	}
	if err := store.Upsert(context.Background(), []Chunk{ghost}); err != nil {
		t.Fatal(err)
	}
	if err := lex.Upsert(context.Background(), []Chunk{ghost}); err != nil {
		t.Fatal(err)
	}
	if store.Count() != produced+1 {
		t.Fatalf("the fixture did not plant its ghost (count %d, expected %d)", store.Count(), produced+1)
	}

	run(t)

	if got, ok := store.Existing(context.Background(), ghost.ID); ok {
		t.Errorf("a chunk under an ID this chunker never emits survived a full rebuild: %q", got.Content)
	}
	storeMatchesScan(t, root, store, lex)
}

// The steady state must stay free. carriedNeedingWrite exists because
// rewriting rows that were already correct cost "~1.9s of a ~2s rebuild"; a
// prune that deletes and rewrites everything each time would hand that back.
func TestReindexingAnUnchangedWorkspacePrunesNothing(t *testing.T) {
	root, store, lex, run := pruneFixture(t)
	writeLines(t, filepath.Join(root, "a.go"), 90)
	writeLines(t, filepath.Join(root, "b.go"), 45)
	run(t)
	before := mustIDs(t, store)

	var pruned int
	lg := log.New(writerFunc(func(p []byte) (int, error) {
		pruned++
		return len(p), nil
	}), "", 0)
	if _, err := buildIndex(context.Background(), root, &fakeEmbedder{dim: 8}, store, lex, lg, true); err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("nothing indexed, so this proves nothing")
	}
	if got := len(mustIDs(t, store)); got != len(before) {
		t.Errorf("an unchanged rebuild changed the store: %d -> %d chunks", len(before), got)
	}
	storeMatchesScan(t, root, store, lex)
}

// THE REUSE PATH, which is where deleting is only half a fix.
//
// Every test above rebuilds with reuse=false, and carryOverUnchanged returns an
// all-false inSync in that mode -- so every surviving chunk is rewritten
// whether or not pruneOrphanedChunks clears its flag, and the clearing is never
// exercised. Found by neutering: disabling that loop failed nothing.
//
// With reuse=true it is load-bearing. carriedNeedingWrite SKIPS a chunk that
// both stores already hold, and an unchanged file's chunks are exactly that --
// right up until the moment their file is deleted wholesale to remove an orphan
// sitting beside them. Delete without clearing the flag and the prune becomes
// data loss: the orphan goes, and so do its innocent neighbours, permanently.
func TestPruningUnderReuseRestoresTheChunksItHadToDeleteAlongside(t *testing.T) {
	root, store, lex, _ := pruneFixture(t)
	writeLines(t, filepath.Join(root, "big.go"), 120)

	emb := &fakeEmbedder{dim: 8}
	lg := log.New(io.Discard, "", 0)
	ctx := context.Background()
	rebuild := func(reuse bool) {
		t.Helper()
		if _, err := buildIndex(ctx, root, emb, store, lex, lg, reuse); err != nil {
			t.Fatal(err)
		}
	}
	rebuild(false)
	healthy := mustIDs(t, store)
	if len(healthy) < 3 {
		t.Fatalf("fixture produced only %d chunks; not enough to lose neighbours", len(healthy))
	}

	// An orphan beside chunks that are unchanged and therefore in sync.
	ghost := Chunk{
		ID: "big.go:7-59", FilePath: "big.go", StartLine: 7, EndLine: 59,
		Content: "STALE CONTENT FROM AN OLDER CHUNKER", Class: FileClassCode,
		Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0},
	}
	if err := store.Upsert(ctx, []Chunk{ghost}); err != nil {
		t.Fatal(err)
	}
	if err := lex.Upsert(ctx, []Chunk{ghost}); err != nil {
		t.Fatal(err)
	}

	// reuse=true: the mode where inSync is actually populated.
	rebuild(true)

	if _, ok := store.Existing(ctx, ghost.ID); ok {
		t.Error("the orphan survived a reuse rebuild")
	}
	storeMatchesScan(t, root, store, lex)
	if got := len(mustIDs(t, store)); got != len(healthy) {
		t.Errorf("the file had %d real chunks before the prune and %d after: deleting the orphan "+
			"took its neighbours with it and they were never written back", len(healthy), got)
	}
}

func mustIDs(t *testing.T, store *ChromemStore) []string {
	t.Helper()
	ids, err := store.AllIDs(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}
