//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// The Windows half of terminal echo control; see termecho_unix.go for why this
// uses x/sys rather than x/term.

// stdinIsTerminal reports whether stdin is a console.
func stdinIsTerminal() bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode) == nil
}

// withoutEcho runs fn with console echo disabled, restoring the previous mode
// afterwards. Returns false when it could not be turned off, so the caller warns
// instead of echoing a secret.
func withoutEcho(fn func()) (disabled bool) {
	handle := windows.Handle(os.Stdin.Fd())
	var before uint32
	if err := windows.GetConsoleMode(handle, &before); err != nil {
		fn()
		return false
	}
	if err := windows.SetConsoleMode(handle, before&^windows.ENABLE_ECHO_INPUT); err != nil {
		fn()
		return false
	}
	defer func() { _ = windows.SetConsoleMode(handle, before) }()
	fn()
	return true
}
