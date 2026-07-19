package main

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"codeterminal/protocol"
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
	srv := &Server{logger: discardLogger(), workspace: "/ws"}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if err := srv.authorizePeer(server); err == nil {
		t.Fatal("authorizePeer accepted a connection with no peer credentials; must fail closed")
	}
}

// TestHandleConn_RefusesUnverifiablePeerBeforeDispatch is the live
// refuse-before-dispatch proof. handleConn is driven with a connection whose
// peer credentials can't be read (net.Pipe): it must close the connection
// without ever reading the handshake or dispatching a request. This is the
// structural guarantee behind the cross-UID rejection that can't itself be
// constructed unprivileged — reaching request dispatch is impossible once
// authorizePeer refuses, exactly as it would for a real UID mismatch.
func TestHandleConn_RefusesUnverifiablePeerBeforeDispatch(t *testing.T) {
	srv := &Server{logger: discardLogger(), workspace: "/ws"}
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		srv.handleConn(server)
		close(done)
	}()

	// The server must refuse and close without replying. A read on the client
	// end therefore fails (EOF/closed) rather than yielding a handshake
	// response — proof no handshake was processed and no request was reached.
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp protocol.HandshakeResponse
	if err := json.NewDecoder(client).Decode(&resp); err == nil {
		t.Fatalf("expected refusal (closed conn) for unverifiable peer, got handshake response: %+v", resp)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not return after refusing an unverifiable peer")
	}
}
