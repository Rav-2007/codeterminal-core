package main

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"codeterminal/protocol"
)

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
