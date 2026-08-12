package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the before/after evidence for the FAIL-2 Gate-5 (b)-bucket
// O_NOFOLLOW hardening: daemon-owned writers whose paths come from
// constants/config (not client input) but which lacked a symlink guard. Run
// against the pre-hardening code every "RefusesSymlink" case FAILS (the writer
// follows the planted symlink and clobbers the victim); every "NormalWrite"
// case passes both before and after (proving no regression). After hardening,
// all pass.
//
// Not directly exercised here (covered by the shared primitives these tests
// pin, plus a real daemon cold-start): main.go's lockfile write, which now
// routes through the same writeFileNoFollow that TestWriteEmbedderStamp_* pins;
// and modelfetch.go's downloadAsset .part write, which routes through the same
// openNoFollow(O_RDWR|O_CREATE|O_TRUNC) that TestWriteExtractedFile_* pins.

const victimSentinel = "DO-NOT-CLOBBER\n"

// plantSymlinkOverVictim creates a victim file with a known sentinel outside
// dir, then a symlink at linkPath pointing to it, and returns the victim path.
// A writer that follows the symlink overwrites the sentinel; a hardened one
// refuses and leaves it intact.
func plantSymlinkOverVictim(t *testing.T, linkPath string) (victimPath string) {
	t.Helper()
	victimPath = filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victimPath, []byte(victimSentinel), 0644); err != nil {
		t.Fatalf("seeding victim: %v", err)
	}
	if err := os.Symlink(victimPath, linkPath); err != nil {
		t.Fatalf("planting symlink at %s: %v", linkPath, err)
	}
	return victimPath
}

func victimUntouched(t *testing.T, victimPath string) bool {
	t.Helper()
	got, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatalf("reading victim: %v", err)
	}
	return string(got) == victimSentinel
}

// plantSymlinkOverEmptyVictim plants a symlink at linkPath pointing to an
// EMPTY victim file. Used for the SQLite-store guards: a zero-length file is a
// valid new SQLite database, so before the guard exists the driver opens the
// symlink and writes real pages THROUGH it into the victim (err == nil). After
// the guard, the leaf lstat refuses with an error naming "symlink" — which is
// how these tests distinguish the guard from SQLite's own not-a-database error
// (that error would fire on any non-empty non-DB target, guard or not).
func plantSymlinkOverEmptyVictim(t *testing.T, linkPath string) (victimPath string) {
	t.Helper()
	victimPath = filepath.Join(t.TempDir(), "victim.db")
	if err := os.WriteFile(victimPath, nil, 0644); err != nil {
		t.Fatalf("seeding empty victim: %v", err)
	}
	if err := os.Symlink(victimPath, linkPath); err != nil {
		t.Fatalf("planting symlink at %s: %v", linkPath, err)
	}
	return victimPath
}

func victimStillEmpty(t *testing.T, victimPath string) bool {
	t.Helper()
	fi, err := os.Stat(victimPath)
	if err != nil {
		t.Fatalf("stat victim: %v", err)
	}
	return fi.Size() == 0
}

// --- embedderstamp.go: writeEmbedderStamp (writeFileNoFollow) ---

func TestWriteEmbedderStamp_NormalWrite(t *testing.T) {
	dir := t.TempDir()
	if err := writeEmbedderStamp(dir, NewPlaceholderEmbedder(384)); err != nil {
		t.Fatalf("normal stamp write failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, embedderStampFileName)); err != nil {
		t.Fatalf("stamp file not created: %v", err)
	}
}

func TestWriteEmbedderStamp_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, embedderStampFileName)
	victim := plantSymlinkOverVictim(t, link)

	err := writeEmbedderStamp(dir, NewPlaceholderEmbedder(384))
	if err == nil {
		t.Errorf("writeEmbedderStamp followed a symlinked stamp path; want refusal")
	}
	if !victimUntouched(t, victim) {
		t.Errorf("stamp write clobbered a file outside the index dir through a symlink")
	}
}

// --- onnxruntimefetch.go: writeExtractedFile (openNoFollow) ---

func TestWriteExtractedFile_NormalWrite(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "lib.so")
	if err := writeExtractedFile(dest, strings.NewReader("payload")); err != nil {
		t.Fatalf("normal extract write failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "payload" {
		t.Fatalf("extracted file wrong: got %q err %v", got, err)
	}
}

func TestWriteExtractedFile_RefusesSymlink(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "lib.so")
	// writeExtractedFile writes dest+".part" then renames; plant the symlink
	// there — that is the actual open site the hardening guards.
	victim := plantSymlinkOverVictim(t, dest+".part")

	err := writeExtractedFile(dest, strings.NewReader("payload"))
	if err == nil {
		t.Errorf("writeExtractedFile followed a symlinked .part path; want refusal")
	}
	if !victimUntouched(t, victim) {
		t.Errorf("extract write clobbered a file outside the dest dir through a symlink")
	}
}

// --- warnsink.go: warnSink.write (openNoFollow, errors swallowed) ---

func TestWarnSink_NormalAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "warnmode.jsonl")
	w := newWarnSink(path)
	w.write(warnEvent{Detector: "entropy", File: "a.go", Note: "len=10"})
	got, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(got), `"detector":"entropy"`) {
		t.Fatalf("normal warn append missing: got %q err %v", got, err)
	}
}

func TestWarnSink_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "warnmode.jsonl")
	victim := plantSymlinkOverVictim(t, path)

	// write() swallows all errors by design, so the observable signal is that
	// nothing was written THROUGH the symlink to the victim.
	w := newWarnSink(path)
	w.write(warnEvent{Detector: "entropy", File: "a.go", Note: "len=10"})
	if !victimUntouched(t, victim) {
		t.Errorf("warn sink appended through a symlink to a file outside the logs dir")
	}
}

// --- SQLite stores: leaf lstat guard before sql.Open ---

func TestMemoryStore_RefusesSymlinkDBFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	victim := plantSymlinkOverEmptyVictim(t, path)

	s, err := OpenMemoryStore(path)
	if err == nil {
		s.Close()
		t.Fatalf("OpenMemoryStore opened a symlinked db file; want refusal")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal not attributable to the symlink guard: %v", err)
	}
	if !victimStillEmpty(t, victim) {
		t.Errorf("OpenMemoryStore wrote SQLite pages through a symlink to an outside file")
	}
}

func TestMemoryStore_NormalOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("normal memory open failed: %v", err)
	}
	s.Close()
}

func TestFTSChunkStore_RefusesSymlinkDBFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lexicalDBFileName)
	victim := plantSymlinkOverEmptyVictim(t, path)

	s, err := NewFTSChunkStore(dir)
	if err == nil {
		s.Close()
		t.Fatalf("NewFTSChunkStore opened a symlinked db file; want refusal")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal not attributable to the symlink guard: %v", err)
	}
	if !victimStillEmpty(t, victim) {
		t.Errorf("NewFTSChunkStore wrote SQLite pages through a symlink to an outside file")
	}
}

func TestFTSChunkStore_NormalOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatalf("normal lexical open failed: %v", err)
	}
	s.Close()
}
