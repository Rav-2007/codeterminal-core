//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"

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
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), ioctlReadTermios)
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
	before, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		fn()
		return false
	}
	after := *before
	after.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &after); err != nil {
		fn()
		return false
	}
	// Restored on every exit from fn, panic included: leaving a terminal with echo
	// off is a broken shell for the user afterwards.
	defer func() { _ = unix.IoctlSetTermios(fd, ioctlWriteTermios, before) }()

	// AND ON A SIGNAL, which runs no deferred function: Ctrl+C at the key prompt
	// killed the process and left the shell with echo off (FOUND 2026-10-01, on
	// a real pty). The terminal is put back first; then the signal is let
	// through, so the shell still sees an ordinary Ctrl+C.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	defer func() {
		signal.Stop(sigs)
		close(done)
	}()
	go func() {
		select {
		case sig := <-sigs:
			_ = unix.IoctlSetTermios(fd, ioctlWriteTermios, before)
			signal.Reset(sig)
			if s, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(os.Getpid(), s)
			}
		case <-done:
		}
	}()
	fn()
	return true
}
