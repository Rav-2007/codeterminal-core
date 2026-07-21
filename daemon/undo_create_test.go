package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/editapply"
)

// This file is the before/after evidence for Fix C: undoing a CREATED file
// lied. Its before/ snapshot is a 0-byte file -- byte-for-byte identical to the
// snapshot of a file that existed and was empty -- so undo restored the empty
// snapshot, printed "restored new.txt", reported 1 file restored, and left a
// 0-byte file standing where the correct answer was no file at all.
//
// That is the failure class Tier 1 set out to kill: not a refusal, not a crash,
// but a report that disagrees with the disk. The user is told the workspace is
// back the way it was while a file they never had sits in it.
//
// Run against the pre-fix undo, every "Removed" case FAILS with a 0-byte file
// still present. Every "StillRestores" case passes before and after -- they are
// the behaviours this change had to leave alone.

// applyCreate creates relPath through the engine and returns the backup session
// the run wrote, so a test can then undo it.
func applyCreate(t *testing.T, root, relPath, content string) string {
	t.Helper()
	prepared, err := editapply.PrepareEdit(root, editapply.EditBlock{FilePath: relPath, Search: "", Replace: content})
	if err != nil {
		t.Fatalf("PrepareEdit(create %s): %v", relPath, err)
	}
	backupDir, err := editapply.NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}
	if err := editapply.Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply(create %s): %v", relPath, err)
	}
	return backupDir
}

// TestUndoCreate_RemovesTheFileAndSaysSo is the repro.
func TestUndoCreate_RemovesTheFileAndSaysSo(t *testing.T) {
	root := realTempDir(t)
	backupDir := applyCreate(t, root, "new.txt", "hello\n")

	var out bytes.Buffer
	restored, guarded, err := runUndoSession(root, backupDir, false, strings.NewReader(""), &out, discardLogger())
	if err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if len(guarded) != 0 {
		t.Errorf("guarded = %v, want none: the file is exactly as the apply run left it", guarded)
	}

	// The honesty invariant: report and disk agree.
	if _, statErr := os.Stat(filepath.Join(root, "new.txt")); statErr == nil {
		t.Error("THE LIE: undo left new.txt on disk; undoing a create must remove the file")
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("Stat: %v", statErr)
	}
	if restored != 1 {
		t.Errorf("reverted count = %d, want 1", restored)
	}
	if !strings.Contains(out.String(), "removed new.txt") {
		t.Errorf("report = %q, want it to say the file was removed", out.String())
	}
	if strings.Contains(out.String(), "restored new.txt") {
		t.Errorf("report = %q, says 'restored' for a file it deleted", out.String())
	}
}

// TestUndoCreate_EmptyPreexistingFileStillRestores is the conflation guard, and
// the reason a marker was needed at all rather than a "the snapshot is 0 bytes"
// heuristic. This file EXISTED and was empty; the apply run filled it. Undoing
// that restores the empty file. Deleting it would be a new data-loss bug
// introduced by the fix for the old one.
func TestUndoCreate_EmptyPreexistingFileStillRestores(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "empty.txt", "")

	prepared, err := editapply.PrepareEdit(root, editapply.EditBlock{FilePath: "empty.txt", Search: "", Replace: "filled\n"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	backupDir, _ := editapply.NewBackupSessionDir(root)
	if err := editapply.Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var out bytes.Buffer
	if _, _, err := runUndoSession(root, backupDir, false, strings.NewReader(""), &out, discardLogger()); err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}

	got, statErr := os.ReadFile(filepath.Join(root, "empty.txt"))
	if statErr != nil {
		t.Fatalf("DELETED A PRE-EXISTING FILE: empty.txt is gone after undo (%v)", statErr)
	}
	if len(got) != 0 {
		t.Errorf("empty.txt = %q, want it restored to empty", got)
	}
	if !strings.Contains(out.String(), "restored empty.txt") {
		t.Errorf("report = %q, want a restore", out.String())
	}
}

