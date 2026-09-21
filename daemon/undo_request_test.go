package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// undoViaHandler drives handleUndo exactly like handleConn does -- encode
// into a buffer, decode the single response back out.
func undoViaHandler(t *testing.T, srv *Server, req protocol.UndoRequest) protocol.UndoResponse {
	t.Helper()
	var buf bytes.Buffer
	srv.handleUndo(json.NewEncoder(&buf), req)
	var resp protocol.UndoResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decoding UndoResponse: %v (raw: %s)", err, buf.String())
	}
	return resp
}

func TestHandleUndo_RestoresAppliedEditAndReturnsCount(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	srv := &Server{logger: discardLogger(), workspace: root}

	applyResp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	if !applyResp.Applied {
		t.Fatalf("apply = %+v, want Applied", applyResp)
	}

	undoResp := undoViaHandler(t, srv, protocol.UndoRequest{Undo: true, BackupSessionDir: applyResp.BackupDir})
	if undoResp.Error != "" {
		t.Fatalf("undo = %+v, want no error", undoResp)
	}
	if undoResp.Restored != 1 {
		t.Errorf("Restored = %d, want 1", undoResp.Restored)
	}
	if len(undoResp.Guarded) != 0 {
		t.Errorf("Guarded = %v, want empty", undoResp.Guarded)
	}
	if undoResp.SessionDir != applyResp.BackupDir {
		t.Errorf("SessionDir = %q, want %q", undoResp.SessionDir, applyResp.BackupDir)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); got != original {
		t.Errorf("foo.go = %q, want restored original %q", got, original)
	}
}

// TestHandleUndo_MultiBlockSharedSessionRestoresWholeBatch is the payoff of
// the shared-backup-session work from the multi-block apply slice: undoing
// the ONE shared session dir reverts every block that was applied into it,
// not just the block whose apply happened to return that dir first.
func TestHandleUndo_MultiBlockSharedSessionRestoresWholeBatch(t *testing.T) {
	root := realTempDir(t)
	fooOriginal := "package main\n\nfunc old() {}\n"
	barOriginal := "package main\n\nfunc oldBar() {}\n"
	writeTempFile(t, root, "foo.go", fooOriginal)
	writeTempFile(t, root, "bar.go", barOriginal)
	srv := &Server{logger: discardLogger(), workspace: root}

	resp1 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	resp2 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit:             protocol.EditBlockWire{FilePath: "bar.go", Search: "func oldBar() {}", Replace: "func newBar() {}"},
		BackupSessionDir: resp1.BackupDir,
	})
	if !resp1.Applied || !resp2.Applied {
		t.Fatalf("applies failed: resp1=%+v resp2=%+v", resp1, resp2)
	}

	undoResp := undoViaHandler(t, srv, protocol.UndoRequest{Undo: true, BackupSessionDir: resp1.BackupDir})
	if undoResp.Error != "" {
		t.Fatalf("undo = %+v, want no error", undoResp)
	}
	if undoResp.Restored != 2 {
		t.Errorf("Restored = %d, want 2 (whole shared batch)", undoResp.Restored)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); got != fooOriginal {
		t.Errorf("foo.go = %q, want %q", got, fooOriginal)
	}
	if got := readFileString(t, filepath.Join(root, "bar.go")); got != barOriginal {
		t.Errorf("bar.go = %q, want %q", got, barOriginal)
	}
}

func TestHandleUndo_EmptyBackupSessionDirUsesMostRecent(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	srv := &Server{logger: discardLogger(), workspace: root}

	applyResp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	if !applyResp.Applied {
		t.Fatalf("apply = %+v, want Applied", applyResp)
	}

	undoResp := undoViaHandler(t, srv, protocol.UndoRequest{Undo: true}) // no BackupSessionDir
	if undoResp.Error != "" {
		t.Fatalf("undo = %+v, want no error", undoResp)
	}
	if undoResp.Restored != 1 {
		t.Errorf("Restored = %d, want 1", undoResp.Restored)
	}
	if undoResp.SessionDir != applyResp.BackupDir {
		t.Errorf("SessionDir = %q, want the most recent session %q", undoResp.SessionDir, applyResp.BackupDir)
	}
}

