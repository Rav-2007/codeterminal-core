//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}

// withEchoOff runs fn with the terminal's echo off, and puts it back after --
// also when fn fails.
func withEchoOff(f *os.File, fn func() error) error {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return fn()
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet); err != nil {
		return err
	}
	defer func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }()
	return fn()
}
