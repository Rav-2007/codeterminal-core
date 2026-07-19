package main

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// These tests are the regression cover for FAIL-3's Gate 5 (DoS hardening).
// They reproduce the socket-axis audit's exact exploit shapes against a real
// unix-socket listener running the real Server.Serve/handleConn, and prove
// each of the three additive limits closes its shape:
//
//   - message-size cap        (limitedConn.remaining / defaultMaxRequestBytes)
//   - read/idle deadline       (limitedConn idle re-arm / defaultConnIdleTimeout)
//   - concurrent-connection cap (Serve's semaphore / defaultMaxConns)
//
// None of them involves authentication; the limits fire regardless of who is
// connecting, which is the whole point (the auth-model decision is separate).

// startTestServer runs srv.Serve on a fresh unix socket in a temp dir and
// returns its path. The listener is closed on test cleanup.
func startTestServer(t *testing.T, srv *Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return path
}

// dialAndHandshake opens a connection and completes the version handshake,
// returning the live connection. Completing the handshake is the
// synchronization point that guarantees the server has accepted the
// connection and its handler goroutine is holding a concurrency slot.
func dialAndHandshake(t *testing.T, path string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := json.NewEncoder(c).Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion, ClientName: "test",
	}); err != nil {
		t.Fatalf("send handshake: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := json.NewDecoder(c).Decode(&hs); err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	if !hs.Ok {
		t.Fatalf("handshake rejected: %+v", hs)
	}
	return c
}

// roundTripPrompt handshakes, sends req, and reads a single TokenResponse. It
// returns a non-nil error if the handshake, the request write, or the response
// read fails — i.e. any way the server can reject/close the connection surfaces
// as an error, which is exactly what the size-cap test needs to observe.
func roundTripPrompt(t *testing.T, path string, req protocol.PromptRequest) (protocol.TokenResponse, error) {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		return protocol.TokenResponse{}, err
	}
	defer c.Close()
	// Guard the whole exchange so a hung test fails fast rather than blocking.
	c.SetDeadline(time.Now().Add(20 * time.Second))

	enc := json.NewEncoder(c)
	dec := json.NewDecoder(c)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"}); err != nil {
		return protocol.TokenResponse{}, err
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		return protocol.TokenResponse{}, err
	}
	if !hs.Ok {
		return protocol.TokenResponse{}, fmt.Errorf("handshake rejected: %+v", hs)
	}
	if err := enc.Encode(req); err != nil {
		return protocol.TokenResponse{}, err
	}
	var resp protocol.TokenResponse
	if err := dec.Decode(&resp); err != nil {
		return protocol.TokenResponse{}, err
	}
	return resp, nil
}

// TestServe_SizeCap_RejectsOversizeAdmitsLegit reproduces the audit's ~200 MB
// request shape (scaled down) against a small cap: a request over the cap is
// rejected before it can be decoded, while a multi-megabyte legitimate request
// under the cap still decodes and is handled normally. The legitimate request
// uses Reset:true so it completes without needing a model backend, but it
// carries a 6 MiB Prompt so it genuinely exercises the size gate with real
// large-payload bytes rather than a trivially small message.
func TestServe_SizeCap_RejectsOversizeAdmitsLegit(t *testing.T) {
	const cap = 8 << 20 // 8 MiB cap for a fast, deterministic test
	srv := &Server{logger: discardLogger(), workspace: "/ws", maxRequestBytes: cap}
	path := startTestServer(t, srv)

	// Under the cap (~6 MiB): decodes and returns a bare Done:true (Reset path,
	// no model call). Proves the cap does not punish large-but-legitimate
	// traffic.
	under := protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Reset:           true,
		Prompt:          strings.Repeat("x", 6<<20),
	}
	resp, err := roundTripPrompt(t, path, under)
	if err != nil {
		t.Fatalf("legitimate ~6 MiB request should succeed under an 8 MiB cap, got: %v", err)
	}
	if !resp.Done || resp.Error != "" || resp.Token != "" {
		t.Fatalf("legitimate request: want bare Done:true, got %+v", resp)
	}

	// Over the cap (~10 MiB): the server stops reading at the cap and closes,
	// so the exchange fails (write or read error) — the request is never
	// decoded, so RSS never balloons.
	over := protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Reset:           true,
		Prompt:          strings.Repeat("x", 10<<20),
	}
	if _, err := roundTripPrompt(t, path, over); err == nil {
		t.Fatal("oversized ~10 MiB request should be rejected (connection closed), but the exchange succeeded")
	}
}

