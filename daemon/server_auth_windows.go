//go:build windows

package main

import (
	"fmt"
	"net"
)

// authorizePeer verifies that the process on the other end of an accepted pipe
// connection runs as the same USER as this daemon, and returns an error (which
// the caller turns into an immediate refusal) otherwise.
//
// NOT RUN ON HARDWARE — see peercred_windows.go.
//
// WHY THIS IS NOT THE SAME FUNCTION AS THE POSIX ONE. Identity on Windows is a
// SID, not a uid. There is no number to compare, os.Getuid() reports -1, and a
// pipe connection is not a syscall.Conn. Mapping one onto the other would mean
// inventing a uid the OS does not recognise, so the two implementations are
// separate and each says what it actually checks.
//
// WHAT ACTUALLY DEFENDS THIS SOCKET, IN ORDER:
//
//  1. The pipe's DACL, which grants this user's SID and nobody else, enforced
//     by the object manager at CreateFile time. A process belonging to another
//     user is refused BEFORE reaching this function. That is the load-bearing
//     control, and it is enforced earlier than the Unix equivalent is.
//  2. This check, which confirms from the kernel who connected.
//
// It FAILS CLOSED, exactly as the POSIX version does: any error obtaining or
// comparing the identity is a refusal, never a silent pass. That is worth
// stating because layer 1 makes it tempting to treat layer 2 as advisory — it
// is not, and a peer whose identity cannot be established is refused even
// though the DACL already vouched for it.
func (s *Server) authorizePeer(conn net.Conn) error {
	cred, err := readPeerCredFromConn(conn)
	if err != nil {
		return err
	}
	self, err := selfSID()
	if err != nil {
		return err
	}
	if err := checkPeerSID(cred.sid, self); err != nil {
		return fmt.Errorf("%w (peer pid=%d)", err, cred.pid)
	}
	return nil
}

// checkPeerSID compares a connecting peer's OS-reported SID against the
// daemon's own.
//
// Split out as a pure function for the same reason checkPeerUID is: the
// security-critical match/mismatch decision is then unit-testable without
// constructing a real cross-user pipe, which an unprivileged test process
// cannot do. An empty SID on either side is a failure rather than a match, so
// two unknowns never compare equal — the bug that would turn this check into a
// rubber stamp.
func checkPeerSID(peerSID, selfSID string) error {
	if selfSID == "" {
		return fmt.Errorf("daemon user identity unavailable; cannot verify peer %q", peerSID)
	}
	if peerSID == "" {
		return fmt.Errorf("peer user identity unavailable; refusing rather than trusting an unidentified peer")
	}
	if peerSID != selfSID {
		return fmt.Errorf("peer user %s does not match daemon user %s", peerSID, selfSID)
	}
	return nil
}
