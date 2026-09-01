package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// daemonVersion is reported to clients in the handshake response.
const daemonVersion = "0.1.0-skeleton"

// Server accepts client connections on the UDS listener, performs the
// version handshake, and proxies one prompt per connection to the model API.
type Server struct {
	apiBase       string
	apiKey        string
	cfg           *Config
	modelOverride string // optional testing override; bypasses the router when set
	systemPrompt  string
	tcpToken      string // Bearer token for TCP auth; empty if UDS
	logger        *log.Logger

	// embedder and store are both nil when retrieval is disabled or
	// unavailable (see setupRetrieval in retrieval_setup.go) — every use
	// of them (gatherContext, in context.go) must handle that.
	embedder Embedder
	store    VectorStore
	// lexicalStore is the keyword/FTS5 tier that complements store's
	// semantic search (see rerank.go's fuseRRF). It degrades independently
	// and more loosely than embedder/store: nil here means only the lexical
	// half of retrieval is unavailable, not retrieval as a whole — every use
	// of it (retrieveTopK, via gatherContext) must treat nil as "semantic
	// only" rather than "retrieval disabled".
	lexicalStore       LexicalStore
	retrievalTopK      int
	contextBudgetChars int
	// expandPolicy is how far a hit is widened before budgeting, or nil for
	// defaultExpandPolicy (chunkexpand.go).
	//
	// A POINTER, not a value, because the zero expandPolicy is meaningful --
	// TopN 0 turns expansion off -- so an unset field and "expansion disabled"
	// would otherwise be indistinguishable, and every Server built before this
	// field existed would have silently lost expansion. nil means "unset".
	//
	// It is per-Server for the same reason retrievalTopK and contextBudgetChars
	// are: these three together decide how much retrieved context reaches the
	// model, and measuring that trade-off needs two Servers that differ only in
	// them. See TestContextDilutionEarnsItsTokens.
	expandPolicy *expandPolicy
	// retrievalDisabledReason is the specific, client-safe explanation of why
	// embedder/store are nil, set once at startup by setupRetrieval (Fix 8).
	// Without it every cause reported the same misleading "no embedder/index
	// configured" string, which blamed the index for what was usually a
	// helper-binary-not-found. Empty when retrieval is working.
	retrievalDisabledReason string
	debugContext            bool // --debug-context: log full retrieved chunk content
	rerankDisabled          bool // --no-rerank / retrieval.rerank_disabled: raw similarity order, no class weighting

	// workspace is this daemon's own resolved (absolute) grounding
	// workspace — set once at startup regardless of whether retrieval is
	// actually enabled, purely so it can be reported back to clients via
	// GroundingInfo (see buildGroundingInfo in context.go).
	workspace string

	// memory is the cross-session conversation-memory store, keyed by
	// workspace (see daemon/memory.go). nil when unavailable (missing
	// state dir, unwritable, or corrupt DB) — every use must handle that,
	// degrading to "no cross-session memory" rather than failing the
	// request, exactly like embedder/store above.
	memory *MemoryStore

	// freshness memoises "has the workspace changed since the index was built"
	// for the status handler (see indexfreshness.go). nil disables the check
	// entirely, which is what every test Server that does not opt in gets --
	// deliberately, so a directory walk never appears in a test that did not
	// ask for one.
	freshness *freshnessCache

	// warnSink is the durable, local, append-only home for warn-mode
	// (log-only) chunk-secret fire events (see warnsink.go / logChunkScrub).
	// nil is a valid no-op sink — a failing or unconfigured sink must never
	// affect a request, so every use goes through its nil-safe write method.
	warnSink *warnSink

	// toolAudit is the durable, local, append-only record of every agent-mode
	// tool-call decision (see toolaudit.go). It is the half of "you approve
	// every call and everything is audited" that survives the process. nil is a
	// valid no-op sink, and an audit-write failure never fails a turn.
	toolAudit *toolAuditSink

	// Connection resource limits (FAIL-3, Gate 5 DoS hardening). Zero means
	// "use the default" — see defaultMaxRequestBytes / defaultConnIdleTimeout /
	// defaultMaxConns and the resolved* accessors below. Production leaves them
	// zero (main.go never sets them); tests set small values to exercise the
	// limits quickly. None of these is an auth control — they cap resource use,
	// not access.
	maxRequestBytes int64
	connIdleTimeout time.Duration
	maxConns        int

	// counters is what this daemon has done since it started (see counters.go).
	// Never nil in production (main.go builds the Server literal with one) and
	// nil-safe everywhere, so a test that builds a bare Server still works.
	counters *counters

	// applyLocks serializes the filesystem-mutating request paths per workspace
	// root (FAIL-3, Gate 6 — data-integrity). It maps a resolved workspace root
	// to the *sync.Mutex guarding it; lockWorkspace get-or-creates and holds it
	// across an Apply's read-modify-write + backup/prune, or an Undo's validate
	// + restore, so two such operations on the SAME workspace can't interleave
	// while different workspaces never block each other. sync.Map's zero value
	// is ready to use, so no constructor is needed and every existing Server
	// literal gets correct serialization for free. See lockWorkspace.
	applyLocks sync.Map

	// inFlight counts connections currently being handled, so shutdown can wait
	// for them instead of exiting from under them. Serve adds before dispatching
	// and the handler goroutine subtracts on the way out; WaitForDrain blocks on
	// it. Zero value is ready to use, so every existing Server literal (including
	// the test ones) gets the same accounting for free.
	inFlight sync.WaitGroup

	// toolsInFlight counts tool calls currently EXECUTING, which is a stricter
	// thing than a connection being open. Shutdown consults it because a cut
	// tool call is the one case where the old "a cut prompt mutates nothing"
	// assumption fails: a Lane B tool is somebody else's subprocess doing work
	// this daemon cannot characterise or undo. See WaitForDrain.
	toolsInFlight atomic.Int64

	// shutdownCtx is cancelled when the daemon begins shutting down. Only the
	// agent loop consults it, and only between steps: it is how a turn stops
	// starting NEW tool calls once shutdown has begun, which is what keeps
	// toolDrainGrace bounded to the one call already running.
	//
	// Nil means "never cancelled" (see shutdownContext), so every existing
	// Server literal, including the test ones, keeps working untouched.
	shutdownCtx context.Context

	// lspBridge manages the connection to native LSP servers (like gopls)
	// for precise AST/compiler-based definitions and references.
	lspBridge *LSPBridge
}

