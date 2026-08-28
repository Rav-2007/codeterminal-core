package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexing_SecretsNeverStored proves the secret-skip boundary end to
// end: it runs the real indexing pipeline (ScanWorkspace -> embed -> Upsert)
// against a workspace containing a real .env and a fake key.pem, then reads
// back what actually landed in the persisted store — not the logs — and
// asserts the secret content is absent and no chunk's FilePath ends in .env
// or .pem.
func TestIndexing_SecretsNeverStored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "SECRET_TOKEN=abc123\n")
	writeFile(t, filepath.Join(dir, "key.pem"), "-----BEGIN PRIVATE KEY-----\nSECRET_TOKEN=abc123\n-----END PRIVATE KEY-----\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")

	store, err := NewChromemStore(filepath.Join(dir, indexDirName))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	embedder := NewPlaceholderEmbedder(embedDim)
	ctx := context.Background()

	scan, err := buildIndex(ctx, dir, embedder, store, nil, discardLogger(), false)
	if err != nil {
		t.Fatalf("buildIndex: %v", err)
	}

	// Check what was scanned, pre-store.
	for _, c := range scan.Chunks {
		if strings.Contains(c.Content, "SECRET_TOKEN") {
			t.Fatalf("secret content in scanned chunk %s: %q", c.ID, c.Content)
		}
		if strings.HasSuffix(c.FilePath, ".env") || strings.HasSuffix(c.FilePath, ".pem") {
			t.Fatalf("a secret file was scanned into a chunk: %s", c.FilePath)
		}
	}

	// Check what's actually persisted, by querying the store broadly enough
	// to see every stored document.
	queryVec, err := embedder.Embed(ctx, []string{"main"})
	if err != nil {
		t.Fatalf("embedding query: %v", err)
	}
	stored, err := store.Query(ctx, queryVec[0], 100)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("expected at least one stored chunk (main.go)")
	}
	for _, c := range stored {
		if strings.Contains(c.Content, "SECRET_TOKEN") {
			t.Fatalf("secret content persisted in stored chunk %s: %q", c.ID, c.Content)
		}
		if strings.HasSuffix(c.FilePath, ".env") || strings.HasSuffix(c.FilePath, ".pem") {
			t.Fatalf("a secret file was persisted to the store: %s", c.FilePath)
		}
	}
}

// TestIndexing_CodeterminalPrunedAndGitignoreAppendedIdempotently proves
// requirement 2: .codeterminal/ is pruned from indexing (never indexes its
// own index directory) and is appended to the workspace .gitignore exactly
// once even across repeated runs.
func TestIndexing_CodeterminalPrunedAndGitignoreAppendedIdempotently(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

	runOnce := func() *ScanResult {
		store, err := NewChromemStore(filepath.Join(dir, indexDirName))
		if err != nil {
			t.Fatalf("NewChromemStore: %v", err)
		}
		scan, err := buildIndex(context.Background(), dir, NewPlaceholderEmbedder(embedDim), store, nil, discardLogger(), false)
		if err != nil {
			t.Fatalf("buildIndex: %v", err)
		}
		if err := ensureGitignoreEntry(dir, gitignoreEntry); err != nil {
			t.Fatalf("ensureGitignoreEntry: %v", err)
		}
		return scan
	}

	scan1 := runOnce()
	for _, c := range scan1.Chunks {
		if strings.HasPrefix(c.FilePath, ".codeterminal") {
			t.Fatalf("run 1: index directory was itself indexed: %s", c.FilePath)
		}
	}

	// Run again: .codeterminal/index now has real content on disk (gob
	// files from run 1). This is the sharper version of the same assertion
	// — the prune must hold even once there's something in there to prune.
	scan2 := runOnce()
	for _, c := range scan2.Chunks {
		if strings.HasPrefix(c.FilePath, ".codeterminal") {
			t.Fatalf("run 2: index directory was itself indexed: %s", c.FilePath)
		}
	}
	if scan2.Skipped[SkipIgnoredDir] == 0 {
		t.Error("run 2: expected .codeterminal to be counted as a pruned ignored_dir")
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("reading .gitignore: %v", err)
	}
	count := strings.Count(string(data), gitignoreEntry)
	if count != 1 {
		t.Fatalf(".codeterminal/ appears %d time(s) in .gitignore after two index runs, want exactly 1", count)
	}
}

