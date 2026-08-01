package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// applyEditViaHandler drives handleApplyEdit exactly like handleConn does —
// encode into a buffer, decode the single response back out — without
// standing up a real net.Conn, since handleApplyEdit itself takes only the
// encoder and the already-decoded request.
func applyEditViaHandler(t *testing.T, srv *Server, req protocol.ApplyEditRequest) protocol.ApplyEditResponse {
	t.Helper()
	var buf bytes.Buffer
	srv.handleApplyEdit(json.NewEncoder(&buf), req)
	var resp protocol.ApplyEditResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decoding ApplyEditResponse: %v (raw: %s)", err, buf.String())
	}
	return resp
}

// TestHandleApplyEdit_EmptyBackupSessionDirCreatesFreshDirPerRequest is the
// backward-compat check: a client that never sends BackupSessionDir (the
// zero value, matching every client before this field existed) must see
// exactly today's behavior — a brand-new backup session directory for
// every ApplyEditRequest, never reused across calls.
func TestHandleApplyEdit_EmptyBackupSessionDirCreatesFreshDirPerRequest(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	writeTempFile(t, root, "bar.go", "package main\n\nfunc oldBar() {}\n")
	srv := &Server{logger: discardLogger(), workspace: root}

	resp1 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	if !resp1.Applied || resp1.BackupDir == "" {
		t.Fatalf("first apply = %+v, want Applied with a BackupDir", resp1)
	}

	resp2 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "bar.go", Search: "func oldBar() {}", Replace: "func newBar() {}"},
	})
	if !resp2.Applied || resp2.BackupDir == "" {
		t.Fatalf("second apply = %+v, want Applied with a BackupDir", resp2)
	}

	if resp1.BackupDir == resp2.BackupDir {
		t.Errorf("both requests got backup dir %q, want two distinct dirs when BackupSessionDir is never sent", resp1.BackupDir)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".codeterminal", "backups"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("backups dir entries = %v (err=%v), want exactly 2 session dirs", entries, err)
	}
}

// TestHandleApplyEdit_SharedBackupSessionDirReusesSameSessionAcrossBlocks is
// the main new behavior: a client that echoes back the BackupDir from its
// first ApplyEditResponse as the second request's BackupSessionDir gets
// both blocks' before/after snapshots landed under the SAME session
// directory — the VS Code sequential-review flow's mechanism for sharing
// one backup session across a whole multi-block run, mirroring the
// TUI/CLI's in-process backupDir reuse.
func TestHandleApplyEdit_SharedBackupSessionDirReusesSameSessionAcrossBlocks(t *testing.T) {
	root := realTempDir(t)
	fooOriginal := "package main\n\nfunc old() {}\n"
	barOriginal := "package main\n\nfunc oldBar() {}\n"
	writeTempFile(t, root, "foo.go", fooOriginal)
	writeTempFile(t, root, "bar.go", barOriginal)
	srv := &Server{logger: discardLogger(), workspace: root}

	resp1 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	if !resp1.Applied || resp1.BackupDir == "" {
		t.Fatalf("first apply = %+v, want Applied with a BackupDir", resp1)
	}

	resp2 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit:             protocol.EditBlockWire{FilePath: "bar.go", Search: "func oldBar() {}", Replace: "func newBar() {}"},
		BackupSessionDir: resp1.BackupDir,
	})
	if !resp2.Applied {
		t.Fatalf("second apply = %+v, want Applied", resp2)
	}
	if resp2.BackupDir != resp1.BackupDir {
		t.Errorf("second apply backup dir = %q, want it to match the first apply's %q", resp2.BackupDir, resp1.BackupDir)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".codeterminal", "backups"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("backups dir entries = %v (err=%v), want exactly ONE shared session dir", entries, err)
	}

	sessionDir := filepath.Join(root, ".codeterminal", "backups", entries[0].Name())
	if got := readFileString(t, filepath.Join(sessionDir, "before", "foo.go")); got != fooOriginal {
		t.Errorf("before/foo.go = %q, want the pre-edit original %q", got, fooOriginal)
	}
	if got := readFileString(t, filepath.Join(sessionDir, "before", "bar.go")); got != barOriginal {
		t.Errorf("before/bar.go = %q, want the pre-edit original %q", got, barOriginal)
	}
	if got := readFileString(t, filepath.Join(sessionDir, "after", "foo.go")); !strings.Contains(got, "func new_() {}") {
		t.Errorf("after/foo.go = %q, want the applied edit", got)
	}
	if got := readFileString(t, filepath.Join(sessionDir, "after", "bar.go")); !strings.Contains(got, "func newBar() {}") {
		t.Errorf("after/bar.go = %q, want the applied edit", got)
	}

	// The whole point of a shared session: `edits undo` on this ONE dir
	// reverts BOTH blocks in a single restore run.
	var undoOut bytes.Buffer
	if _, _, _, err := runUndoSession(root, sessionDir, false, strings.NewReader(""), &undoOut, discardLogger()); err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); got != fooOriginal {
		t.Errorf("foo.go after undo = %q, want restored original %q", got, fooOriginal)
	}
	if got := readFileString(t, filepath.Join(root, "bar.go")); got != barOriginal {
		t.Errorf("bar.go after undo = %q, want restored original %q", got, barOriginal)
	}
	if !strings.Contains(undoOut.String(), "2 file(s) restored") {
		t.Errorf("undo output = %q, want both files reported restored", undoOut.String())
	}
}

// TestHandleApplyEdit_UnrecognizedBackupSessionDirFallsBackToFreshDir covers
// the safety fallback: a BackupSessionDir that doesn't resolve to a real,
// workspace-confined session directory (never actually issued by this
// daemon for this workspace) is ignored rather than trusted blindly — the
// request still succeeds, just with a freshly created session dir, exactly
// as if the field had been omitted.
func TestHandleApplyEdit_UnrecognizedBackupSessionDirFallsBackToFreshDir(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	srv := &Server{logger: discardLogger(), workspace: root}

	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit:             protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
		BackupSessionDir: "/tmp/not-a-real-session-dir-daemon-never-issued",
	})
	if !resp.Applied {
		t.Fatalf("apply = %+v, want Applied (falls back to a fresh dir rather than failing)", resp)
	}
	if resp.BackupDir == "/tmp/not-a-real-session-dir-daemon-never-issued" {
		t.Errorf("BackupDir = %q, want a fresh workspace-confined dir, not the untrusted input echoed back", resp.BackupDir)
	}
	if !strings.HasPrefix(resp.BackupDir, filepath.Join(root, ".codeterminal", "backups")) {
		t.Errorf("BackupDir = %q, want it confined under %s", resp.BackupDir, filepath.Join(root, ".codeterminal", "backups"))
	}
}
