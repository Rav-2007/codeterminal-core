//go:build windows

package editapply

import "testing"

// makeDirUnwritable has no Windows implementation yet, so the tests that need
// one are recorded NOT RUN rather than reported as product failures.
//
// os.Chmod on Windows sets FILE_ATTRIBUTE_READONLY, which does not apply to
// directories and does not stop a file being created inside one -- so the
// POSIX injection does not merely fail here, it silently does nothing, and the
// three tests that use it reported "expected Apply to fail" against an Apply
// that was correct.
//
// The real equivalent is an explicit deny-ACE for FILE_ADD_FILE on this user,
// written with SetNamedSecurityInfo and removed in cleanup. That is genuine work
// with its own failure modes -- a deny-ACE that outlives a failed test makes the
// temp directory undeletable -- so it belongs in the Windows confinement pass
// (Track C2) alongside junctions, ADS and 8.3 names, not smuggled in here.
//
// Skipping is the honest state: these tests currently prove the rollback and
// manifest paths on POSIX only, and this file is where that is written down
// instead of being inferred from a build tag on an unrelated file.
func makeDirUnwritable(t *testing.T, dir string) {
	t.Helper()
	_ = dir
	t.Skip("NOT RUN: forcing a directory write failure on Windows needs a deny-ACE, not chmod; " +
		"the rollback path this test covers is UNVERIFIED here (Track C2)")
}
