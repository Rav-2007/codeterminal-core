package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/editapply"
)

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(data)
}

func writeTempFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return full
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return real
}

func TestApplyEditBlocks_ConfirmYesApplies(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	blocks := []editapply.EditBlock{{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"}}

	var out bytes.Buffer
	if err := applyEditBlocks(root, blocks, nil, strings.NewReader("y\n"), &out, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	got := readFileString(t, filepath.Join(root, "foo.go"))
	if !strings.Contains(got, "func new_() {}") {
		t.Errorf("file content = %q, want it to contain the replacement", got)
	}
	if !strings.Contains(out.String(), "1 applied, 0 skipped, 0 refused") {
		t.Errorf("summary = %q, want 1 applied", out.String())
	}
}

func TestApplyEditBlocks_ConfirmDeclineDoesNotApply(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "garbage\n", "yes\n"} {
		t.Run(answer, func(t *testing.T) {
			root := realTempDir(t)
			original := "package main\n\nfunc old() {}\n"
			writeTempFile(t, root, "foo.go", original)
			blocks := []editapply.EditBlock{{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"}}

			var out bytes.Buffer
			if err := applyEditBlocks(root, blocks, nil, strings.NewReader(answer), &out, discardLogger()); err != nil {
				t.Fatalf("applyEditBlocks: %v", err)
			}

			got := readFileString(t, filepath.Join(root, "foo.go"))
			if got != original {
				t.Errorf("file was modified on input %q: got %q, want unchanged %q", answer, got, original)
			}
			if !strings.Contains(out.String(), "0 applied, 1 skipped, 0 refused") {
				t.Errorf("summary = %q, want 0 applied, 1 skipped (input %q)", out.String(), answer)
			}
		})
	}
}

func TestApplyEditBlocks_RefusedEditsNeverWrite(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n")
	writeTempFile(t, root, ".env", "SECRET=1\n")

	blocks := []editapply.EditBlock{
		{FilePath: "foo.go", Search: "func nonexistent() {}", Replace: "x"}, // not found
		{FilePath: "../outside.txt", Search: "a", Replace: "b"},             // path escape
		{FilePath: ".env", Search: "SECRET=1", Replace: "SECRET=2"},         // secret file
	}

	var out bytes.Buffer
	// No 'y' needed: all three should be refused before any confirmation prompt.
	if err := applyEditBlocks(root, blocks, nil, strings.NewReader(""), &out, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	if got := readFileString(t, filepath.Join(root, "foo.go")); got != "package main\n" {
		t.Errorf("foo.go was modified: %q", got)
	}
	if got := readFileString(t, filepath.Join(root, ".env")); got != "SECRET=1\n" {
		t.Errorf(".env was modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside.txt")); err == nil {
		t.Error("outside.txt was created outside the workspace root")
	}
	if !strings.Contains(out.String(), "0 applied, 0 skipped, 3 refused") {
		t.Errorf("summary = %q, want 3 refused", out.String())
	}
}

func TestApplyEditBlocks_MultiEditMixedOutcomes(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "a.go", "package main\n\nfunc a() {}\n")
	writeTempFile(t, root, "b.go", "package main\n\nfunc b() {}\n")

	blocks := []editapply.EditBlock{
		{FilePath: "a.go", Search: "func a() {}", Replace: "func a2() {}"}, // will accept
		{FilePath: "b.go", Search: "func b() {}", Replace: "func b2() {}"}, // will decline
		{FilePath: "a.go", Search: "func missing() {}", Replace: "x"},      // will refuse
	}

	var out bytes.Buffer
	if err := applyEditBlocks(root, blocks, nil, strings.NewReader("y\nn\n"), &out, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	if got := readFileString(t, filepath.Join(root, "a.go")); !strings.Contains(got, "func a2() {}") {
		t.Errorf("a.go = %q, want the accepted edit applied", got)
	}
	if got := readFileString(t, filepath.Join(root, "b.go")); !strings.Contains(got, "func b() {}") {
		t.Errorf("b.go = %q, want the declined edit left alone", got)
	}
	if !strings.Contains(out.String(), "1 applied, 1 skipped, 1 refused") {
		t.Errorf("summary = %q, want 1 applied, 1 skipped, 1 refused", out.String())
	}
}

func TestApplyEditBlocks_BackupRecoverable(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	blocks := []editapply.EditBlock{{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"}}

	var out bytes.Buffer
	if err := applyEditBlocks(root, blocks, nil, strings.NewReader("y\n"), &out, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".codeterminal", "backups"))
	if err != nil {
		t.Fatalf("reading backups dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 backup session dir, got %d", len(entries))
	}
	sessionDir := filepath.Join(root, ".codeterminal", "backups", entries[0].Name())

	backedUp := readFileString(t, filepath.Join(sessionDir, "before", "foo.go"))
	if backedUp != original {
		t.Errorf("backup content = %q, want the original pre-edit content %q", backedUp, original)
	}
}

func TestEditsUndo_UnchangedFileRestoresCleanly(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	blocks := []editapply.EditBlock{{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"}}

	var applyOut bytes.Buffer
	if err := applyEditBlocks(root, blocks, nil, strings.NewReader("y\n"), &applyOut, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	sessionDir := latestBackupSession(t, root)

	var undoOut bytes.Buffer
	if _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), &undoOut, discardLogger()); err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}

	got := readFileString(t, filepath.Join(root, "foo.go"))
	if got != original {
		t.Errorf("after undo, foo.go = %q, want the original %q", got, original)
	}
	if !strings.Contains(undoOut.String(), "1 file(s) restored") {
		t.Errorf("undo output = %q, want it to report 1 file restored", undoOut.String())
	}
}

// TestEditsUndo_ModifiedFileIsGuardedNotClobbered is the undo-safety
// addition: a file that was hand-edited again after the apply run must not
// be silently overwritten by undo, while any other unmodified file in the
// same session still restores normally.
func TestEditsUndo_ModifiedFileIsGuardedNotClobbered(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "changed.go", "package main\n\nfunc old() {}\n")
	writeTempFile(t, root, "untouched.go", "package main\n\nfunc keep() {}\n")

	blocks := []editapply.EditBlock{
		{FilePath: "changed.go", Search: "func old() {}", Replace: "func new_() {}"},
		{FilePath: "untouched.go", Search: "func keep() {}", Replace: "func kept() {}"},
	}

	var applyOut bytes.Buffer
	if err := applyEditBlocks(root, blocks, nil, strings.NewReader("y\ny\n"), &applyOut, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	// Simulate the user doing more hand-edits to changed.go after the apply.
	handEdited := "package main\n\nfunc new_() {}\n\nfunc addedByHand() {}\n"
	if err := os.WriteFile(filepath.Join(root, "changed.go"), []byte(handEdited), 0644); err != nil {
		t.Fatalf("simulating hand edit: %v", err)
	}

	sessionDir := latestBackupSession(t, root)

	// Decline the guarded-overwrite prompt.
	var undoOut bytes.Buffer
	if _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader("n\n"), &undoOut, discardLogger()); err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}

	if got := readFileString(t, filepath.Join(root, "changed.go")); got != handEdited {
		t.Errorf("changed.go = %q, want the hand-edited content preserved (not clobbered)", got)
	}
	if got := readFileString(t, filepath.Join(root, "untouched.go")); !strings.Contains(got, "func keep() {}") {
		t.Errorf("untouched.go = %q, want it restored to its pre-apply original (\"func keep() {}\")", got)
	}
	if !strings.Contains(undoOut.String(), "changed.go") {
		t.Errorf("undo output = %q, want it to list changed.go as guarded", undoOut.String())
	}
	if !strings.Contains(undoOut.String(), "NOT restored automatically") {
		t.Errorf("undo output = %q, want a clear warning about the guarded file", undoOut.String())
	}

	// Now retry with --force and confirm it restores even the guarded file.
	if err := os.WriteFile(filepath.Join(root, "changed.go"), []byte(handEdited), 0644); err != nil {
		t.Fatalf("re-simulating hand edit: %v", err)
	}
	var forcedOut bytes.Buffer
	if _, _, err := runUndoSession(root, sessionDir, true, strings.NewReader(""), &forcedOut, discardLogger()); err != nil {
		t.Fatalf("runUndoSession (force): %v", err)
	}
	original := "package main\n\nfunc old() {}\n"
	if got := readFileString(t, filepath.Join(root, "changed.go")); got != original {
		t.Errorf("after forced undo, changed.go = %q, want the original %q", got, original)
	}
}

func latestBackupSession(t *testing.T, root string) string {
	t.Helper()
	backupsRoot := filepath.Join(root, ".codeterminal", "backups")
	sessionDir, err := resolveBackupSession(backupsRoot, "")
	if err != nil {
		t.Fatalf("resolveBackupSession: %v", err)
	}
	return sessionDir
}
