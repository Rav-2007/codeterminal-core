//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// The POSIX backend for "kill the server and everything it spawned". See
// procgroup.go for what this exists to stop and which badserver mode proves it.

// processGroup has nothing to carry on POSIX: the group id IS the leader's pid,
// so a pid is all a later kill needs.
type processGroup struct{}

// prepare puts the child in its OWN process group before it starts, so a later
// negative-pid signal reaches everything it went on to spawn.
//
// helperproc.go deliberately does NOT do this, and the difference is the point:
// the embedder helper is our own binary, we know it forks nothing, and killing
// its pid is killing all of it. An MCP server is somebody else's program.
func (g *processGroup) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// adopt is a no-op: Setpgid already took effect at fork time.
func (g *processGroup) adopt(cmd *exec.Cmd) error { return nil }

// killAll SIGKILLs everything in the server's process group.
//
// Negative pid means "the group whose id is pid", which is this server's group
// because prepare set Setpgid. Errors are discarded on purpose: ESRCH means the
// group is already empty, which is the outcome being asked for.
//
// It is safe to call after the leader has exited. A process group id is not
// reused while any member remains, so a negative signal either reaches the
// survivors or reaches nobody — it cannot land on an unrelated process.
//
// THE PID GUARD IS NOT DEFENSIVE PADDING. kill(2) reads a pid of 0 as "every
// process in the CALLER's process group" — and -0 is 0 — so killAll(0) would
// SIGKILL this daemon and every one of its children. No current caller can
// reach it (Close reads cmd.Process.Pid, which is always positive), but the
// Windows sibling ignores its pid argument entirely, so a future refactor that
// starts passing 0 would be silent there and catastrophic here.
//
// Deliberately NOT covered by a test: the only way to exercise the unguarded
// path is to let it fire, and a neutered run would kill the test binary and
// everything sharing its process group.
func (g *processGroup) killAll(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// release is a no-op: a process group holds no handle.
func (g *processGroup) release() {}

// processAlive probes liveness without delivering anything. An error means the
// pid is no longer ours to signal, which is what "gone" means here.
func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// killProcess SIGKILLs one process, not its group.
func killProcess(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
