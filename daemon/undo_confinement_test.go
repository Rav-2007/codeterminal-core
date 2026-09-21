package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAt writes content at an absolute path, creating parent dirs. Used by
// the confinement regression tests to stage backup sessions and out-of-root
// attacker targets.
func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestRestoreOne_RefusesFinalComponentSymlinkEscape is the FAIL-2 "Repro A"
// regression: the silent daemon path (force=false). A destination whose leaf
// is a symlink to an out-of-root file, pre-seeded so it passes the "unchanged
// since apply" guard, must NOT be written through. If this test ever fails,
// restoreOne has lost its leaf/ancestor symlink confinement.
func TestRestoreOne_RefusesFinalComponentSymlinkEscape(t *testing.T) {
	root := realTempDir(t)
	outside := realTempDir(t) // sibling of root, NOT under it

	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260719-000000")
	writeAt(t, filepath.Join(sessionDir, "before", "foo.txt"), "ORIGINAL-SECRET-BYTES")
	writeAt(t, filepath.Join(sessionDir, "after", "foo.txt"), "EDITED")

	target := filepath.Join(outside, "target.txt")
	writeAt(t, target, "EDITED") // == recorded "after" so the guard treats it as safe
	if err := os.Symlink(target, filepath.Join(root, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), io.Discard, discardLogger())
	if err == nil {
		t.Errorf("expected runUndoSession to refuse the escaping symlink, got nil error")
	}
	if got := readFileString(t, target); got != "EDITED" {
		t.Fatalf("ESCAPE: out-of-root target was written: %q (want unchanged %q)", got, "EDITED")
	}
}

// TestRestoreOne_RefusesIntermediateDirSymlinkEscape is the FAIL-2 "Repro B"
// regression: the force path (edits undo --force) with an intermediate
// DIRECTORY symlink. O_NOFOLLOW on the leaf alone does not catch this — it
// requires resolving/confining the ancestor prefix. If this test fails, that
// ancestor-prefix confinement has regressed.
func TestRestoreOne_RefusesIntermediateDirSymlinkEscape(t *testing.T) {
	root := realTempDir(t)
	outside := realTempDir(t)

	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260719-000001")
	writeAt(t, filepath.Join(sessionDir, "before", "sub", "pwn.txt"), "ATTACKER-CHOSEN-PAYLOAD")
	// no after/ snapshot -> guarded -> only reached under force

	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	escaped := filepath.Join(outside, "pwn.txt")

	_, _, _, err := runUndoSession(root, sessionDir, true, strings.NewReader("y\n"), io.Discard, discardLogger())
	if err == nil {
		t.Errorf("expected runUndoSession to refuse the intermediate-dir symlink, got nil error")
	}
	if _, statErr := os.Stat(escaped); !os.IsNotExist(statErr) {
		got, _ := os.ReadFile(escaped)
		t.Fatalf("ESCAPE: out-of-root file created: %s = %q", escaped, got)
	}
}

// TestRestoreOne_RestoresDeletedFileWithinRoot is the critical negative check:
// the confinement fix must NOT break legitimate restore of a file (and its
// parent dirs) that no longer exist on disk. Resolving the FULL dest path via
// EvalSymlinks would fail here with "lstat ... no such file" — the naive fix
// the audit rejected. This must succeed cleanly.
func TestRestoreOne_RestoresDeletedFileWithinRoot(t *testing.T) {
	root := realTempDir(t)

	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260719-000002")
	// Deleted file whose parent directory is also gone since the apply run.
	writeAt(t, filepath.Join(sessionDir, "before", "pkg", "gone.txt"), "RESTORED-CONTENT")
	// no after/ snapshot + file absent on disk -> guarded -> restore under force

	restored, _, _, err := runUndoSession(root, sessionDir, true, strings.NewReader("y\n"), io.Discard, discardLogger())
	if err != nil {
		t.Fatalf("legit deleted-file undo must succeed, got: %v", err)
	}
	if restored != 1 {
		t.Errorf("restored = %d, want 1", restored)
	}
	if got := readFileString(t, filepath.Join(root, "pkg", "gone.txt")); got != "RESTORED-CONTENT" {
		t.Errorf("restored content = %q, want %q", got, "RESTORED-CONTENT")
	}
}

// TestRestoreOne_OrdinaryUndoUnchanged confirms the common case (existing
// file, no symlinks) still restores its pre-apply content.
func TestRestoreOne_OrdinaryUndoUnchanged(t *testing.T) {
	root := realTempDir(t)
	writeAt(t, filepath.Join(root, "foo.txt"), "EDITED")

	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260719-000003")
	writeAt(t, filepath.Join(sessionDir, "before", "foo.txt"), "ORIGINAL")
	writeAt(t, filepath.Join(sessionDir, "after", "foo.txt"), "EDITED")

	restored, _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), io.Discard, discardLogger())
	if err != nil {
		t.Fatalf("ordinary undo failed: %v", err)
	}
	if restored != 1 {
		t.Errorf("restored = %d, want 1", restored)
	}
	if got := readFileString(t, filepath.Join(root, "foo.txt")); got != "ORIGINAL" {
		t.Errorf("content = %q, want %q", got, "ORIGINAL")
	}
}

// TestRestoreOne_RefusesSecretNamedFile confirms parity with Apply(): undo of
// a secret-named file (.env) is a hard refusal, matching the forward edit
// path which would refuse to write it in the first place.
func TestRestoreOne_RefusesSecretNamedFile(t *testing.T) {
	root := realTempDir(t)
	writeAt(t, filepath.Join(root, ".env"), "SECRET=v2")

	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260719-000004")
	writeAt(t, filepath.Join(sessionDir, "before", ".env"), "SECRET=v1")
	writeAt(t, filepath.Join(sessionDir, "after", ".env"), "SECRET=v2")

	_, _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), io.Discard, discardLogger())
	if err == nil {
		t.Errorf("expected undo of a secret-named file to be refused, got nil error")
	}
	if got := readFileString(t, filepath.Join(root, ".env")); got != "SECRET=v2" {
		t.Errorf(".env was written despite secret-name refusal: %q", got)
	}
}