// shutdownContext returns the cancellation signal for long-running work,
// defaulting to a never-cancelled context on a Server that was built without
// one.
func (s *Server) shutdownContext() context.Context {
	if s.shutdownCtx == nil {
		return context.Background()
	}
	return s.shutdownCtx
}

// WaitForDrain blocks until every in-flight connection has finished, or until
// timeout elapses. It reports whether the drain completed.
//
// The caller must have closed the listener first: Serve's Accept loop exits on
// net.ErrClosed, and only then is the set of in-flight connections a fixed set
// that can actually reach zero. Calling this while still accepting would wait on
// a moving target.
//
// Before this, shutdown was `ln.Close(); os.Remove(socket)` and then main
// returned, which ends the process and every handler goroutine with it. Serve's
// semaphore bounded concurrency but nothing ever waited on it. A prompt cut that
// way is merely annoying, but an ApplyEditRequest is a multi-file write: cutting
// it mid-batch leaves some files written and some not, with the backup session
// half-populated. Undo can recover that, but only if the user knows to run it.
// AGENT MODE CHANGED THE PREMISE. Everything above was written when the worst
// a cut request could do was abandon a prompt (harmless) or half-write an edit
// batch (recoverable via the backup session). A turn that runs tools can be
// mid-tool-call, and a Lane B tool is an arbitrary subprocess doing arbitrary
// work -- so "5 seconds is generous" stops being true exactly when a tool is
// executing.
//
// Hence two graces rather than one. The ordinary timeout still applies to
// ordinary work; a turn with a tool actually running gets toolDrainGrace
// instead. It is not longer for its own sake: the loop stops starting NEW tool
// calls as soon as the context is cancelled, so this waits out at most the one
// call already in flight.
func (s *Server) WaitForDrain(timeout time.Duration) bool {
	if s.toolsInFlight.Load() > 0 && timeout < toolDrainGrace {
		timeout = toolDrainGrace
	}
	done := make(chan struct{})
	go func() {
		s.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// route decides which tier handles the next request. preferredTier is the
// client's explicit models.json tier name (PromptRequest.Tier); when set and
// valid it wins. Otherwise promptKind may escalate to reasoning. An empty
// preferredTier and unrecognized promptKind resolve to the default tier.
func (s *Server) route(promptKind, preferredTier string) RouteDecision {
	if s.modelOverride != "" {
		return RouteDecision{Tier: "override", Slug: s.modelOverride, Reason: "manual override via --model flag"}
	}
	return Route(s.cfg, RouteInput{HasExitSignal: false, PromptKind: promptKind, PreferredTier: preferredTier})
}

// Serve accepts connections until the listener is closed. Each connection is
// handled in its own goroutine so slow or streaming clients never block
// others.
//
// A buffered channel gates concurrency (FAIL-3, Gate 5): the accept loop
// otherwise spawns one unbounded goroutine per connection. The gate is
// non-blocking — a connection past the ceiling is rejected (closed)
// immediately rather than queued (which would just relocate the unbounded
// growth) or allowed to block the accept loop (which would freeze new-
// connection handling entirely). The slot is released with a defer so a
// panicking handler frees its slot too. A panic in a handler no longer
// propagates out of the goroutine either: handleConn now recovers it and
// contains it to the one connection (see handleConn's defer). This bounds how
// many connections run at once; it is not an auth control and never inspects
// who is connecting.
func (s *Server) Serve(ln net.Listener) {
	sem := make(chan struct{}, s.resolvedMaxConns())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger.Printf("accept error: %v", err)
			}
			return
		}
		select {
		case sem <- struct{}{}:
			// Add BEFORE the goroutine starts. Doing it inside would race
			// shutdown: WaitForDrain could observe a zero counter and report a
			// clean drain while this connection's handler had not yet begun.
			s.inFlight.Add(1)
			go func() {
				defer s.inFlight.Done()
				defer func() { <-sem }()
				s.handleConn(conn)
			}()
		default:
			s.logger.Printf("connection refused: at capacity (%d concurrent connections)", cap(sem))
			conn.Close()
		}
	}
}

// handleConn is the entry point for every accepted connection: it
// authenticates the peer and, only if that passes, hands off to serveConn for
// the handshake and request dispatch.
//
// Authentication happens before touching the connection for anything else
// (FAIL-3, Gate 3). Until this check, any process that could open the socket
// was treated as fully trusted; instead we confirm via the OS that the peer
// runs as this daemon's own UID, and refuse — before the handshake is read,
// before any request is decoded or dispatched — if it doesn't, or if the check
// itself fails. Fails closed. See authorizePeer.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	// Panic backstop (FAIL-3 scoped follow-up, given real urgency by C1). A
	// panic anywhere below — a bounds bug reaching a slice index, a nil
	// dereference in some handler, a future regression — used to propagate out
	// of this goroutine and terminate the whole daemon, dropping every other
	// in-flight client. Recover here so the blast radius is exactly this one
	// connection: it is logged and closed (the deferred Close above still runs
	// during unwinding), and the daemon keeps serving everyone else. This is
	// defense-in-depth, not a substitute for fixing the panics themselves (the
	// C1 boundary check removes the known embedder-response one at its source).
	defer func() {
		if r := recover(); r != nil {
			s.count(func(c *counters) { c.panicsRecovered.Add(1) })
			s.logger.Printf("recovered from panic while handling a connection: %v\n%s", r, debug.Stack())
		}
	}()

	if err := protocol.AuthorizePeer(conn); err != nil {
		s.count(func(c *counters) { c.peerAuthRefused.Add(1) })
		s.logger.Printf("connection refused: %v", err)
		return
	}

	s.serveConn(conn)
}

