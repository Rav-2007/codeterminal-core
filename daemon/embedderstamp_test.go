package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbedderStamp_RoundTripMatches(t *testing.T) {
	dir := t.TempDir()
	embedder := &fakeEmbedder{dim: 384}

	if err := writeEmbedderStamp(dir, embedder); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}
	if err := checkEmbedderStamp(dir, embedder, false); err != nil {
		t.Fatalf("checkEmbedderStamp should accept a matching stamp, got: %v", err)
	}
}

func TestEmbedderStamp_MismatchedIDRefused(t *testing.T) {
	dir := t.TempDir()
	builtWith := NewPlaceholderEmbedder(embedDim)
	queriedWith := &fakeEmbedder{dim: embedDim} // same Dim, different ID — this is the case Dim alone can't catch

	if err := writeEmbedderStamp(dir, builtWith); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	err := checkEmbedderStamp(dir, queriedWith, false)
	if err == nil {
		t.Fatal("expected a mismatch error when IDs differ despite equal Dim, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
	}
}

func TestEmbedderStamp_MissingStampOnNonEmptyStoreRefused(t *testing.T) {
	dir := t.TempDir() // no stamp file written at all — simulates a pre-existing 5a index

	err := checkEmbedderStamp(dir, &fakeEmbedder{dim: embedDim}, false)
	if err == nil {
		t.Fatal("expected an error for a missing stamp on a non-empty store, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
	}
}

func TestEmbedderStamp_MissingStampOnEmptyStoreAllowed(t *testing.T) {
	dir := t.TempDir()

	err := checkEmbedderStamp(dir, &fakeEmbedder{dim: embedDim}, true)
	if err != nil {
		t.Fatalf("an empty store should never be refused for a missing stamp, got: %v", err)
	}
}

func TestEmbedderStamp_CorruptStampRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	err := checkEmbedderStamp(dir, &fakeEmbedder{dim: embedDim}, false)
	if err == nil {
		t.Fatal("expected an error for a corrupt stamp file, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
	}
}
