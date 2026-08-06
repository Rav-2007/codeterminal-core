package main

import (
	"os"
	"testing"
)

// denyReads makes path unreadable by this process and reports whether it
// SUCCEEDED, having checked rather than assumed.
//
// Two Gate 7 tests need a file that exists and cannot be read: one asserts the
// oracle still says "permission denied" for that state, the other that the
// absolute path is scrubbed out of the resulting error. Both built the fixture
// with os.Chmod(path, 0000) guarded by os.Getuid() != 0, and both of those are
// POSIX assumptions:
//
//   - chmod 0000 does not deny reads on Windows. Go maps a Unix mode onto
//     FILE_ATTRIBUTE_READONLY, which is about writing. The fixture was an
//     ordinary readable file and the tests asserted on whatever came back --
//     one of them got "search text not found" and failed on the first Windows
//     CI run, which is how this was noticed.
//   - os.Getuid() returns -1 on Windows, so the root guard read as "not root"
//     and ran the block anyway.
//
// THE VERIFICATION READ IS THE POINT, and it is why this returns a bool rather
// than just doing the thing. Applying a restriction is not the same as having
// one: a root user on POSIX and a process holding SeBackupPrivilege on Windows
// both read straight through. Without the read-back, a helper that silently
// failed would hand the caller a readable file and the test would assert
// something else entirely while reporting success. That is the vacuous-test
// failure mode this campaign keeps finding, so the helper closes it rather than
// leaving it to each caller.
//
// A caller that gets false must say NOT RUN and say why -- never pass quietly.
func denyReads(t *testing.T, path string) (ok bool, why string) {
	t.Helper()

	if why := denyReadsRaw(t, path); why != "" {
		return false, why
	}
	if _, err := os.ReadFile(path); err == nil {
		return false, "the restriction was applied and this process can still read the file anyway"
	}
	return true, ""
}

// The helper's own premise. If denyReads ever starts returning true for a file
// that is in fact readable, every test built on it is measuring nothing, and
// this is the one place that would say so.
func TestDenyReads_ActuallyDeniesReads(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "victim.txt"
	if err := os.WriteFile(path, []byte("secret\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ok, why := denyReads(t, path)
	if !ok {
		t.Skipf("NOT RUN: %s; every test that depends on an unreadable fixture is UNVERIFIED here", why)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("denyReads reported success but the file is still readable")
	}
}
