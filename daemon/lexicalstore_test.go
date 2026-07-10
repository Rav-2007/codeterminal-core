package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFTSChunkStore_UpsertSearchRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFTSChunkStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer store.Close()

	chunks := []Chunk{
		{ID: "a.go:1-10", FilePath: "a.go", StartLine: 1, EndLine: 10, Class: FileClassCode, Content: "func SearchRequest() {}"},
		{ID: "b.go:1-10", FilePath: "b.go", StartLine: 1, EndLine: 10, Class: FileClassCode, Content: "func unrelatedThing() {}"},
	}
	if err := store.Upsert(context.Background(), chunks); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Search(context.Background(), "where is SearchRequest defined", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1, results=%+v", len(results), results)
	}
	got := results[0]
	if got.FilePath != "a.go" || got.StartLine != 1 || got.EndLine != 10 {
		t.Errorf("metadata round-trip mismatch: %+v", got)
	}
	if got.Class != FileClassCode {
		t.Errorf("Class = %q, want %q", got.Class, FileClassCode)
	}
	if got.Content != "func SearchRequest() {}" {
		t.Errorf("Content = %q, want the original chunk content", got.Content)
	}
	if got.ID != "a.go:1-10" {
		t.Errorf("ID = %q, want %q", got.ID, "a.go:1-10")
	}
}

func TestFTSChunkStore_UpsertReplacesRatherThanDuplicates(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFTSChunkStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer store.Close()

	chunk := Chunk{ID: "a.go:1-10", FilePath: "a.go", StartLine: 1, EndLine: 10, Class: FileClassCode, Content: "func Original() {}"}
	if err := store.Upsert(context.Background(), []Chunk{chunk}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	updated := chunk
	updated.Content = "func Renamed() {}"
	if err := store.Upsert(context.Background(), []Chunk{updated}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	results, err := store.Search(context.Background(), "Renamed", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results for the same chunk ID re-indexed, want exactly 1 (replaced, not duplicated): %+v", len(results), results)
	}
	if results[0].Content != "func Renamed() {}" {
		t.Errorf("Content = %q, want the updated content", results[0].Content)
	}
}

func TestFTSChunkStore_SearchOnEmptyStoreReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFTSChunkStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer store.Close()

	results, err := store.Search(context.Background(), "anything at all", 5)
	if err != nil {
		t.Fatalf("unexpected error on empty store: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

func TestFTSChunkStore_UpsertEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFTSChunkStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer store.Close()

	if err := store.Upsert(context.Background(), nil); err != nil {
		t.Fatalf("Upsert(nil) should be a no-op, got error: %v", err)
	}
}

func TestBuildLexicalQuery_DropsStopwordsAndQuotesRemainingTokens(t *testing.T) {
	got := buildLexicalQuery("where is the ZDR refusal string matched")
	want := `"ZDR" OR "refusal" OR "string" OR "matched"`
	if got != want {
		t.Errorf("buildLexicalQuery = %q, want %q", got, want)
	}
}

func TestBuildLexicalQuery_PreservesDottedTokens(t *testing.T) {
	// "does" and "do" are both stopwords -- only the dotted identifier
	// itself should survive.
	got := buildLexicalQuery("what does fmt.Println do")
	want := `"fmt.Println"`
	if got != want {
		t.Errorf("buildLexicalQuery = %q, want %q", got, want)
	}
}

func TestBuildLexicalQuery_AllStopwordsYieldsEmptyString(t *testing.T) {
	got := buildLexicalQuery("what is the")
	if got != "" {
		t.Errorf("buildLexicalQuery(%q) = %q, want empty string", "what is the", got)
	}
}

func TestFTSChunkStore_SearchWithOnlyStopwordsReturnsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFTSChunkStore(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer store.Close()

	chunk := Chunk{ID: "a.go:1-10", FilePath: "a.go", StartLine: 1, EndLine: 10, Class: FileClassCode, Content: "func Foo() {}"}
	if err := store.Upsert(context.Background(), []Chunk{chunk}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Search(context.Background(), "what is the", 5)
	if err != nil {
		t.Fatalf("a stopword-only query must not error, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results for a stopword-only query, want 0", len(results))
	}
}
