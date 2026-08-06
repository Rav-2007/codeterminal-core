package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A workspace reached through a SYMLINK must still accept edits from the MCP
// tools. It did not, and the two tools that got it wrong are the two the model
// drives.
//
// HOW THIS WAS FOUND, because it is the point. The Windows runner refused an
// ordinary edit with
//
//	PrepareEdit: path "foo.txt" resolves outside the workspace root
//
// which reads like a security refusal and is actually an outage. The mechanism
// is not Windows-specific at all:
//
//	PrepareEdit's parameter is named realWorkspaceRoot and it does NOT resolve
//	one -- it trusts the caller. resolveSafeTarget then compares that root
//	against filepath.EvalSymlinks(root/relPath) with filepath.Rel, which is a
//	BYTE comparison. Hand it an unresolved root and the two sides disagree on
//	every path, so Rel returns "..\..\…" and every edit is refused.
//
// Three of the five callers resolved first (server.go twice, apply_cmd.go, and
// the TUI at main.go:83). daemon/mcp_ast_edit.go and daemon/mcpbuiltin.go passed
// s.workspace straight through, and s.workspace is filepath.Abs only --
// validateWorkspace never calls EvalSymlinks.
//
// WHERE IT BITES IN PRODUCTION:
//   - macOS: /tmp is a symlink to /private/tmp. A workspace under /tmp, or any
//     home directory reached through one, refuses every model-proposed edit.
//   - Linux: any workspace opened through a symlink (~/work -> /mnt/data/work).
//   - Windows: 8.3 short names (C:\Users\RUNNER~1\...) and junctions, which is
//     the form CI happened to hand it.
//
// It survived because every existing test passes t.TempDir() directly, and on a
// Linux runner /tmp is a real directory -- so the unresolved root and the
// resolved one are byte-identical and the bug is invisible. This test plants the
// symlink explicitly rather than depending on the platform's /tmp.
func TestMCPTools_AcceptEditsThroughASymlinkedWorkspace(t *testing.T) {
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// The workspace the daemon is told about is a symlink TO the real one, which
	// is exactly what /tmp on macOS is.
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("NOT RUN: this platform will not create a symlink (%v); the property is untested here", err)
	}

	s := builtinTestServer(t)
	s.workspace = link // absolute, unresolved -- what validateWorkspace produces

	sink := &proposalSink{}
	res, err := s.builtinProposeEdit(context.Background(),
		json.RawMessage(`{"path":"main.go","search":"func main() {}","replace":"func main() { println(1) }"}`), sink)
	if err != nil {
		t.Fatalf("builtinProposeEdit: %v", err)
	}
	if res.IsError {
		t.Fatalf("an ordinary edit was REFUSED because the workspace is a symlink: %s\n"+
			"this is an availability outage wearing a confinement error's clothes", res.Content)
	}
	if len(sink.blocks) != 1 {
		t.Fatalf("expected one proposal, got %d", len(sink.blocks))
	}
}

// The confinement gate must STILL refuse a real escape when the root is a
// symlink -- the fix above must not have been "trust the caller and stop
// checking".
//
// THE VECTOR IS A PLANTED SYMLINK, NOT "../escape.txt", and the difference is
// the whole value of this test. The first version used ".." and was worthless:
// resolveSafeTarget rejects a lexically-escaping path at the TOP of the
// function, before it ever computes filepath.Rel. Deleting the Rel containment
// check outright left that version GREEN. Measured, not assumed -- the check was
// neutered and the test still passed.
//
// A link planted INSIDE the workspace is lexically innocent ("inside-link/…"
// contains no ".."), so it survives the early gate and can only be caught by the
// resolve-then-compare step. That is the line under test, so that is the line
// the vector has to reach.
func TestMCPTools_StillRefuseEscapesThroughASymlinkedWorkspace(t *testing.T) {
	real := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "escape.txt"), []byte("victim\n"), 0600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("NOT RUN: this platform will not create a symlink (%v)", err)
	}
	// The escape hatch, planted inside the workspace the model can see.
	if err := os.Symlink(outside, filepath.Join(real, "inside-link")); err != nil {
		t.Skipf("NOT RUN: this platform will not create a symlink (%v)", err)
	}

	s := builtinTestServer(t)
	s.workspace = link

	sink := &proposalSink{}
	res, err := s.builtinProposeEdit(context.Background(),
		json.RawMessage(`{"path":"inside-link/escape.txt","search":"victim","replace":"owned"}`), sink)
	if err != nil {
		t.Fatalf("builtinProposeEdit: %v", err)
	}
	if !res.IsError {
		t.Fatalf("ESCAPE: inside-link/escape.txt resolves OUTSIDE the workspace and was accepted; "+
			"the edit would have rewritten %s", filepath.Join(outside, "escape.txt"))
	}
}
