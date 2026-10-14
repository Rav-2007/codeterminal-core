//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package main

import "os"

// No portable way to turn echo off here: the value is read as typed, so pipe it
// in rather than typing it where it can be seen.
func isTerminal(*os.File) bool { return false }

func withEchoOff(_ *os.File, fn func() error) error { return fn() }
