//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// AN ORPHAN A FORCE-KILLED DAEMON LEFT IS REAPED AT THE NEXT STARTUP. This is
// the F1 residual closed: clean shutdown reaps its own scopes, but a SIGKILL or
// crash runs no deferred code, so a scope -- and any process the build
// backgrounded in it -- would otherwise run until the machine is rebooted. The
// next daemon's startup sweep finds it by the dead pid in its name and kills the
// cgroup. Here a scope is named as a now-dead process would have named it, made
// to hold a backgrounded sleep, and the sweep must leave the sleep gone.
//
// The self-skip and alive-skip halves of the decision are proven deterministically
// by TestSelectOrphanScopesReapsOnlyDeadDaemonsScopes; this proves the end-to-end
// list-parse-kill path on a host that actually has the limiter.
//
// Neuter check: empty out reapOrphanedSandboxScopes, and the sleep survives.
func TestAnOrphanedScopeFromADeadDaemonIsReapedAtStartup(t *testing.T) {
	if !mcp.LandlockUsable() {
		t.Skip("NOT RUN: Landlock cannot be enforced on this host")
	}
	if !mcp.LimiterUsable() {
		t.Skip("NOT RUN: no usable systemd user limiter here, so no sandbox scope is ever created")
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Skip("NOT RUN: no systemctl on PATH")
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		t.Skip("NOT RUN: no systemd-run on PATH")
	}

	// A pid that is DEAD by construction: run a trivial command and wait for it to
	// exit. That stands in for the daemon that created the scope and was then
	// force-killed. Tiny pid-reuse window -- if the number was taken by a live
	// process before we could use it, skip rather than flake.
	throwaway := exec.Command("true")
	if err := throwaway.Run(); err != nil {
		t.Fatalf("spawning a throwaway process: %v", err)
	}
	deadPID := throwaway.Process.Pid
	if processAlive(deadPID) {
		t.Skip("NOT RUN: the throwaway pid was reused before the test could use it")
	}

	// A scope named as that dead daemon would have named it, holding a BACKGROUNDED
	// sleep. The foreground shell backgrounds the sleep, records its pid and exits,
	// so systemd-run returns and the sleep is reparented to a subreaper (the user
	// manager) -- exactly the orphan a build leaves behind, and, crucially, one
	// that is truly reaped rather than left a zombie when the cgroup is killed.
	// (This mirrors TestABackgroundedProcessIsReapedWhenTheCallEnds; an `exec
	// sleep` here would leave systemd-run its parent, and kill(pid,0) reports a
	// zombie as alive.)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "inner.pid")
	unit := fmt.Sprintf("mochiii-sandbox-%d-1.scope", deadPID)
	scope := exec.Command(systemdRun, "--user", "--scope", "--quiet", "--collect",
		"--unit="+unit, "--", "sh", "-c", "sleep 120 & echo $! > "+pidfile)
	scope.Env = mcp.LimiterEnv(nil)
	if err := scope.Run(); err != nil {
		t.Fatalf("creating the orphan scope: %v", err)
	}
	t.Cleanup(func() {
		teardown := exec.Command(systemctl, mcp.ScopeTeardownArgs(unit)...)
		teardown.Env = mcp.LimiterEnv(nil)
		_ = teardown.Run()
	})

	var innerPID int
	for i := 0; i < 200; i++ {
		if data, err := os.ReadFile(pidfile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && p > 0 {
				innerPID = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if innerPID == 0 || !processAlive(innerPID) {
		t.Fatalf("the orphan scope's backgrounded process never came up (pidfile %q)", pidfile)
	}
	t.Cleanup(func() { _ = syscall.Kill(innerPID, syscall.SIGKILL) })

	// The sweep a fresh daemon runs at startup.
	builtinTestServer(t).reapOrphanedSandboxScopes()

	for i := 0; i < 200; i++ {
		if !processAlive(innerPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("a process left in an orphaned scope (pid %d) survived the startup sweep", innerPID)
}

// THE INSTANT REAPER KILLS THE SCOPE THE MOMENT THE DAEMON DIES (P1, the residual
// the startup sweep only bounded). A real scope holds a backgrounded sleep; a
// reaper watches a pipe standing in for the daemon's death pipe. While the write
// end is open (the daemon "lives") the reaper must not fire; closing it (the
// daemon "dies") must reap the scope within milliseconds -- bwrap's
// --die-with-parent, without a PID namespace.
//
// Neuter check: make sandboxReaperMain return before reapSandboxScope, and the
// sleep survives the simulated death.
func TestTheInstantReaperKillsTheScopeWhenTheDaemonDies(t *testing.T) {
	if !mcp.LandlockUsable() || !mcp.LimiterUsable() {
		t.Skip("NOT RUN: no usable systemd user limiter, so no scope is created")
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Skip("NOT RUN: no systemctl")
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		t.Skip("NOT RUN: no systemd-run")
	}

	dir := t.TempDir()
	pidfile := filepath.Join(dir, "inner.pid")
	unit := fmt.Sprintf("mochiii-sandbox-%d-%d.scope", os.Getpid(), 900000+time.Now().UnixNano()%100000)
	scope := exec.Command(systemdRun, "--user", "--scope", "--quiet", "--collect",
		"--unit="+unit, "--", "sh", "-c", "sleep 120 & echo $! > "+pidfile)
	scope.Env = mcp.LimiterEnv(nil)
	if err := scope.Run(); err != nil {
		t.Fatalf("creating the scope: %v", err)
	}
	t.Cleanup(func() {
		td := exec.Command(systemctl, mcp.ScopeTeardownArgs(unit)...)
		td.Env = mcp.LimiterEnv(nil)
		_ = td.Run()
	})

	var innerPID int
	for i := 0; i < 200; i++ {
		if b, err := os.ReadFile(pidfile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
				innerPID = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if innerPID == 0 || !processAlive(innerPID) {
		t.Fatalf("the scope's process never came up (pidfile %q)", pidfile)
	}
	t.Cleanup(func() { _ = syscall.Kill(innerPID, syscall.SIGKILL) })

	// The daemon's death pipe, driven by the test.
	deathR, deathW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reaper, err := spawnSandboxReaperWith(unit, deathR)
	if err != nil {
		t.Fatalf("spawning the reaper: %v", err)
	}
	_ = deathR.Close() // the reaper holds its own dup; the parent copy is not needed
	t.Cleanup(func() { _ = reaper.Process.Kill(); _, _ = reaper.Process.Wait() })

	// The daemon still lives: the reaper must NOT fire.
	time.Sleep(300 * time.Millisecond)
	if !processAlive(innerPID) {
		t.Fatal("the reaper killed the scope while the daemon was still alive (death pipe open)")
	}

	// The daemon dies: closing the write end is the EOF the reaper waits for.
	_ = deathW.Close()
	for i := 0; i < 200; i++ {
		if !processAlive(innerPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the scope's process (pid %d) survived the daemon's simulated death", innerPID)
}
