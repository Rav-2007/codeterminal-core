package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// fakeEmbedderFactory returns a newEmbedder function that hands back a
// fixed fakeEmbedder, recording whether stop was called.
func fakeEmbedderFactory(stopped *bool) func(*log.Logger) (Embedder, func(), error) {
	return func(*log.Logger) (Embedder, func(), error) {
		return &fakeEmbedder{dim: embedDim}, func() { *stopped = true }, nil
	}
}

func erroringEmbedderFactory(err error) func(*log.Logger) (Embedder, func(), error) {
	return func(*log.Logger) (Embedder, func(), error) {
		return nil, nil, err
	}
}

func baseTestConfig() *Config {
	return &Config{
		ConfigVersion: 1,
		DefaultTier:   "primary",
		Tiers:         map[string]ModelTier{"primary": {Slug: "test-model", Active: true}},
	}
}

func TestSetupRetrieval_DisabledFlagShortCircuits(t *testing.T) {
	var stopped bool
	cfg := baseTestConfig()

	rs := setupRetrieval(cfg, t.TempDir(), true, discardLogger(), fakeEmbedderFactory(&stopped))
	embedder, store, stop := rs.Embedder, rs.Store, rs.Stop
	stop()

	if embedder != nil || store != nil {
		t.Fatal("expected nil embedder/store when disabled via flag")
	}
	if stopped {
		t.Error("embedder factory was never invoked when disabled via flag, stop should be a no-op")
	}
}

func TestSetupRetrieval_ConfigDisabledShortCircuits(t *testing.T) {
	var stopped bool
	cfg := baseTestConfig()
	cfg.Retrieval.Disabled = true

	rs := setupRetrieval(cfg, t.TempDir(), false, discardLogger(), fakeEmbedderFactory(&stopped))
	embedder, store, stop := rs.Embedder, rs.Store, rs.Stop
	stop()

	if embedder != nil || store != nil {
		t.Fatal("expected nil embedder/store when disabled via config")
	}
}

func TestSetupRetrieval_NoIndexDegradesGracefully(t *testing.T) {
	var stopped bool
	cfg := baseTestConfig()
	workspace := t.TempDir() // no .codeterminal/index here at all

	rs := setupRetrieval(cfg, workspace, false, discardLogger(), fakeEmbedderFactory(&stopped))
	embedder, store, stop := rs.Embedder, rs.Store, rs.Stop
	stop()

	if embedder != nil || store != nil {
		t.Fatal("expected nil embedder/store when no index exists yet")
	}
	if stopped {
		t.Error("embedder factory should never have been invoked before the index-existence check")
	}
}

func TestSetupRetrieval_EmbedderStartFailureDegradesGracefully(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, indexDirName), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := baseTestConfig()

	rs := setupRetrieval(cfg, workspace, false, discardLogger(),
		erroringEmbedderFactory(errors.New("simulated: embedder helper failed to start")))
	embedder, store, stop := rs.Embedder, rs.Store, rs.Stop
	stop() // must not panic even though setup never got an embedder

	if embedder != nil || store != nil {
		t.Fatal("expected nil embedder/store when the embedder fails to start")
	}
}

