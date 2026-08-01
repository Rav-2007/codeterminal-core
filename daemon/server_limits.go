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

	// granted and grantCeiling bound how much the approval channel may add to
	// remaining over the whole connection. See grantReadBudget.
	granted      int64
	grantCeiling int64
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

// maxApprovalResponseBytes is the read budget granted for ONE tool-approval
// answer. A ToolApprovalResponse is a call id, a hex digest and a verb; 4 KiB
// is already generous.
//
// THE AGGREGATE IS WHAT MATTERS, and the earlier version of this comment got
// it wrong: it said the total was "bounded by the turn's iteration ceiling",
// which is not a bound at all. Grants are per ASK, and asks are per TOOL CALL
// -- and the number of tool calls in one iteration is whatever the provider's
// stream contains, which toolCallAccumulator does not cap. So the aggregate was
// a function of provider output, not of any configured ceiling (H6, registered
// in the 2026-08-01 gate and unchecked until now).
//
// grantCeiling makes the bound real and independent of both: see
// grantReadBudget.
const maxApprovalResponseBytes = 4 * 1024

// approvalGrantFraction bounds the TOTAL the approval channel may add to a
// connection's read budget, as a fraction of that budget.
//
// A quarter, so the invariant is one sentence: no matter how many times a
// client is asked, the approval channel cannot extend what it may send by more
// than 25% of the connection's original allowance. At the 16 MiB default that
// is 4 MiB, or roughly a thousand answers -- far more than any real turn and
// far less than unbounded.
const approvalGrantFraction = 4

// approvalIdleTimeout is how long a client may take to answer one approval
// before the daemon stops waiting. Sized for a HUMAN reading a tool name and
// its arguments and deciding — the ordinary 60s idle timeout is sized for a
// machine that has already made up its mind, and applying it here would reap
// the connection of any user who paused to think.
//
// Expiry is a DENIAL, never an error and never a hang: the loop feeds the model
// a refusal and carries on. Silence is not consent, and a consent prompt that
// blocks forever is a worse failure than one that gives up.
const approvalIdleTimeout = 5 * time.Minute

// grantReadBudget tops up the connection's remaining read allowance, up to a
// cumulative ceiling.
//
// The whole-connection cap exists so a client cannot make the daemon buffer
// without bound BEFORE its request is decoded. An approval answer arrives
// AFTER that point, in reply to a question the daemon chose to ask, so it is
// not covered by the original budget and would otherwise be refused by a
// connection that had already spent its allowance on a large prompt. Granting
// is deliberately explicit and per-answer rather than raising the cap: the
// daemon extends the budget exactly as far as the thing it just asked for.
//
// grantCeiling is what makes "small and finite" true rather than merely
// intended. Exhausting it is not an error and does not change any decision: the
// grant is simply not made, the client's answer either fits in what remains or
// the read fails, and a failed read is already a denial. The safe direction.
//
// Zero grantCeiling means unset, which is what every test that builds a
// limitedConn by hand gets; those never approach any of these numbers.
func (c *limitedConn) grantReadBudget(n int64) {
	if c.grantCeiling > 0 && c.granted+n > c.grantCeiling {
		n = c.grantCeiling - c.granted
	}
	if n <= 0 {
		return
	}
	c.granted += n
	c.remaining += n
}

// withIdleTimeout widens (or narrows) the idle deadline for a bounded window,
// returning a func that restores the previous value. Callers must defer the
// restore, so a long human-scale wait cannot leak into the machine-scale reads
// that follow it on the same connection.
func (c *limitedConn) withIdleTimeout(d time.Duration) func() {
	prev := c.idleTimeout
	c.idleTimeout = d
	return func() { c.idleTimeout = prev }
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
//
// THAT LAST CLAUSE IS NO LONGER UNIVERSALLY TRUE, and leaving it unqualified
// would be a documented falsehood. An agent turn can be mid-tool-call, and a
// Lane B tool is somebody else's subprocess doing work this daemon cannot
// characterise. It remains true of every non-agent turn, which is still the
// overwhelming majority and the only kind a daemon with mcp.enabled unset can
// have -- so the 5s default stays, and toolDrainGrace covers the exception.
const shutdownGrace = 5 * time.Second

// toolDrainGrace is the drain timeout used instead of shutdownGrace while a
// tool call is actually executing (see Server.WaitForDrain).
//
// Bounded by what it is waiting for rather than generous: the agent loop stops
// starting new tool calls the moment its context is cancelled, so this waits
// out at most ONE in-flight call. 30s is enough for a tool doing real local
// work and short enough that Ctrl-C still feels like Ctrl-C. A tool slower than
// this is cut, and the log says so.
const toolDrainGrace = 30 * time.Second