func TestEnsureGitignoreEntry_CreatesFileIfAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGitignoreEntry(dir, gitignoreEntry); err != nil {
		t.Fatalf("ensureGitignoreEntry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("reading .gitignore: %v", err)
	}
	if strings.TrimSpace(string(data)) != gitignoreEntry {
		t.Errorf(".gitignore content = %q, want just %q", string(data), gitignoreEntry)
	}
}

func TestEnsureGitignoreEntry_AppendsWithoutDuplicating(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")

	if err := ensureGitignoreEntry(dir, gitignoreEntry); err != nil {
		t.Fatalf("ensureGitignoreEntry (1st): %v", err)
	}
	if err := ensureGitignoreEntry(dir, gitignoreEntry); err != nil {
		t.Fatalf("ensureGitignoreEntry (2nd): %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("reading .gitignore: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "*.log") {
		t.Error("existing .gitignore content was clobbered")
	}
	if strings.Count(content, gitignoreEntry) != 1 {
		t.Fatalf(".codeterminal/ appears %d time(s), want exactly 1: %q", strings.Count(content, gitignoreEntry), content)
	}
}

// TestBuildIndex_AcceptsInjectedEmbedder proves Embedder-interface
// swappability: buildIndex takes the interface, so a fake with a different
// dimension than the placeholder can be injected in a test without touching
// PlaceholderEmbedder at all.
func TestBuildIndex_AcceptsInjectedEmbedder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n")

	store, err := NewChromemStore(filepath.Join(dir, indexDirName))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	fake := &fakeEmbedder{dim: 8}
	scan, err := buildIndex(context.Background(), dir, fake, store, nil, discardLogger(), false)
	if err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	if len(scan.Chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(scan.Chunks))
	}
	if len(scan.Chunks[0].Vector) != 8 {
		t.Fatalf("Vector len = %d, want 8 (from the fake embedder, not the 384-dim placeholder)", len(scan.Chunks[0].Vector))
	}
	if scan.Chunks[0].Vector[0] != 1 {
		t.Fatalf("Vector[0] = %v, want 1 (the fake's fixed output, proving it — not the placeholder — was used)", scan.Chunks[0].Vector[0])
	}
}

