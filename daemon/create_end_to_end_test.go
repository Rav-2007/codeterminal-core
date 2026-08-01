package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/editapply"
)

// TestEndToEnd_EditAndCreateApplyThenUndo is the test whose absence let Fix 7
// ship broken.
//
// Fix 7 gave the engine file creation and was verified by calling PrepareEdit
// directly. Every one of those tests passed. Meanwhile the parser sitting in
// front of every shipped client rejected the same blocks before the engine was
// reached, one create block failed the entire response and took a valid sibling
// edit down with it, and undoing a created file left a 0-byte file behind while
// reporting success. Three defects, none of which a single engine-layer test
// could see, because none of them are in the engine.
//
// So this test uses no engine-layer shortcut anywhere. It starts from a raw
// model response -- the actual string a model produces -- and walks the whole
// production path:
//
//	editapply.ParseEditBlocks   the parser every client enters through
//	applyEditBlocks             the CLI's own confirm loop (`edits apply`)
//	runUndoSession              the undo core the CLI and daemon both call
//
// It asserts the disk AND the report at every step, because a report that
// disagrees with the disk is the specific failure this whole tier exists to
// eliminate.
func TestEndToEnd_EditAndCreateApplyThenUndo(t *testing.T) {
	root := realTempDir(t)
	const originalMain = "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "main.go", originalMain)

	response := strings.Join([]string{
		"I renamed the function and added a note file.",
		"",
		"path: main.go",
		"<<<<<<< SEARCH",
		"func old() {}",
		"=======",
		"func renamed() {}",
		">>>>>>> REPLACE",
		"",
		"path: docs/notes.md",
		"<<<<<<< SEARCH",
		"=======",
		"# Notes",
		"",
		"Renamed old() to renamed().",
		">>>>>>> REPLACE",
	}, "\n")
	const wantCreated = "# Notes\n\nRenamed old() to renamed()."

	// --- parse: the production parser, not a hand-built []EditBlock ---
	blocks, rejected := editapply.ParseEditBlocks(response)
	if len(rejected) != 0 {
		t.Fatalf("parser refused %+v; both blocks are well-formed", rejected)
	}
	if len(blocks) != 2 {
		t.Fatalf("parsed %d block(s), want 2 (the edit and the create)", len(blocks))
	}

	// --- apply: the CLI's confirm loop ---
	var applyOut bytes.Buffer
	if err := applyEditBlocks(root, blocks, rejected, strings.NewReader("y\ny\n"), &applyOut, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v\n%s", err, applyOut.String())
	}

	if got := readFileString(t, filepath.Join(root, "main.go")); got != "package main\n\nfunc renamed() {}\n" {
		t.Errorf("main.go = %q, want the edit applied", got)
	}
	if got := readFileString(t, filepath.Join(root, "docs/notes.md")); got != wantCreated {
		t.Errorf("docs/notes.md = %q, want %q", got, wantCreated)
	}
	if !strings.Contains(applyOut.String(), "2 applied, 0 skipped, 0 refused") {
		t.Errorf("apply report = %q, want 2 applied", applyOut.String())
	}

	sessionDir, err := resolveBackupSession(filepath.Join(root, ".codeterminal", "backups"), "")
	if err != nil {
		t.Fatalf("resolveBackupSession: %v", err)
	}

	// --- undo: the shared undo core ---
	var undoOut bytes.Buffer
	restored, _, guarded, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), &undoOut, discardLogger())
	if err != nil {
		t.Fatalf("runUndoSession: %v\n%s", err, undoOut.String())
	}
	if len(guarded) != 0 {
		t.Errorf("guarded = %v, want none: neither file changed after the apply", guarded)
	}

	// The edit reverts.
	if got := readFileString(t, filepath.Join(root, "main.go")); got != originalMain {
		t.Errorf("main.go = %q, want the original %q back", got, originalMain)
	}
	// The created file is GONE -- not restored as 0 bytes.
	if _, statErr := os.Stat(filepath.Join(root, "docs/notes.md")); !os.IsNotExist(statErr) {
		t.Errorf("THE LIE: docs/notes.md still exists after undo (stat err = %v)", statErr)
	}

	// And the report matches what is actually on disk, in both directions.
	report := undoOut.String()
	if restored != 2 {
		t.Errorf("reverted count = %d, want 2", restored)
	}
	if !strings.Contains(report, "restored main.go") {
		t.Errorf("report = %q, want it to name the restore", report)
	}
	if !strings.Contains(report, "removed docs/notes.md (created by this apply run)") {
		t.Errorf("report = %q, want it to name the removal and why", report)
	}
	if strings.Contains(report, "restored docs/notes.md") {
		t.Errorf("report = %q, claims a file was restored that was deleted", report)
	}
	if !strings.Contains(report, "1 restored, 1 removed") {
		t.Errorf("report = %q, want the counts broken down", report)
	}
}

// TestEndToEnd_CreateRefusedDoesNotCostTheEdit is the same walk with the
// adversarial half: the create block targets a protected path. The edit must
// still apply, the create must still be refused, and undo must revert exactly
// the one file that actually changed -- no phantom entry for the file that was
// never created.
func TestEndToEnd_CreateRefusedDoesNotCostTheEdit(t *testing.T) {
	root := realTempDir(t)
	const originalMain = "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "main.go", originalMain)

	response := strings.Join([]string{
		"path: main.go",
		"<<<<<<< SEARCH",
		"func old() {}",
		"=======",
		"func renamed() {}",
		">>>>>>> REPLACE",
		"",
		"path: .git/hooks/pre-commit",
		"<<<<<<< SEARCH",
		"=======",
		"#!/bin/sh",
		"echo pwned",
		">>>>>>> REPLACE",
	}, "\n")

	blocks, rejected := editapply.ParseEditBlocks(response)
	var applyOut bytes.Buffer
	if err := applyEditBlocks(root, blocks, rejected, strings.NewReader("y\ny\n"), &applyOut, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(root, ".git", "hooks", "pre-commit")); statErr == nil {
		t.Fatal("CREATED A GIT HOOK through the full production path")
	}
	if got := readFileString(t, filepath.Join(root, "main.go")); got != "package main\n\nfunc renamed() {}\n" {
		t.Errorf("main.go = %q, want the legitimate edit applied anyway", got)
	}
	if !strings.Contains(applyOut.String(), "1 applied, 0 skipped, 1 refused") {
		t.Errorf("apply report = %q, want 1 applied and 1 refused", applyOut.String())
	}

	sessionDir, err := resolveBackupSession(filepath.Join(root, ".codeterminal", "backups"), "")
	if err != nil {
		t.Fatalf("resolveBackupSession: %v", err)
	}
	var undoOut bytes.Buffer
	restored, _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), &undoOut, discardLogger())
	if err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if restored != 1 {
		t.Errorf("reverted = %d, want exactly 1 (the refused create left nothing to revert)", restored)
	}
	if got := readFileString(t, filepath.Join(root, "main.go")); got != originalMain {
		t.Errorf("main.go = %q, want the original back", got)
	}
}
