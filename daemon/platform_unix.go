//go:build unix

package main

import (
	"os"
	"syscall"
)

// The POSIX half of the two syscall-level primitives this package needs. See
// platform.go for what each is for; the Windows half is platform_windows.go.

// openNoFollow opens path with O_NOFOLLOW ORed into flag, so a symlink at the
// final path component is refused (ELOOP) rather than followed. The refusal is
// atomic — open(2) itself fails, so there is no stat-then-open window.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
}

// processAlive reports whether pid refers to a still-running process, using
// signal 0 (no-op: delivers nothing, just checks existence/permission).
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// killProcess terminates a process unconditionally. Used by the tests that
// simulate a helper crash; the production shutdown path asks politely first.
func killProcess(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// restrictToOwner makes path readable by its owner and nobody else.
//
// On POSIX that is exactly the chmod this replaces, and perm is honoured
// verbatim — 0700 for a directory, 0600 for a file — so nothing about the
// existing behaviour changes. The seam exists for the Windows half, where
// os.Chmod cannot express this at all.
func restrictToOwner(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}
