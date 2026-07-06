package main

import (
	"encoding/json"
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

// TestEmbedderStamp_OldSchemaVersionRefused proves the index-schema staleness
// guard added alongside file-class metadata: a stamp written before
// IndexSchemaVersion existed (or with an explicitly older value) must be
// refused even though the embedder identity itself matches, since its
// chunks predate class metadata entirely.
func TestEmbedderStamp_OldSchemaVersionRefused(t *testing.T) {
	dir := t.TempDir()
	embedder := &fakeEmbedder{dim: embedDim}

	// Simulate a stamp written by a pre-class-metadata binary: same
	// embedder identity, but IndexSchemaVersion absent (zero value) rather
	// than the field just not existing in the JSON, since both decode the
	// same way.
	oldStamp := embedderStamp{EmbedderID: embedder.ID(), Dim: embedder.Dim(), IndexSchemaVersion: 0}
	data, err := json.Marshal(oldStamp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), data, 0644); err != nil {
		t.Fatalf("writing old stamp: %v", err)
	}

	err = checkEmbedderStamp(dir, embedder, false)
	if err == nil {
		t.Fatal("expected an error for an old index-schema-version stamp, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
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
