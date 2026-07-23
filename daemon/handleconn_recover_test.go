package main

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"codeterminal/protocol"
)

// This file is the before/after evidence for C1's panic-backstop half. It
// forces a REAL panic through the REAL connection door — a prompt whose
// retrieval step panics — over a REAL unix socket driving Server.Serve exactly
// as production, and asserts the blast radius is exactly one connection: the
// daemon survives and serves the next client.
//
// The panic here is deliberately NOT the C1 embedder-count bug (that one is now
// caught at the boundary and returns a clean error). It is a DIFFERENT panic in
// the same handler path, which is the point of a backstop: it must contain the
// next unbounded-index / nil-deref bug too, not just the one we know about.
//
// Run with handleConn's recover() removed, conn A's panic propagates out of the
// Serve goroutine and crashes the whole test binary — every test aborts. That
// is the fail-when-neutered signal.

// panicEmbedder is an Embedder whose query path panics, standing in for any
// handler-path panic (a future bounds bug, a nil deref) without depending on
// the specific C1 shape.
type panicEmbedder struct{}

func (panicEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	panic("panicEmbedder.Embed: simulated handler panic")
}
func (panicEmbedder) EmbedQuery(ctx context.Context, texts []string) ([][]float32, error) {
	panic("panicEmbedder.EmbedQuery: simulated handler panic")
}
func (panicEmbedder) Dim() int   { return embedDim }
func (panicEmbedder) ID() string { return "panic-embedder" }

// TestHandleConn_PanicIsContainedToOneConnection drives two connections at a
// daemon whose retrieval panics: the first triggers the panic, the second must
// still get a well-formed answer, proving the process was not taken down.
func TestHandleConn_PanicIsContainedToOneConnection(t *testing.T) {
	srv := &Server{
		logger:        discardLogger(),
		workspace:     t.TempDir(),
		modelOverride: "test-model", // skip Route(cfg): keep the only panic the embedder's
		embedder:      panicEmbedder{},
		store:         emptyStore{}, // non-nil so gatherContext proceeds to retrieveTopK
		retrievalTopK: defaultK,
	}
	path := filepath.Join(t.TempDir(), "recover.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })

	// Connection A: a non-empty prompt reaches gatherContext -> retrieveTopK ->
	// EmbedQuery, which panics. With the backstop, the connection is simply
	// closed under us; without it, the daemon would be gone.
	func() {
		c, enc, dec := recoverConn(t, path)
		defer c.Close()
		if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "trigger the panic"}); err != nil {
			t.Fatalf("prompt send: %v", err)
		}
		// The recovered handler closes the connection without writing a
		// response, so the client sees EOF/an error rather than a token.
		var resp protocol.TokenResponse
		if err := dec.Decode(&resp); err == nil {
			t.Fatalf("expected the panicking connection to be closed without a response, got %+v", resp)
		}
	}()

	// Connection B: proof of life. An empty prompt is answered by serveConn
	// before retrieval runs, so it never touches the panicking embedder — a
	// clean, well-formed response here means the daemon is still accepting and
	// serving after A panicked.
	c, enc, dec := recoverConn(t, path)
	defer c.Close()
	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: ""}); err != nil {
		t.Fatalf("second prompt send: %v", err)
	}
	var resp protocol.TokenResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("daemon did not survive the panic: second connection got %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("expected the empty-prompt refusal on the surviving daemon, got %+v", resp)
	}
}

// recoverConn dials + handshakes, returning a connection ready for one request.
func recoverConn(t *testing.T, path string) (net.Conn, *json.Encoder, *json.Decoder) {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	enc := json.NewEncoder(c)
	dec := json.NewDecoder(c)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "recover"}); err != nil {
		t.Fatalf("handshake send: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake recv: %v", err)
	}
	if !hs.Ok {
		t.Fatalf("handshake rejected: %+v", hs)
	}
	c.SetDeadline(time.Now().Add(20 * time.Second))
	return c, enc, dec
}
