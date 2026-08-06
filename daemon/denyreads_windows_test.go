//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

// denyReadsRaw makes path unreadable by this process, or reports that it
// cannot. See denyreads_test.go for the contract and denyreads_unix_test.go for
// the reference implementation.
//
// os.Chmod(path, 0000) DOES NOT DO THIS ON WINDOWS. Go maps a Unix mode onto
// FILE_ATTRIBUTE_READONLY, which blocks writing and has nothing to say about
// reading -- so the two Gate 7 tests that used it were, on Windows, feeding
// Apply an ordinary readable file and asserting on whatever came back. One of
// them reported "search text not found" where it wanted a permission error.
//
// The real mechanism is a DENY ace. It is placed FIRST in the DACL (which
// ACLFromEntries does for us -- SetEntriesInAcl canonicalises deny before
// allow), so it beats the allow ace that follows, and a deny ace binds an
// administrator exactly as it binds anyone else. The allow ace is not optional:
// without it nothing is granted, DELETE included, and t.TempDir's cleanup could
// not remove the fixture.
func denyReadsRaw(t *testing.T, path string) (why string) {
	t.Helper()

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("determining this user's SID: %v", err)
	}
	trustee := windows.TRUSTEE{
		TrusteeForm:  windows.TRUSTEE_IS_SID,
		TrusteeType:  windows.TRUSTEE_IS_USER,
		TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
	}

	set := func(entries []windows.EXPLICIT_ACCESS) error {
		acl, err := windows.ACLFromEntries(entries, nil)
		if err != nil {
			return err
		}
		return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil, nil, acl, nil)
	}

	allowAll := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee:           trustee,
	}
	denyRead := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.DENY_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee:           trustee,
	}

	if err := set([]windows.EXPLICIT_ACCESS{denyRead, allowAll}); err != nil {
		return "this filesystem would not take a DENY ace: " + err.Error()
	}
	t.Cleanup(func() { _ = set([]windows.EXPLICIT_ACCESS{allowAll}) })
	return ""
}
