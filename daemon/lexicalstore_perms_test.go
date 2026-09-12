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

	assertOwnerOnly(t, idx, "the index directory")

	db := filepath.Join(idx, lexicalDBFileName)
	for _, p := range []string{db, db + "-wal", db + "-shm"} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue // a sidecar that does not exist cannot leak
		}
		assertOwnerOnly(t, p, "it holds indexed source text")
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

	assertOwnerOnly(t, idx, "an install that already had an index must not stay world-readable")
}

// The embedder stamp is the third file in the index directory and was the only
// one still written world-readable.
//
// 2.3-A. lexical.db is 0600, chromem's collection directory is 0700, and the
// stamp was 0644 — with no stated reason; the write's comment addresses
// O_NOFOLLOW and is silent on the mode. It is NOT a live disclosure, because
// the containing directory is 0700 and nobody else can traverse to it, and it
// is filed as the defence-in-depth inconsistency it is rather than inflated.
//
// What it would disclose if the directory mode ever regressed is small but not
// nothing: the exact ONNX runtime and model build, and a timestamp for when the
// workspace was last indexed. The reason to fix it is that a file's own mode
// should not depend on its directory's for its safety, which is the same
// argument the sidecar test above makes.
func TestEmbedderStampIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	embedder := &PlaceholderEmbedder{}
	if err := writeEmbedderStamp(dir, embedder); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	fi, err := os.Stat(filepath.Join(dir, embedderStampFileName))
	if err != nil {
		t.Fatalf("stamp was not written: %v", err)
	}
	// Vacuity floor: a zero-length or absent file would satisfy a mode check
	// without the stamp ever having been written.
	if fi.Size() == 0 {
		t.Fatal("vacuity floor: the stamp is empty, so its mode proves nothing")
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("embedder stamp is %v, want owner-only (0600). Its siblings in this same "+
			"directory are 0600/0700; this one was 0644 with no reason given.", mode)
	}
}
