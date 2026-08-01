//go:build darwin

package main

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
//
// Identical in shape and meaning to the Linux type, deliberately: authorizePeer
// is one function with one rule, and the only thing that varies by platform is
// how the kernel is asked.
type peerCred struct {
	uid uint32
	pid int32
}

// readPeerCred retrieves the connecting peer's credentials from a Unix-domain
// socket via LOCAL_PEERCRED, macOS's equivalent of Linux's SO_PEERCRED. As
// there, the kernel fills these in at connect() time from the peer process's
// real credentials, so they identify who actually opened the connection
// independently of any bytes sent afterwards — which is what makes this
// authentication rather than a self-asserted claim.
//
// WHY THIS FILE EXISTS. Until now every non-Linux build fell through to
// peercred_other.go, whose readPeerCred errors unconditionally, and
// authorizePeer correctly treats an error as a refusal. Fail-closed is the
// right rule and it was doing its job — but on macOS it meant the daemon
// refused EVERY connection, so no Mac user could use the product at all. That
// is a security control turned into an availability failure by a missing
// platform implementation, not by a decision anyone made.
//
// TWO DIFFERENCES FROM LINUX, both stated rather than smoothed over:
//
//  1. LOCAL_PEERCRED yields no pid. macOS exposes it separately as
//     LOCAL_PEERPID, which is queried below on a best-effort basis: pid is used
//     only to make a refusal log line identifying, never in the decision, so a
//     kernel that will not answer costs a nicety and not a check. uid — the
//     thing authorizePeer actually compares — always comes from the kernel or
//     this function errors.
//  2. Xucred carries a group list; it is ignored, exactly as the Linux gid is.
//     The rule is same-uid, and widening it to groups would be a policy change
//     wearing a portability costume.
//
// Any failure — the conn does not expose a raw fd, the Control callback fails,
// or the getsockopt itself errors — is returned as an error so the caller can
// fail CLOSED (refuse the connection) instead of guessing at an identity.
func readPeerCred(conn syscall.Conn) (peerCred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return peerCred{}, fmt.Errorf("obtaining raw connection: %w", err)
	}

	var xucred *unix.Xucred
	var pid int
	var credErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		xucred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credErr != nil {
			return
		}
		// Best-effort, and deliberately not checked: see the doc comment. A pid
		// of 0 in a log line is better than refusing a connection whose uid the
		// kernel just vouched for.
		pid, _ = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); ctrlErr != nil {
		return peerCred{}, fmt.Errorf("accessing socket fd: %w", ctrlErr)
	}
	if credErr != nil {
		return peerCred{}, fmt.Errorf("reading LOCAL_PEERCRED: %w", credErr)
	}
	if xucred == nil {
		return peerCred{}, fmt.Errorf("LOCAL_PEERCRED returned no credentials")
	}
	return peerCred{uid: xucred.Uid, pid: int32(pid)}, nil
}
