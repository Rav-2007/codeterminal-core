//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// Terminal echo control, so a key typed at a prompt is not left sitting in the
// scrollback -- or in whatever is recording the session.
//
// Done with x/sys, which this module already depends on, rather than
// golang.org/x/term: a new direct dependency would have to earn its way past the
// supply-chain gate, and this is twenty lines.

// stdinIsTerminal reports whether stdin is a terminal, by asking for its terminal
// attributes -- the same question, answered by whether the ioctl works.
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}

// withoutEcho runs fn with terminal echo disabled on stdin, restoring the
// previous state afterwards even if fn panics.
//
// Returns false when echo could not be turned off, so the caller can WARN rather
// than silently echo a secret: a key typed in the clear is a leak the user should
// be told about, not one that is quietly allowed to happen.
func withoutEcho(fn func()) (disabled bool) {
	fd := int(os.Stdin.Fd())
	before, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		fn()
		return false
	}
	after := *before
	after.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &after); err != nil {
		fn()
		return false
	}
	// Restored on every exit from fn, panic included: leaving a terminal with echo
	// off is a broken shell for the user afterwards.
	defer func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, before) }()
	fn()
	return true
}
