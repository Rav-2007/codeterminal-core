package main

import (
	"sync/atomic"

	"codeterminal/protocol"
)

// The daemon's counters.
//
// Parity with the proxy's (proxy/metrics.go), and for the same reason: the log
// explains one request, the counters are how anyone notices that a whole class of
// request has been failing since this morning. The daemon's log is stderr plus an
// optional --log-file, so "how many applies were refused today" was previously a
// grep over whatever happened to be captured -- if anything was.
//
// Two deliberate differences from the proxy:
//
//  1. They ride the EXISTING StatusResponse over the existing 0600,
//     SO_PEERCRED-authenticated Unix socket. No new mechanism, no new surface,
//     and specifically no HTTP listener -- the daemon's stated invariant is that
//     it never opens a network port, and counters are not worth weakening the
//     thing they report on. This is the same argument status.go itself made.
//  2. Plain atomics, no locks. handleStatus's contract is that it takes no locks
//     and cannot perturb what it reports; a health surface that can block a
//     request, or change behaviour by being called, is worse than none.
type counters struct {
	// Requests, by the discriminator serveConn dispatched on.
	prompts  atomic.Int64
	applies  atomic.Int64
	undos    atomic.Int64
	searches atomic.Int64
	statuses atomic.Int64
	resets   atomic.Int64

	// Outcomes of the two filesystem-mutating paths, which are the ones where a
	// failure leaves state behind.
	appliesFailed atomic.Int64
	undosFailed   atomic.Int64

	// Refusals, before a request is dispatched at all. Each of these was
	// previously visible only as a single stderr line on a connection that then
	// went away.
	peerAuthRefused   atomic.Int64
	versionMismatched atomic.Int64
	oversized         atomic.Int64
	malformed         atomic.Int64
	emptyPrompts      atomic.Int64

	// Faults contained by handleConn's recover. A daemon that has contained four
	// hundred panics is in a different state from one that has contained none,
	// and until now nothing but the log could tell them apart.
	panicsRecovered atomic.Int64
}

// count applies f to this server's counters, if it has any.
//
// The closure form keeps every increment nil-safe through one helper instead of
// one method per field, which matters because a bare Server literal in a test has
// no counters and must still work exactly as before.
func (s *Server) count(f func(*counters)) {
	if s == nil || s.counters == nil {
		return
	}
	f(s.counters)
}

// snapshot renders the counters for a StatusResponse. Reads are individually
// atomic and deliberately not consistent with each other: a status surface that
// took a lock to give a coherent snapshot could block the requests it counts,
// which is a worse trade than a number being one request stale.
func (c *counters) snapshot() *protocol.StatusCounters {
	if c == nil {
		return nil
	}
	return &protocol.StatusCounters{
		Prompts:           c.prompts.Load(),
		Applies:           c.applies.Load(),
		AppliesFailed:     c.appliesFailed.Load(),
		Undos:             c.undos.Load(),
		UndosFailed:       c.undosFailed.Load(),
		Searches:          c.searches.Load(),
		Statuses:          c.statuses.Load(),
		Resets:            c.resets.Load(),
		PeerAuthRefused:   c.peerAuthRefused.Load(),
		VersionMismatched: c.versionMismatched.Load(),
		Oversized:         c.oversized.Load(),
		Malformed:         c.malformed.Load(),
		EmptyPrompts:      c.emptyPrompts.Load(),
		PanicsRecovered:   c.panicsRecovered.Load(),
	}
}
