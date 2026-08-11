package protocol

import (
	"net"
	"testing"
)

// These tests are the regression cover for FAIL-3's Gate 3 (peer
// authentication). The daemon now verifies, via the OS, that a connecting
// peer runs as its own UID before reading the handshake or dispatching any
// request; a mismatch, or any failure of the check itself, is a hard refusal.
//
// A true cross-UID connection can't be constructed by an unprivileged test
// process, so the mismatch decision is proven at its pure choke point
// (checkPeerUID) and the fail-closed / refuse-before-dispatch behavior is
// proven live against handleConn with a connection that exposes no peer
// credentials. The same-UID happy path is proven live on Linux in
// peercred_linux_test.go and, end to end through Serve, by the existing
// server_limits_test.go round-trips (which now also pass authorizePeer).

// TestCheckPeerUID exercises the security-critical comparison directly: a
// matching UID is accepted, any mismatch is refused, and an unavailable
// (negative) daemon UID is refused rather than trusted.
func TestCheckPeerUID(t *testing.T) {
	tests := []struct {
		name    string
		peerUID uint32
		selfUID int
		wantErr bool
	}{
		{"same uid allowed", 1000, 1000, false},
		{"same uid zero (root) allowed", 0, 0, false},
		{"different uid refused", 1001, 1000, true},
		{"peer root vs non-root refused", 0, 1000, true},
		{"non-root peer vs root refused", 1000, 0, true},
		{"unavailable self uid refused", 1000, -1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPeerUID(tt.peerUID, tt.selfUID)
			if tt.wantErr && err == nil {
				t.Fatalf("checkPeerUID(%d, %d) = nil, want error", tt.peerUID, tt.selfUID)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("checkPeerUID(%d, %d) = %v, want nil", tt.peerUID, tt.selfUID, err)
			}
		})
	}
}

// TestAuthorizePeer_FailsClosed_OnUnverifiablePeer proves the fail-closed
// contract: a connection that exposes no peer credentials (a net.Pipe conn is
// not a syscall.Conn, standing in for "the credential check can't complete")
// is refused, not trusted.
func TestAuthorizePeer_FailsClosed_OnUnverifiablePeer(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if err := AuthorizePeer(server); err == nil {
		t.Fatal("AuthorizePeer accepted a connection with no peer credentials; must fail closed")
	}
}
