//go:build windows

package tui

import (
	"os"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW, which package syscall does not name.
const createNoWindow = 0x08000000

// detachedProcAttr starts the daemon with a console of its own that is never
// shown, in its own process group. Its own console means closing the terminal
// does not close it, and the embedder helper it starts inherits that hidden
// console instead of opening a window. DETACHED_PROCESS would leave the daemon
// with no console at all, and the helper would then get a visible one.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

// terminateProcess stops the daemon. Windows has no SIGTERM to send another
// process, so this is TerminateProcess -- what the VS Code extension does when
// it restarts the daemon it started. None of the daemon's shutdown runs; its
// embedder helper ends with it through a job object
// (daemon/helperlifetime_windows.go).
func terminateProcess(p *os.Process) error {
	return p.Kill()
}
