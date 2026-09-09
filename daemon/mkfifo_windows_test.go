//go:build windows

package main

import "errors"

// mkfifoForTest reports that this platform has no filesystem FIFO.
//
// Never reached: the one caller skips on Windows first. It exists so the
// package COMPILES there, which is the half a runtime skip cannot do. See
// mkfifo_unix_test.go for why this pair exists at all.
func mkfifoForTest(string) error {
	return errors.New("Windows has no filesystem FIFO; its named pipes are not reachable by a workspace path")
}
