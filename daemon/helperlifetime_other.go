//go:build !windows

package main

import (
	"log"
	"os/exec"
)

// startHelperProcess starts the helper. Here a daemon is stopped with SIGTERM,
// and its shutdown stops the helper (HelperProcess.Stop). Windows, where every
// stop is TerminateProcess, binds the helper to the daemon instead: see
// helperlifetime_windows.go.
func startHelperProcess(cmd *exec.Cmd, _ *log.Logger) error { return cmd.Start() }
