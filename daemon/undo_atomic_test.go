package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// This file is the before/after evidence for Fix 2 of the atomic-write family:
// the undo mirror of Fix 1. runUndoSession used to restore files one at a time
// in a single walk, returning on the first failure -- so a session whose middle
// file could not be restored left the earlier files reverted, never reached the
// later ones, and (through handleUndo, which discarded the count whenever err
// was non-nil) reported restored:0. The user was told nothing was reverted
// while the workspace sat in a mixed half-reverted state.
//
// Run against the pre-fix walk, every "RevertsNothing"/"AccurateCount" case
// FAILS. The "still restores all" cases pass before and after, proving the
// normal multi-file undo and the hand-edit guarding were not traded away.

// stageThreeFileSession builds a workspace of three applied files plus the
// backup session that would have produced them: before/ holds each file's
// pre-apply original, after/ holds the applied content still on disk, so all
// three classify as safe-to-restore.
func stageThreeFileSession(t *testing.T, root, sessionDir string) {
	t.Helper()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeAt(t, filepath.Join(root, name), "EDITED-"+name)
		writeAt(t, filepath.Join(sessionDir, "before", name), "ORIGINAL-"+name)
		writeAt(t, filepath.Join(sessionDir, "after", name), "EDITED-"+name)
	}
}

// breakMiddleFile makes the middle file of the walk (b.txt -- WalkDir is
// lexical, so a.txt, b.txt, c.txt) unrestorable, by replacing its backup
// snapshot with a symlink. restoreOne has always refused to read a backup entry
// through a symlink, which makes this a deterministic, permission-independent,
// root-safe injector for "the middle file fails mid-walk".
func breakMiddleFile(t *testing.T, sessionDir string) {
	t.Helper()
	victim := filepath.Join(sessionDir, "before", "b.txt")
	if err := os.Remove(victim); err != nil {
		t.Fatalf("removing b.txt backup: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.txt"), victim); err != nil {
		t.Fatalf("planting symlink backup entry: %v", err)
	}
}

func assertAllStillEdited(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"a.txt", "c.txt"} {
		if got := readFileString(t, filepath.Join(root, name)); got != "EDITED-"+name {
			t.Errorf("HALF-REVERTED WORKSPACE: %s = %q, want it left at %q -- a failed undo must revert nothing",
				name, got, "EDITED-"+name)
		}
	}
}

// TestRunUndoSession_MidWalkFailureRevertsNothing is the Fix 2 repro at the
// core: a three-file session whose middle file cannot be restored must leave
// every file exactly as it found it, and say so.
func TestRunUndoSession_MidWalkFailureRevertsNothing(t *testing.T) {
	root := realTempDir(t)
	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260721-000000")
	stageThreeFileSession(t, root, sessionDir)
	breakMiddleFile(t, sessionDir)

	restored, _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), io.Discard, discardLogger())
	if err == nil {
		t.Fatal("expected runUndoSession to fail when a backed-up file cannot be restored, got nil")
	}
	if restored != 0 {
		t.Errorf("restored = %d, want 0 -- nothing may be committed when the batch cannot complete", restored)
	}
	assertAllStillEdited(t, root)
}

// TestHandleUndo_MidWalkFailureReportsWhatIsActuallyOnDisk is the same repro
// over the wire, where the review found it. The claim the client receives must
// be true of the workspace: if the response says nothing was restored, nothing
// on disk may have been reverted.
func TestHandleUndo_MidWalkFailureReportsWhatIsActuallyOnDisk(t *testing.T) {
	root := realTempDir(t)
	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260721-000001")
	stageThreeFileSession(t, root, sessionDir)
	breakMiddleFile(t, sessionDir)

	srv := &Server{logger: discardLogger(), workspace: root}
	resp := undoViaHandler(t, srv, protocol.UndoRequest{Undo: true, BackupSessionDir: sessionDir})

	if resp.Error == "" {
		t.Fatal("expected an error in the UndoResponse, got none")
	}
	if resp.Restored != 0 {
		t.Errorf("Restored = %d, want 0", resp.Restored)
	}
	assertAllStillEdited(t, root)
}

