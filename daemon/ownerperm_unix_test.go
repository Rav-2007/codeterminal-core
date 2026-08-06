//go:build unix

package main

import (
	"os"
	"testing"
)

// assertOwnerOnly checks that path is readable by its owner and nobody else.
//
// MIRRORED by ownerperm_windows_test.go, which asks the same question of a
// different access-control model. The two are not interchangeable
// implementations of one check -- they are two checks of one PROPERTY, and the
// property is what every caller is written against.
//
// The POSIX half is the reference and is simply what the five tests that used
// to inline this already did.
func assertOwnerOnly(t *testing.T, path, what string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s (%s): %v", path, what, err)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("%s mode = %04o, want no group/other bits — %s", path, perm, what)
	}
}