func TestSetupRetrieval_StaleIndexDegradesGracefully(t *testing.T) {
	workspace := t.TempDir()
	indexDir := filepath.Join(workspace, indexDirName)

	vs, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	builtWith := NewPlaceholderEmbedder(embedDim)
	chunk := Chunk{ID: "1", FilePath: "a.go", StartLine: 1, EndLine: 1, Content: "package a", Vector: make([]float32, embedDim)}
	chunk.Vector[0] = 1
	if err := vs.Upsert(context.Background(), []Chunk{chunk}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := writeEmbedderStamp(indexDir, builtWith); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	var stopped bool
	cfg := baseTestConfig()
	// fakeEmbedder's ID ("fake-test-embedder-v1") deliberately differs from
	// builtWith's ("placeholder-hash-v1") despite matching Dim.
	rs := setupRetrieval(cfg, workspace, false, discardLogger(), fakeEmbedderFactory(&stopped))
	embedder, store, stop := rs.Embedder, rs.Store, rs.Stop
	stop()

	if embedder != nil || store != nil {
		t.Fatal("expected nil embedder/store when the index was built with a different embedder")
	}
	if !stopped {
		t.Error("expected the started embedder to be stopped after the stamp mismatch was detected")
	}
}

func TestSetupRetrieval_SuccessReturnsUsableEmbedderAndStore(t *testing.T) {
	workspace := t.TempDir()
	indexDir := filepath.Join(workspace, indexDirName)

	vs, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	fe := &fakeEmbedder{dim: embedDim}
	chunk := Chunk{ID: "1", FilePath: "a.go", StartLine: 1, EndLine: 1, Content: "package a", Vector: make([]float32, embedDim)}
	if err := vs.Upsert(context.Background(), []Chunk{chunk}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := writeEmbedderStamp(indexDir, fe); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	var stopped bool
	cfg := baseTestConfig()
	cfg.Retrieval.TopK = 3
	cfg.Retrieval.ContextBudgetChars = 1234

	rs := setupRetrieval(cfg, workspace, false, discardLogger(), fakeEmbedderFactory(&stopped))
	embedder, store, lexicalStore, stop, topK, budget := rs.Embedder, rs.Store, rs.LexicalStore, rs.Stop, rs.TopK, rs.ContextBudgetChars
	defer stop()

	if embedder == nil || store == nil {
		t.Fatal("expected a usable embedder/store on a matching, healthy index")
	}
	if lexicalStore == nil {
		t.Error("expected a usable lexical store alongside the vector store when the lexical index opens cleanly")
	}
	if topK != 3 {
		t.Errorf("topK = %d, want 3 (from config)", topK)
	}
	if budget != 1234 {
		t.Errorf("budget = %d, want 1234 (from config)", budget)
	}
}

// TestSetupRetrieval_LexicalIndexFailureDegradesGracefully proves the
// requirement this feature must preserve exactly: a lexical-tier failure is
// NOT a retrieval failure. It forces NewFTSChunkStore to fail by pre-creating
// a directory at the exact path the lexical index would open as a file
// (lexical.db), then asserts embedder/store still come back usable while
// lexicalStore alone is nil -- semantic-only retrieval must keep working.
func TestSetupRetrieval_LexicalIndexFailureDegradesGracefully(t *testing.T) {
	workspace := t.TempDir()
	indexDir := filepath.Join(workspace, indexDirName)

	vs, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	fe := &fakeEmbedder{dim: embedDim}
	chunk := Chunk{ID: "1", FilePath: "a.go", StartLine: 1, EndLine: 1, Content: "package a", Vector: make([]float32, embedDim)}
	if err := vs.Upsert(context.Background(), []Chunk{chunk}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := writeEmbedderStamp(indexDir, fe); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	// Sabotage the lexical index specifically: a directory where lexical.db
	// (a file) needs to open forces NewFTSChunkStore to fail without
	// touching the already-healthy vector store or embedder stamp at all.
	if err := os.MkdirAll(filepath.Join(indexDir, lexicalDBFileName), 0755); err != nil {
		t.Fatalf("pre-creating lexical.db as a directory: %v", err)
	}

	var stopped bool
	cfg := baseTestConfig()
	rs := setupRetrieval(cfg, workspace, false, discardLogger(), fakeEmbedderFactory(&stopped))
	embedder, store, lexicalStore, stop := rs.Embedder, rs.Store, rs.LexicalStore, rs.Stop
	defer stop()

	if embedder == nil || store == nil {
		t.Fatal("a lexical index failure must not disable semantic retrieval — expected a usable embedder/store")
	}
	if lexicalStore != nil {
		t.Error("expected a nil lexical store when the lexical index fails to open")
	}
}

func TestRetrievalConfig_DefaultsApplyWhenUnset(t *testing.T) {
	var c RetrievalConfig
	if got := c.resolvedTopK(); got != defaultK {
		t.Errorf("resolvedTopK() = %d, want defaultK (%d)", got, defaultK)
	}
	if got := c.resolvedContextBudgetChars(); got != defaultContextBudgetChars {
		t.Errorf("resolvedContextBudgetChars() = %d, want defaultContextBudgetChars (%d)", got, defaultContextBudgetChars)
	}
	if c.Disabled {
		t.Error("zero-value RetrievalConfig must default to enabled (Disabled=false) for backward compatibility")
	}
}
