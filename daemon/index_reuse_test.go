package main

// Incremental indexing: buildIndex carries a chunk's embedding over from the
// previous build when the text under that id is byte-identical, so a re-index
// pays the model only for what actually changed.
//
// The measurement that motivated it (this repo's own tree, 377 files / 3,304
// chunks, real BGE helper): a full re-index took 2m15s and re-embedded every
// chunk. With carry-over it takes 0.86s. That is only sound if the carry-over
// is exact, which is what these tests pin -- every one of them is a way the
// reuse could serve a vector for code that is no longer there.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// embedCount totals the texts an embedder was actually asked for.
func embedCount(c *callCountingEmbedder) int {
	total := 0
	for _, n := range c.callSizes {
		total += n
	}
	return total
}

// reindex runs a second buildIndex over the same dir and store with reuse on.
func reindex(t *testing.T, dir string, store VectorStore, lex LexicalStore) *callCountingEmbedder {
	t.Helper()
	counting := &callCountingEmbedder{Embedder: NewPlaceholderEmbedder(embedDim)}
	if _, err := buildIndex(context.Background(), dir, counting, store, lex, discardLogger(), true); err != nil {
		t.Fatalf("re-index: %v", err)
	}
	return counting
}

func firstIndex(t *testing.T, dir string) (*ChromemStore, *FTSChunkStore) {
	t.Helper()
	indexDir := filepath.Join(dir, indexDirName)
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	lex, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lex.Close() })
	if _, err := buildIndex(context.Background(), dir, NewPlaceholderEmbedder(embedDim),
		store, lex, discardLogger(), false); err != nil {
		t.Fatalf("first index: %v", err)
	}
	return store, lex
}

// The whole point: a re-index of an untouched tree must call the model zero
// times.
func TestReindexOfUnchangedTreeEmbedsNothing(t *testing.T) {
	dir := t.TempDir()
	writeManyTinyFiles(t, dir, 12)
	store, lex := firstIndex(t, dir)

	counting := reindex(t, dir, store, lex)

	if n := embedCount(counting); n != 0 {
		t.Errorf("a re-index of an unchanged tree embedded %d chunk(s), want 0", n)
	}
	if store.Count() != 12 {
		t.Errorf("store holds %d chunk(s) after re-index, want 12", store.Count())
	}
}

// THE LOAD-BEARING CASE. A chunk id encodes a line RANGE, so editing a file in
// place without changing its length leaves the id identical while the text
// underneath it changes. Carrying the old vector over on the strength of the id
// alone would serve the embedding of code that no longer exists -- silently,
// and forever, since every later re-index would make the same judgement.
func TestReindexReEmbedsChangedContentUnderTheSameChunkID(t *testing.T) {
	dir := t.TempDir()
	writeManyTinyFiles(t, dir, 12)
	store, lex := firstIndex(t, dir)

	// writeManyTinyFiles writes "package p\n\nfunc F3() {}\n" into file003.go, so
	// F3 -> G3 is a same-length in-place edit: identical line count, identical
	// chunk id, different text. Exactly the shape an id-only reuse check misses.
	target := filepath.Join(dir, "file003.go")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	after := strings.Replace(string(before), "func F3()", "func G3()", 1)
	if after == string(before) {
		t.Fatalf("premise broken: the rewrite changed nothing (content was %q)", before)
	}
	if len(after) != len(before) {
		t.Fatalf("premise broken: rewrite changed length %d -> %d", len(before), len(after))
	}
	if err := os.WriteFile(target, []byte(after), 0600); err != nil {
		t.Fatal(err)
	}

	counting := reindex(t, dir, store, lex)

	if n := embedCount(counting); n != 1 {
		t.Errorf("edited chunk: %d chunk(s) re-embedded, want exactly 1", n)
	}

	// And the stored text must be the NEW text, not the carried-over old one.
	// The chunk id is looked up from the scan rather than assumed.
	scan, err := ScanWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	var checked bool
	for _, c := range scan.Chunks {
		if c.FilePath != "file003.go" {
			continue
		}
		stored, found := store.Existing(context.Background(), c.ID)
		if !found {
			t.Fatalf("the edited chunk %s is missing from the store entirely", c.ID)
		}
		if !strings.Contains(stored.Content, "func G3()") {
			t.Errorf("the store still holds pre-edit text for %s: %q", c.ID, stored.Content)
		}
		checked = true
	}
	if !checked {
		t.Fatal("premise broken: the edited file produced no chunks")
	}
}

// A file deleted between builds must not have its chunks carried over as if
// they were still there.
func TestReindexDoesNotResurrectDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	writeManyTinyFiles(t, dir, 12)
	store, lex := firstIndex(t, dir)

	if err := os.Remove(filepath.Join(dir, "file005.go")); err != nil {
		t.Fatal(err)
	}
	counting := reindex(t, dir, store, lex)

	if n := embedCount(counting); n != 0 {
		t.Errorf("deleting a file caused %d re-embed(s), want 0", n)
	}
	// The scan no longer produces that chunk, so nothing re-upserts it. What it
	// must NOT do is claim the deleted file is still part of this build.
	hits, err := lex.Search(context.Background(), "file005", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.FilePath == "file005.go" {
			t.Errorf("a deleted file is still served by lexical search: %s", h.FilePath)
		}
	}
}

// reuse=false is how indexWorkspace says "the vectors on disk came from a
// different embedder". Nothing may be carried over on that path, however
// identical the text looks.
func TestReuseDisabledEmbedsEverything(t *testing.T) {
	dir := t.TempDir()
	writeManyTinyFiles(t, dir, 12)
	store, lex := firstIndex(t, dir)

	counting := &callCountingEmbedder{Embedder: NewPlaceholderEmbedder(embedDim)}
	if _, err := buildIndex(context.Background(), dir, counting, store, lex, discardLogger(), false); err != nil {
		t.Fatal(err)
	}

	if n := embedCount(counting); n != 12 {
		t.Errorf("with reuse disabled, %d chunk(s) were embedded, want all 12", n)
	}
}

// A chunk the vector store has but the lexical store does not must still be
// WRITTEN, not skipped -- otherwise a build that ran while lexical.db was
// unavailable leaves those chunks permanently unfindable by symbol.
func TestReindexRewritesChunksMissingFromTheLexicalStore(t *testing.T) {
	dir := t.TempDir()
	writeManyTinyFiles(t, dir, 12)

	indexDir := filepath.Join(dir, indexDirName)
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	// First build with NO lexical tier: the vector store is populated, the
	// lexical one does not exist yet. This is the divergence the check exists for.
	if _, err := buildIndex(context.Background(), dir, NewPlaceholderEmbedder(embedDim),
		store, nil, discardLogger(), false); err != nil {
		t.Fatal(err)
	}

	lex, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lex.Close() }()

	counting := reindex(t, dir, store, lex)

	// No model call is needed -- the text is unchanged -- but the rows must land.
	if n := embedCount(counting); n != 0 {
		t.Errorf("%d chunk(s) were re-embedded, want 0 (the text did not change)", n)
	}

	// Asserted against the lexical store's own membership rather than a search,
	// because these fixture files contain "func F7() {}" and not their own
	// filename -- a search for the wrong string would pass this test for the
	// wrong reason.
	scan, err := ScanWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Chunks) == 0 {
		t.Fatal("premise broken: no chunks scanned")
	}
	missing := 0
	for _, c := range scan.Chunks {
		if !lex.Has(context.Background(), c.ID) {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d/%d chunk(s) present in the vector store but absent from the lexical store were "+
			"skipped rather than written, leaving them unfindable by lexical search", missing, len(scan.Chunks))
	}
}
