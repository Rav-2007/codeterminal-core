//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package main

import "golang.org/x/sys/unix"

// The termios ioctls are TIOCGETA and TIOCSETA on macOS and the BSDs, and
// TCGETS and TCSETS elsewhere (termecho_ioctl_other.go) -- golang.org/x/term
// splits them the same way. FOUND 2026-10-01: termecho_unix.go named the Linux
// pair for every Unix, and the daemon had not compiled for macOS since
// 838e4b4; crossvet checked only Windows.
const (
	ioctlReadTermios  = unix.TIOCGETA
	ioctlWriteTermios = unix.TIOCSETA
)