// TestUndoCreate_HandEditedCreatedFileIsGuarded: the undo guard applies to
// removals exactly as it does to restores. A created file the user has since
// changed is theirs now, and must not be silently deleted.
func TestUndoCreate_HandEditedCreatedFileIsGuarded(t *testing.T) {
	root := realTempDir(t)
	backupDir := applyCreate(t, root, "new.txt", "hello\n")
	writeTempFile(t, root, "new.txt", "hello\nand my own line\n")

	var out bytes.Buffer
	restored, guarded, err := runUndoSession(root, backupDir, false, strings.NewReader("n\n"), &out, discardLogger())
	if err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if restored != 0 {
		t.Errorf("reverted = %d, want 0", restored)
	}
	if len(guarded) != 1 || guarded[0] != "new.txt" {
		t.Errorf("guarded = %v, want [new.txt]", guarded)
	}
	if got := readFileString(t, filepath.Join(root, "new.txt")); got != "hello\nand my own line\n" {
		t.Errorf("new.txt = %q, want the user's edit untouched", got)
	}
}

// TestUndoCreate_HandEditedCreatedFileRemovedOnForce is the other half: the
// guard is a prompt, not a veto. Confirming still removes it.
func TestUndoCreate_HandEditedCreatedFileRemovedOnForce(t *testing.T) {
	root := realTempDir(t)
	backupDir := applyCreate(t, root, "new.txt", "hello\n")
	writeTempFile(t, root, "new.txt", "hello\nand my own line\n")

	var out bytes.Buffer
	restored, guarded, err := runUndoSession(root, backupDir, true, strings.NewReader(""), &out, discardLogger())
	if err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if restored != 1 || len(guarded) != 0 {
		t.Errorf("reverted = %d, guarded = %v, want 1 and none", restored, guarded)
	}
	if _, statErr := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(statErr) {
		t.Errorf("new.txt survived a forced undo (stat err = %v)", statErr)
	}
}

