package main

import (
	"codeterminal/protocol"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Wire-visible degraded-state reporting.
//
// The A6-A9 debug pass named the pattern this file closes: the daemon degrades
// gracefully and reports it honestly -- to stderr -- while the bytes it puts on
// the socket stay indistinguishable from a fully healthy response. Reconfirmed
// live before writing any of this: with the lexical index deliberately made
// unopenable, stderr said "lexical retrieval tier disabled" and "lexical=false"
// while the wire carried {"grounded":true,"chunks":3} -- identical, apart from
// the workspace label, to the healthy baseline. The TUI rendered "grounded ✓ 3
// chunk(s)". Retrieval quality had halved and nothing said so.
//
// Every string below is client-facing, so each follows the Fix 8 / Gate 7
// discipline: say WHAT is reduced and what it costs the user, never WHERE
// anything lives. No paths, hosts, or upstream error text -- the daemon log
// keeps the full diagnostic (including the absolute index path and the
// underlying open error), and the client gets the consequence.
const (
	detailLexicalDown       = "keyword (lexical) search is unavailable, so answers are grounded by semantic similarity alone; exact identifier matches may be missed"
	detailMemoryDown        = "cross-session conversation memory is unavailable; this conversation works normally but will not be remembered after the client closes"
	detailFallbacks         = "provider routing permits fallbacks outside the configured zero-data-retention constraints, so a request may be served by a non-ZDR endpoint"
	detailNonZDR            = "zero-data-retention routing is not enforced for this daemon"
	detailCollection        = "providers that may store or train on request data are permitted for this daemon"
	detailWorkspaceTooLarge = "workspace is too large (> 10,000 files), semantic search is disabled"
)

// degradations reports every subsystem currently running in a reduced mode.
// Returns nil when nothing is degraded, so TokenResponse.Degraded's omitempty
// drops the field entirely on a healthy daemon -- a healthy response is
// byte-identical to what it was before this existed.
//
// All of these are resolved at startup and constant for the daemon's lifetime,
// which is why this can be computed per-request without cost and without
// racing anything: retrieval and memory are wired once in main.go and never
// re-opened, and the routing config is read-only after load.
//
// THAT INVARIANT IS LOAD-BEARING, and index staleness deliberately breaks it --
// which is why staleness is NOT here. See statusDegradations below.
func (s *Server) degradations() []protocol.Degradation {
	var out []protocol.Degradation

	// Reported ONLY when the semantic tier is actually working. If retrieval is
	// off entirely, GroundingInfo.Reason already says so with a specific cause,
	// and adding "the lexical half is also down" would be noise about a
	// subsystem the user has already been told is not running at all.
	if s.store != nil && s.embedder != nil && s.lexicalStore == nil {
		out = append(out, protocol.Degradation{
			Component: protocol.DegradedLexicalRetrieval,
			Detail:    detailLexicalDown,
		})
	}

	if s.memory == nil {
		out = append(out, protocol.Degradation{
			Component: protocol.DegradedMemory,
			Detail:    detailMemoryDown,
		})
	}

	out = append(out, s.routingDegradations()...)

	if present, meta := s.workspaceTooLargeMarker(); present {
		out = append(out, protocol.Degradation{
			Component: protocol.DegradedWorkspaceTooLarge,
			Detail:    detailWorkspaceTooLarge,
			Metadata:  meta,
		})
	}

	return out
}

// routingDegradations reports the privacy-posture weakenings a daemon's ZDR
// config has relative to the secure default.
//
// Each corresponds to one WEAKEN-bool in ZDRConfig, whose zero value is the
// strictest setting precisely so that "off" is never reachable by omission.
// The same reasoning applies to reporting: if someone has explicitly opted out
// of a guarantee the product's whole pitch rests on, the client asking for
// that guarantee should be able to see it, not have to read models.json.
//
// A nil cfg (a Server built directly by a test) reports nothing rather than
// panicking, matching Server.noScrub's tolerance of the same case.
func (s *Server) routingDegradations() []protocol.Degradation {
	if s.cfg == nil {
		return nil
	}
	var out []protocol.Degradation
	if s.cfg.ZDR.AllowNonZDR {
		out = append(out, protocol.Degradation{Component: protocol.DegradedProviderRouting, Detail: detailNonZDR})
	}
	if s.cfg.ZDR.AllowDataCollection {
		out = append(out, protocol.Degradation{Component: protocol.DegradedProviderRouting, Detail: detailCollection})
	}
	if s.cfg.ZDR.AllowFallbacks {
		out = append(out, protocol.Degradation{Component: protocol.DegradedProviderRouting, Detail: detailFallbacks})
	}
	return out
}

// logDegradations writes one startup line per degraded subsystem, so the
// daemon log and the wire agree about what is reduced. The log line is the
// operator's copy; the wire report is the user's.
func (s *Server) logDegradations() {
	for _, d := range s.degradations() {
		s.logger.Printf("degraded: component=%s: %s", d.Component, d.Detail)
	}
}

// statusDegradations is degradations() plus the ones that can CHANGE while the
// daemon runs. Today that is exactly one: index staleness.
//
// This is a separate function, not an argument to degradations(), and the
// separation is structural rather than a performance tweak:
//
//   - degradations()'s doc comment states that everything it reports is
//     constant for the daemon's lifetime, and several callers rely on that by
//     computing it per-prompt without caching. Index staleness violates it in
//     both directions -- editing a file makes a fresh index stale, and
//     re-indexing makes it fresh again, neither requiring a restart. Folding it
//     in would quietly falsify a comment that other code trusts.
//
//   - It costs a directory walk. The prompt path must not pay that per turn,
//     and a signal that made every prompt slower would be removed by the first
//     person who profiled it.
//
//   - A prompt reports grounding per-answer already (GroundingInfo). The right
//     place for "your index is behind" is the health surface an operator or an
//     extension polls, not a line appended to every reply.
//
// The sweep is cached (freshnessCache), so polling status in a loop does not
// turn a health check into load.
func (s *Server) statusDegradations(now time.Time) []protocol.Degradation {
	out := s.degradations()

	// Only meaningful when the semantic tier is actually serving. With
	// retrieval off, StatusRetrieval.Reason already says so specifically, and
	// "your index is stale" about an index nobody is reading is noise.
	if s.store == nil || s.embedder == nil || s.workspace == "" {
		return out
	}
	if s.freshness == nil {
		return out
	}

	indexDir := filepath.Join(s.workspace, indexDirName)
	res := s.freshness.get(now, func() indexFreshnessResult {
		return scanIndexFreshness(s.workspace, readStampBuiltAt(indexDir))
	})
	if d := indexStaleDegradation(res); d != nil {
		// The operator's copy gets the evidence; the wire gets counts only.
		s.logger.Printf("degraded: component=%s: %d file(s) modified after the index was built at %s",
			d.Component, res.Changed, res.BuiltAt.UTC().Format(time.RFC3339))
		out = append(out, *d)
	}
	return out
}

// maxTooLargeMarkerBytes caps how much of the workspace-too-large marker is
// read, and the number is DERIVED FROM ITS ONLY WRITER rather than picked.
//
// index_cmd.go writes the marker with one fmt.Sprintf carrying four fields:
//
//	{"file_count": %d, "limit_exceeded": true, "time_ms": %d, "mem_mb": %d}
//
// The literal text is 65 bytes and the three integers are at most 19 digits
// each even at int64 maxima, so the largest payload the writer can produce is
// 122 bytes. 4 KiB is 33x that, and one filesystem block, so it cannot become a
// real limit through ordinary growth -- another field of the same kind costs
// about 25 bytes and there is room for a hundred and sixty of them.
//
// Against what it is actually for, it is 65,536x smaller: the measured probe
// used a 256 MiB marker.
const maxTooLargeMarkerBytes = 4096

// workspaceTooLargeMarker reports whether the indexer left its
// workspace-too-large marker, and what metadata it carries.
//
// THE MARKER IS WORKSPACE CONTENT, WHICH MEANS IT IS UNTRUSTED. It lives at
// .codeterminal/index/TOO_LARGE inside the workspace, so a cloned repository can
// ship one, and degradations() runs on EVERY prompt. The previous
// implementation was one os.ReadFile plus a json.Unmarshal into map[string]any,
// with no cap, no link check, and no test of what the path even pointed at.
// Three things followed from that one read, and one cap plus one fstat closes
// all three:
//
//  1. MEMORY. Measured: a 256 MiB marker cost 512 MiB of heap -- 2x, because
//     the bytes are read and then the JSON is decoded into a second structure --
//     in 1.20 s, on every turn.
//  2. WIRE INFLATION. The decoded map goes into TokenResponse.Degraded, so the
//     same 256 MiB marker produced a 268,435,598-byte message sent to the client
//     at the start of every turn. Same root cause, same fix: bytes that are
//     never read cannot be forwarded.
//  3. BLOCKING, which is the worst of the three and was not what the size
//     framing predicted. On POSIX, open(2) on a FIFO with no writer waits
//     forever. Measured: os.ReadFile on a FIFO planted at that path was still
//     parked after three seconds with no path to return. A 512 MiB allocation is
//     recoverable; a handler blocked forever holds one of 128 connection slots
//     and hangs every turn that follows. See platform.go for openNonBlock.
//
// BEHAVIOUR AT THE CAP IS DEFINED AND VISIBLE, never silent. The marker's
// EXISTENCE is the signal -- the workspace is too large, semantic search is off
// -- and its content is optional detail. So an oversized, malformed, or
// not-a-regular-file marker still reports the degradation and drops only the
// metadata, and says so in the daemon log. Suppressing the degradation instead
// would let anyone who can write one byte into the workspace hide the fact that
// retrieval is disabled, which is the wrong direction for an honesty signal.
//
// The log line is written on every affected turn rather than once, deliberately:
// the alternative is per-Server state, and this function's doc comment promises
// it can be computed per-request "without cost and without racing anything".
// A repeated line about a pathological marker is cheaper than making that
// promise false.
func (s *Server) workspaceTooLargeMarker() (present bool, meta map[string]any) {
	path := filepath.Join(s.workspace, ".codeterminal", "index", "TOO_LARGE")

	// O_NOFOLLOW: a symlink at the leaf is refused rather than followed, so the
	// marker cannot be aimed at /dev/zero or at a file outside the workspace.
	// O_NONBLOCK: see above -- this is what stops a FIFO parking the turn.
	f, err := openNoFollow(path, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		if !os.IsNotExist(err) {
			// Not "absent" -- present and unreadable, which is a different
			// state and must not be reported as the same one. A symlink at the
			// leaf lands here.
			s.logger.Printf("degraded: workspace-too-large marker exists but could not be opened: %v", err)
		}
		return false, nil
	}
	defer f.Close()

	// fstat on the DESCRIPTOR, not stat on the path: a stat-then-open pair has a
	// window in which the thing that was checked is not the thing that was
	// opened, and the marker sits in a directory the workspace controls.
	info, err := f.Stat()
	switch {
	case err != nil:
		s.logger.Printf("degraded: workspace-too-large marker could not be stat'd; reporting the degradation without its metadata: %v", err)
		return true, nil
	case !info.Mode().IsRegular():
		s.logger.Printf("degraded: workspace-too-large marker is not a regular file (mode %s); reporting the degradation without its metadata", info.Mode())
		return true, nil
	}

	// One byte past the cap, so "exactly at the cap" and "over it" are
	// distinguishable without reading any further.
	payload, err := io.ReadAll(io.LimitReader(f, maxTooLargeMarkerBytes+1))
	if err != nil {
		s.logger.Printf("degraded: workspace-too-large marker could not be read; reporting the degradation without its metadata: %v", err)
		return true, nil
	}
	if len(payload) > maxTooLargeMarkerBytes {
		s.logger.Printf("degraded: workspace-too-large marker exceeds %d bytes; reporting the degradation without its metadata", maxTooLargeMarkerBytes)
		return true, nil
	}
	if len(payload) == 0 {
		return true, nil
	}
	if err := json.Unmarshal(payload, &meta); err != nil {
		s.logger.Printf("degraded: workspace-too-large marker is not valid JSON; reporting the degradation without its metadata: %v", err)
		return true, nil
	}
	return true, meta
}
