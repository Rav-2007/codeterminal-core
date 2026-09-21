package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// forceBwrap pins what BwrapUsable answers for the length of a test.
func forceBwrap(t *testing.T, usable bool) {
	t.Helper()
	orig := mcp.BwrapUsable
	t.Cleanup(func() { mcp.BwrapUsable = orig })
	mcp.BwrapUsable = func() bool { return usable }
}

// forceLandlock pins what LandlockUsable answers for the length of a test.
func forceLandlock(t *testing.T, usable bool) {
	t.Helper()
	orig := mcp.LandlockUsable
	t.Cleanup(func() { mcp.LandlockUsable = orig })
	mcp.LandlockUsable = func() bool { return usable }
}

// THE PROMPT NAMES THE BACKEND THE COMMAND WILL GET, WHAT IT DOES NOT HOLD, AND
// -- WHEN NOTHING CONFINES -- WHY. Each backend is forced in turn, so a
// description that stopped following the selection fails here on any machine.
//
// Neuter check: make sandboxConfinementSentence return the bwrap sentence
// unconditionally, and the landlock and none rows fail.
func TestTheSandboxExecPromptDescribesTheBackendItGets(t *testing.T) {
	s := builtinTestServer(t)

	forceBwrap(t, true)
	forceLandlock(t, true)
	if desc := s.sandboxExecDescription(); !strings.Contains(desc, "Confined to this workspace on this host.") ||
		strings.Contains(desc, "Landlock") {
		t.Errorf("bwrap: the description is not bwrap's:\n%s", desc)
	}
	if got := s.sandboxExecBackendSummary(); got != "confined by bubblewrap" {
		t.Errorf("bwrap: startup summary %q", got)
	}

	forceBwrap(t, false)
	desc := s.sandboxExecDescription()
	for _, want := range []string{
		"Confined to this workspace by Landlock",
		"not your home folder or /tmp",
		"cannot open Unix sockets",
		"it can see your other running programs",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("landlock: the description does not say %q:\n%s", want, desc)
		}
	}
	if signals := strings.Contains(desc, "send them signals"); signals != (mcp.LandlockABI() < 6) {
		t.Errorf("landlock ABI %d: the description says it can signal other programs=%v:\n%s",
			mcp.LandlockABI(), signals, desc)
	}
	if got := s.sandboxExecBackendSummary(); !strings.HasPrefix(got, "confined by landlock (bwrap ") {
		t.Errorf("landlock: the startup summary does not say why bwrap was passed over: %q", got)
	}

	forceLandlock(t, false)
	desc = s.sandboxExecDescription()
	for _, want := range []string{"NOT confined on this host (", "Landlock is unavailable", "full privileges"} {
		if !strings.Contains(desc, want) {
			t.Errorf("none: the description does not say %q:\n%s", want, desc)
		}
	}
	if got := s.sandboxExecBackendSummary(); !strings.HasPrefix(got, "NOT confined (") {
		t.Errorf("none: startup summary %q", got)
	}
}

// THE HANDLER, UNDER LANDLOCK, FOR REAL -- the failing case from 2026-09-21,
// reproduced on any Landlock kernel by forcing bwrap out of the way. Through
// the real systemd-run limiter where there is one, the real helper, and the
// real toolchain:
//
//   - a build runs (go version)
//   - a Makefile reaching for a file outside the sandbox, or listing /tmp, is refused
//   - the command's TMPDIR is its own, under the sandbox home, and gone afterwards
func TestSandboxExecUnderLandlockRunsBuildsAndRefusesEscapes(t *testing.T) {
	if !mcp.LandlockUsable() {
		t.Skip("NOT RUN: Landlock cannot be enforced on this host")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("NOT RUN: no go on PATH")
	}
	forceBwrap(t, false)
	s := builtinTestServer(t)
	home := s.sandboxExecHome()
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if !s.sandboxExecConfined() {
		t.Fatal("with bwrap out of the way and Landlock usable, sandbox_exec is still unconfined")
	}

	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"go version"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "go version go") {
		t.Fatalf("go did not run under landlock: %q", res.Content)
	}

	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("NOT RUN (the escape half): no make on PATH")
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("the-secret-contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	makefile := fmt.Sprintf("leak:\n\t-cat %s\n\t@ls /tmp >/dev/null 2>&1 && echo tmp-listed || echo tmp-refused\n\t@echo TMPDIR=$$TMPDIR\n", secret)
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make leak"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "the-secret-contents") {
		t.Fatalf("a Makefile read a file outside the sandbox:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "tmp-refused") {
		t.Errorf("a Makefile could list /tmp, which holds other programs' files:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "TMPDIR="+filepath.Join(home, ".tmp", "call-")) {
		t.Errorf("the command's TMPDIR is not its own directory under the sandbox home:\n%s", res.Content)
	}
	if left, _ := os.ReadDir(filepath.Join(home, ".tmp")); len(left) != 0 {
		t.Errorf("the per-call TMPDIR outlived the command: %v", left)
	}
}