// serveConn runs the version handshake and dispatches the single request a
// connection carries. It is reached only after handleConn has authenticated
// the peer, so everything here may assume a same-UID, authorized caller — the
// dispatch logic is deliberately unchanged from before Gate 3. It does not
// close conn; handleConn owns the connection's lifecycle (see its defer).
func (s *Server) serveConn(conn net.Conn) {
	s.logger.Print("client connected")

	// Bound how much this connection can make the daemon buffer, and how long
	// any single read/write may block, before the request is even decoded
	// (FAIL-3, Gate 5). Auth-independent: this caps resource use, not access.
	budget := s.resolvedMaxRequestBytes()
	lc := &limitedConn{
		Conn:        conn,
		remaining:   budget,
		idleTimeout: s.resolvedConnIdleTimeout(),
		// The approval channel may extend this connection's read budget, but
		// only by a fixed fraction of it, however many times it is asked.
		grantCeiling: budget / approvalGrantFraction,
	}
	dec := json.NewDecoder(lc)
	enc := json.NewEncoder(lc)

	var hsReq protocol.HandshakeRequest
	if err := dec.Decode(&hsReq); err != nil {
		s.logger.Printf("handshake read error: %v", err)
		return
	}

	if hsReq.ProtocolVersion != protocol.ProtocolVersion {
		s.count(func(c *counters) { c.versionMismatched.Add(1) })
		s.logger.Printf("rejecting client %q: protocol version %d != %d", hsReq.ClientName, hsReq.ProtocolVersion, protocol.ProtocolVersion)
		enc.Encode(protocol.HandshakeResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Ok:              false,
			Error: fmt.Sprintf("protocol version mismatch: daemon speaks v%d, client speaks v%d",
				protocol.ProtocolVersion, hsReq.ProtocolVersion),
		})
		return
	}

	// Constant-time. A bearer token is the ONLY thing standing in front of a
	// network listener, so an ordinary string compare — which returns on the
	// first differing byte — leaks the token's prefix through response timing.
	// Unreachable today (see protocol.TransportTCP: nothing selects TCP), but
	// this is the line that would be load-bearing the moment anything does, and
	// a timing-safe compare costs nothing.
	if s.tcpToken != "" && subtle.ConstantTimeCompare([]byte(hsReq.BearerToken), []byte(s.tcpToken)) != 1 {
		s.logger.Printf("rejecting client %q: missing or invalid bearer token", hsReq.ClientName)
		enc.Encode(protocol.HandshakeResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Ok:              false,
			Error:           "HTTP 401 Unauthorized: invalid or missing bearer token",
		})
		return
	}

	if err := enc.Encode(protocol.HandshakeResponse{
		ProtocolVersion:  protocol.ProtocolVersion,
		Ok:               true,
		DaemonVersion:    daemonVersion,
		PersistedHistory: s.loadPersistedHistory(),
	}); err != nil {
		s.logger.Printf("handshake write error: %v", err)
		return
	}
	s.logger.Printf("handshake ok with client %q", hsReq.ClientName)

	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, errRequestTooLarge) {
			s.count(func(c *counters) { c.oversized.Add(1) })
			s.logger.Printf("rejecting oversized request (cap %d bytes)", s.resolvedMaxRequestBytes())
			return
		}
		s.count(func(c *counters) { c.malformed.Add(1) })
		s.logger.Printf("request read error: %v", err)
		return
	}

	if isApplyEditRequest(raw) {
		var applyReq protocol.ApplyEditRequest
		if err := json.Unmarshal(raw, &applyReq); err != nil {
			s.count(func(c *counters) { c.malformed.Add(1) })
			s.logger.Printf("apply-edit request decode error: %v", err)
			return
		}
		s.count(func(c *counters) { c.applies.Add(1) })
		if !s.handleApplyEdit(enc, applyReq) {
			s.count(func(c *counters) { c.appliesFailed.Add(1) })
		}
		return
	}

	if isUndoRequest(raw) {
		var undoReq protocol.UndoRequest
		if err := json.Unmarshal(raw, &undoReq); err != nil {
			s.count(func(c *counters) { c.malformed.Add(1) })
			s.logger.Printf("undo request decode error: %v", err)
			return
		}
		s.count(func(c *counters) { c.undos.Add(1) })
		if !s.handleUndo(enc, undoReq) {
			s.count(func(c *counters) { c.undosFailed.Add(1) })
		}
		return
	}

	// Checked before the prompt path, like every other typed request. Until
	// this existed, {"status":true} decoded as a PromptRequest with no prompt
	// and came back "prompt is empty" — the daemon had no answer to "how are
	// you" because nothing had ever asked.
	if isStatusRequest(raw) {
		s.count(func(c *counters) { c.statuses.Add(1) })
		s.handleStatus(enc)
		return
	}

	if isSearchRequest(raw) {
		var searchReq protocol.SearchRequest
		if err := json.Unmarshal(raw, &searchReq); err != nil {
			s.count(func(c *counters) { c.malformed.Add(1) })
			s.logger.Printf("search request decode error: %v", err)
			return
		}
		s.count(func(c *counters) { c.searches.Add(1) })
		s.handleSearch(enc, searchReq)
		return
	}

	var promptReq protocol.PromptRequest
	if err := json.Unmarshal(raw, &promptReq); err != nil {
		s.count(func(c *counters) { c.malformed.Add(1) })
		s.logger.Printf("prompt read error: %v", err)
		return
	}

	// MODE IS VALIDATED HERE, at the first point the request's own content is
	// known, and the canonical form is written back so nothing downstream has to
	// repeat the parse.
	//
	// It is validated at all because the comparison it feeds used to be a bare
	// `mode != "plan"`, which is fail-OPEN: every string that was not exactly
	// that selected the full tool menu. "Plan" from a client that capitalises,
	// "planning" from one that guesses, or a field a future client misspells all
	// resolved to the menu containing sandbox_exec -- the user having asked, in
	// each case, for the mode that runs nothing. A mode the daemon cannot
	// interpret is now refused rather than interpreted permissively.
	//
	// See normalizeMode for why "auto" and "manual" are accepted: the wire field
	// is shared with the VS Code approval picker, and "auto" is what an ordinary
	// turn sends.
	normalizedMode, modeErr := normalizeMode(promptReq.Mode)
	if modeErr != nil {
		s.count(func(c *counters) { c.malformed.Add(1) })
		s.logger.Printf("rejecting prompt: %v", modeErr)
		enc.Encode(protocol.TokenResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Done:            true,
			Error:           modeErr.Error(),
			ErrorClass:      string(ClassInvalidRequest),
		})
		return
	}
	promptReq.Mode = normalizedMode

	if promptReq.Reset {
		s.count(func(c *counters) { c.resets.Add(1) })
		s.resetPersistedHistory()
		enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
		return
	}

	// Reject an empty prompt before anything is spent on it (Fix 14). Sending
	// `{}` used to produce a full, billed model call and a reply to nothing:
	// the daemon has no question to answer, the user is charged for asking it,
	// and the empty exchange is then persisted as conversation. Refused here,
	// at the first point the request's own content is known, so nothing
	// downstream — routing, retrieval, the model call, memory — runs at all.
	if strings.TrimSpace(promptReq.Prompt) == "" {
		s.count(func(c *counters) { c.emptyPrompts.Add(1) })
		s.logger.Print("rejecting empty prompt without calling the model API")
		enc.Encode(protocol.TokenResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Done:            true,
			Error:           "prompt is empty",
			ErrorClass:      string(ClassInvalidRequest),
		})
		return
	}

	s.count(func(c *counters) { c.prompts.Add(1) })
	s.logger.Printf("received prompt (%d bytes), calling model API", len(promptReq.Prompt))

	decision := s.route(promptReq.PromptKind, promptReq.Tier)
	s.logger.Printf("route tier=%s slug=%s reason=%s", decision.Tier, decision.Slug, decision.Reason)

	historyOutcome := prepareHistory(promptReq.History)
	s.logHistory(historyOutcome)

	outcome := s.gatherContext(context.Background(), promptReq.Prompt)
	s.logRetrieval(outcome)

	grounding := buildGroundingInfo(outcome, s.workspace, promptReq.Workspace)
	if grounding.WorkspaceMismatch {
		s.logger.Printf("workspace mismatch: client expected %s, this daemon is grounding against %s", promptReq.Workspace, s.workspace)
	}
	// Sent before any tokens, as its own message, so a client can show
	// grounding status as soon as the turn starts rather than waiting for
	// the whole answer to finish streaming. History rides along on the same
	// message (Fix 13): it is decided at the same point, and a client that
	// silently lost its oldest turns deserves to know before the answer that
	// was written without them starts arriving.
	//
	// Degraded rides along for the same reason, one step further: a reduced
	// subsystem used to reach stderr and stop there, so a response served by a
	// half-working daemon was byte-identical to a healthy one. The user learns
	// it before the answer arrives, not never.
	if err := enc.Encode(protocol.TokenResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Grounding:       grounding,
		History:         buildHistoryInfo(historyOutcome),
		Degraded:        s.degradations(),
	}); err != nil {
		s.logger.Printf("grounding info write error: %v", err)
		return
	}

	// cleanPrompt is scrubbed of high-confidence secret shapes (see
	// daemon/scrub.go) before it's sent anywhere near the model API. Retrieved
	// chunk content (outcome.Chunks) is ALSO scrubbed now, but at a different
	// choke point: the same structural scrub() runs inside renderChunk when the
	// chunks are folded into augmentedPrompt below (Option A,
	// daemon/CHUNK_SCRUB_DESIGN.md §4). That matters because chunk content is
	// NOT local-only — the query embedding is computed locally (ONNX), but the
	// retrieved chunk text itself is POSTed to the hosted completion provider on
	// every grounded turn, so it needs the same protection the typed prompt
	// gets. Chunk scrubbing is PARTIAL: structural signatures only; opaque/novel
	// secrets are not yet closed (they await the warn-mode measurement below —
	// see logChunkScrub).
	cleanPrompt, redactions := scrub(promptReq.Prompt, s.cfg.NoScrub)
	augmentedPrompt := cleanPrompt
	if !outcome.Skipped {
		augmentedPrompt = buildAugmentedUserMessage(cleanPrompt, outcome.Chunks, s.cfg.NoScrub)
		// Measurement/notice pass over the exact chunks folded in above:
		// logs the structural redactions that renderChunk applied (kinds
		// only) and runs the deferred entropy/keyword detectors in log-only
		// warn-mode. This never changes augmentedPrompt.
		s.logChunkScrub(outcome.Chunks)
	}
	// The plan directive is applied to the SYSTEM prompt below, not appended
	// here. See planModeSystemPrompt.
	if len(redactions) > 0 {
		kinds := redactionKinds(redactions)
		s.logger.Printf("scrub: redacted %d suspected secret(s): %s", len(redactions), kinds)
		// Sent before any tokens, as its own message -- same "notify the
		// client immediately, don't wait for the stream to finish" pattern
		// as the Grounding message just above. Kinds only, never the
		// matched text (see protocol.TokenResponse.Redactions).
		if err := enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Redactions: kinds}); err != nil {
			s.logger.Printf("redaction notice write error: %v", err)
			return
		}
	}

	// history/completion note: historyOutcome.Messages (built above from
	// the CLIENT-sent promptReq.History) is the ONLY history fed to the
	// model call. s.memory is a separate write-path-plus-hydration store —
	// it is never merged into this live context, only appended to below
	// (on success) and read back at the next connection's handshake (see
	// loadPersistedHistory). Merging it in here too would double the
	// conversation the model sees.
	routing := s.cfg.ZDR.resolvedProviderRouting()
	var full strings.Builder
	reasoningBytes := 0
	// finishReason is captured from the stream's terminal SSE finish_reason via
	// onFinish below, then mapped to the Incomplete signal on the final Done
	// message (M1). It fires only on a successful stream, so the error path below
	// never reads it (an error is its own abnormal-end signal).
	finishReason := ""
	// streamWithRetry, not streamCompletion (Fix 10): transient failures are
	// retried with jittered backoff, and only while nothing has streamed yet --
	// see its doc comment for the two rules that decide.
	// buildChatMessages moved OUT of streamCompletion to here (Phase 3): the
	// provider seam now takes an already-built message list, because an agent
	// loop must append to one across iterations rather than have it rebuilt from
	// (systemPrompt, history, prompt) on every call. This single-turn path
	// builds exactly the list it always did.
	messages := buildChatMessages(planModeSystemPrompt(s.systemPrompt, promptReq.Mode), historyOutcome.Messages, augmentedPrompt)

	// AGENT MODE FORK. Three conditions, all required (see agentModeEngaged):
	// the config enables it, and this client declared it can answer a mid-turn
	// approval. A client that did not gets the single-turn path below,
	// byte-identical to what it got before agent mode existed -- which is what
	// makes shipping the loop safe before every client can render it.
	if s.agentModeEngaged(hsReq) {
		// dec and lc go with enc because agent mode is the one path that reads
		// from the client AFTER its request: an approval answer comes back on
		// this same connection, needs this same decoder, and needs lc to widen
		// the idle deadline to human scale for the duration of the ask.
		s.runAgentTurn(s.shutdownContext(), enc, dec, lc, promptReq, decision.Slug, messages, routing, &full)
		return
	}

	// No tools on this path. Agent mode has its own entry point; passing nil
	// here is what keeps the request body byte-identical to the pre-tools one.
	_, err := streamWithRetry(context.Background(), s.apiBase, s.apiKey, decision.Slug, messages, nil, routing,
		func(token string) error {
			full.WriteString(token)
			return enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: token})
		},
		func(provider string) {
			s.logger.Printf("model API served by provider=%q (zdr=%t data_collection=%s allow_fallbacks=%t)", provider, routing.ZDR, routing.DataCollection, routing.AllowFallbacks)
			// Surface the same already-observed value to the client on its own
			// message (E1). It rides here rather than on the pre-token Grounding
			// message because the provider is not known until the response
			// stream starts — onProvider fires at most once, at or before the
			// first token. A write error is dropped, exactly like the reasoning
			// callback below: this is optional observability and must not fail a
			// turn the token stream is otherwise completing. Never carries a
			// fallback-vs-primary claim or a ZDR verdict — just "served by X".
			enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Provider: provider})
		},
		// Reasoning tokens go out on their own field, NEVER into `full` (Fix 14).
		// `full` is what gets parsed for SEARCH/REPLACE blocks and written to
		// conversation memory, so folding thinking into it would let a model's
		// musings about an edit be mistaken for the edit, and would persist
		// commentary as if it were the answer. A write error here is dropped
		// rather than returned: optional commentary must not fail a request the
		// token stream is otherwise completing (the next Token encode will
		// surface a genuinely broken connection anyway).
		func(reasoning string) {
			reasoningBytes += len(reasoning)
			enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Reasoning: reasoning})
		},
		// onFinish: record the terminal finish_reason so the final Done message can
		// flag a cut-off answer (M1). Fires at most once, on success only; a plain
		// assignment, no wire write of its own -- the signal rides the Done message.
		func(reason string) {
			finishReason = reason
		},
		s.logger,
	)
	if reasoningBytes > 0 {
		s.logger.Printf("model API: streamed %d byte(s) of reasoning alongside the answer", reasoningBytes)
	}
	if err != nil {
		// The Gate-7 split, now with a class attached (Fix 9). The full upstream
		// error — provider base URL, HTTP status, response body, raw transport
		// text — goes to the local log for the operator via Detail(); the socket
		// caller gets ModelError's client-safe message plus a stable class it can
		// branch on. Being specific about the KIND of failure is not a licence to
		// disclose its shape: the class is derived from upstream detail and never
		// carries it. ModelError.Error() is the safe form precisely so a future
		// %v here cannot leak by accident.
		modelErr := asModelError(err)
		s.logger.Printf("model API error: %s", modelErr.Detail())
		enc.Encode(protocol.TokenResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Done:            true,
			Error:           modelErr.Error(),
			ErrorClass:      string(modelErr.Class),
		})
		return
	}

	blocks, rejections := s.parseAndLogEditBlocks(full.String())
	incomplete := incompleteInfoFor(finishReason)
	if incomplete != nil {
		s.logger.Printf("stream ended early: finish_reason=%q (answer cut off)", finishReason)
	}
	enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true, EditProposals: editProposalsFromBlocks(blocks), EditRejections: rejections, Incomplete: incomplete})
	s.logger.Print("stream complete")

	s.persistTurn(promptReq.Prompt, full.String(), incomplete)
}

