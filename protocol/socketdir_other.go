//go:build !unix && !windows

package protocol

import "fmt"

// ensureOwnerOnlyDir has no portable way to check directory ownership on this
// platform, so it refuses rather than pretending to have checked.
//
// This matches the daemon's peer-authentication posture exactly (see
// protocol/peerauth_other.go): where the OS mechanism a security property depends
// on is not wired up, the answer is to fail closed and say so, not to return
// nil and let the caller believe a check ran.
//
// Windows used to land here and no longer does — socketdir_windows.go can
// actually check ownership, so it does. This file now covers only platforms
// with no implementation at all, which is what it was always meant to mean.
func ensureOwnerOnlyDir(dir string) error {
	return fmt.Errorf("cannot verify that runtime directory %s is owned by this user on this platform; refusing to place a socket there", dir)
}
