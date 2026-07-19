//go:build !linux

package main

import (
	"fmt"
	"syscall"
)

// peerCred mirrors the Linux type only so authorizePeer compiles on every
// platform. On any non-Linux build there is no SO_PEERCRED equivalent wired up
// here (macOS would need LOCAL_PEERCRED/getpeereid; Windows peer credentials
// over AF_UNIX are inconsistent), so readPeerCred below returns an error
// unconditionally.
type peerCred struct {
	uid uint32
	pid int32
}

// readPeerCred has no peer-credential mechanism on this platform, so it always
// errors. authorizePeer treats that as a refusal — the daemon fails CLOSED on
// platforms this fix does not yet cover (per the FAIL-3 Gate 3 fail-closed
// rule) rather than silently trusting the peer. This is the documented,
// flagged gap: peer authentication is verified live on Linux only. See the
// PEERCRED note on authorizePeer in server.go.
func readPeerCred(conn syscall.Conn) (peerCred, error) {
	return peerCred{}, fmt.Errorf("peer credential verification is not implemented on this platform (SO_PEERCRED is Linux-only); refusing to fail closed")
}