// isApplyEditRequest sniffs whether raw is an ApplyEditRequest (identified
// by the presence of its "edit" key) rather than a PromptRequest, so one
// connection can carry either message right after a successful handshake
// with no wire-level discriminator field or protocol version bump — older
// clients only ever send PromptRequest-shaped JSON, which has no "edit"
// key, so this always falls through to the existing prompt path for them.
//
// The key is matched EXACTLY (requestfields.go): {"EDIT":{...}} used to sniff
// true here through Go's case-insensitive struct-tag matching, routing a body
// OpenRouter-style exact parsers would read differently.
func isApplyEditRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	var edit protocol.EditBlockWire
	return hasObjectKey(fields, "edit", &edit)
}

// handleApplyEdit runs an ApplyEditRequest through the same editapply core
// the CLI's `edits apply` and the TUI's edit-review flow use — PrepareEdit
// (exact-match, ambiguity-refuse, workspace confinement, secret-file
// refusal, syntax gate), then Apply (backup + write) only if PrepareEdit
// succeeds. Workspace resolution always uses this daemon's own configured
// s.workspace, never req.Workspace (same convention as PromptRequest —
// see its doc comment), so a client can't redirect writes elsewhere by
// lying about its workspace. Any failure at any stage is reported as
// Applied: false with that stage's error verbatim — the identical string
// PrepareEdit/Apply would produce for the CLI or TUI — and no file is
// written, since Apply is only called after PrepareEdit has already
// succeeded.
// The bool return says whether the edit LANDED. It exists so serveConn can count
// failures in one place instead of at each of the four refusal returns below --
// a counter placed per-return is a counter that a future fifth return silently
// escapes.
func (s *Server) handleApplyEdit(enc *json.Encoder, req protocol.ApplyEditRequest) (applied bool) {
	realRoot, err := editapply.ResolveRealWorkspaceRoot(s.workspace)
	if err != nil {
		s.logger.Printf("apply-edit: resolving workspace root: %v", err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: s.socketSafeError(err)})
		return false
	}

	// Serialize the entire read-modify-write + backup/prune span per workspace
	// (FAIL-3, Gate 6). Acquired here, after the root is resolved (a read-only
	// resolve that needs no lock and supplies the key), and held across
	// PrepareEdit's read, backup-dir resolution's prune, and Apply's write +
	// backup copies — so a concurrent Apply or Undo on this workspace cannot
	// interleave. Released on every return path via defer.
	defer s.lockWorkspace(realRoot)()

	block := editapply.EditBlock{FilePath: req.Edit.FilePath, Search: req.Edit.Search, Replace: req.Edit.Replace}
	prepared, err := editapply.PrepareEdit(realRoot, block)
	if err != nil {
		s.logger.Printf("apply-edit: refused %s: %v", block.FilePath, err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: s.socketSafeError(err, realRoot)})
		return false
	}

	backupDir, err := resolveBackupSessionDir(realRoot, req.BackupSessionDir)
	if err != nil {
		s.logger.Printf("apply-edit: creating backup dir: %v", err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: s.socketSafeError(err, realRoot)})
		return false
	}

	if err := editapply.Apply(realRoot, prepared, backupDir); err != nil {
		s.logger.Printf("apply-edit: failed %s: %v", block.FilePath, err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: s.socketSafeError(err, realRoot)})
		return false
	}

	s.logger.Printf("apply-edit: applied %s (backup: %s)", block.FilePath, backupDir)

	// Success path only, and still under the workspace lock (Fix 5): bring the
	// index in line with what was just written, so the next turn reasons about
	// the edited file rather than whatever `index` last saw. Never reached from
	// a refusal or a failed apply — those left the file untouched.
	s.reindexAfterApply(realRoot, block.FilePath)

	// SyntaxNote and MatchNote come from the SAME Prepared this function already
	// gated on, so there is no second evaluation and no way for the note to
	// describe a different decision than the one that was acted on. Before this,
	// both were computed, used for the refusal decision, and then dropped on the
	// floor at exactly this line -- the socket was the only apply path that
	// discarded them, which is why VS Code was the only surface that could not
	// show them.
	enc.Encode(protocol.ApplyEditResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Applied:         true,
		BackupDir:       backupDir,
		SyntaxNote:      prepared.SyntaxNote,
		MatchNote:       prepared.MatchNote,
	})
	return true
}

