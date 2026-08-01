package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The lexical index holds the user's source text and must not be readable by
// anyone else on the machine.
//
// It was created 0644 inside a 0755 directory while every sibling store treated
// the same class of content as private: memory.db is 0600 in a 0700 directory,
// and chromem makes its own collection directory 0700 — which is why the VECTOR
// half of this same index was already unreadable and the lexical half was not.
//
// The sidecars are asserted separately and deliberately: in WAL mode they hold
// committed rows the main file does not have yet, so a 0600 lexical.db beside a
// 0644 lexical.db-wal leaves the newest chunks readable.
func TestLexicalIndexIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "index")

	s, err := NewFTSChunkStore(idx)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer s.Close()

	// A write, so WAL mode actually produces the sidecars.
	if err := s.Upsert(context.Background(), []Chunk{{
		ID: "a", FilePath: "a.go", StartLine: 1, EndLine: 2, Content: "package a // private",
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	info, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("index directory mode = %04o, want no group/other bits", perm)
	}

	db := filepath.Join(idx, lexicalDBFileName)
	for _, p := range []string{db, db + "-wal", db + "-shm"} {
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue // a sidecar that does not exist cannot leak
		}
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0077 != 0 {
			t.Errorf("%s mode = %04o, want no group/other bits — it holds indexed source text",
				filepath.Base(p), perm)
		}
	}
}

// An index directory that already exists keeps its contents and loses its
// permissive mode. MkdirAll does not touch an existing directory's mode, so
// without an explicit Chmod every installation that already had an index would
// stay world-readable forever — which is every affected user.
func TestExistingLexicalIndexDirectoryIsTightenedOnOpen(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "index")
	if err := os.MkdirAll(idx, 0755); err != nil {
		t.Fatal(err)
	}

	s, err := NewFTSChunkStore(idx)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer s.Close()

	info, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("pre-existing index directory left at %04o; an install that already had an index stays world-readable", perm)
	}
}
