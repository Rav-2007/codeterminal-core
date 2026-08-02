//go:build unix

package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// authorizePeer verifies, via the OS, that the process on the other end of an
// accepted connection runs as the same UID as this daemon, and returns an
// error (which the caller turns into an immediate refusal) otherwise (FAIL-3,
// Gate 3 — peer authentication). This is the daemon's actual access control.
// The Unix socket is created 0600 under a per-user runtime dir, but file
// permissions don't identify the caller, and the Gate 5 resource limits
// deliberately don't either — any process able to connect was, until this
// check, treated as fully trusted. Every legitimate client (CLI, TUI, IDE
// integration) runs as the same user that started the daemon, so a same-UID
// peer is exactly the trusted set, verified with zero client-side config.
//
// PEERCRED: the credentials come from the kernel (SO_PEERCRED on Linux,
// LOCAL_PEERCRED on macOS), fixed at connect() time — not from anything the
// client sends — so a peer cannot forge a different UID on the wire. This
// FAILS CLOSED: any error retrieving or comparing the credential (unsupported
// platform — see peercred_other.go — a conn that exposes no peer credentials,
// or a syscall failure) is returned as a refusal, never a silent pass. The
// returned error is for the daemon's own log only; the caller never sends it
// back to the rejected peer, so this adds no new information-leakage surface.
//
// The Windows counterpart is in server_auth_windows.go. It compares SIDs
// rather than uids, because that is what identity is there — see its header
// for why the two could not share one implementation honestly.
func (s *Server) authorizePeer(conn net.Conn) error {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("connection type %T exposes no peer credentials", conn)
	}
	cred, err := readPeerCred(sc)
	if err != nil {
		return err
	}
	if err := checkPeerUID(cred.uid, os.Getuid()); err != nil {
		return fmt.Errorf("%w (peer pid=%d)", err, cred.pid)
	}
	return nil
}
