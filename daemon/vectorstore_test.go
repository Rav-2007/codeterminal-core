package main

import (
	"context"
	"path/filepath"
	"testing"
)

func unitVec(dim, hot int) []float32 {
	v := make([]float32, dim)
	v[hot] = 1
	return v
}

func TestChromemStore_UpsertQueryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChromemStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	chunks := []Chunk{
		{ID: "a.go:1-10", FilePath: "a.go", StartLine: 1, EndLine: 10, Content: "package a", Vector: unitVec(8, 0)},
		{ID: "b.go:1-10", FilePath: "b.go", StartLine: 1, EndLine: 10, Content: "package b", Vector: unitVec(8, 1)},
		{ID: "c.go:1-10", FilePath: "c.go", StartLine: 1, EndLine: 10, Content: "package c", Vector: unitVec(8, 2)},
	}
	if err := store.Upsert(context.Background(), chunks); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Query(context.Background(), unitVec(8, 1), 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	got := results[0]
	if got.ID != "b.go:1-10" {
		t.Errorf("ID = %q, want %q", got.ID, "b.go:1-10")
	}
	if got.FilePath != "b.go" || got.StartLine != 1 || got.EndLine != 10 {
		t.Errorf("metadata round-trip mismatch: %+v", got)
	}
	if got.Content != "package b" {
		t.Errorf("Content = %q, want %q", got.Content, "package b")
	}
	if got.Score < 0.99 {
		t.Errorf("Score = %v, want ~1 (exact match)", got.Score)
	}
}

func TestChromemStore_QueryClampsKToDocumentCount(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChromemStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	chunks := []Chunk{
		{ID: "a.go:1-10", FilePath: "a.go", StartLine: 1, EndLine: 10, Content: "x", Vector: unitVec(4, 0)},
		{ID: "b.go:1-10", FilePath: "b.go", StartLine: 1, EndLine: 10, Content: "y", Vector: unitVec(4, 1)},
	}
	if err := store.Upsert(context.Background(), chunks); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Query(context.Background(), unitVec(4, 0), 10)
	if err != nil {
		t.Fatalf("Query with k > count should clamp, not error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (clamped to document count)", len(results))
	}
}

func TestChromemStore_QueryOnEmptyStoreReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChromemStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	results, err := store.Query(context.Background(), unitVec(4, 0), 5)
	if err != nil {
		t.Fatalf("unexpected error on empty store: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

func TestChromemStore_UpsertEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChromemStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	if err := store.Upsert(context.Background(), nil); err != nil {
		t.Fatalf("Upsert(nil) should be a no-op, got error: %v", err)
	}
}
