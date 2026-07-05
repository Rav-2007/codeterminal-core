package main

import (
	"context"
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
	embedder := NewPlaceholderEmbedder(placeholderDim)
	ctx := context.Background()

	scan, err := buildIndex(ctx, dir, embedder, store)
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
		scan, err := buildIndex(context.Background(), dir, NewPlaceholderEmbedder(placeholderDim), store)
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
	scan, err := buildIndex(context.Background(), dir, fake, store)
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

	results, err := retrieveTopK(context.Background(), "anything", 5, fake, store)
	if err != nil {
		t.Fatalf("retrieveTopK: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}
