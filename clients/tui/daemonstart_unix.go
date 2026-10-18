//go:build !windows

package main

import (
	"os"
	"syscall"
)

// detachedProcAttr puts a daemon this client starts in a session of its own,
// so closing the terminal (SIGHUP to the terminal's session) does not take it
// down with the chat.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminateProcess asks the daemon to stop. SIGTERM is the signal it shuts
// down cleanly on (daemon/main.go), stopping its embedder helper with it.
func terminateProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
