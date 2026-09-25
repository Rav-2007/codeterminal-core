package main

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"mochiii/protocol"
)

// The daemon's health surface.
//
// Before this, an operator had no way to ask a running daemon anything. There
// were no metrics and no health endpoint; the only account of the daemon's
// state was its stderr, which is unavailable if it was started detached, gone
// if it was not captured, and useful only to someone who already knows which
// lines matter. Determining whether a running daemon was healthy required
// reading the source.
//
// Three deliberate choices, all reversible but each with a reason:
//
//  1. It rides the EXISTING Unix socket, not an HTTP port. The daemon's stated
//     invariant is that it never listens on a network port; the socket is 0600
//     and SO_PEERCRED-authenticated (Gate 3), and a localhost HTTP listener
//     would be neither. Health reporting is not worth weakening the thing it
//     reports on, and the socket already multiplexes apply/undo/search through
//     the same discriminator peek, so this adds a message rather than a
//     mechanism.
//
//  2. It reports the two retrieval tiers separately. A single "retrieval: ok"
//     boolean would have re-hidden the exact state (semantic up, lexical down)
//     that motivated this cluster.
//
//  3. It reuses the same Degradation values the prompt path pushes, so the
//     pushed and pulled views of health cannot drift apart.

// startedAt is set once at process start and read by the status handler for
// uptime. A package-level var rather than a Server field so it measures the
// PROCESS's life, not the moment a Server literal happened to be built (which
// in tests is arbitrary and in main.go is after retrieval setup has already
// spent real time opening the index and starting the embedder).
var startedAt = time.Now()

// isStatusRequest sniffs whether raw is a StatusRequest, by the presence of
// its "status" key -- mirroring isUndoRequest/isSearchRequest exactly.
// StatusRequest always serializes "status" (no omitempty, see its doc
// comment), and no other request type has that key, so a real one is always
// caught here and every existing message shape still falls through unchanged.
//
// The key is matched EXACTLY (requestfields.go).
func isStatusRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "status")
}

// handleStatus answers a StatusRequest from state the Server already holds.
//
// It is strictly read-only and takes no locks: every field below is either
// fixed at startup (workspace, retrieval wiring, config) or a cheap read
// (Count, uptime), so asking a busy daemon for its status can never block a
// request or perturb what it reports. That matters for a health check
// specifically -- one that can hang, or that changes behavior by being called,
// is worse than none.
func (s *Server) handleStatus(enc *json.Encoder) {
	decision := s.route("", "")

	resp := protocol.StatusResponse{
		ProtocolVersion:  protocol.ProtocolVersion,
		DaemonVersion:    daemonVersion,
		PID:              os.Getpid(),
		UptimeSeconds:    int64(time.Since(startedAt).Seconds()),
		Workspace:        s.workspace,
		Tier:             decision.Tier,
		Model:            decision.Slug,
		AvailableTiers:   availableTiers(s.cfg),
		Retrieval:        s.statusRetrieval(),
		MemoryAvailable:  s.memory != nil,
		APIKeyConfigured: func() bool { k, _ := s.credentials(); return k != "" }(),
		// statusDegradations, not degradations: this is the ONE surface that
		// reports index staleness, which is not constant for the daemon's
		// lifetime and costs a stat-only sweep. The prompt path keeps the cheap,
		// lifetime-constant set.
		Degraded: s.statusDegradations(time.Now()),
		Counters: s.counters.snapshot(),
	}
	if s.cfg != nil {
		resp.ConfigVersion = s.cfg.ConfigVersion
		resp.ConfigWarnings = s.cfg.Warnings()
	}

	s.logger.Printf("status: requested (uptime=%ds retrieval=%t lexical=%t memory=%t degraded=%d)",
		resp.UptimeSeconds, resp.Retrieval.Enabled, resp.Retrieval.Lexical, resp.MemoryAvailable, len(resp.Degraded))

	if err := enc.Encode(resp); err != nil {
		s.logger.Printf("status write error: %v", err)
	}
}

// availableTiers copies models.json tiers into the wire shape, sorted by name
// so clients get a stable list for "/model" menus.
func availableTiers(cfg *Config) []protocol.StatusTier {
	if cfg == nil || len(cfg.Tiers) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.Tiers))
	for name := range cfg.Tiers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]protocol.StatusTier, 0, len(names))
	for _, name := range names {
		t := cfg.Tiers[name]
		out = append(out, protocol.StatusTier{
			Name:   name,
			Slug:   t.Slug,
			Active: t.Active,
			Note:   t.Note,
		})
	}
	return out
}

// statusRetrieval describes both retrieval tiers independently. Enabled
// tracks the SEMANTIC tier (embedder + vector store), which is what
// gatherContext actually gates on; Lexical tracks the FTS5 tier, which
// degrades on its own and never disables retrieval as a whole.
func (s *Server) statusRetrieval() protocol.StatusRetrieval {
	enabled := s.embedder != nil && s.store != nil

	out := protocol.StatusRetrieval{
		Enabled: enabled,
		Lexical: s.lexicalStore != nil,
	}
	if !enabled {
		// The same specific, client-safe cause GroundingInfo reports (Fix 8),
		// not a re-guess at it.
		out.Reason = s.retrievalDisabledReason
		if out.Reason == "" {
			out.Reason = "retrieval unavailable for this daemon"
		}
		return out
	}

	out.TopK = s.retrievalTopK
	out.ContextBudgetChars = s.contextBudgetChars
	out.IndexedChunks = s.store.Count()
	return out
}
