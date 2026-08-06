package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE DOOR THE USER COMES IN.
//
// sandbox_test.go's six tests all stub lookPath and assert on the ARGUMENT LIST
// WrapCommand builds. Not one of them ever executed bwrap — and so an argument
// bwrap does not accept sat in that list through every test run and through a
// whole QA campaign that recorded the sandbox as CONFIRMED. The flag was
// --nosuid, which is a mount(2) option rather than a bwrap flag; bwrap answered
// "Unknown option --nosuid" and refused the entire invocation, so every
// sandboxed command failed before running a single instruction.
//
// This is PART 0 rule 4 of this project's own discipline — "a fix's test must
// enter through the same door the user does" — and it is the second time that
// rule has been learned here: Tier 2's Fix 7 passed every engine-layer test
// while being unreachable from every shipped client.
//
// These tests RUN bwrap. They skip where it is not installed, which is honest,
// and they are the reason a future invalid flag fails a test rather than a user.

func requireBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap is not installed; this test needs a real sandbox to execute")
	}
}

// Every flag WrapCommand emits must be one bwrap accepts. A single unknown
// option makes the whole sandbox a no-op that fails closed into "broken".
func TestBubblewrap_ArgumentsAreAcceptedByRealBwrap(t *testing.T) {
	requireBwrap(t)

	ws := t.TempDir()
	cmd, args, err := WrapCommand("/bin/true", nil, SandboxConfig{
		Mode:          SandboxBubblewrap,
		WorkspaceRoot: ws,
		AllowNetwork:  true,
	})
	if err != nil {
		t.Fatalf("WrapCommand: %v", err)
	}
	if cmd != "bwrap" {
		t.Fatalf("expected bwrap, got %q", cmd)
	}

	out, err := exec.Command(cmd, args...).CombinedOutput()
	if strings.Contains(string(out), "Unknown option") {
		t.Errorf("bwrap rejected an argument WrapCommand generated: %s\nfull args: %v", out, args)
	}
	if err != nil && !strings.Contains(string(out), "Unknown option") {
		t.Logf("bwrap exited %v (environment-dependent); output: %s", err, out)
	}
}

// The sandbox has to actually WORK, not merely be accepted: a command inside it
// must run and produce output.
func TestBubblewrap_RunsACommandAndReturnsItsOutput(t *testing.T) {
	requireBwrap(t)

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("SENTINEL\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd, args, err := WrapCommand("/bin/cat", []string{filepath.Join(ws, "hello.txt")}, SandboxConfig{
		Mode:          SandboxBubblewrap,
		WorkspaceRoot: ws,
		AllowNetwork:  true,
	})
	if err != nil {
		t.Fatalf("WrapCommand: %v", err)
	}

	out, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("running inside the sandbox failed: %v\noutput: %s\nargs: %v", err, out, args)
	}
	if !strings.Contains(string(out), "SENTINEL") {
		t.Errorf("the sandboxed command did not read the workspace file; got %q", out)
	}
}

// The confinement claim itself: a path OUTSIDE the workspace must not be
// readable from inside the sandbox. Without this, "sandboxed" means only
// "wrapped in bwrap", which is not the same thing.
func TestBubblewrap_CannotReadOutsideTheWorkspace(t *testing.T) {
	requireBwrap(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("MUST-NOT-BE-READABLE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()

	cmd, args, err := WrapCommand("/bin/cat", []string{secret}, SandboxConfig{
		Mode:          SandboxBubblewrap,
		WorkspaceRoot: ws,
		AllowNetwork:  true,
	})
	if err != nil {
		t.Fatalf("WrapCommand: %v", err)
	}

	out, _ := exec.Command(cmd, args...).CombinedOutput()
	if strings.Contains(string(out), "MUST-NOT-BE-READABLE") {
		t.Errorf("a file outside the workspace was readable from inside the sandbox; confinement is not in force.\noutput: %s", out)
	}
}

// --unshare-net must actually remove the network, since it is the control that
// turns "can read a secret" into "cannot send it anywhere".
func TestBubblewrap_UnshareNetRemovesTheNetwork(t *testing.T) {
	requireBwrap(t)
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("`ip` is not installed; needed to observe the network namespace")
	}

	ws := t.TempDir()
	cmd, args, err := WrapCommand("/usr/sbin/ip", []string{"addr"}, SandboxConfig{
		Mode:          SandboxBubblewrap,
		WorkspaceRoot: ws,
		AllowNetwork:  false, // the flag under test
	})
	if err != nil {
		t.Fatalf("WrapCommand: %v", err)
	}
	out, _ := exec.Command(cmd, args...).CombinedOutput()

	// In a fresh network namespace only loopback exists. Seeing a real
	// interface means --unshare-net did not take effect.
	for _, iface := range []string{"eth0", "wlan0", "enp", "wlp"} {
		if strings.Contains(string(out), iface) {
			t.Errorf("interface %q is visible inside a supposedly network-isolated sandbox:\n%s", iface, out)
		}
	}
}
