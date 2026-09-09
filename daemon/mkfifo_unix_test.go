//go:build !windows

package main

import "syscall"

// mkfifoForTest creates a FIFO, or reports why this platform cannot.
//
// IT EXISTS BECAUSE A RUNTIME SKIP CANNOT SAVE A COMPILE-TIME SYMBOL.
// TestTooLargeMarker_FIFODoesNotHangTheTurn already skipped on Windows via
// runtime.GOOS, which reads like enough and is not: syscall.Mkfifo does not
// EXIST on Windows, so the package failed to build there long before any skip
// could run. `go vet` and the test suite on this Linux box were both green;
// crossvet (GOOS=windows) is what found it.
//
// Split into a build-tagged pair, which is this package's existing idiom for
// exactly this problem -- see denyreads_unix_test.go/denyreads_windows_test.go,
// ownerperm_*, and testaddr_*.
func mkfifoForTest(path string) error { return syscall.Mkfifo(path, 0o644) }
