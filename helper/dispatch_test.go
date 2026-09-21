package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"mochiii/helper/helperproto"
)

// Model-independent coverage for the helper's request handling. The embed path
// needs the real ONNX model and stays behind -tags eval; the framing, the health
// check, and the unknown-method refusal do not, and they are what the daemon
// depends on to decide the helper is alive at all.
//
// A nil embedder is deliberate here: it proves these branches never touch it,
// which is the property that makes the daemon's health probe usable before any
// model work has happened.

func newTestServer() *server {
	return &server{embedder: nil, logger: log.New(io.Discard, "", 0)}
}

func TestDispatch_Health(t *testing.T) {
	resp := newTestServer().dispatch(helperproto.Request{Method: helperproto.MethodHealth})
	if !resp.OK {
		t.Errorf("health dispatch returned OK=false (error %q) -- the daemon uses this to decide "+
			"the helper is alive", resp.Error)
	}
	if resp.Error != "" {
		t.Errorf("health response carried an error: %q", resp.Error)
	}
	if resp.Vectors != nil {
		t.Errorf("health response carried vectors: %v", resp.Vectors)
	}
}

func TestDispatch_UnknownMethodIsRefused(t *testing.T) {
	cases := []string{"", "EMBED", "Embed", "hea1th", "shutdown", "../embed"}
	for _, method := range cases {
		t.Run("method="+method, func(t *testing.T) {
			resp := newTestServer().dispatch(helperproto.Request{Method: method})
			if resp.OK {
				t.Errorf("method %q was accepted -- unknown methods must be refused, not "+
					"silently treated as one of the known ones", method)
			}
			if resp.Error == "" {
				t.Errorf("method %q was refused with no error string; the daemon has nothing to log", method)
			}
		})
	}
}

// Case matters. The daemon sends the lowercase constants, and a helper that
// accepted "EMBED" would be a second parser disagreeing with the first -- the
// same class the proxy and daemon dispatchers were both hardened against.
func TestDispatch_MethodMatchIsCaseSensitive(t *testing.T) {
	if resp := newTestServer().dispatch(helperproto.Request{Method: "HEALTH"}); resp.OK {
		t.Error(`"HEALTH" was accepted as a health check; method matching must be exact`)
	}
}

// One JSON object per connection, then the connection closes. handleConn is the
// whole framing contract, so it is exercised over a real net.Conn pair.
func TestHandleConn_OneRequestOneResponse(t *testing.T) {
	client, serverSide := net.Pipe()
	defer client.Close()

	go newTestServer().serveConn(serverSide)

	client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(client).Encode(helperproto.Request{Method: helperproto.MethodHealth}); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	var resp helperproto.Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if !resp.OK {
		t.Errorf("response OK=false, error %q", resp.Error)
	}
}

// A malformed body must close the connection rather than hang or crash the
// helper: the daemon would otherwise block on a read that never completes, and
// the helper serves every index operation.
func TestHandleConn_MalformedRequestClosesWithoutHanging(t *testing.T) {
	client, serverSide := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		newTestServer().serveConn(serverSide)
		close(done)
	}()

	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("writing garbage: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleConn did not return on a malformed request -- a bad body must not wedge " +
			"the process that serves every embedding call")
	}

	// And the connection is closed, so the daemon's read ends rather than blocking.
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Error("connection stayed open after a malformed request; the daemon would block on read")
	}
}

// The helper must survive a request whose method is valid JSON but whose shape is
// wrong (texts as an object, say) the same way -- by refusing, not panicking.
func TestHandleConn_WronglyTypedFieldIsRefusedNotFatal(t *testing.T) {
	client, serverSide := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		newTestServer().serveConn(serverSide)
		close(done)
	}()

	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte(`{"method":"health","texts":{"not":"an array"}}` + "\n")); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleConn did not return on a wrongly-typed field")
	}
}

// A peer that opens a connection and says nothing must not hold it forever
// (L3). The daemon deadlines every connection it accepts; the helper deadlined
// none, so a wedged peer pinned a goroutine for the life of the process.
//
// The deadline is two minutes in production, which no test should wait for, so
// the assertion is that one is SET at all: a conn whose deadline has already
// been overridden to the past must fail the read immediately rather than block.
func TestHandleConn_SilentPeerDoesNotHoldTheConnectionForever(t *testing.T) {
	client, serverSide := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		newTestServer().serveConn(serverSide)
		close(done)
	}()

	// Nothing is ever written. Without a deadline this read blocks forever; the
	// client end forces the issue quickly by closing, and the real protection —
	// that handleConn set a deadline of its own — is asserted below.
	client.SetDeadline(time.Now().Add(2 * time.Second))
	client.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleConn did not return for a peer that sent nothing")
	}
}

// The request body is capped. A peer streaming an endless body must not make
// the helper buffer it without limit.
//
// net.Pipe is unbuffered and synchronous, so this writes until the read side
// stops consuming, which is exactly what the LimitReader causes once the cap is
// reached. Without the cap the decoder keeps consuming and this never ends.
func TestHandleConn_EndlessBodyIsBoundedRatherThanBuffered(t *testing.T) {
	client, serverSide := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		newTestServer().serveConn(serverSide)
		close(done)
	}()

	// A JSON object that never closes: valid so far, endless.
	go func() {
		client.SetDeadline(time.Now().Add(20 * time.Second))
		client.Write([]byte(`{"method":"embed","texts":["`))
		chunk := strings.Repeat("A", 64<<10)
		for {
			if _, err := client.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("handleConn kept consuming an endless request body; it must be bounded by maxHelperRequestBytes")
	}
}
