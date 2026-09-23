//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// egressTestServer sets up a landlock+egress sandbox_exec server, or skips.
func egressTestServer(t *testing.T) (*Server, mcp.SandboxConfig) {
	t.Helper()
	if !mcp.LandlockUsable() {
		t.Skip("NOT RUN: Landlock cannot be enforced on this host")
	}
	if !mcp.EgressFilterUsable() {
		t.Skip("NOT RUN: the egress firewall does not enforce on this host")
	}
	forceBwrap(t, false)
	s := builtinTestServer(t)
	cfg := s.sandboxExecConfig()
	if mcp.ResolveMode(cfg) != mcp.SandboxLandlock {
		t.Skipf("NOT RUN: cfg resolved to %v, not landlock", mcp.ResolveMode(cfg))
	}
	t.Cleanup(func() { _ = os.RemoveAll(s.sandboxExecHome()) })
	return s, cfg
}

// THE FIREWALL MUST NOT BREAK THE TOOL. The helper hands its listener back over
// an fd that has to survive the whole launch chain -- systemd-run's scope, the
// env(1) that strips the bus, then the helper -- and if it does not, the helper
// fails to set up and the command never runs at all. So the first thing to prove
// about the egress path is that an ordinary build still works through it.
//
// Neuter check: break the fd number WrapCommand passes (--egress-fd 9), and this
// fails with "handing back the egress listener".
func TestAnEgressFilteredCommandStillRuns(t *testing.T) {
	s, _ := egressTestServer(t)
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("NOT RUN: no make on PATH")
	}
	makefile := "hello:\n\t@echo sandboxed-ok\n"
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make hello"}`))
	if err != nil {
		t.Fatalf("an egress-filtered command failed to run: %v", err)
	}
	if !strings.Contains(res.Content, "sandboxed-ok") {
		t.Errorf("the command did not run under the egress filter:\n%s", res.Content)
	}
}

// AND IT MUST ACTUALLY BLOCK. Through the real tool, a build that tries to reach
// the cloud metadata endpoint is refused, while the same tool reaching loopback
// is not -- the whole point of the firewall, measured through sandbox_exec rather
// than asserted from the unit level.
//
// curl is the probe because the landlock policy already grants /usr; the test
// skips where it is absent rather than pretending to have checked.
func TestMetadataIsRefusedThroughTheRealTool(t *testing.T) {
	s, _ := egressTestServer(t)
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("NOT RUN: no make on PATH")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("NOT RUN: no curl on PATH to probe with")
	}
	// --max-time keeps a blocked connect from stalling the 30s tool budget; a
	// refusal by the firewall is immediate (EPERM), so only a NON-blocked run
	// would need the timeout.
	makefile := "meta:\n\t@curl -s --max-time 5 -o /dev/null http://169.254.169.254/ ; echo \"curl-exit=$$?\"\n"
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make meta"}`))
	if err != nil {
		t.Fatalf("running the probe failed: %v", err)
	}
	// THE EXIT CODE DISCRIMINATES, which is what keeps this from being a vacuous
	// test on a machine that has no metadata endpoint anyway. Measured on this
	// host, unsandboxed: curl HANGS and exits 28 ("Connection timed out after
	// 5002 milliseconds"). Through the firewall it exits 7 ("Failed to connect")
	// in a tenth of a second, because connect(2) was refused with EPERM before a
	// packet was sent. So:
	//
	//	7  -> the firewall refused it            (what must happen)
	//	28 -> nothing refused it, the route just died (firewall NOT acting)
	//	0  -> it REACHED the metadata endpoint       (the vulnerability)
	if !strings.Contains(res.Content, "curl-exit=7") {
		t.Errorf("the metadata connect was not refused by the firewall (want curl-exit=7; "+
			"28 means it timed out unrefused, 0 means it got through):\n%s", res.Content)
	}
	t.Logf("metadata probe result: %s", strings.TrimSpace(res.Content))
}
