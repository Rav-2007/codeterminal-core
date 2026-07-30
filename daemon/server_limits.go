// Connection resource limits — FAIL-3 Gate 5 (DoS hardening). limitedConn (the
// per-connection read-byte cap + idle deadline), the default limit constants, and
// the resolved* accessors that back them. Deliberately auth-independent: these cap
// resource use, never identify the caller. The connection-ceiling semaphore that
// consumes resolvedMaxConns lives inline in Serve (server.go). See BACKLOG.md,
// "P3 daemon security review — socket axis" / Gate 5 (commit ccf8b8d).

package main

import (
	"errors"
	"net"
	"time"
)

// Connection resource limits (FAIL-3, Gate 5 — DoS hardening). These bound
// what any single socket client, or many at once, can make the daemon spend
// on memory and goroutines. They are deliberately AUTH-INDEPENDENT: nothing
// here inspects who is connecting (no peer credential, token, or handshake
// verification) — they cap resource use regardless of whether the peer is a
// trusted same-uid client or not. Each Server field below overrides the
// matching default when non-zero (same "0 => default" convention as
// RetrievalConfig.resolvedTopK); production leaves them zero and gets these
// defaults, tests inject smaller values to exercise the limits quickly.
const (
	// defaultMaxRequestBytes caps the total bytes one connection may make the
	// daemon buffer while decoding its handshake + single request, before the
	// JSON is even parsed. The socket-axis audit drove RSS to ~896 MB with a
	// single ~200 MB request against an unbounded json.NewDecoder(conn); this
	// bounds that to tens of MiB. 16 MiB is ~16x the indexer's own per-file
	// ceiling (maxFileSize, chunker.go) and leaves generous room for the
	// largest legitimate message — an ApplyEditRequest carrying a whole file's
	// Search+Replace, or a prompt with large pasted content plus history —
	// while sitting ~12x below the 200 MB exploit.
	defaultMaxRequestBytes = 16 << 20 // 16 MiB

	// defaultConnIdleTimeout bounds how long a single read or write on a client
	// connection may block with no progress. It is an IDLE deadline, re-armed
	// around every Read and Write (see limitedConn), not a whole-request
	// budget: a legitimately slow client that keeps sending or draining bytes
	// is never punished, but a half-open connection that opens and then never
	// completes its request — the audit's 100-pinned-connection repro — has its
	// blocked read time out and is reaped instead of pinning a goroutine
	// forever. Idle gaps between writes (e.g. model time-to-first-token) never
	// trip it: only a genuinely blocked read or write can exceed the deadline.
	defaultConnIdleTimeout = 60 * time.Second

	// defaultMaxConns caps concurrent client connections. Serve otherwise
	// accepts in an unbounded loop, one goroutine per connection, with no
	// ceiling (the audit's third finding). 128 is far above any legitimate
	// concurrency — a single CLI/TUI/VS Code client uses one short-lived
	// connection per request — so normal use never approaches it, while it
	// bounds worst-case goroutine growth and, together with
	// defaultMaxRequestBytes, worst-case simultaneously-buffered memory.
	defaultMaxConns = 128
)

func (s *Server) resolvedMaxRequestBytes() int64 {
	if s.maxRequestBytes > 0 {
		return s.maxRequestBytes
	}
	return defaultMaxRequestBytes
}

func (s *Server) resolvedConnIdleTimeout() time.Duration {
	if s.connIdleTimeout > 0 {
		return s.connIdleTimeout
	}
	return defaultConnIdleTimeout
}

func (s *Server) resolvedMaxConns() int {
	if s.maxConns > 0 {
		return s.maxConns
	}
	return defaultMaxConns
}

// errRequestTooLarge is returned by limitedConn.Read once a connection has
// tried to make the daemon buffer more than its byte budget, so handleConn can
// tell an oversized request apart from an ordinary read error in the logs.
var errRequestTooLarge = errors.New("request exceeds maximum size")

// limitedConn wraps a client connection with two auth-independent resource
// bounds used by handleConn (FAIL-3, Gate 5): a cumulative cap on bytes read
// before the request is decoded, and an idle deadline re-armed around every
// read and write. It is deliberately NOT an authentication or authorization
// layer — it never inspects who is connecting, only how much they consume.
type limitedConn struct {
	net.Conn
	remaining   int64         // read-byte budget left before errRequestTooLarge
	idleTimeout time.Duration // re-armed around each Read and Write
}

// Read enforces the byte budget and re-arms the read deadline before each
// underlying read. Because json.Decoder reads incrementally, a request larger
// than the budget drives remaining to zero and the next read (the one that
// would push past the cap) fails with errRequestTooLarge — a request of exactly
// the budget still decodes, one of budget+1 bytes does not.
func (c *limitedConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errRequestTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.idleTimeout)); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(p)
	c.remaining -= int64(n)
	return n, err
}

// Write re-arms the write deadline before each underlying write, so a client
// that stops draining the response stream (filling the socket buffer until a
// write blocks) is reaped instead of pinning the handler goroutine. Idle gaps
// between writes — e.g. model time-to-first-token — are not affected, since
// only a genuinely blocked write can exceed the deadline.
func (c *limitedConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.idleTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

// shutdownGrace is how long shutdown waits for in-flight connections after the
// listener closes (see Server.WaitForDrain).
//
// Sized by what a CUT request can damage, not by how long one can take. The
// filesystem-mutating paths -- Apply's multi-file write plus its backup session,
// Undo's restore -- are local disk I/O measured in milliseconds, so 5s is generous
// for every case where being cut leaves real mess behind. A streaming prompt can
// legitimately run for minutes; waiting that out would make Ctrl-C feel broken,
// and a cut prompt mutates nothing and costs only a re-ask.
const shutdownGrace = 5 * time.Second
