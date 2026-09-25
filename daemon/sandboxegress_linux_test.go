//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// egressBackend is one backend whose egress firewall can be proven on this host.
type egressBackend struct {
	name  string
	mode  mcp.SandboxMode
	force func(t *testing.T)
}

// egressBackends lists the backends whose firewall actually enforces here, so
// every test below runs against each of them.
//
// BOTH ARE EXERCISED WHERE BOTH WORK, and that is not redundancy: under bwrap the
// trapped process sits inside a PID and user namespace and the supervisor has to
// reach across both, which the Landlock path never asks of it. One passing says
// nothing about the other.
//
// The probes are evaluated BEFORE any backend is stubbed, because each caches its
// answer for the life of the process -- asking while stubbed would freeze the
// wrong answer in for every later test.
func egressBackends() []egressBackend {
	bwrapOK := mcp.BwrapUsable() && mcp.BwrapEgressUsable()
	landlockOK := mcp.LandlockUsable() && mcp.EgressFilterUsable()
	var out []egressBackend
	if bwrapOK {
		out = append(out, egressBackend{"bubblewrap", mcp.SandboxBubblewrap, func(t *testing.T) {
			forceBwrap(t, true)
		}})
	}
	if landlockOK {
		out = append(out, egressBackend{"landlock", mcp.SandboxLandlock, func(t *testing.T) {
			forceBwrap(t, false)
			forceLandlock(t, true)
		}})
	}
	return out
}

// egressTestServer sets up a sandbox_exec server on the given backend, with the
// egress firewall on, or skips.
func egressTestServer(t *testing.T, b egressBackend) *Server {
	t.Helper()
	b.force(t)
	s := builtinTestServer(t)
	cfg := s.sandboxExecConfig()
	if mcp.ResolveMode(cfg) != b.mode {
		t.Skipf("NOT RUN: cfg resolved to %v, not %v", mcp.ResolveMode(cfg), b.mode)
	}
	t.Cleanup(func() { _ = os.RemoveAll(s.sandboxExecHome()) })
	return s
}

// eachEgressBackend runs fn for every backend whose firewall enforces here.
func eachEgressBackend(t *testing.T, fn func(t *testing.T, s *Server)) {
	t.Helper()
	backends := egressBackends()
	if len(backends) == 0 {
		const why = "no backend on this host can enforce the egress firewall"
		// A FIREWALL THAT STOPS ENFORCING MUST NOT GO QUIET. Every probe below
		// fails closed, so a regression in the mechanism turns this whole suite
		// into skips rather than failures -- which is exactly how a security
		// feature dies unnoticed. Where the environment promises a sandbox, say so
		// loudly instead. (Measured: neutering readTargetMem does precisely this.)
		if os.Getenv("MOCHIII_REQUIRE_SANDBOX") != "" {
			t.Fatalf("MOCHIII_REQUIRE_SANDBOX is set, so the egress firewall MUST be exercised here, but %s. "+
				"Either the host lost the capability or the firewall stopped enforcing; skipping would "+
				"silently drop the only coverage the metadata block has.", why)
		}
		t.Skip("NOT RUN: " + why)
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) { fn(t, egressTestServer(t, b)) })
	}
}

// THE FIREWALL MUST NOT BREAK THE TOOL. The helper hands its listener back over
// an fd that has to survive the whole launch chain -- systemd-run's scope, the
// env(1) that strips the bus, and under bwrap the namespaces too -- and if it
// does not, the helper fails to set up and the command never runs at all. So the
// first thing to prove about the egress path is that an ordinary build still
// works through it.
//
// Neuter check: break the fd number WrapCommand passes (--egress-fd 9), and this
// fails with "handing back the egress listener".
func TestAnEgressFilteredCommandStillRuns(t *testing.T) {
	eachEgressBackend(t, func(t *testing.T, s *Server) {
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
	})
}

// AND IT MUST ACTUALLY BLOCK. Through the real tool, a build that tries to reach
// the cloud metadata endpoint is refused -- the whole point of the firewall,
// measured through sandbox_exec rather than asserted from the unit level.
//
// curl is the probe because both backends already grant /usr; the test skips
// where it is absent rather than pretending to have checked.
func TestMetadataIsRefusedThroughTheRealTool(t *testing.T) {
	eachEgressBackend(t, func(t *testing.T, s *Server) {
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
			return
		}
		t.Logf("metadata probe result: %s", strings.TrimSpace(res.Content))
	})
}

// AND IT MUST NOT DENY WHAT IT HAS NO QUARREL WITH -- from a GRANDCHILD of the
// command, which is the shape every real build has.
//
// This is the regression the design's one fragile assumption would cause. The
// supervisor must read the destination out of the connecting process, and that
// needs ptrace access to it. Yama's PR_SET_PTRACER grant, measured, is NOT
// inherited by forked children -- so if the access rode on that grant alone, the
// command itself would be judged correctly while every child it forks got EPERM
// on connect, and `make` would lose the network for its recipes. What actually
// carries the access is the daemon being an ANCESTOR of the whole tree, which
// yama honours to any depth and across bwrap's namespaces.
//
// So: make forks a shell, the shell forks curl, and curl must reach a listener
// this test owns. A local listener, not the internet, so the test measures the
// firewall and not the network.
//
// Neuter check: make readTargetMem return an error unconditionally, and this
// flips to curl-exit=7 -- the firewall failing closed on a process it cannot read.
func TestAForkedGrandchildStillReachesAnAllowedAddress(t *testing.T) {
	eachEgressBackend(t, func(t *testing.T, s *Server) {
		if _, err := exec.LookPath("make"); err != nil {
			t.Skip("NOT RUN: no make on PATH")
		}
		if _, err := exec.LookPath("curl"); err != nil {
			t.Skip("NOT RUN: no curl on PATH to probe with")
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("reached"))
		})}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		// make -> sh -> curl: curl is a GRANDCHILD of the helper, and never called
		// PR_SET_PTRACER itself.
		makefile := fmt.Sprintf("fetch:\n\t@curl -s --max-time 5 -o /dev/null http://%s/ ; echo \"curl-exit=$$?\"\n",
			ln.Addr().String())
		if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"), []byte(makefile), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make fetch"}`))
		if err != nil {
			t.Fatalf("running the probe failed: %v", err)
		}
		if !strings.Contains(res.Content, "curl-exit=0") {
			t.Errorf("a forked grandchild could not reach an ALLOWED address (want curl-exit=0; "+
				"7 means the firewall refused a connect it had no reason to):\n%s", res.Content)
			return
		}
		t.Logf("forked-grandchild probe result: %s", strings.TrimSpace(res.Content))
	})
}