// TestServe_IdleDeadlineReapsHalfOpenConns reproduces the audit's 100 pinned
// half-open connections: 100 clients connect and then never send their
// handshake. Before the fix these pinned 100 handler goroutines indefinitely.
// With the idle deadline, each blocked handshake read times out and every
// handler goroutine exits — shown by the goroutine count returning to baseline
// after the deadline elapses.
func TestServe_IdleDeadlineReapsHalfOpenConns(t *testing.T) {
	const (
		n           = 100
		idleTimeout = 1 * time.Second
	)
	srv := &Server{logger: discardLogger(), workspace: "/ws", connIdleTimeout: idleTimeout}
	path := startTestServer(t, srv)

	base := runtime.NumGoroutine()

	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conns = append(conns, c) // half-open: connected, never sends a byte
	}
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})

	// Within the idle window, the handler goroutines are blocked reading the
	// handshake that never comes.
	time.Sleep(idleTimeout / 4)
	pinned := runtime.NumGoroutine()
	if pinned < base+n/2 {
		t.Fatalf("expected ~%d half-open handler goroutines pinned; base=%d pinned=%d", n, base, pinned)
	}

	// After the idle deadline elapses, the blocked reads time out and the
	// goroutines are reaped back toward baseline.
	waitUntil := time.Now().Add(idleTimeout + 4*time.Second)
	for runtime.NumGoroutine() > base+10 && time.Now().Before(waitUntil) {
		time.Sleep(50 * time.Millisecond)
	}
	after := runtime.NumGoroutine()
	if after > base+15 {
		t.Fatalf("half-open handler goroutines not reaped by the idle deadline: base=%d pinned=%d after=%d", base, pinned, after)
	}
	t.Logf("half-open reap: base=%d pinned=%d after=%d (idleTimeout=%s)", base, pinned, after, idleTimeout)
}

// TestServe_ConnCeilingRejectsExcess proves that connections beyond the
// configured ceiling are rejected (closed immediately) rather than accepted
// into unbounded goroutine growth. maxConns slots are filled with live,
// handshaked connections; the next connection is refused with no handshake
// response.
func TestServe_ConnCeilingRejectsExcess(t *testing.T) {
	const maxConns = 4
	srv := &Server{
		logger:          discardLogger(),
		workspace:       "/ws",
		maxConns:        maxConns,
		connIdleTimeout: 10 * time.Second, // keep held conns alive through the test
	}
	path := startTestServer(t, srv)

	// Fill every slot. Each handshaked connection's handler is now blocked
	// reading the request, still holding its slot.
	held := make([]net.Conn, 0, maxConns)
	for i := 0; i < maxConns; i++ {
		held = append(held, dialAndHandshake(t, path))
	}
	t.Cleanup(func() {
		for _, c := range held {
			c.Close()
		}
	})

	// The next connection is past the ceiling. The server closes it, so an
	// attempted handshake gets no response (read error / EOF).
	extra, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial extra: %v", err)
	}
	defer extra.Close()
	extra.SetDeadline(time.Now().Add(3 * time.Second))
	_ = json.NewEncoder(extra).Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion, ClientName: "extra",
	})
	var hs protocol.HandshakeResponse
	if err := json.NewDecoder(extra).Decode(&hs); err == nil {
		t.Fatalf("connection past the ceiling should be rejected, but got a handshake response: %+v", hs)
	}

	// Freeing a slot lets a new connection through again — the cap is a live
	// gate, not a permanent lockout.
	held[0].Close()
	held = held[1:]
	// Give the freed handler goroutine a moment to release its slot.
	time.Sleep(200 * time.Millisecond)
	admitted := dialAndHandshake(t, path)
	admitted.Close()
}