// TestRetrieveTopK_AcceptsInjectedEmbedder mirrors the above for the
// retrieval path.
func TestRetrieveTopK_AcceptsInjectedEmbedder(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChromemStore(filepath.Join(dir, indexDirName))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	fake := &fakeEmbedder{dim: 8}
	if err := store.Upsert(context.Background(), []Chunk{
		{ID: "a.go:1-1", FilePath: "a.go", StartLine: 1, EndLine: 1, Content: "x", Vector: unitVec(8, 0)},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := retrieveTopK(context.Background(), "anything", 5, fake, store, nil, true)
	if err != nil {
		t.Fatalf("retrieveTopK: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}

// callCountingEmbedder wraps an Embedder and records how many texts each
// Embed call received, so a test can prove buildIndex actually split a large
// chunk set into multiple bounded calls instead of one big one.
type callCountingEmbedder struct {
	Embedder
	callSizes []int
}

func (c *callCountingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c.callSizes = append(c.callSizes, len(texts))
	return c.Embedder.Embed(ctx, texts)
}

// writeManyTinyFiles writes n distinct single-chunk Go files (well under
// chunker.go's 40-line window) into dir, named file000.go, file001.go, ...
// so ScanWorkspace(dir) produces exactly n chunks.
func writeManyTinyFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("file%03d.go", i)), fmt.Sprintf("package p\n\nfunc F%d() {}\n", i))
	}
}

// TestBuildIndex_EmbedsInBatchesNotOneCall proves the actual fix: a chunk
// count above indexEmbedBatchSize is split across multiple Embed calls, each
// no larger than the batch size, rather than one call carrying every chunk
// (which is what exceeded the helper's fixed per-call timeout on the real
// repo).
func TestBuildIndex_EmbedsInBatchesNotOneCall(t *testing.T) {
	dir := t.TempDir()
	const numFiles = indexEmbedBatchSize + 5 // forces exactly 2 batches
	writeManyTinyFiles(t, dir, numFiles)

	store, err := NewChromemStore(filepath.Join(dir, indexDirName))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	counting := &callCountingEmbedder{Embedder: NewPlaceholderEmbedder(embedDim)}

	scan, err := buildIndex(context.Background(), dir, counting, store, nil, discardLogger(), false)
	if err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	if len(scan.Chunks) != numFiles {
		t.Fatalf("got %d chunks, want %d", len(scan.Chunks), numFiles)
	}

	if len(counting.callSizes) != 2 {
		t.Fatalf("Embed was called %d time(s), want exactly 2 (a %d-chunk set batched at %d/call)", len(counting.callSizes), numFiles, indexEmbedBatchSize)
	}
	for i, size := range counting.callSizes {
		if size > indexEmbedBatchSize {
			t.Errorf("call %d embedded %d texts, want at most %d", i, size, indexEmbedBatchSize)
		}
	}
	total := 0
	for _, size := range counting.callSizes {
		total += size
	}
	if total != numFiles {
		t.Fatalf("Embed calls covered %d texts total, want %d", total, numFiles)
	}

	if store.Count() != numFiles {
		t.Fatalf("store.Count() = %d, want %d (every batch's chunks must still be persisted)", store.Count(), numFiles)
	}
}

// failingAfterNEmbedder wraps an Embedder and fails starting on its (n+1)th
// Embed call, simulating a batch failing partway through a multi-batch
// index run.
type failingAfterNEmbedder struct {
	Embedder
	callsBeforeFailure int
	calls              int
}

func (f *failingAfterNEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.calls > f.callsBeforeFailure {
		return nil, fmt.Errorf("simulated embedding failure on call %d", f.calls)
	}
	return f.Embedder.Embed(ctx, texts)
}

// TestIndexWorkspace_BatchFailureLeavesNoValidStampedIndex is the KEY safety
// test: a batch failing partway through indexWorkspace must not leave behind
// anything that later passes as a valid, complete index. It proves both
// halves of that guarantee — the error names the failing batch, and a fresh
// checkEmbedderStamp call against the same indexDir (exactly what a real
// `retrieve` or daemon startup would do) refuses it — even though the first
// batch's chunks are already durably persisted in the store.
func TestIndexWorkspace_BatchFailureLeavesNoValidStampedIndex(t *testing.T) {
	dir := t.TempDir()
	const numFiles = indexEmbedBatchSize + 5 // forces exactly 2 batches
	writeManyTinyFiles(t, dir, numFiles)

	indexDir := filepath.Join(dir, indexDirName)
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	base := NewPlaceholderEmbedder(embedDim)
	failing := &failingAfterNEmbedder{Embedder: base, callsBeforeFailure: 1} // batch 1 succeeds, batch 2 fails

	_, err = indexWorkspace(context.Background(), indexDir, dir, failing, store, nil, discardLogger())
	if err == nil {
		t.Fatal("expected an error from a mid-index batch failure, got nil")
	}
	if !strings.Contains(err.Error(), "batch 2/2") {
		t.Fatalf("error = %q, want it to identify the failing batch (batch 2/2)", err.Error())
	}

	if store.Count() != indexEmbedBatchSize {
		t.Fatalf("store.Count() = %d, want %d (only the first, successful batch should be persisted)", store.Count(), indexEmbedBatchSize)
	}

	if err := checkEmbedderStamp(indexDir, base, store.Count() == 0); err == nil {
		t.Fatal("expected checkEmbedderStamp to refuse a store left stamp-less by a mid-index failure, but it passed")
	}
}

// TestIndexWorkspace_SuccessfulRunLeavesValidStamp is the success-path
// counterpart: proves indexWorkspace's stamp handling doesn't just refuse
// failures, it also correctly accepts a fully-completed multi-batch index.
func TestIndexWorkspace_SuccessfulRunLeavesValidStamp(t *testing.T) {
	dir := t.TempDir()
	const numFiles = indexEmbedBatchSize + 5
	writeManyTinyFiles(t, dir, numFiles)

	indexDir := filepath.Join(dir, indexDirName)
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	embedder := NewPlaceholderEmbedder(embedDim)
	scan, err := indexWorkspace(context.Background(), indexDir, dir, embedder, store, nil, discardLogger())
	if err != nil {
		t.Fatalf("indexWorkspace: %v", err)
	}
	if len(scan.Chunks) != numFiles {
		t.Fatalf("got %d chunks, want %d", len(scan.Chunks), numFiles)
	}

	if err := checkEmbedderStamp(indexDir, embedder, store.Count() == 0); err != nil {
		t.Fatalf("checkEmbedderStamp rejected a fully-completed index: %v", err)
	}
}

// erroringLexicalStore simulates a lexical tier that opened successfully but
// fails at query time (e.g. a corrupt/locked lexical.db) -- distinct from
// setupRetrieval's "failed to open at all" degradation, this is the
// runtime-error path retrieveTopK itself must swallow.
type erroringLexicalStore struct{}

func (erroringLexicalStore) Upsert(ctx context.Context, chunks []Chunk) error { return nil }
func (erroringLexicalStore) Search(ctx context.Context, query string, k int) ([]Chunk, error) {
	return nil, fmt.Errorf("simulated: lexical index unavailable")
}
func (erroringLexicalStore) DeleteByFilePath(ctx context.Context, relPath string) error { return nil }

func (erroringLexicalStore) AllIDs(ctx context.Context) ([]string, error) { return nil, nil }
func (erroringLexicalStore) Close() error                                 { return nil }

// TestRetrieveTopK_LexicalSearchErrorDegradesToSemanticOnly proves the
// hybrid retrieval path's core resilience guarantee: a lexical tier that
// errors at query time must never fail the whole retrieval, only silently
// drop its contribution -- semantic results must still come back exactly as
// if lexicalStore were nil, since retrieval must never block generation.
func TestRetrieveTopK_LexicalSearchErrorDegradesToSemanticOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChromemStore(filepath.Join(dir, indexDirName))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	fake := &fakeEmbedder{dim: 8}
	if err := store.Upsert(context.Background(), []Chunk{
		{ID: "a.go:1-1", FilePath: "a.go", StartLine: 1, EndLine: 1, Content: "x", Vector: unitVec(8, 0)},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := retrieveTopK(context.Background(), "anything", 5, fake, store, erroringLexicalStore{}, true)
	if err != nil {
		t.Fatalf("retrieveTopK should degrade to semantic-only on a lexical search error, not fail outright: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (the semantic hit, lexical tier silently dropped)", len(results))
	}
	if results[0].FilePath != "a.go" {
		t.Errorf("FilePath = %q, want %q", results[0].FilePath, "a.go")
	}
}

// fakeLexicalStore is a minimal in-memory LexicalStore stub for tests that
// need to prove a lexical hit actually changes retrieveTopK's output (as
// opposed to erroringLexicalStore above, which proves the opposite: that a
// failure doesn't).
type fakeLexicalStore struct {
	hits []Chunk
}

func (f *fakeLexicalStore) Upsert(ctx context.Context, chunks []Chunk) error { return nil }
func (f *fakeLexicalStore) Search(ctx context.Context, query string, k int) ([]Chunk, error) {
	if k < len(f.hits) {
		return f.hits[:k], nil
	}
	return f.hits, nil
}
func (f *fakeLexicalStore) DeleteByFilePath(ctx context.Context, relPath string) error { return nil }

func (f *fakeLexicalStore) AllIDs(ctx context.Context) ([]string, error) { return nil, nil }
func (f *fakeLexicalStore) Close() error                                 { return nil }

// TestRetrieveTopK_LexicalHitOutsideSemanticPoolStillSurfaces is the
// end-to-end version of TestFuseRRF_LexicalOnlyChunkStillSurfacesEvenWithNoSemanticRank:
// a chunk the lexical tier finds, but that never appears in the semantic
// store's results at all (simulating a chunk ranked far outside the
// semantic overfetch pool, exactly the measured real-repo failure this
// whole feature fixes), must still reach the final top-k.
func TestRetrieveTopK_LexicalHitOutsideSemanticPoolStillSurfaces(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChromemStore(filepath.Join(dir, indexDirName))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	fake := &fakeEmbedder{dim: 8}
	if err := store.Upsert(context.Background(), []Chunk{
		{ID: "unrelated.go:1-1", FilePath: "unrelated.go", StartLine: 1, EndLine: 1, Content: "x", Vector: unitVec(8, 0)},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	lex := &fakeLexicalStore{hits: []Chunk{
		{ID: "exact-symbol.go:91-130", FilePath: "exact-symbol.go", StartLine: 91, EndLine: 130, Class: FileClassCode, Content: "func TargetSymbol() {}"},
	}}

	results, err := retrieveTopK(context.Background(), "TargetSymbol", 5, fake, store, lex, true)
	if err != nil {
		t.Fatalf("retrieveTopK: %v", err)
	}

	found := false
	for _, r := range results {
		if r.FilePath == "exact-symbol.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lexical-only hit missing from final results: %+v", results)
	}
}
