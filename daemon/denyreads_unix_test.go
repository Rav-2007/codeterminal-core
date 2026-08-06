//go:build unix

package main

import (
	"os"
	"testing"
)

// denyReadsRaw makes path unreadable by this process, or reports that it
// cannot. See denyreads_test.go for the contract and denyreads_windows_test.go
// for the mirror.
//
// Running as root is the case it cannot serve: mode 0000 does not stop root
// reading, so the fixture would be a lie. Named here rather than left to the
// verification read so the reason reaching the log is the real one.
func denyReadsRaw(t *testing.T, path string) (why string) {
	t.Helper()

	if os.Getuid() == 0 {
		return "running as root, which reads a 0000 file regardless of its mode"
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatalf("chmod 0000 on %s: %v", path, err)
	}
	// So t.TempDir's cleanup can still remove it.
	t.Cleanup(func() { _ = os.Chmod(path, 0644) })
	return ""
}