// TestHandleUndo_UnrecognizedBackupSessionDirIsHardRefused confirms the
// untrusted-path defense: a BackupSessionDir this daemon never issued for
// this workspace is refused outright, with NO fallback to "undo the most
// recent session instead" -- an unrecognized value has no sensible
// fallback, unlike the apply path's fresh-dir fallback.
func TestHandleUndo_UnrecognizedBackupSessionDirIsHardRefused(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	srv := &Server{logger: discardLogger(), workspace: root}

	applyResp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	if !applyResp.Applied {
		t.Fatalf("apply = %+v, want Applied", applyResp)
	}

	undoResp := undoViaHandler(t, srv, protocol.UndoRequest{
		Undo:             true,
		BackupSessionDir: "/tmp/not-a-real-session-dir-daemon-never-issued",
	})
	if undoResp.Error == "" {
		t.Fatalf("undo = %+v, want a hard refusal error", undoResp)
	}
	if undoResp.Restored != 0 {
		t.Errorf("Restored = %d, want 0 (hard refusal, no fallback to latest)", undoResp.Restored)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); strings.Contains(got, "func old() {}") {
		t.Errorf("foo.go = %q; want it to remain in the applied (post-edit) state, unaffected by the refused undo", got)
	}
}

// TestHandleUndo_HandEditedFileIsGuardedAndReportedHonestly is the core
// honest-partial-undo guarantee: a file changed since the apply run is
// never silently overwritten, and the caller is told exactly which file
// was left alone (Guarded), not just handed a possibly-misleading total.
func TestHandleUndo_HandEditedFileIsGuardedAndReportedHonestly(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "changed.go", "package main\n\nfunc old() {}\n")
	writeTempFile(t, root, "untouched.go", "package main\n\nfunc keep() {}\n")
	srv := &Server{logger: discardLogger(), workspace: root}

	resp1 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "changed.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	resp2 := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit:             protocol.EditBlockWire{FilePath: "untouched.go", Search: "func keep() {}", Replace: "func kept() {}"},
		BackupSessionDir: resp1.BackupDir,
	})
	if !resp1.Applied || !resp2.Applied {
		t.Fatalf("applies failed: resp1=%+v resp2=%+v", resp1, resp2)
	}

	handEdited := "package main\n\nfunc new_() {}\n\nfunc addedByHand() {}\n"
	if err := os.WriteFile(filepath.Join(root, "changed.go"), []byte(handEdited), 0644); err != nil {
		t.Fatalf("simulating hand edit: %v", err)
	}

	undoResp := undoViaHandler(t, srv, protocol.UndoRequest{Undo: true, BackupSessionDir: resp1.BackupDir})
	if undoResp.Error != "" {
		t.Fatalf("undo = %+v, want no top-level error", undoResp)
	}
	if undoResp.Restored != 1 {
		t.Errorf("Restored = %d, want 1 (only untouched.go)", undoResp.Restored)
	}
	if len(undoResp.Guarded) != 1 || undoResp.Guarded[0] != "changed.go" {
		t.Errorf("Guarded = %v, want [\"changed.go\"]", undoResp.Guarded)
	}
	if got := readFileString(t, filepath.Join(root, "changed.go")); got != handEdited {
		t.Errorf("changed.go = %q, want the hand-edited content preserved (guarded, not clobbered)", got)
	}
	if got := readFileString(t, filepath.Join(root, "untouched.go")); !strings.Contains(got, "func keep() {}") {
		t.Errorf("untouched.go = %q, want it restored to its pre-apply original", got)
	}
}

// TestHandleUndo_SecondUndoOfSameSessionReportsZeroRestored confirms the
// double-click safety property found during Phase 0: once a session has
// been restored, its files' current content matches their BEFORE snapshot,
// not the AFTER snapshot runUndoSession compares against -- so a second
// undo of the same session sees every file as guarded (changed since the
// apply run, from runUndoSession's point of view) and restores nothing,
// rather than erroring or doing anything destructive. This is naturally
// safe, not specially handled -- but a client must still surface the
// guarded file honestly rather than claim "0 restored" with no explanation.
func TestHandleUndo_SecondUndoOfSameSessionReportsZeroRestored(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	srv := &Server{logger: discardLogger(), workspace: root}

	applyResp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"},
	})
	if !applyResp.Applied {
		t.Fatalf("apply = %+v, want Applied", applyResp)
	}

	first := undoViaHandler(t, srv, protocol.UndoRequest{Undo: true, BackupSessionDir: applyResp.BackupDir})
	if first.Restored != 1 {
		t.Fatalf("first undo Restored = %d, want 1", first.Restored)
	}

	second := undoViaHandler(t, srv, protocol.UndoRequest{Undo: true, BackupSessionDir: applyResp.BackupDir})
	if second.Error != "" {
		t.Fatalf("second undo = %+v, want no error", second)
	}
	if second.Restored != 0 {
		t.Errorf("second undo Restored = %d, want 0 (nothing left in the post-apply state to restore)", second.Restored)
	}
	if len(second.Guarded) != 1 || second.Guarded[0] != "foo.go" {
		t.Errorf("second undo Guarded = %v, want [\"foo.go\"] -- its current content no longer matches the after-snapshot", second.Guarded)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); got != original {
		t.Errorf("foo.go = %q, want it to remain the restored original (unaffected by the second undo)", got)
	}
}