// resolveBackupSessionDir returns the backup session directory Apply should
// use for this request: existing, if it names a directory this daemon
// already created for realWorkspaceRoot (i.e. a BackupDir a prior
// ApplyEditResponse returned to the same client, echoed back via
// ApplyEditRequest.BackupSessionDir) — this is how several blocks applied
// over separate connections in one client-side review share a single
// backup session, matching the CLI/TUI's in-process backupDir reuse (see
// editapply.BackupOriginal's idempotency doc). Otherwise (empty, or a value
// that doesn't check out) a fresh session directory is created, identical
// to the pre-existing per-request behavior.
func resolveBackupSessionDir(realWorkspaceRoot, existing string) (string, error) {
	if existing != "" && isWorkspaceBackupSessionDir(realWorkspaceRoot, existing) {
		return existing, nil
	}
	return editapply.NewBackupSessionDir(realWorkspaceRoot)
}

// isWorkspaceBackupSessionDir reports whether dir is an existing directory
// confined to realWorkspaceRoot/.codeterminal/backups. A well-behaved
// client only ever echoes back a path this daemon itself issued, but this
// check is still applied so a stale, malformed, or crafted value can never
// redirect backup writes outside the intended tree — the same confinement
// discipline ResolveSafeTargetPath applies to edit targets.
func isWorkspaceBackupSessionDir(realWorkspaceRoot, dir string) bool {
	backupsRoot := filepath.Join(realWorkspaceRoot, ".codeterminal", "backups")
	rel, err := filepath.Rel(backupsRoot, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// isUndoRequest sniffs whether raw is an UndoRequest (identified by the
// presence of its "undo" key, mirroring isApplyEditRequest's peek-for-"edit"
// approach) rather than a PromptRequest. UndoRequest always serializes
// "undo" (no omitempty, see its doc comment), so a real one is always
// caught here; older clients that don't know this message only ever send
// PromptRequest-shaped JSON, which has no "undo" key, so they always fall
// through to the prompt path unaffected.
//
// The key is matched EXACTLY (requestfields.go). This is the sniffer that most
// needed it: {"UNDO":true} used to route here, and undo restores files from
// backup over the user's current work.
func isUndoRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "undo")
}

