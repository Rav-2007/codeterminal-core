package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// END TO END: npm must still RUN after toolchainExposure narrowed the toolchain
// grant. nvm's `npm` is a symlink into lib/node_modules/npm, so the old
// single-resolved-root rule exposed npm's package dir and lost `node`; the new
// rule also exposes the version dir that holds node. This runs the real
// `npm --version` through the real sandbox and requires a version string back --
// which can only happen if node was found inside the sandbox.
//
// Run under BOTH backends that confine on this host, because each computes the
// exposure through its own bind/grant path (bwrap --ro-bind, landlock accessReadExec).
func TestSandboxExec_NpmRunsUnderNarrowedExposure(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("NOT RUN: no npm on PATH")
	}

	run := func(t *testing.T) {
		s := builtinTestServer(t)
		home := s.sandboxExecHome()
		t.Cleanup(func() { _ = os.RemoveAll(home) })
		if !s.sandboxExecConfined() {
			t.Skip("NOT RUN: no sandbox backend confines on this host")
		}
		res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"npm --version"}`))
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("npm did not run under the sandbox (node not found via the toolchain exposure?): %q", res.Content)
		}
		if !strings.ContainsAny(res.Content, "0123456789") || !strings.Contains(res.Content, ".") {
			t.Fatalf("npm --version did not return a version: %q", res.Content)
		}
	}

	t.Run("default backend", run)

	if mcp.LandlockUsable() {
		t.Run("landlock", func(t *testing.T) {
			forceBwrap(t, false) // push past bwrap to the landlock backend
			run(t)
		})
	}
}
