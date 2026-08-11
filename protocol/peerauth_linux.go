//go:build linux

package protocol

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerCred is the connecting peer's OS-reported identity, retrieved from the
// kernel rather than from anything the client sends on the wire — so a
// malicious peer cannot forge it by lying in the handshake. uid is what
// authorizePeer actually checks; pid is carried only so a refusal can be
// logged with something identifying about the caller.
type peerCred struct {
	uid uint32
	pid int32
}

// readPeerCred retrieves the connecting peer's credentials from a Unix-domain
// socket via SO_PEERCRED (Linux-specific). The kernel fills these in at
// connect() time from the peer process's real credentials, so they identify
// who actually opened the connection independently of any bytes sent
// afterwards — this is what makes it usable as authentication rather than a
// self-asserted claim.
//
// Any failure — the conn does not expose a raw fd, the Control callback fails,
// or the getsockopt itself errors — is returned as an error so the caller can
// fail CLOSED (refuse the connection) instead of guessing at an identity.
func readPeerCred(conn syscall.Conn) (peerCred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return peerCred{}, fmt.Errorf("obtaining raw connection: %w", err)
	}

	var ucred *unix.Ucred
	var credErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		ucred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); ctrlErr != nil {
		return peerCred{}, fmt.Errorf("accessing socket fd: %w", ctrlErr)
	}
	if credErr != nil {
		return peerCred{}, fmt.Errorf("reading SO_PEERCRED: %w", credErr)
	}
	if ucred == nil {
		return peerCred{}, fmt.Errorf("SO_PEERCRED returned no credentials")
	}
	return peerCred{uid: ucred.Uid, pid: ucred.Pid}, nil
}