// handleUndo runs an UndoRequest through the exact same runUndoSession the
// CLI's `edits undo` already uses — no restore logic is reimplemented here.
// Workspace resolution always uses this daemon's own configured s.workspace,
// never req.Workspace, same convention as every other request type.
//
// When req.BackupSessionDir is supplied, it must be a directory this daemon
// itself could have issued (confined to the workspace's backups tree, still
// present on disk) — reusing isWorkspaceBackupSessionDir verbatim, the same
// check ApplyEditRequest's BackupSessionDir goes through. Unlike the apply
// path, a value that fails this check is a hard refusal, not a fallback: an
// unrecognized session has no sensible "undo something else instead"
// behavior. An empty BackupSessionDir instead resolves to the most recent
// session, via the same resolveBackupSession the CLI uses.
//
// force is always false and in is always empty here: a socket client has no
// controlling terminal to answer runUndoSession's "overwrite anyway? [y/N]"
// prompt, so a file that changed since the apply run is always left guarded
// rather than silently forced — the same safe default the CLI gets by just
// pressing enter. Guarded files are returned to the caller, never hidden.
// The bool return says whether the undo completed, for the same reason
// handleApplyEdit has one.
func (s *Server) handleUndo(enc *json.Encoder, req protocol.UndoRequest) (reverted bool) {
	realRoot, err := editapply.ResolveRealWorkspaceRoot(s.workspace)
	if err != nil {
		s.logger.Printf("undo: resolving workspace root: %v", err)
		enc.Encode(protocol.UndoResponse{ProtocolVersion: protocol.ProtocolVersion, Error: s.socketSafeError(err)})
		return false
	}

	// Serialize per workspace (FAIL-3, Gate 6): held across session validation
	// and every restore write, and mutually exclusive with a concurrent Apply on
	// the same workspace — whose prune would otherwise delete this session out
	// from under the walk, and whose write would otherwise defeat the
	// "unchanged since apply" guard. Released on every return path via defer.
	defer s.lockWorkspace(realRoot)()

	backupsRoot := filepath.Join(realRoot, ".codeterminal", "backups")

	var sessionDir string
	if req.BackupSessionDir != "" {
		if !isWorkspaceBackupSessionDir(realRoot, req.BackupSessionDir) {
			s.logger.Printf("undo: refused unrecognized session dir %q (under %s)", req.BackupSessionDir, backupsRoot)
			enc.Encode(protocol.UndoResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Error:           s.scrubPaths(fmt.Sprintf("backup session %q not found under %s", req.BackupSessionDir, backupsRoot), realRoot),
			})
			return false
		}
		sessionDir = req.BackupSessionDir
	} else {
		sessionDir, err = resolveBackupSession(backupsRoot, "")
		if err != nil {
			s.logger.Printf("undo: resolving latest session: %v", err)
			enc.Encode(protocol.UndoResponse{ProtocolVersion: protocol.ProtocolVersion, Error: s.socketSafeError(err, realRoot)})
			return false
		}
	}

	restored, removed, guarded, err := runUndoSession(realRoot, sessionDir, false, strings.NewReader(""), io.Discard, s.logger)
	if err != nil {
		// Report the count and the guarded list ALONGSIDE the error, never
		// instead of it (Fix 2). This used to send a bare error with Restored
		// left at zero, which told the client nothing had been reverted while
		// runUndoSession's file-by-file walk had in fact already reverted part
		// of the workspace. runUndoSession is all-or-nothing now, so restored
		// is normally 0 here — but it is the real count when the batch failed
		// during its commit phase, and the client must be told the truth about
		// disk in that case rather than a convenient zero.
		s.logger.Printf("undo: restoring %s: %v (restored %d, guarded %d)", sessionDir, err, restored, len(guarded))
		enc.Encode(protocol.UndoResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Restored:        restored,
			Removed:         removed,
			Guarded:         guarded,
			SessionDir:      sessionDir,
			Error:           s.socketSafeError(err, realRoot),
		})
		return false
	}

	// "reverted", not "restored": a session that created files reverts them by
	// deleting them, and runUndoSession has already logged the per-shape
	// breakdown (Fix C).
	s.logger.Printf("undo: reverted %d file(s) from %s (%d restored, %d removed, %d guarded)",
		restored, sessionDir, restored-removed, removed, len(guarded))
	enc.Encode(protocol.UndoResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Restored:        restored,
		Removed:         removed,
		Guarded:         guarded,
		SessionDir:      sessionDir,
	})
	return true
}