// TestRunUndoSession_AllRestorableStillRevertsEveryFile is the negative
// control: making undo all-or-nothing must not make it restore less. Three
// files, nothing broken, all three revert.
func TestRunUndoSession_AllRestorableStillRevertsEveryFile(t *testing.T) {
	root := realTempDir(t)
	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260721-000002")
	stageThreeFileSession(t, root, sessionDir)

	restored, _, guarded, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), io.Discard, discardLogger())
	if err != nil {
		t.Fatalf("clean three-file undo must succeed, got: %v", err)
	}
	if restored != 3 {
		t.Errorf("restored = %d, want 3", restored)
	}
	if len(guarded) != 0 {
		t.Errorf("guarded = %v, want none", guarded)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if got := readFileString(t, filepath.Join(root, name)); got != "ORIGINAL-"+name {
			t.Errorf("%s = %q, want %q", name, got, "ORIGINAL-"+name)
		}
	}
}

// TestRunUndoSession_GuardedFileStillBlocksOnlyItself pins that the hand-edit
// guard keeps its existing semantics under the batched restore: a file changed
// since the apply is reported guarded and left alone, while the rest of the
// session still reverts. Guarding is a deliberate exclusion from the batch, not
// a batch failure.
func TestRunUndoSession_GuardedFileStillBlocksOnlyItself(t *testing.T) {
	root := realTempDir(t)
	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260721-000003")
	stageThreeFileSession(t, root, sessionDir)
	writeAt(t, filepath.Join(root, "b.txt"), "HAND-EDITED-SINCE-APPLY")

	restored, _, guarded, err := runUndoSession(root, sessionDir, false, strings.NewReader("n\n"), io.Discard, discardLogger())
	if err != nil {
		t.Fatalf("a guarded file must not fail the run, got: %v", err)
	}
	if restored != 2 {
		t.Errorf("restored = %d, want 2 (a.txt and c.txt)", restored)
	}
	if len(guarded) != 1 || guarded[0] != "b.txt" {
		t.Errorf("guarded = %v, want [b.txt]", guarded)
	}
	if got := readFileString(t, filepath.Join(root, "b.txt")); got != "HAND-EDITED-SINCE-APPLY" {
		t.Errorf("b.txt = %q, want the hand-edited content preserved", got)
	}
	for _, name := range []string{"a.txt", "c.txt"} {
		if got := readFileString(t, filepath.Join(root, name)); got != "ORIGINAL-"+name {
			t.Errorf("%s = %q, want %q", name, got, "ORIGINAL-"+name)
		}
	}
}

// TestRunUndoSession_ForcedBatchWithUnrestorableGuardedFileRevertsNothing
// extends atomicity across the forced path: when the user answers "yes" to
// overwriting guarded files, those join the same batch, so one unrestorable
// member must still leave the whole workspace untouched.
func TestRunUndoSession_ForcedBatchWithUnrestorableGuardedFileRevertsNothing(t *testing.T) {
	root := realTempDir(t)
	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260721-000004")
	stageThreeFileSession(t, root, sessionDir)
	// b.txt changed since the apply -> guarded; its backup is also unrestorable,
	// so forcing the overwrite must fail the batch rather than half-apply it.
	writeAt(t, filepath.Join(root, "b.txt"), "HAND-EDITED-SINCE-APPLY")
	breakMiddleFile(t, sessionDir)

	restored, _, _, err := runUndoSession(root, sessionDir, true, strings.NewReader("y\n"), io.Discard, discardLogger())
	if err == nil {
		t.Fatal("expected the forced batch to fail on the unrestorable file, got nil")
	}
	if restored != 0 {
		t.Errorf("restored = %d, want 0", restored)
	}
	assertAllStillEdited(t, root)
	if got := readFileString(t, filepath.Join(root, "b.txt")); got != "HAND-EDITED-SINCE-APPLY" {
		t.Errorf("b.txt = %q, want the hand-edited content preserved", got)
	}
}

// TestRunUndoSession_LeavesNoStagingResidue confirms the staging the atomic
// commit needs is cleaned up on the abort path -- a failed undo must not
// litter the workspace with temp files any more than it may litter it with
// half-reverted content.
func TestRunUndoSession_LeavesNoStagingResidue(t *testing.T) {
	root := realTempDir(t)
	sessionDir := filepath.Join(root, ".mochiii", "backups", "20260721-000005")
	stageThreeFileSession(t, root, sessionDir)
	breakMiddleFile(t, sessionDir)

	if _, _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), io.Discard, discardLogger()); err == nil {
		t.Fatal("expected failure")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), undoStagingPrefix) {
			t.Errorf("staging residue left behind: %s", e.Name())
		}
	}
}
