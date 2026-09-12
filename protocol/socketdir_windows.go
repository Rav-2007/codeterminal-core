//go:build windows

package protocol

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tokenOwner mirrors Win32's TOKEN_OWNER, which is a single pointer to a SID.
// Declared here because x/sys/windows exposes the TokenOwner info class but no
// GetTokenOwner accessor, unlike GetTokenUser and GetTokenPrimaryGroup.
type tokenOwner struct {
	Owner *windows.SID
}

// currentTokenOwnerSID returns the SID Windows stamps as OWNER on objects this
// process creates — which is not, in general, this process's user SID.
//
// THIS DISTINCTION IS THE BUG. ensureOwnerOnlyDir compared the directory's owner
// against GetTokenUser, and CI's first Windows test run refused to start:
//
//	runtime directory C:\Users\runneradmin\AppData\Local\codeterminal is owned
//	by S-1-5-32-544, not by this user (S-1-5-21-...-500)
//
// S-1-5-32-544 is BUILTIN\Administrators. When a token carries the
// Administrators group with the SE_GROUP_OWNER attribute — the normal state of
// an elevated token — Windows sets that GROUP as the owner of everything the
// token creates. So the daemon created the directory, then refused to use it,
// for every administrator on Windows. An availability outage, and the same shape
// as the macOS peer-auth outage peerauth_darwin.go exists to fix.
//
// TokenOwner is the right question because Win32 defines it as "the default
// owner SID applied to objects created by this token". Comparing against it asks
// exactly what this function means to ask — was this directory created by a
// token equivalent to mine? — where TokenUser only ever asked a proxy question
// that happens to coincide on Unix.
//
// IT IS ALSO THE SECURE CHOICE, not merely the working one, and the reason is
// that it self-adjusts rather than special-casing a well-known SID:
//
//   - A NON-ADMIN user meeting a directory owned by Administrators still gets a
//     refusal, because their TokenOwner is their own user SID. Hardcoding
//     "accept S-1-5-32-544" would have handed them a directory they do not
//     control; this does not.
//   - An admin running UNELEVATED has Administrators as DENY-ONLY in the
//     filtered token, which cannot be an owner, so Windows sets TokenOwner to
//     the user SID and the comparison is user-to-user. No group enumeration to
//     get wrong, and no way for a disabled or deny-only group to widen the test.
//   - Accepting Administrators when we ARE Administrators is not a weakened
//     boundary: every principal who could rewrite that directory's ACL is one we
//     already belong to. transport_windows.go states the same boundary for the
//     pipe DACL — "Administrators and SYSTEM can still take ownership, exactly
//     as root can on Unix".
//
// NOT RUN ON HARDWARE by this author; CI's windows-latest runner is the evidence.
func currentTokenOwnerSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	// Sized by the same grow-and-retry loop x/sys uses internally for the token
	// accessors it does provide; a SID is variable-length.
	n := uint32(64)
	for {
		b := make([]byte, n)
		err := windows.GetTokenInformation(token, windows.TokenOwner, &b[0], uint32(len(b)), &n)
		if err == nil {
			// The SID lives INSIDE b, so stringify before b can be collected.
			s := (*tokenOwner)(unsafe.Pointer(&b[0])).Owner.String()
			runtime.KeepAlive(b)
			return s, nil
		}
		if err != windows.ERROR_INSUFFICIENT_BUFFER || n <= uint32(len(b)) {
			return "", err
		}
	}
}

// ensureOwnerOnlyDir makes dir safe to put a lockfile in: it must be a real
// directory, owned by this user, and not a reparse point.
//
// This REPLACES the unconditional refusal that socketdir_other.go still gives
// every other platform. That refusal was correct while nothing here could check
// ownership; it is not correct now that something can, and leaving it would
// have meant the daemon refusing to start on Windows for a reason that had
// stopped being true.
//
// NOT RUN ON HARDWARE — see transport_windows.go.
//
// WHAT IS AND IS NOT AT STAKE HERE. On Unix this directory holds the SOCKET,
// so its mode is load-bearing: a permissive directory means a connectable
// socket. On Windows the socket is a named pipe, which lives in the kernel
// object namespace and carries its own DACL (see transport_windows.go), so this
// directory holds only the LOCKFILE. That is still worth protecting — the
// lockfile names the pipe a client will trust and connect to — but the failure
// it prevents is misdirection, not direct access.
//
// Three checks, mirroring socketdir_unix.go's, in the order that matters:
//
//  1. Lstat, not Stat, and refuse a REPARSE POINT. This is the check with the
//     biggest platform difference: Windows directory junctions need no
//     privilege to create, where Unix symlinks do, so the substitution this
//     refuses is CHEAPER for an attacker here than it is there.
//  2. Owner, read from the file's security descriptor. A directory we did not
//     create is not ours to use whatever its ACL says right now, because its
//     owner can rewrite that ACL at any time.
//  3. No mode tightening. Unix chmods a permissive directory back to 0700;
//     there is no equivalent single call here, and %LOCALAPPDATA% is already
//     per-user and per-user-ACL'd by the OS. Stated rather than silently
//     skipped, because the two implementations otherwise look parallel.
func ensureOwnerOnlyDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime path %s is not a directory", dir)
	}
	// Junctions and symlinks both surface as irregular here. Refuse rather than
	// resolve: a directory standing in for another is not the directory we
	// checked the owner of.
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("runtime directory %s is a reparse point (junction or symlink); refusing to use it", dir)
	}

	sd, err := windows.GetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("cannot determine the owner of runtime directory %s: %w", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("cannot determine the owner of runtime directory %s: %w", dir, err)
	}

	// currentTokenOwnerSID, NOT currentUserSID. See its comment: the two differ
	// on an elevated token, and using the user SID here refused every
	// administrator on Windows. currentUserSID is still correct for the pipe
	// DACL, which deliberately grants the individual user rather than a group.
	self, err := currentTokenOwnerSID()
	if err != nil {
		return fmt.Errorf("cannot determine this token's owner to check runtime directory %s: %w", dir, err)
	}
	if owner.String() != self {
		// Name the likeliest cause. The common way to reach this with a
		// legitimate directory is an elevation mismatch: a daemon run elevated
		// once leaves a directory owned by Administrators, and a later
		// unelevated run cannot claim it. Refusing is correct — an unelevated
		// process genuinely does not control that directory — but "wrong SID" on
		// its own sends the reader looking for a compromise.
		return fmt.Errorf("runtime directory %s is owned by %s, but objects created by this process are owned by %s; "+
			"refusing to place a lockfile in a directory this process does not own. If that directory was created by an "+
			"elevated run, either delete it or run the daemon at the same elevation",
			dir, owner.String(), self)
	}
	return nil
}
