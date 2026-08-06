package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the before/after evidence for Fix 1 of the atomic-write family:
// Apply() used to write the target file BEFORE recording its post-apply
// snapshot, so any failure in that snapshot step returned an error while the
// file on disk had already been mutated -- a mutation the caller was told did
// not happen, and which undo then refused to revert (no after/ snapshot to
// match against). Run against the pre-fix ordering, both "LeavesFileUnmodified"
// cases FAIL (the file holds the new content despite the error); the happy-path
// cases in apply_test.go pass both before and after, proving no regression.

// prepareOne is the shared setup: a workspace with one file, a backup session
// dir, and a PreparedEdit ready to apply.
func prepareOne(t *testing.T, original, search, replace string) (root, backupDir string, prepared *PreparedEdit) {
	t.Helper()
	root = realTempDir(t)
	writeTempFile(t, root, "foo.txt", original)

	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}
	prepared, err = PrepareEdit(root, EditBlock{FilePath: "foo.txt", Search: search, Replace: replace})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	return root, backupDir, prepared
}

// blockBackupSubdir makes writeBackupCopy's MkdirAll fail deterministically for
// one of the two snapshot subdirs by planting a regular FILE where the
// directory needs to be. This is the review's "BackupAfter fails (e.g. full
// disk)" repro without needing a full disk.
func blockBackupSubdir(t *testing.T, backupDir, subdir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(backupDir, subdir), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("planting %s blocker: %v", subdir, err)
	}
}

// TestApply_BackupAfterFailureLeavesFileUnmodified is the Fix 1 repro. When the
// post-apply snapshot cannot be recorded, Apply must report failure AND leave
// the workspace exactly as it found it -- reported status must always match
// disk.
func TestApply_BackupAfterFailureLeavesFileUnmodified(t *testing.T) {
	root, backupDir, prepared := prepareOne(t, "hello world\n", "hello", "goodbye")
	blockBackupSubdir(t, backupDir, "after")

	err := Apply(root, prepared, backupDir)
	if err == nil {
		t.Fatal("expected Apply to fail when the post-apply snapshot cannot be recorded, got nil")
	}
	if !strings.Contains(err.Error(), "post-apply snapshot") {
		t.Errorf("error = %v, want it to name the post-apply snapshot step", err)
	}

	got := readFile(t, filepath.Join(root, "foo.txt"))
	if got != "hello world\n" {
		t.Fatalf("UNREPORTED MUTATION: file = %q, want it untouched (%q) after a reported failure", got, "hello world\n")
	}
}

// TestApply_BackupOriginalFailureLeavesFileUnmodified pins the same invariant
// for the first fallible step. This one held before the fix too (the write came
// after it); it is here so the ordering guarantee is pinned end to end.
func TestApply_BackupOriginalFailureLeavesFileUnmodified(t *testing.T) {
	root, backupDir, prepared := prepareOne(t, "hello world\n", "hello", "goodbye")
	blockBackupSubdir(t, backupDir, "before")

	if err := Apply(root, prepared, backupDir); err == nil {
		t.Fatal("expected Apply to fail when the pre-edit backup cannot be recorded, got nil")
	}
	if got := readFile(t, filepath.Join(root, "foo.txt")); got != "hello world\n" {
		t.Fatalf("UNREPORTED MUTATION: file = %q, want it untouched (%q)", got, "hello world\n")
	}
}

// TestApply_WriteFailureRollsBackAfterSnapshot covers the ordering's one new
// edge: with the snapshot now recorded BEFORE the write, a failing write would
// otherwise leave after/<file> advertising content that never reached disk.
// Undo compares the file against that snapshot to decide whether it is safe to
// revert, so a stale one would wrongly guard a file that was never touched.
// Apply must roll the snapshot back to what it was.
func TestApply_WriteFailureRollsBackAfterSnapshot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory is still writable, so the write cannot be made to fail this way")
	}
	root, backupDir, prepared := prepareOne(t, "hello world\n", "hello", "goodbye")

	// Make the target's directory unwritable so the final write -- and only the
	// final write -- fails, after both snapshot steps have already succeeded. The
	// atomic writer creates its temp file in this directory, so removing write
	// permission there fails the write itself while leaving the already-created
	// backup session dir (under .codeterminal/backups, whose parents stay
	// writable) reachable. Injecting via a read-only FILE no longer works: the
	// atomic rename needs directory-write, not file-write, and would succeed.
	target := filepath.Join(root, "foo.txt")
	makeDirUnwritable(t, root)

	if err := Apply(root, prepared, backupDir); err == nil {
		t.Fatal("expected Apply to fail on an unwritable target, got nil")
	}
	if got := readFile(t, target); got != "hello world\n" {
		t.Fatalf("file = %q, want it untouched (%q)", got, "hello world\n")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "after", "foo.txt")); !os.IsNotExist(err) {
		got := readFile(t, filepath.Join(backupDir, "after", "foo.txt"))
		t.Fatalf("STALE SNAPSHOT: after/foo.txt exists (%q) though the write never landed", got)
	}
}

// TestApply_WriteFailureRestoresPriorAfterSnapshot is the same rollback, but for
// the second block in a run that touches an already-applied file: the snapshot
// must revert to the FIRST block's content (what is actually on disk), not
// vanish and not advance to the failed block's content.
func TestApply_WriteFailureRestoresPriorAfterSnapshot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory is still writable")
	}
	root := realTempDir(t)
	writeTempFile(t, root, "foo.txt", "one\ntwo\n")
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}

	first, err := PrepareEdit(root, EditBlock{FilePath: "foo.txt", Search: "one", Replace: "1"})
	if err != nil {
		t.Fatalf("PrepareEdit (first): %v", err)
	}
	if err := Apply(root, first, backupDir); err != nil {
		t.Fatalf("Apply (first): %v", err)
	}

	second, err := PrepareEdit(root, EditBlock{FilePath: "foo.txt", Search: "two", Replace: "2"})
	if err != nil {
		t.Fatalf("PrepareEdit (second): %v", err)
	}
	target := filepath.Join(root, "foo.txt")
	// Fail only the second block's write by removing write permission on the
	// target's directory (where the atomic writer stages its temp file), after
	// the first block has already applied. See the note in
	// TestApply_WriteFailureRollsBackAfterSnapshot on why the file-mode approach
	// no longer induces a failure under atomic rename.
	makeDirUnwritable(t, root)

	if err := Apply(root, second, backupDir); err == nil {
		t.Fatal("expected Apply to fail on an unwritable target, got nil")
	}

	onDisk := readFile(t, target)
	snapshot := readFile(t, filepath.Join(backupDir, "after", "foo.txt"))
	if snapshot != onDisk {
		t.Fatalf("STALE SNAPSHOT: after/foo.txt = %q but disk = %q; they must agree", snapshot, onDisk)
	}
	if onDisk != "1\ntwo\n" {
		t.Errorf("disk = %q, want the first block's result %q", onDisk, "1\ntwo\n")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}