// TestUndoCreate_AlreadyDeletedCreatedFileIsNotAnError: the user deleting the
// created file themselves reaches the same end state undo wants. It is guarded
// (its content no longer matches the after/ snapshot, because there is no
// content), and forcing through must not fail the batch over a file that is
// already in the desired state.
func TestUndoCreate_AlreadyDeletedCreatedFileIsNotAnError(t *testing.T) {
	root := realTempDir(t)
	backupDir := applyCreate(t, root, "new.txt", "hello\n")
	if err := os.Remove(filepath.Join(root, "new.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	var out bytes.Buffer
	restored, _, err := runUndoSession(root, backupDir, true, strings.NewReader(""), &out, discardLogger())
	if err != nil {
		t.Fatalf("runUndoSession on an already-deleted created file: %v", err)
	}
	if restored != 1 {
		t.Errorf("reverted = %d, want 1", restored)
	}
	if _, statErr := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(statErr) {
		t.Errorf("new.txt exists after undo (stat err = %v)", statErr)
	}
}

// TestUndoCreate_MixedSessionRestoresAndRemoves is the shape a real turn
// produces: one file changed, one file created, one undo.
func TestUndoCreate_MixedSessionRestoresAndRemoves(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "main.go", "package main\n\nfunc old() {}\n")

	backupDir, _ := editapply.NewBackupSessionDir(root)
	for _, block := range []editapply.EditBlock{
		{FilePath: "main.go", Search: "func old() {}", Replace: "func renamed() {}"},
		{FilePath: "notes.md", Search: "", Replace: "# Notes\n"},
	} {
		prepared, err := editapply.PrepareEdit(root, block)
		if err != nil {
			t.Fatalf("PrepareEdit(%s): %v", block.FilePath, err)
		}
		if err := editapply.Apply(root, prepared, backupDir); err != nil {
			t.Fatalf("Apply(%s): %v", block.FilePath, err)
		}
	}

	var out bytes.Buffer
	restored, guarded, err := runUndoSession(root, backupDir, false, strings.NewReader(""), &out, discardLogger())
	if err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if restored != 2 || len(guarded) != 0 {
		t.Errorf("reverted = %d, guarded = %v, want 2 and none", restored, guarded)
	}
	if got := readFileString(t, filepath.Join(root, "main.go")); got != "package main\n\nfunc old() {}\n" {
		t.Errorf("main.go = %q, want the original content back", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, "notes.md")); !os.IsNotExist(statErr) {
		t.Errorf("notes.md survived undo (stat err = %v)", statErr)
	}
	report := out.String()
	if !strings.Contains(report, "restored main.go") || !strings.Contains(report, "removed notes.md") {
		t.Errorf("report = %q, want it to name the restore and the removal separately", report)
	}
	if !strings.Contains(report, "2 file(s) reverted") || !strings.Contains(report, "1 restored, 1 removed") {
		t.Errorf("summary = %q, want it to break down the counts", report)
	}
}

// TestUndoCreate_RemovalStillRefusesUnsafePaths is the FAIL-2 parity check for
// the new revert shape. A fabricated backup session -- a session dir is
// attacker-influenceable in the threat model FAIL-2 established -- must not be
// able to turn undo into an arbitrary-unlink primitive by naming a protected or
// escaping path in the created-file record. Every gate that guards a restore
// guards a removal.
func TestUndoCreate_RemovalStillRefusesUnsafePaths(t *testing.T) {
	for _, tc := range []struct{ name, rel string }{
		{"git hook", filepath.Join(".git", "hooks", "pre-commit")},
		{"git config", filepath.Join(".git", "config")},
		{"secret name", ".env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
			victim := writeTempFile(t, root, tc.rel, "important\n")

			// Fabricate a session that claims the run created the victim.
			sessionDir := filepath.Join(root, ".codeterminal", "backups", "20200101-000000")
			if err := os.MkdirAll(filepath.Join(sessionDir, "before", filepath.Dir(tc.rel)), 0755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(sessionDir, "after", filepath.Dir(tc.rel)), 0755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(filepath.Join(sessionDir, "before", tc.rel), nil, 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if err := os.WriteFile(filepath.Join(sessionDir, "after", tc.rel), []byte("important\n"), 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if err := os.WriteFile(filepath.Join(sessionDir, "created-files"), []byte(tc.rel+"\n"), 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			_, _, err := runUndoSession(root, sessionDir, true, strings.NewReader("y\n"), &bytes.Buffer{}, discardLogger())
			if err == nil {
				t.Errorf("undo did not refuse a removal of %s", tc.rel)
			}
			if _, statErr := os.Stat(victim); statErr != nil {
				t.Errorf("UNLINKED A PROTECTED PATH: %s is gone (%v)", tc.rel, statErr)
			}
		})
	}
}

// TestUndoCreate_ManifestNamingAnUnwalkedPathCannotDeleteIt pins the property
// that keeps the manifest from being a path source. Undo acts only on paths its
// own walk of before/ found; the manifest is consulted as a membership test on
// those paths and nothing else. A line naming a file the session never backed
// up is therefore inert, not a delete instruction.
func TestUndoCreate_ManifestNamingAnUnwalkedPathCannotDeleteIt(t *testing.T) {
	root := realTempDir(t)
	bystander := writeTempFile(t, root, "bystander.txt", "not part of any run\n")
	backupDir := applyCreate(t, root, "new.txt", "hello\n")

	// Append a path the session has no before/ snapshot for.
	manifest := filepath.Join(backupDir, "created-files")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("ReadFile manifest: %v", err)
	}
	if err := os.WriteFile(manifest, append(data, []byte("bystander.txt\n")...), 0644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}

	if _, _, err := runUndoSession(root, backupDir, true, strings.NewReader(""), &bytes.Buffer{}, discardLogger()); err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if _, statErr := os.Stat(bystander); statErr != nil {
		t.Errorf("DELETED AN UNRELATED FILE named only in the manifest: %v", statErr)
	}
}
