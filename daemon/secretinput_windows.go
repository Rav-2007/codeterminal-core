//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// withEchoOff runs fn with the console's echo off, and puts it back after.
func withEchoOff(f *os.File, fn func() error) error {
	h := windows.Handle(f.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return fn()
	}
	if err := windows.SetConsoleMode(h, old&^windows.ENABLE_ECHO_INPUT|windows.ENABLE_LINE_INPUT); err != nil {
		return err
	}
	defer func() { _ = windows.SetConsoleMode(h, old) }()
	return fn()
}
