//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"mochiii/daemon/mcp"
)

// INSTANT REAPING, bwrap's --die-with-parent without a PID namespace.
//
// The startup sweep (reapOrphanedSandboxScopes) bounds a hard-killed daemon's
// leftover to the NEXT daemon start. This closes the window to nothing: for each
// landlock call in a scope, a tiny reaper process outlives the daemon and kills
// the scope the INSTANT the daemon dies, detected by EOF on a pipe the daemon
// holds open for its whole life. A clean shutdown or a normal call end kills the
// reaper before it can fire (it only ever acts on the death-EOF), so it can never
// reap a live thing.

// sandboxReaperArgUnit is how the reaper is told which scope it guards.
const sandboxReaperArgUnit = "--unit="

// sandboxDeathPipe is the daemon-lifetime death pipe, created once. The daemon
// holds the WRITE end for its whole life; any death -- exit, panic, SIGKILL,
// crash -- closes it and every reader gets EOF. The READ end is what each reaper
// watches (dup'd into it via ExtraFiles). Both ends are O_CLOEXEC (Go's default
// for os.Pipe), so no exec'd child -- not the command, not systemd-run -- ever
// inherits a write end that would hold the pipe open past the daemon's death.
var sandboxDeathPipe = sync.OnceValues(func() (*os.File, *os.File) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil
	}
	return r, w
})

// spawnSandboxReaper starts the reaper that guards unit. It re-executes this
// daemon binary as `<self> __sandbox-reaper --unit=<unit>`, hands it the death
// pipe's read end as fd 3, and detaches it into its own session so a
// group-directed kill of the daemon does not also take the reaper before it can
// act. Returns nil (and no error) when there is no death pipe -- the startup
// sweep remains the backstop.
func spawnSandboxReaper(unit string) (*exec.Cmd, error) {
	deathR, deathW := sandboxDeathPipe()
	if deathR == nil || deathW == nil {
		return nil, nil
	}
	return spawnSandboxReaperWith(unit, deathR)
}

// spawnSandboxReaperWith is spawnSandboxReaper with the death-pipe read end
// supplied, so a test can drive the reaper with a pipe it closes itself rather
// than the daemon-lifetime one.
func spawnSandboxReaperWith(unit string, deathR *os.File) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, sandboxReaperArg, sandboxReaperArgUnit+unit)
	// The bus, so the reaper's systemctl kill reaches the user manager -- the same
	// environment reapSandboxScope needs.
	cmd.Env = mcp.LimiterEnv(nil)
	cmd.ExtraFiles = []*os.File{deathR} // becomes fd 3 in the reaper
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// stopSandboxReaper ends the reaper on the normal path (the deferred
// reapSandboxScope already tore the scope down). Killing it is safe: the reaper
// only reaps on death-EOF, and the unit name is unique per call and never
// reused, so even a reaper that fired anyway would only kill an already-gone
// unit.
func stopSandboxReaper(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// sandboxReaperMain is the reaper process. It blocks reading the death pipe (fd
// 3); when the daemon dies the write end closes and the read returns EOF, and the
// reaper kills the scope with the same mechanism the deferred reap uses. Any
// other outcome (a stray byte, a read error) is treated as a signal to reap too:
// failing safe here means a leftover build process is stopped, never left.
func sandboxReaperMain(args []string) int {
	var unit string
	for _, a := range args {
		if strings.HasPrefix(a, sandboxReaperArgUnit) {
			unit = strings.TrimPrefix(a, sandboxReaperArgUnit)
		}
	}
	if unit == "" {
		fmt.Fprintln(os.Stderr, "mochiii sandbox-reaper: no --unit")
		return 2
	}
	death := os.NewFile(3, "death")
	if death == nil {
		return 2
	}
	// Block until the daemon's write end closes. EOF is the daemon's death.
	_, _ = io.Copy(io.Discard, death)
	reapSandboxScope(unit)
	return 0
}
