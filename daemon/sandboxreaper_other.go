//go:build !linux

package main

import "os/exec"

// The instant-reaper is a no-op off Linux: only the Landlock backend puts a
// command in a named systemd scope, and that backend is Linux-only. See
// sandboxreaper_linux.go for the real reaper.

func spawnSandboxReaper(string) (*exec.Cmd, error) { return nil, nil }

func stopSandboxReaper(*exec.Cmd) {}

func sandboxReaperMain([]string) int { return 0 }
