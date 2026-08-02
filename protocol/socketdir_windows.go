//go:build windows

package protocol

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

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

	self, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("cannot determine this user's identity to check runtime directory %s: %w", dir, err)
	}
	if owner.String() != self {
		return fmt.Errorf("runtime directory %s is owned by %s, not by this user (%s); refusing to place a lockfile in a directory this user does not own",
			dir, owner.String(), self)
	}
	return nil
}
