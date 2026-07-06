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

	embedder, store, stop, _, _ := setupRetrieval(cfg, t.TempDir(), true, discardLogger(), fakeEmbedderFactory(&stopped))
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

	embedder, store, stop, _, _ := setupRetrieval(cfg, t.TempDir(), false, discardLogger(), fakeEmbedderFactory(&stopped))
	stop()

	if embedder != nil || store != nil {
		t.Fatal("expected nil embedder/store when disabled via config")
	}
}

func TestSetupRetrieval_NoIndexDegradesGracefully(t *testing.T) {
	var stopped bool
	cfg := baseTestConfig()
	workspace := t.TempDir() // no .codeterminal/index here at all

	embedder, store, stop, _, _ := setupRetrieval(cfg, workspace, false, discardLogger(), fakeEmbedderFactory(&stopped))
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

	embedder, store, stop, _, _ := setupRetrieval(cfg, workspace, false, discardLogger(),
		erroringEmbedderFactory(errors.New("simulated: embedder helper failed to start")))
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
	embedder, store, stop, _, _ := setupRetrieval(cfg, workspace, false, discardLogger(), fakeEmbedderFactory(&stopped))
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

	embedder, store, stop, topK, budget := setupRetrieval(cfg, workspace, false, discardLogger(), fakeEmbedderFactory(&stopped))
	defer stop()

	if embedder == nil || store == nil {
		t.Fatal("expected a usable embedder/store on a matching, healthy index")
	}
	if topK != 3 {
		t.Errorf("topK = %d, want 3 (from config)", topK)
	}
	if budget != 1234 {
		t.Errorf("budget = %d, want 1234 (from config)", budget)
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
