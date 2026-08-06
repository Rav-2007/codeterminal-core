//go:build unix

// POSIX-ONLY, and labelled rather than skipped.
//
// All three tests here are symlink-shaped, and inodeOf reads syscall.Stat_t,
// which does not exist on Windows -- so this file did not COMPILE for
// GOOS=windows, which a `go vet` on that platform reports as a hard failure.
// The runtime t.Skip inside inodeOf could never help: the break is at build
// time, not at run time.
//
// A build constraint rather than a cross-platform rewrite because the evidence
// itself is POSIX-shaped, exactly as confinement_conformance_test.go:123
// already records ("symlink vectors are POSIX-shaped"). Windows has symlinks
// too, but its escape vectors are junctions, alternate data streams, 8.3 short
// names and reserved device names -- a different table of attacks that needs
// its own file and its own evidence. That is Track C2 of the master plan, and
// pretending these three tests cover it would be worse than admitting they
// do not.

package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// This file is the before/after evidence for C2: the forward edit-write must
// not follow a symlink planted at the target leaf (workspace escape), and must
// commit atomically (no partial/corrupt file on an interrupted write). Run
// against the pre-fix plain os.WriteFile, the two escape tests FAIL — the write
// follows the link and clobbers a file outside the workspace; the atomicity
// test's temp-file / inode-swap assertions FAIL too. After routing Apply through
// writeFileAtomicNoFollow, all pass.
//
// The escape is exercised the way it actually happens: the leaf does not exist
// when the edit is PREPARED (a create), and a symlink is planted at it before
// the write — the TUI human-confirm window. Apply is the single core both the
// CLI and the TUI call, so driving it directly is the production path.

// planForwardCreate prepares a creating edit for relPath, then returns the
// pieces needed to plant something at the leaf and apply. The leaf is confirmed
// absent at prepare time.
func planForwardCreate(t *testing.T, relPath, content string) (root, backupDir string, prepared *PreparedEdit) {
	t.Helper()
	root = realTempDir(t)
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}
	prepared, err = PrepareEdit(root, EditBlock{FilePath: relPath, Search: "", Replace: content})
	if err != nil {
		t.Fatalf("PrepareEdit (create): %v", err)
	}
	if !prepared.Creates {
		t.Fatalf("expected a creating edit for %s", relPath)
	}
	return root, backupDir, prepared
}

// TestApply_ForwardWriteRefusesDanglingLeafSymlinkEscape is the report's primary
// escape scenario: a DANGLING symlink at the create target, pointing outside the
// workspace. Pre-fix, os.WriteFile follows it and CREATES the victim file
// outside; post-fix, Apply refuses and nothing appears outside.
func TestApply_ForwardWriteRefusesDanglingLeafSymlinkEscape(t *testing.T) {
	root, backupDir, prepared := planForwardCreate(t, "planted.txt", "MODEL-CONTROLLED CONTENT\n")

	// A victim path OUTSIDE the workspace that does not exist yet.
	outside := filepath.Join(t.TempDir(), "victim-created.txt")
	// Plant a dangling symlink at the target leaf, in the confirm window.
	if err := os.Symlink(outside, filepath.Join(root, "planted.txt")); err != nil {
		t.Fatalf("planting dangling symlink: %v", err)
	}

	err := Apply(root, prepared, backupDir)
	if err == nil {
		t.Fatal("expected Apply to refuse writing through a planted leaf symlink, got nil")
	}
	if _, statErr := os.Lstat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("ESCAPE: a file was created outside the workspace at %s (content=%q)", outside, readFile(t, outside))
	}
}

// TestWriteFileAtomicNoFollow_RefusesSymlinkAndPreservesVictim tests the writer
// unit directly with a symlink at the destination pointing to an EXISTING file
// outside the workspace. This case is normally caught upstream by Apply's
// VerifyUnchanged staleness check (a create whose target now exists is refused),
// so exercising the writer in isolation is what proves the writer's own leaf
// refusal. Pre-fix (a plain os.WriteFile) this would truncate+overwrite the
// victim through the link; the hardened writer refuses and leaves it intact.
func TestWriteFileAtomicNoFollow_RefusesSymlinkAndPreservesVictim(t *testing.T) {
	dir := realTempDir(t)

	const sentinel = "DO-NOT-CLOBBER\n"
	victim := filepath.Join(t.TempDir(), "victim-existing.txt")
	if err := os.WriteFile(victim, []byte(sentinel), 0644); err != nil {
		t.Fatalf("seeding victim: %v", err)
	}
	dest := filepath.Join(dir, "planted.txt")
	if err := os.Symlink(victim, dest); err != nil {
		t.Fatalf("planting symlink over victim: %v", err)
	}

	err := writeFileAtomicNoFollow(dest, []byte("MODEL-CONTROLLED CONTENT\n"), 0644)
	if err == nil {
		t.Fatal("expected writeFileAtomicNoFollow to refuse a symlinked destination, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %v, want it to name the symlink refusal", err)
	}
	if got := readFile(t, victim); got != sentinel {
		t.Fatalf("CORRUPTION: victim outside the workspace was overwritten through the symlink: got %q, want %q", got, sentinel)
	}
}

// TestApply_ForwardWriteIsAtomicRename pins the mechanism that closes the
// non-atomic-corruption half: the target is committed by renaming a fully
// written temp file, not truncated-then-written in place. A successful edit
// therefore swaps the target's inode (a plain in-place os.WriteFile keeps it)
// and leaves no staging temp file behind. That the target is only ever a
// complete file — never a truncated partial — follows directly.
func TestApply_ForwardWriteIsAtomicRename(t *testing.T) {
	root := realTempDir(t)
	target := writeTempFile(t, root, "foo.txt", "hello world\n")
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}

	beforeIno := inodeOf(t, target)

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "foo.txt", Search: "hello", Replace: "goodbye"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := readFile(t, target); got != "goodbye world\n" {
		t.Fatalf("content = %q, want %q", got, "goodbye world\n")
	}
	if afterIno := inodeOf(t, target); afterIno == beforeIno {
		t.Errorf("inode unchanged (%d): the write was in-place, not an atomic temp+rename", afterIno)
	}

	// No staging temp file left behind in the target's directory.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".codeterminal-apply-") {
			t.Errorf("leftover staging temp file %q after a successful atomic write", e.Name())
		}
	}
}

// inodeOf returns path's inode number, used to distinguish an atomic
// temp+rename commit (new inode) from an in-place rewrite (same inode).
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("inode not available on this platform")
	}
	return st.Ino
}
