//go:build !linux && !darwin && !windows

package protocol

import (
	"fmt"
	"syscall"
)

// peerCred mirrors the Linux type only so authorizePeer compiles on every
// platform. Linux (SO_PEERCRED), macOS (LOCAL_PEERCRED) and Windows (the pipe's
// DACL plus GetNamedPipeClientProcessId) each have a real implementation in
// their own file; this is what is left, and on it there is no peer-credential
// mechanism wired up, so readPeerCred below returns an error unconditionally.
type peerCred struct {
	uid uint32
	pid int32
}

// readPeerCred has no peer-credential mechanism on this platform, so it always
// errors. authorizePeer treats that as a refusal — the daemon fails CLOSED on
// platforms this fix does not yet cover (per the FAIL-3 Gate 3 fail-closed
// rule) rather than silently trusting the peer.
//
// BE CLEAR ABOUT WHAT FAILING CLOSED COSTS HERE: it does not degrade the
// daemon on such a platform, it refuses every connection, so the product does
// not run at all. That was the state on macOS until peerauth_darwin.go and on
// Windows until peerauth_windows.go — correct, and not the same thing as
// supported.
// See the PEERCRED note on authorizePeer in server.go.
func readPeerCred(conn syscall.Conn) (peerCred, error) {
	return peerCred{}, fmt.Errorf("peer credential verification is not implemented on this platform (SO_PEERCRED is Linux-only, LOCAL_PEERCRED macOS-only); refusing rather than trusting an unverified peer")
}
