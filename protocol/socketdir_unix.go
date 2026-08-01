//go:build unix

package protocol

import (
	"fmt"
	"os"
	"syscall"
)

// ensureOwnerOnlyDir makes dir safe to put a socket and a lockfile in: it must
// be a real directory, owned by this user, reachable by nobody else.
//
// WHY THIS IS NOT PARANOIA. RuntimeDir falls back to os.TempDir() when
// XDG_RUNTIME_DIR is unset — which is the NORMAL state on macOS, a platform the
// daemon now actually supports. That makes the parent /tmp: world-writable,
// shared, and somewhere another user can create our directory name before we
// do. os.MkdirAll succeeds against an existing directory WITHOUT changing its
// mode or checking its owner, so a pre-created 0777 directory would have been
// accepted silently, and every guarantee downstream (the socket's 0600, the
// lockfile's O_NOFOLLOW) is a guarantee about a file inside a directory
// somebody else can rename things in.
//
// Three checks, in the order that matters:
//
//  1. Lstat, not Stat — a symlink standing where the directory should be must
//     be refused, not followed to wherever it points.
//  2. Owner. A directory we did not create is not ours to use, whatever its
//     mode says right now, because its owner can change that mode at any time.
//  3. Mode, tightened rather than merely inspected: an existing directory of
//     our own that predates this check (or a permissive umask) is fixed in
//     place, since refusing to start over our own directory would be a poor
//     trade for the user.
//
// Refusals are fatal at the call site. Failing closed is right here: the
// alternative is serving a socket in a directory an attacker controls.
func ensureOwnerOnlyDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime directory %s is a symlink; refusing to use it", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime path %s is not a directory", dir)
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine the owner of runtime directory %s", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("runtime directory %s is owned by uid %d, not by this user (uid %d); refusing to place a socket in a directory this user does not own",
			dir, st.Uid, os.Getuid())
	}

	if info.Mode().Perm()&0077 != 0 {
		if err := os.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("restricting runtime directory %s to owner-only: %w", dir, err)
		}
	}
	return nil
}
