// Peer authentication — FAIL-3 Gate 3. AuthorizePeer / checkPeerUID are the
// daemon's access control: verify via the OS (SO_PEERCRED) that a connecting peer
// runs as this daemon's own UID, fail-closed, before any request is dispatched. The
// platform-specific credential readers are in peerauth_linux.go (SO_PEERCRED),
// peerauth_darwin.go (LOCAL_PEERCRED), peerauth_windows.go (the pipe's DACL plus
// GetNamedPipeClientProcessId) and peerauth_other.go (no mechanism, refuses);
// handleConn (daemon/server.go — a DIFFERENT MODULE, which is why this names the
// path and not the bare basename) calls AuthorizePeer as its first post-accept
// step. helper/main.go calls it too, on the embedder socket. See BACKLOG.md
// Gate 3 (commit 517c069).
//
// EVERY FILENAME ABOVE WAS WRONG UNTIL 2026-09-12. These files began as
// daemon/peercred_*.go and daemon/server_auth*.go; moving them into protocol/ so
// both sockets could share one gate renamed them, and 27 references across 24
// files kept the old names. Nothing caught it: scripts/docs-coderefs.sh checks
// .md files only, so a doc comment naming a file that does not exist is gated by
// nothing at all.

package protocol

import "fmt"

// checkPeerUID compares a connecting peer's OS-reported UID against the
// daemon's own. It is split out as a pure function so the security-critical
// match/mismatch decision is unit-testable without constructing a real
// cross-UID socket, which an unprivileged test process cannot do. selfUID is
// os.Getuid()'s int; a negative selfUID (Getuid reports -1 on platforms
// without the concept) is itself treated as a failure, so the daemon never
// trusts a peer it cannot meaningfully compare against.
func checkPeerUID(peerUID uint32, selfUID int) error {
	if selfUID < 0 {
		return fmt.Errorf("daemon uid unavailable (%d); cannot verify peer uid=%d", selfUID, peerUID)
	}
	if peerUID != uint32(selfUID) {
		return fmt.Errorf("peer uid=%d does not match daemon uid=%d", peerUID, selfUID)
	}
	return nil
}
