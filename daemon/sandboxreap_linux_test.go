//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"mochiii/daemon/mcp"
)

// systemctlPath resolves the reaper's binary once, to an absolute path where
// systemctl exists, so a later change to the daemon's PATH cannot redirect the
// teardown. The bare-name fallback keeps a host without systemd a no-op. (F2)
func TestSystemctlPathIsResolvedOnce(t *testing.T) {
	p := systemctlPath()
	if p == "" {
		t.Fatal("systemctlPath returned empty")
	}
	if _, err := exec.LookPath("systemctl"); err == nil && !filepath.IsAbs(p) {
		t.Errorf("systemctl is on PATH but systemctlPath returned a non-absolute %q", p)
	}
}

// A PROCESS THE COMMAND BACKGROUNDS DOES NOT OUTLIVE THE CALL. bwrap gets this
// from its PID namespace; landlock has none, so the handler names the limiter's
// scope and kills the whole cgroup when the call returns. Here a recipe
// backgrounds a `sleep`, records its host pid (there is no PID namespace, so the
// pid is real to this test), and the test checks the pid is gone afterwards.
//
// Gated on the exact path that reaps: landlock chosen AND the limiter actually
// bounding it (LimitsApply), since only then is there a scope to kill.
//
// Neuter check: drop the `defer reapSandboxScope(unit)` in builtinSandboxExec,
// and the sleep survives the call.
func TestABackgroundedProcessIsReapedWhenTheCallEnds(t *testing.T) {
	if !mcp.LandlockUsable() {
		t.Skip("NOT RUN: Landlock cannot be enforced on this host")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("NOT RUN: no make on PATH")
	}
	forceBwrap(t, false)
	s := builtinTestServer(t)
	cfg := s.sandboxExecConfig()
	if mcp.ResolveMode(cfg) != mcp.SandboxLandlock || !mcp.LimitsApply(cfg) {
		t.Skip("NOT RUN: landlock commands are not placed in a systemd scope here (no usable limiter), so there is no cgroup to reap")
	}
	home := s.sandboxExecHome()
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	// make runs each recipe line in a shell, so `$$!` reaches the shell as `$!`,
	// the pid of the backgrounded sleep. The foreground part (echo) returns at
	// once; the sleep is left running in the scope's cgroup.
	makefile := "linger:\n\t@sleep 120 & echo $$! > linger.pid\n"
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make linger"}`)); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(s.workspace, "linger.pid"))
	if err != nil {
		t.Fatalf("the recipe recorded no backgrounded pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad pid %q: %v", data, err)
	}
	// Safety net: never leave the sleep running if the reap did not work.
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// The reap ran (deferred) before builtinSandboxExec returned; SIGKILL is not
	// synchronous, so give the process a moment to be gone.
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("a process the command backgrounded (pid %d) survived the call", pid)
}