// isSearchRequest sniffs whether raw is a SearchRequest (identified by the
// presence of its "search" key), mirroring isUndoRequest's peek-for-"undo"
// approach. SearchRequest always serializes "search" (no omitempty, see its
// doc comment), so a real one is always caught here; older clients that
// don't know this message only ever send PromptRequest-shaped JSON, which
// has no "search" key, so they always fall through to the prompt path
// unaffected.
//
// The key is matched EXACTLY (requestfields.go).
func isSearchRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "search")
}

// defaultSearchLimit caps how many results a SearchRequest returns when the
// client doesn't specify one (Limit <= 0) -- the same "client omits it,
// daemon picks a sensible default" shape as maxHistoryTurns (history.go),
// though kept as its own constant since the two aren't the same concept
// (recent conversation turns to replay to the model vs. search hits to show
// a human).
const defaultSearchLimit = 20

// handleSearch runs a SearchRequest through MemoryStore.SearchTurns — no
// search logic lives here, this only resolves the workspace, applies the
// default limit, and translates the result to wire types. Workspace
// resolution always uses this daemon's own configured s.workspace, never
// req.Workspace, same convention as every other request type: a client
// can't search another workspace's history by lying about which one it's
// asking for.
//
// s.memory == nil (cross-session memory unavailable — see setupMemoryStore)
// is reported as an explicit Error, not a silent empty result set: an empty
// Results slice must always mean "searched and found nothing" (SearchTurns's
// own no-match-is-not-an-error contract), never "couldn't search at all" —
// the same "never let two different outcomes look identical" instinct
// UndoResponse.Guarded exists for.
func (s *Server) handleSearch(enc *json.Encoder, req protocol.SearchRequest) {
	if s.memory == nil {
		enc.Encode(protocol.SearchResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Error:           "conversation memory is not available",
		})
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	hits, err := s.memory.SearchTurns(context.Background(), s.workspace, req.Query, limit)
	if err != nil {
		s.logger.Printf("search: %v", err)
		enc.Encode(protocol.SearchResponse{ProtocolVersion: protocol.ProtocolVersion, Error: err.Error()})
		return
	}

	results := make([]protocol.SearchResult, len(hits))
	for i, h := range hits {
		results[i] = protocol.SearchResult{Role: h.Role, Snippet: h.Snippet, CreatedAt: h.CreatedAt}
	}
	s.logger.Printf("search: %d result(s) for query (%d bytes)", len(results), len(req.Query))
	enc.Encode(protocol.SearchResponse{ProtocolVersion: protocol.ProtocolVersion, Results: results})
}

