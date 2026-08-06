//go:build windows

package protocol

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// ownerOf reads the OWNER SID from a path's security descriptor, the same way
// ensureOwnerOnlyDir does. Kept here rather than shared so the test observes the
// object independently instead of through the function under test.
func ownerOf(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(%s): %v", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("reading owner of %s: %v", path, err)
	}
	return owner.String()
}

// THE REGRESSION TEST FOR THE WINDOWS ADMIN OUTAGE.
//
// ensureOwnerOnlyDir compared a directory's owner against GetTokenUser, and on
// an elevated token those are different SIDs: Windows stamps BUILTIN\
// Administrators (S-1-5-32-544) as the owner of everything such a token creates.
// The daemon therefore created its own runtime directory and then refused to use
// it, for every administrator on Windows.
//
// This asserts the invariant that failure violated, stated as a property rather
// than as a list of SIDs: THE SID WE COMPARE AGAINST MUST BE THE SID WINDOWS
// ACTUALLY STAMPS ON DIRECTORIES WE CREATE. It is written to be true on every
// Windows host regardless of elevation -- an elevated runner and an unelevated
// laptop both satisfy it, and both would have caught the bug.
//
// Neuter check, for whoever revisits this: swap currentTokenOwnerSID back to
// currentUserSID and this test fails on any elevated Windows host, which is what
// CI's windows-latest runner is. It cannot be verified from Linux, so CI is the
// evidence.
func TestCurrentTokenOwnerSID_IsWhatWindowsStampsOnDirectoriesWeCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "created-by-this-process")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}

	stamped := ownerOf(t, dir)

	got, err := currentTokenOwnerSID()
	if err != nil {
		t.Fatalf("currentTokenOwnerSID: %v", err)
	}
	if got != stamped {
		t.Errorf("currentTokenOwnerSID() = %s, but Windows stamped %s as the owner of a directory this "+
			"very process just created.\nThese must agree or ensureOwnerOnlyDir refuses the daemon's own "+
			"runtime directory -- which is the administrator outage this test exists to prevent.", got, stamped)
	}
}

// The end-to-end half: the gate must ACCEPT a directory this process created.
//
// A confinement check that only ever refuses is indistinguishable from a total
// outage, which is precisely the failure being fixed here -- so the acceptance
// case is asserted, not assumed. Mirrors the intent of
// TestConfinementConformance_*AllowsOrdinaryPaths.
func TestEnsureOwnerOnlyDir_AcceptsADirectoryThisProcessCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	if err := ensureOwnerOnlyDir(dir); err != nil {
		t.Errorf("ensureOwnerOnlyDir refused a directory this process just created: %v", err)
	}
}

// The refusal half, so the fix above cannot have been achieved by making the
// gate permissive. A reparse point is the one hostile shape reachable without
// another user account on the box: junctions need no privilege on Windows, which
// is what makes them cheaper for an attacker here than symlinks are on Unix.
func TestEnsureOwnerOnlyDir_RefusesAJunction(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatalf("creating %s: %v", real, err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		// Creating a symlink needs either developer mode or elevation. Where it
		// is unavailable, say so rather than reporting a pass that never ran.
		t.Skipf("NOT RUN: cannot create a reparse point on this host (%v); the junction refusal is UNVERIFIED here", err)
	}
	if err := ensureOwnerOnlyDir(link); err == nil {
		t.Error("ensureOwnerOnlyDir accepted a reparse point; a directory standing in for another is not the directory whose owner was checked")
	}
}
