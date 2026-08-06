//go:build windows

package main

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// assertOwnerOnly checks that path is readable by its owner and nobody else.
//
// MIRRORED from ownerperm_unix_test.go. Same property, different model: there
// are no mode bits here, so asserting on os.Stat().Mode().Perm() -- which is
// what the five tests used to do -- read a CONSTANT. Go synthesises 0666 or
// 0777 on Windows from the single FILE_ATTRIBUTE_READONLY bit, so those tests
// reported "mode = 0666, want 0600" no matter what the code did, and would have
// gone on reporting it if the daemon had left the files wide open.
//
// The real question is the DACL, asked in two parts:
//
//   - Is it PROTECTED? An unprotected DACL merges ACEs inherited from the
//     parent directory, so a restriction under a permissive parent is
//     decorative. In SDDL that is the "P" flag on the D: section.
//   - Does it grant anyone but this user? Anything else is the leak.
//
// SDDL rather than walking ACEs by hand: x/sys/windows does not export GetAce,
// and a failure message containing the whole descriptor is worth more to
// whoever reads it than one containing an ACE index.
//
// STRICT ON PURPOSE. restrictToOwner sets exactly one ACE, so anything else
// present is something we did not intend and should hear about. A false failure
// here costs one CI round-trip; a false pass costs the property.
func assertOwnerOnly(t *testing.T, path, what string) {
	t.Helper()

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the DACL of %s (%s): %v", path, what, err)
	}
	sddl := sd.String()

	dacl, ok := daclSection(sddl)
	if !ok {
		t.Fatalf("%s (%s) has no DACL at all in %q; with no DACL every access is GRANTED", path, what, sddl)
	}
	if !strings.HasPrefix(dacl, "P") {
		t.Errorf("%s (%s) has an UNPROTECTED DACL (%q): ACEs inherited from the parent directory "+
			"are merged in, so the restriction is decorative under a permissive parent", path, what, sddl)
	}

	self, err := currentUserSIDString()
	if err != nil {
		t.Fatalf("determining this user's SID: %v", err)
	}
	for _, trustee := range aceTrustees(dacl) {
		if trustee != self {
			t.Errorf("%s (%s) grants access to %s, which is not this user (%s). Full descriptor: %q",
				path, what, trustee, self, sddl)
		}
	}
}

func currentUserSIDString() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

// daclSection returns the body of the "D:" section of an SDDL string, i.e.
// everything after "D:" up to the "S:" (SACL) section or the end.
func daclSection(sddl string) (string, bool) {
	i := strings.Index(sddl, "D:")
	if i < 0 {
		return "", false
	}
	rest := sddl[i+2:]
	// The SACL section, if any, follows. Its "S:" cannot appear inside an ACE
	// because an ACE is parenthesised and its fields are ;-separated.
	if j := strings.Index(rest, "S:"); j >= 0 && !strings.Contains(rest[:j], "(") {
		rest = rest[:j]
	} else if j := strings.LastIndex(rest, ")S:"); j >= 0 {
		rest = rest[:j+1]
	}
	return rest, true
}

// aceTrustees returns the trustee field of every ACE in an SDDL DACL body.
// Each ACE is (type;flags;rights;object_guid;inherit_object_guid;trustee).
func aceTrustees(dacl string) []string {
	var out []string
	for _, ace := range strings.Split(dacl, "(") {
		end := strings.Index(ace, ")")
		if end < 0 {
			continue // the leading flags, before the first ACE
		}
		fields := strings.Split(ace[:end], ";")
		if len(fields) < 6 {
			continue
		}
		out = append(out, fields[5])
	}
	return out
}