// loadPersistedHistory returns this daemon's cross-session conversation
// memory for its own workspace (most recent maxHistoryTurns, oldest
// first), or nil if memory is unavailable or empty. Included in every
// HandshakeResponse — see the doc comment on
// protocol.HandshakeResponse.PersistedHistory for who's actually meant to
// consume it (only a client's own startup/preflight connection).
// Rows are re-validated on the way out with the same validTurn predicate
// prepareHistory applies to client-supplied turns (Fix 13), so a row written
// before the write-side guard existed — an empty assistant turn from a
// zero-content upstream response — can't re-hydrate a client's transcript and
// ride back in as history. prepareHistory would refuse it again on the return
// trip; filtering here means the client never displays it either.
func (s *Server) loadPersistedHistory() []protocol.Turn {
	if s.memory == nil {
		return nil
	}
	turns, err := s.memory.LoadRecentTurns(context.Background(), s.workspace, maxHistoryTurns)
	if err != nil {
		s.logger.Printf("loading persisted history: %v", err)
		return nil
	}
	kept := make([]protocol.Turn, 0, len(turns))
	for _, t := range turns {
		if validTurn(t) {
			kept = append(kept, t)
		}
	}
	if dropped := len(turns) - len(kept); dropped > 0 {
		s.logger.Printf("persisted history: dropped %d invalid/empty turn(s) on load", dropped)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// persistTurn appends the just-completed exchange (the user's raw prompt —
// never the grounding-augmented version, since retrieved context is
// re-derived fresh every turn, not something to remember — and the
// assistant's full answer) to cross-session memory, write-through. Called
// only after streamCompletion has already returned successfully, so a
// mid-stream failure (including a client disconnecting before the answer
// finished) never persists a truncated answer as if it were complete.
// An EMPTY answer is never written (Fix 14). A zero-content upstream response
// used to be persisted as an empty assistant turn, which then re-hydrated into
// the client's transcript at the next handshake and rode along in every later
// request — one bad response permanently seated in the context window, teaching
// the model that an empty answer is an acceptable shape. This is the write-side
// half of that fix; prepareHistory/loadPersistedHistory hold the read side
// (history.go), which is what cleans up rows written before this guard existed.
// Both halves are deliberate: neither alone covers the other's case.
//
// The user's prompt is dropped with it rather than stored alone — a question
// with no answer is not an exchange, and persisting half of one would leave
// memory claiming the assistant simply never replied.
// A CUT-OFF ANSWER IS PERSISTED AS CUT OFF. The doc above promises a truncated
// answer is never stored as if it were complete, and it held for the case it
// was written about -- a mid-stream failure, which returns an error and never
// reaches here. It did NOT hold for the case that actually happens: a stream
// that ends early on finish_reason (or an agent turn stopped by a budget) is a
// SUCCESSFUL stream, so it landed in memory indistinguishable from a finished
// reply, re-hydrated at the next session's handshake, and was fed back to the
// model forever after as a complete thought it had chosen to end there.
//
// The note is appended to the stored content rather than kept in a column
// beside it, deliberately: memory rows are re-validated through prepareHistory
// on the way out (LoadRecentTurns), the wording is the daemon's either way, and
// baking it in means the fact cannot be separated from the text it qualifies by
// any later reader -- including one that predates this field. Requires no
// migration, and a hydrated turn carries no Incomplete slug, so it cannot be
// annotated twice.
func (s *Server) persistTurn(prompt, answer string, incomplete *protocol.IncompleteInfo) {
	if s.memory == nil {
		return
	}
	if strings.TrimSpace(answer) == "" {
		s.logger.Print("not persisting turn: the model returned an empty answer")
		return
	}
	if incomplete != nil {
		if note := incompleteHistoryNote(incomplete.Reason); note != "" {
			answer += note
		}
	}
	ctx := context.Background()
	if err := s.memory.AppendTurn(ctx, s.workspace, "user", prompt); err != nil {
		s.logger.Printf("persisting user turn: %v", err)
		return
	}
	if err := s.memory.AppendTurn(ctx, s.workspace, "assistant", answer); err != nil {
		s.logger.Printf("persisting assistant turn: %v", err)
	}
}

// resetPersistedHistory clears this daemon's cross-session memory for its
// own workspace — the server-side half of ctrl+n (see PromptRequest.Reset).
func (s *Server) resetPersistedHistory() {
	if s.memory == nil {
		return
	}
	if err := s.memory.ClearWorkspace(context.Background(), s.workspace); err != nil {
		s.logger.Printf("clearing persisted history: %v", err)
		return
	}
	s.logger.Print("persisted history cleared")
}

// parseAndLogEditBlocks parses the just-completed response for SEARCH/REPLACE
// edit blocks and logs a structured summary, returning whatever it found (nil
// on a response with no readable blocks). Nothing is applied to disk here or by
// the caller sending EditProposals on — this is parse-only; PrepareEdit's
// safety gates run only later, when a client actually sends an
// ApplyEditRequest for one of these.
//
// A block the parser refuses no longer discards the ones it could read (Fix B):
// one bad hunk used to cost the whole response, so a reply carrying a good edit
// beside a bad one proposed nothing at all. Each refusal is logged with its own
// line number and reason rather than collapsed into a single "parse error".
//
// CLOSED 2026-08-30. Rejections now leave this function alongside the blocks
// and are carried on TokenResponse.EditRejections, so a client can say "3
// proposed, 1 unreadable" instead of silently showing two. The gap and the
// argument that kept it open are both preserved below, because the argument is
// still correct and is the reason the new field is its own channel.
//
// The gap, as it stood: refusals reached the daemon log and stopped there.
// EditProposals carries proposals only, so a client showed the readable blocks
// and said nothing about the refused ones. That cost little when a refusal meant
// a malformed SEARCH/REPLACE block — rare, and the user could see its raw text
// anyway. A diff refuses PER HUNK, with reasons the user can act on (a delete, a
// rename, an executable mode, a truncated hunk), so an agent-mode user could
// watch four hunks apply and never learn a fifth was refused, or why.
//
// It was NOT smuggled through Degraded, then or now: that field
// means "this daemon is running in a reduced mode", and a hunk this engine
// cannot express is not a daemon degradation. Borrowing the channel would buy
// one message at the cost of the word, and Degraded is worth more meaning
// exactly one thing. EditRejections is that channel, and it means one thing too.
//
// The comment used to say the field "belongs with the protocol change the
// incremental streaming work already has to make". That coupling was not real:
// the field is additive and lands on the Done message beside EditProposals,
// where nothing about streaming has to change first. It shipped on its own.
func (s *Server) parseAndLogEditBlocks(response string) ([]editapply.EditBlock, []protocol.EditRejectionWire) {
	payload := editapply.ParseEditPayload(response)
	blocks, rejected := payload.Blocks, payload.Rejected

	// Converted once, up front, so both return paths carry the same thing. The
	// zero-blocks path below is the one that matters most: a response whose
	// edits are ALL malformed used to return nil and send the client nothing at
	// all, which is the case where the user most needs to be told something
	// happened.
	wire := make([]protocol.EditRejectionWire, 0, len(rejected))
	for _, bad := range rejected {
		wire = append(wire, protocol.EditRejectionWire{Line: bad.Line, Reason: bad.Reason})
	}
	if len(wire) == 0 {
		// nil, not an empty slice: `omitempty` distinguishes them on the wire,
		// and "no rejections" should send no field rather than `[]`.
		wire = nil
	}

	if payload.Format == editapply.FormatUnifiedDiff {
		s.logger.Printf("response carried a unified diff; read %d hunk(s), %d refused", len(blocks), len(rejected))
	}
	for _, bad := range rejected {
		s.logger.Printf("edit block refused at line %d: %v", bad.Line, bad.Reason)
	}
	if len(blocks) == 0 {
		if len(rejected) > 0 {
			s.logger.Printf("parsed 0 usable edit block(s), %d refused", len(rejected))
		} else if hint := payload.Hint; payload.Format == editapply.FormatUnrecognised {
			// Zero blocks AND zero rejections is the parser saying "this is a
			// plain-text answer". For a response full of unified-diff hunks
			// that is wrong, and the wrongness was invisible: nothing was
			// logged, EditProposals was empty, and every client rendered a
			// perfectly ordinary reply. Logged here so an operator debugging
			// "the model keeps proposing edits and nothing happens" finds the
			// answer in one grep instead of inferring it.
			s.logger.Printf("response carries an edit this engine cannot read (kind=%s line=%d); no proposals sent", hint.Kind, hint.Line)
		}
		return nil, wire
	}

	s.logger.Printf("parsed %d edit block(s), %d refused", len(blocks), len(rejected))
	for i, b := range blocks {
		s.logger.Printf("  block %d: path=%s search_lines=%d replace_lines=%d",
			i+1, b.FilePath, lineCount(b.Search), lineCount(b.Replace))
	}
	return blocks, wire
}

// editProposalsFromBlocks converts parsed edit blocks to their wire form for
// TokenResponse.EditProposals. A nil/empty input returns nil, so the
// json:"...,omitempty" tag drops the field entirely for plain-text answers.
func editProposalsFromBlocks(blocks []editapply.EditBlock) []protocol.EditBlockWire {
	if len(blocks) == 0 {
		return nil
	}
	wire := make([]protocol.EditBlockWire, len(blocks))
	for i, b := range blocks {
		wire[i] = protocol.EditBlockWire{FilePath: b.FilePath, Search: b.Search, Replace: b.Replace}
	}
	return wire
}

// lineCount returns the number of lines in s, treating an empty string as
// zero lines rather than one.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
