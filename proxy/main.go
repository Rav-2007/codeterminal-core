// Command codeterminal-proxy is the smallest possible managed-tier proxy in
// front of OpenRouter. It exists to hold the OpenRouter API key server-side
// so it never ships to end users. It adds the key and forwards the request
// body byte-for-byte (buffered once, to allow the quota reservation below --
// content is never parsed beyond a single-field max_tokens peek, see
// peekMaxTokens), then streams the response back without buffering. Every
// call is first gated on a caller-supplied Mochiii key, validated against
// Supabase (see authorize) -- unauthorized requests never reach OpenRouter.
//
// Quota is enforced by an atomic reserve-then-true-up, not a check-then-act
// read: reserveQuota atomically reserves an estimated token count BEFORE the
// call is forwarded (a key that can't fit the reservation under its
// token_limit is refused with 429, fail-closed, before OpenRouter is ever
// contacted), and finalizeUsage/correctUsage reconcile that estimate against
// real usage once the response completes. See QUOTA_RESERVATION_DESIGN.md
// for the full design -- this replaces an earlier check-then-forward gate
// (checkQuota/recordUsage) that had a confirmed TOCTOU race under
// concurrent requests: reads and writes were separate round trips, so
// concurrent requests could all observe the same stale "under limit" state.
//
// Every reservation also opens a durable pending_corrections row in the same
// atomic statement, which its correction closes. A reservation whose process
// died before correcting therefore leaves a record behind, and
// startReconciliationSweep claims and loudly reports those. This does not
// recover the dead request's true usage -- nothing can -- it bounds the
// resulting inaccuracy in time and forces it into the safe (over-metered)
// direction. See QUOTA_RESERVATION_DESIGN.md §5(e).
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultUpstreamBase = "https://openrouter.ai/api/v1"
	chatCompletionsPath = "/chat/completions"

	// upstreamTimeout bounds a single forwarded call so a stalled upstream
	// can't hang a connection (and its goroutine) forever. Matches the
	// daemon's own requestTimeout (daemon/provider.go) since this proxy
	// sits directly in that same call path.
	upstreamTimeout = 5 * time.Minute

	// supabaseAuthTimeout bounds the key-lookup call separately from
	// upstreamTimeout: a slow/down Supabase must fail the request closed
	// in seconds, not eat minutes of the inference budget before OpenRouter
	// is ever contacted.
	supabaseAuthTimeout = 5 * time.Second

	// maxRequestBodyBytes caps the incoming request body the proxy will
	// read. Generous for a chat-completions payload (prompt + retrieved
	// context + capped history), just enough to stop an unbounded body
	// from exhausting memory.
	maxRequestBodyBytes = 4 << 20 // 4MB

	// maxAuthResponseBytes caps the Supabase lookup response. The query
	// only ever selects `id` and is filtered to at most one active row, so
	// this is generous headroom, not a real limit in practice.
	maxAuthResponseBytes = 64 * 1024

	// usageUpdateTimeout bounds the fire-and-forget per-key usage RPC call,
	// separate from the request the tokens were counted on: it runs after
	// that response has already been fully relayed to the client, on its
	// own context, so it must not hang indefinitely.
	usageUpdateTimeout = 5 * time.Second

	// serverReadTimeout bounds the total time to read one request
	// (headers + body), separate from and looser than ReadHeaderTimeout
	// below. There's no legitimate reason for a client to trickle a
	// request body slowly -- bodies are capped at maxRequestBodyBytes and
	// contain no streaming input, only the outbound response streams --
	// so this closes the slow-body-drip gap ReadHeaderTimeout alone
	// doesn't cover (Phase 3 security review, area 6).
	serverReadTimeout = 30 * time.Second

	// serverWriteTimeout bounds one request's total response-write time.
	// Must exceed upstreamTimeout (the outbound call to OpenRouter can
	// legitimately run up to 5 minutes for a long streamed completion) or
	// this would truncate real in-progress streams -- set with headroom
	// above it rather than equal to it.
	serverWriteTimeout = 6 * time.Minute

	// serverIdleTimeout bounds how long a keep-alive connection may sit
	// idle between requests. Doesn't affect an active request (that's
	// serverReadTimeout/serverWriteTimeout's job) -- just reclaims
	// connections nothing is using.
	serverIdleTimeout = 120 * time.Second

	// shutdownGrace is how long a SIGTERM'd process keeps serving in-flight
	// requests before it stops waiting and exits.
	//
	// Sized against the PLATFORM, not against upstreamTimeout. Railway sends
	// SIGTERM and then SIGKILLs after its own grace window; a value above that
	// window buys nothing, because the kernel ends the process regardless. 25s
	// sits under the commonly-documented 30s so the process gets to finish on its
	// own terms and log that it did.
	//
	// It is deliberately far BELOW upstreamTimeout (5m). A long completion still
	// in flight at 25s is cut -- that is unavoidable when the platform is going to
	// kill us anyway. What matters is that it is cut with the reservation
	// discharged (P1.1's deferred finalizer runs as the handler unwinds) instead
	// of stranded, which is the difference between a client retrying and an
	// account being permanently over-charged. See docs/ROBUSTNESS_BASELINE.md §3.
	shutdownGrace = 25 * time.Second

	// preDrainDelay is the window between flipping /health to 503 and calling
	// srv.Shutdown, carved out of shutdownGrace.
	//
	// It exists because of a flaw caught by actually running the shutdown drill
	// rather than reasoning about it: http.Server.Shutdown closes the LISTENER
	// immediately, so once it is called no new connection is accepted at all and
	// the 503 nothing can connect to is unobservable. Announcing then instantly
	// refusing means the load balancer learns this replica is leaving by getting
	// a connection error -- which is the exact behaviour the flip was added to
	// avoid.
	//
	// So: flip, keep accepting for this long so at least one health poll can see
	// the 503 and pull the instance from rotation, and only then stop accepting.
	// 3s covers a 1-2s health-check interval with margin, and it is spent while
	// in-flight requests continue, so it costs nothing but shutdown latency.
	preDrainDelay = 3 * time.Second

	// keyLogPrefixLen is the most of a caller-supplied key that is ever
	// written to a log line -- never the full key.
	keyLogPrefixLen = 8

	sseInitialBufferSize = 64 * 1024
	sseMaxLineSize       = 1024 * 1024

	// maxNonStreamResponseBytes caps the buffered upstream response on
	// the non-SSE reply path (see handleChatCompletions), so a usage
	// figure can be peeked out of it before it's written to the client.
	// The daemon always sets stream:true (daemon/provider.go) so this
	// path is not exercised by real traffic today; the cap is defensive
	// generosity for whatever else calls this proxy, not a tuned limit.
	maxNonStreamResponseBytes = 16 << 20 // 16MB

	// defaultReservationTokens sizes a quota reservation when the
	// incoming request doesn't declare max_tokens -- true for all of
	// today's daemon traffic (it never sets the field). This is a
	// typical-case admission-control size, not a worst-case bound: real
	// per-provider ceilings for the active model (deepseek/deepseek-v4-flash)
	// run up to 1,048,576 tokens (OpenRouter's own endpoint listing,
	// checked directly -- see QUOTA_RESERVATION_DESIGN.md §3), and no
	// fixed default can cover that without a single reservation consuming
	// an entire default-quota key. finalizeUsage/correctUsage reconcile
	// the difference after the fact.
	defaultReservationTokens = 4096

	// maxReservationTokens clamps a caller-declared max_tokens so one
	// request can't claim an outsized reservation. A declared value ABOVE it is
	// now refused outright rather than silently clamped -- see the
	// max_tokens_too_large check in handleChatCompletions for why clamping the
	// reservation while forwarding the caller's larger number was the mismatch
	// that created the overshoot in the first place.
	maxReservationTokens = 32768

	// absoluteMaxRequestTokens is the hard per-request output ceiling enforced
	// mid-stream (see streamSSE / requestTokenCeiling), applied REGARDLESS of how
	// much quota headroom a key has. Quota bounds what a key may spend in total;
	// this bounds what any ONE request may spend, so a large enterprise quota
	// cannot be drained by a single call. Sized at 2x maxReservationTokens: high
	// enough that no legitimate completion reaches it, low enough to bound the
	// damage from a caller that declares no max_tokens and lets the provider's own
	// (far larger) default ceiling apply.
	absoluteMaxRequestTokens = 65536

	// maxBytesPerChunkGuard sizes the mid-stream BYTE bound relative to the token
	// ceiling (see budgetExceeded): a stream may carry at most
	// ceiling * maxBytesPerChunkGuard bytes.
	//
	// 512 is comfortably above a real OpenRouter chunk, which runs ~294 bytes of
	// JSON envelope (id, provider, model, object, created, choices[].index,
	// delta.role, finish_reason, native_finish_reason, logprobs) around a single
	// token of delta content. Scaling the bound by the ceiling rather than fixing
	// it keeps the guard proportional: a nearly-exhausted key with a ceiling of
	// 150 gets ~76KB, a full key gets ~32MB.
	//
	// This bound exists ONLY to catch the pathological case chunk-counting misses
	// -- a provider that batches an entire answer into a handful of huge chunks.
	// It is deliberately NOT a token estimate. An earlier version of this code
	// divided accumulated line length by 4 ("bytes per token") and took the max
	// against the chunk count; because it measured the repeated ENVELOPE rather
	// than the content, it scored ~73 tokens per real token and would have
	// truncated every legitimate completion at roughly 900 tokens.
	maxBytesPerChunkGuard = 512

	// correctUsageMaxAttempts bounds correctUsage's retries against a
	// transient Supabase failure. Not full durability against a sustained
	// outage or a process crash mid-retry -- see
	// QUOTA_RESERVATION_DESIGN.md §5(d)/(e) for why a lost correction
	// isn't uniformly safe. The crash case (e) is covered instead by the
	// pending_corrections outbox and the sweep below.
	correctUsageMaxAttempts = 3

	// reconciliationSweepInterval is how often the outbox is checked for
	// reservations no correction ever closed (QUOTA_RESERVATION_DESIGN.md
	// §5(e)). Frequent enough that an abandoned reservation surfaces within
	// ~20 minutes of the crash that stranded it (this interval plus
	// pendingCorrectionStaleAfterMinutes), cheap enough to be a single
	// indexed DELETE that returns nothing at all in the normal case.
	reconciliationSweepInterval = 5 * time.Minute

	// pendingCorrectionStaleAfterMinutes is how old a pending_corrections
	// row must be before the sweep treats it as abandoned rather than
	// in-flight. It MUST exceed the longest lifetime a legitimate request
	// can have, or a live request's own reservation would be swept out from
	// under it: the ceilings here are upstreamTimeout (5m) and
	// serverWriteTimeout (6m), so 15 minutes is a >2x margin.
	pendingCorrectionStaleAfterMinutes = 15

	// reconciliationSweepTimeout bounds one sweep RPC call. Looser than
	// usageUpdateTimeout because a sweep after a long outage can return
	// many rows, and a sweep that gives up early just leaves the rows to be
	// claimed by the next tick -- there is no correctness cost to waiting.
	reconciliationSweepTimeout = 30 * time.Second

	// maxSweepResponseBytes caps the sweep's response body. Much larger
	// than maxAuthResponseBytes because this response scales with the
	// number of abandoned reservations, not with a single fixed row.
	maxSweepResponseBytes = 4 << 20 // 4MB

	// maxDrainBytes bounds how much of an already-decoded response body
	// decodeCappedJSON will read past its decode cap in order to reach EOF and let
	// http.Transport re-pool the connection.
	//
	// Deliberately larger than every decode cap above, because the only case that
	// needs draining is the one where the body exceeded its cap -- a drain bounded
	// by the cap itself can never reach EOF and buys nothing. 8MB clears any
	// plausible PostgREST response (the largest legitimate one, the sweep's, is
	// capped at 4MB) while still being a bound rather than an open invitation.
	maxDrainBytes = 8 << 20 // 8MB

	// defaultAllowedModels is the managed tier's shipped model set (the three
	// tiers in models.json). Used when ALLOWED_MODELS is unset, so the
	// cost-authorization check is on by default rather than opt-in.
	defaultAllowedModels = "deepseek/deepseek-v4-flash," +
		"qwen/qwen3-coder-30b-a3b-instruct," +
		"deepseek/deepseek-r1"
)

// correctUsageRetryBackoff is the delay between correctUsage's retry
// attempts -- short, since this only needs to ride out a transient blip,
// not a sustained outage (a slice, not a const, since Go has no const
// []time.Duration).
var correctUsageRetryBackoff = []time.Duration{250 * time.Millisecond, 750 * time.Millisecond}

// allowedResponseHeaders is the sole set of headers relayed from
// OpenRouter's response to the caller. Verified against the daemon (this
// proxy's actual caller, daemon/provider.go streamCompletion): it reads
// no response headers at all, only the status code and the SSE body
// content, so this list is deliberately minimal rather than tuned to any
// specific requirement -- Content-Type is kept only because a client has
// a reasonable general expectation of it, not because anything here
// depends on it. Previously every OpenRouter response header was
// forwarded verbatim except three hop-by-hop ones, which leaked
// OpenRouter's own CORS policy, Set-Cookie (Cloudflare bot-management
// cookie scoped to openrouter.ai), Permissions-Policy, and cf-ray to
// every caller of this proxy (Phase 3 security review, areas 3/5).
var allowedResponseHeaders = []string{"Content-Type"}

// accountMetadataFields lists top-level JSON fields observed on
// OpenRouter's non-streaming response bodies that identify our own
// account rather than the request/response itself. Every error body
// probed during the Phase 3 security review (invalid model, invalid
// role, missing content, excessive max_tokens, empty messages -- five
// distinct OpenRouter-side validation failures) carried exactly one such
// field: "user_id", set to an OpenRouter account identifier, forwarded
// verbatim to the caller before this fix. Stripped from every
// non-streaming response, not just errors, in case a future response
// shape carries the same field on success.
var accountMetadataFields = []string{"user_id"}

// decodeCappedJSON decodes one JSON value from resp.Body under a byte cap, then
// drains whatever the decoder left so the connection can be re-pooled.
//
// The drain is the only reason this helper exists, and its value is narrower than
// it looks -- measured rather than assumed (docs/ROBUSTNESS_BASELINE.md §4).
//
// The tempting story is that json.Decoder.Decode stops at the first complete value
// and never reaches EOF, so http.Transport never re-pools and every Supabase call
// pays a fresh TCP+TLS handshake. That story is FALSE for normal responses, and was
// measured false at 0B, 100B, 4KB and 64KB: the decoder's buffered reader consumes
// through EOF while filling its buffer, so for a Content-Length body the transport
// sees EOF and re-pools anyway. The unexplained ~80ms on reserveQuota is NOT this,
// and this change must not be reported as a latency fix.
//
// What IS real is the over-cap case. When the response exceeds cap, Decode stops
// early with bytes still unread and pooling genuinely breaks: 20 distinct TCP
// connections across 20 sequential requests, against 1 when drained. Such a
// response also fails to decode and is refused, so this is the already-degraded
// path -- precisely when you least want to also pay a handshake per request.
//
// The drain is bounded, but by maxDrainBytes rather than by cap. Bounding it by
// cap would be self-defeating and was: the case that needs draining is precisely
// the one where the body EXCEEDED cap, so a cap-sized drain cannot reach EOF and
// the connection is lost anyway. Caught by the test below rather than by reading
// this code, which is why the test asserts the connection count instead of merely
// asserting that a drain was attempted.
//
// Reading unboundedly would trade a bounded cost for an unbounded one, so a body
// past maxDrainBytes correctly loses its connection. That bound is safe to make
// generous here because this reader is Supabase -- our own backend -- and the
// decode cap exists to bound MEMORY, not because the peer is hostile.
func decodeCappedJSON(resp *http.Response, cap int64, v any) error {
	err := json.NewDecoder(io.LimitReader(resp.Body, cap)).Decode(v)
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	return err
}

// stripAccountMetadata removes accountMetadataFields from a JSON object
// body, returning it unchanged if it isn't a JSON object (never errors
// the caller over a body this proxy doesn't otherwise parse or validate)
// or doesn't carry any of those fields.
func stripAccountMetadata(body []byte) []byte {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	stripped := false
	for _, field := range accountMetadataFields {
		if _, ok := parsed[field]; ok {
			delete(parsed, field)
			stripped = true
		}
	}
	if !stripped {
		return body
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return body
	}
	return out
}

func main() {
	logger := log.New(os.Stderr, "codeterminal-proxy: ", log.LstdFlags)

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		logger.Fatal("OPENROUTER_API_KEY must be set (server environment only -- never a file, never hardcoded)")
	}

	upstreamBase := os.Getenv("OPENROUTER_API_BASE")
	if upstreamBase == "" {
		upstreamBase = defaultUpstreamBase
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseServiceRoleKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	if supabaseURL == "" || supabaseServiceRoleKey == "" {
		logger.Printf("WARNING: SUPABASE_URL and/or SUPABASE_SERVICE_ROLE_KEY not set -- every request will fail auth and be rejected with 401 (fail closed)")
	}

	// RAILWAY_GIT_COMMIT_SHA is set automatically by Railway's build
	// environment; empty in any local/non-Railway run, which is not fatal
	// -- /health still serves, just with an "unknown" commit, so this can
	// never block startup.
	buildCommit := os.Getenv("RAILWAY_GIT_COMMIT_SHA")
	if buildCommit == "" {
		buildCommit = "unknown"
	}

	// ALLOWED_MODELS overrides the shipped tier set; empty disables the check and
	// is warned about loudly so "unrestricted" is never a silent default.
	allowedModels := parseAllowedModels(os.Getenv("ALLOWED_MODELS"))
	if len(allowedModels) == 0 {
		allowedModels = parseAllowedModels(defaultAllowedModels)
	}
	logger.Printf("model allow-list: %d model(s) permitted", len(allowedModels))

	p := newProxy(apiKey, strings.TrimRight(upstreamBase, "/")+chatCompletionsPath,
		supabaseURL, supabaseServiceRoleKey, logger, allowedModels)

	// One cancel signal shared by every background loop, tripped by the first
	// SIGTERM/SIGINT so nothing keeps working after the process is on its way out.
	shutdownCtx, beginShutdown := context.WithCancel(context.Background())
	defer beginShutdown()

	// Crash recovery for quota reservations (QUOTA_RESERVATION_DESIGN.md §5(e)).
	// Deliberately started before ListenAndServe: a process that just came back
	// from a crash should be sweeping the reservations that crash stranded, and
	// the sweep is independent of whether this instance is serving traffic yet.
	go p.startReconciliationSweep(shutdownCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", p.rateLimitedHealth(makeHealthHandler(buildCommit, &p.draining)))
	mux.HandleFunc(chatCompletionsPath, p.handleChatCompletions)       // "/chat/completions"
	mux.HandleFunc("/v1"+chatCompletionsPath, p.handleChatCompletions) // "/v1/chat/completions" alias
	// Catch-all for every unregistered path. Without it those requests 404 out of
	// the mux without passing through admitPreAuth at all -- an unauthenticated,
	// completely unthrottled endpoint. Cheap per request, but unbounded is
	// unbounded. A catch-all is used rather than wrapping the whole mux, because a
	// wrapper would charge the pre-auth bucket twice on the three real routes.
	mux.HandleFunc("/", p.rateLimitedNotFound())

	srv := &http.Server{
		Addr: ":" + port,
		// Wrapping the whole mux is safe here in a way the rate limiters are not
		// (see the catch-all route above, which exists precisely because wrapping
		// would double-charge the pre-auth bucket): recovery has no per-request
		// budget to spend, so applying it once at the outside is correct.
		Handler:           recoverPanics(logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}

	// Bind before serving so a bind failure is a startup error here, rather than
	// something the serve goroutine discovers later.
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		logger.Fatalf("listening on %s: %v", srv.Addr, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger.Printf("listening on :%s -> upstream %s", port, p.upstreamURL)
	if err := serveUntilSignal(srv, ln, logger, &p.draining, beginShutdown, sigCh); err != nil {
		logger.Fatal(err)
	}
	logger.Print("exiting")
}

// serveUntilSignal serves ln until a signal arrives, then drains and returns.
//
// Extracted from main so the shutdown sequence -- now the most safety-critical
// code in this file -- is reachable from a test. It was written inline first, and
// measured: proxy coverage fell from 80.3% to 78.5% because none of it could be
// exercised. Everything below is testable with a synthetic signal channel and a
// listener on port 0.
//
// The sequence and its ordering both matter:
//
//  1. Flip /health to 503 FIRST. The load balancer takes a moment to notice, and
//     until it does it keeps routing new requests here; a 503 with a Retry-After
//     is a far better answer than a connection reset.
//  2. Stop the background loops (the reconciliation sweep), so nothing keeps
//     working on behalf of a server that is going away.
//  3. Keep ACCEPTING for preDrainDelay. http.Server.Shutdown closes the listener
//     immediately, so without this pause the 503 is announced on a port that
//     instantly stops answering -- the balancer would learn about the shutdown
//     from a connection error, which is what the flip exists to prevent. This was
//     a real flaw in the first cut, caught by running the drill.
//  4. Shutdown: stop accepting, close idle keep-alives, wait for active handlers.
//     Each handler that returns runs P1.1's deferred finalizer, so a request cut
//     short here is BILLED rather than stranded.
//
// Returns nil on a clean shutdown, including a drain that hit its deadline -- that
// is a degraded outcome, not a startup failure, and it is reported in the log where
// an operator will see it. A non-nil error means the server could not serve at all.
func serveUntilSignal(srv *http.Server, ln net.Listener, logger *log.Logger, draining *atomic.Bool, beginShutdown func(), sigCh <-chan os.Signal) error {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		sig := <-sigCh

		draining.Store(true)
		logger.Printf("received %s, draining: /health now reports 503, still accepting for %s so the load balancer can notice, then finishing in-flight requests (grace %s)", sig, preDrainDelay, shutdownGrace)

		beginShutdown()

		time.Sleep(preDrainDelay)

		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace-preDrainDelay)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			// Deadline hit with handlers still running. Say so plainly: those are
			// the requests whose reservations may still be stranded, and an
			// operator needs to know the drain was incomplete rather than reading
			// a clean-looking exit.
			logger.Printf("drain INCOMPLETE after %s (%v) -- in-flight requests were cut; any reservation they had not yet corrected will surface in the next reconciliation sweep", shutdownGrace-preDrainDelay, err)
			return
		}
		logger.Print("drain complete, all in-flight requests finished")
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	// Serve returns ErrServerClosed the moment Shutdown is called, so block until
	// the drain actually finishes. Returning here would let the caller exit and
	// kill the very handlers Shutdown is waiting for -- reintroducing the bug with
	// extra steps.
	<-shutdownDone
	return nil
}

type proxy struct {
	apiKey                 string
	upstreamURL            string
	logger                 *log.Logger
	client                 *http.Client
	supabaseURL            string
	supabaseServiceRoleKey string

	// Admission control (see ratelimit.go). preAuth* run BEFORE the Supabase
	// lookup so a flood of bad keys cannot amplify into a third party; keyRate
	// and inFlight run after, keyed by the authenticated api_keys.id.
	preAuthPerSource *rateLimiter
	preAuthGlobal    *rateLimiter
	keyRate          *rateLimiter
	keyTokens        *rateLimiter
	inFlight         *inFlightLimiter

	// allowedModels is the cost-authorization allow-list (see modelAllowed).
	// Empty means unrestricted.
	allowedModels map[string]bool

	// draining is set once a shutdown signal arrives and never cleared. It only
	// changes what /health reports; it deliberately does NOT gate
	// handleChatCompletions. Refusing in-flight or newly-arrived work ourselves
	// would duplicate what http.Server.Shutdown already does correctly (stop
	// accepting, let active handlers finish) while adding a race of our own
	// between the check and the reservation.
	draining atomic.Bool
}

// newProxy builds a proxy with admission control wired up. Constructing the
// limiters here (rather than lazily) keeps them non-nil for every code path,
// including tests, so a missing limiter can never silently mean "unlimited".
func newProxy(apiKey, upstreamURL, supabaseURL, supabaseServiceRoleKey string, logger *log.Logger, allowedModels map[string]bool) *proxy {
	return &proxy{
		apiKey:                 apiKey,
		upstreamURL:            upstreamURL,
		logger:                 logger,
		supabaseURL:            supabaseURL,
		supabaseServiceRoleKey: supabaseServiceRoleKey,
		// No blanket http.Client.Timeout: a streaming response can
		// legitimately run for minutes. Each request's own context deadline
		// (upstreamTimeout) is what bounds it instead.
		client:           &http.Client{},
		preAuthPerSource: newRateLimiter(preAuthRatePerSourcePerSecond, preAuthBurstPerSource),
		preAuthGlobal:    newRateLimiter(preAuthRateGlobalPerSecond, preAuthBurstGlobal),
		keyRate:          newRateLimiter(keyRatePerSecond, keyBurst),
		keyTokens:        newRateLimiter(keyTokenRatePerSecond, keyTokenBurst),
		inFlight:         newInFlightLimiter(maxInFlightPerKey, maxInFlightTotal),
		allowedModels:    allowedModels,
	}
}

// admitPreAuth applies the unauthenticated-surface limits. It runs before any
// Supabase call, so auth-attempt floods are bounded before they can amplify into
// a third-party dependency. Per-source first (spoofable via X-Forwarded-For),
// with the global bucket as the backstop that spoofing cannot evade.
func (p *proxy) admitPreAuth(w http.ResponseWriter, r *http.Request) bool {
	if !p.preAuthGlobal.allow("global") {
		p.logger.Printf("rate: refused (pre-auth global ceiling)")
		tooManyRequests(w, "global")
		return false
	}
	src := clientSource(r)
	if !p.preAuthPerSource.allow(src) {
		p.logger.Printf("rate: refused (pre-auth per-source)")
		tooManyRequests(w, "source")
		return false
	}
	return true
}

// healthResponse is the /health body. commit lets a deploy-verify step
// confirm which build Railway is actually running, since Railway's own
// dashboard commit doesn't guarantee the running container matches it.
type healthResponse struct {
	Status string `json:"status"`
	Commit string `json:"commit,omitempty"`
}

// healthCommitVisible reports whether /health may disclose the build commit.
//
// /health is the only UNAUTHENTICATED route, and the commit SHA it returned
// identified the exact running build to anyone on the internet — free version
// fingerprinting that tells an attacker precisely which code (and which
// known-fixed bugs) is deployed. Low severity on its own, but it is disclosure
// to an anonymous caller for no operational benefit that an authenticated
// channel could not provide.
//
// Kept opt-in rather than deleted: the field exists because Railway's dashboard
// commit does not guarantee which container is actually running, so a
// deploy-verify step legitimately wants it. HEALTH_EXPOSE_COMMIT=1 restores it
// for that use.
func healthCommitVisible() bool {
	return os.Getenv("HEALTH_EXPOSE_COMMIT") == "1"
}

// rateLimitedHealth applies the pre-auth limits to /health. It is the only
// unauthenticated route, so without this it is an unbounded free endpoint: the
// audit served 200 unauthenticated /health requests in 0.1s with zero throttling.
func (p *proxy) rateLimitedHealth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.admitPreAuth(w, r) {
			return
		}
		next(w, r)
	}
}

// recoverPanics contains a panic in any handler to the one request that caused it.
//
// net/http already recovers a handler panic at the connection level, so this does
// not exist to keep the process alive. It exists for the two things net/http's
// recovery does NOT do, both of which matter here:
//
//  1. It logs with a stack. net/http logs the panic to the server's ErrorLog in a
//     format that is not this proxy's, and an operator chasing a 500 needs the
//     stack next to the request's own log lines, not in a different shape.
//  2. It answers the client. net/http's recovery closes the connection without a
//     response, so the caller sees a reset rather than a status. The daemon's
//     error handling distinguishes a 5xx from a transport failure, and a reset is
//     the more confusing of the two to debug.
//
// It deliberately does NOT touch the reservation. P1.1 made discharging it a
// `defer` inside handleChatCompletions, which runs during panic unwinding before
// this recovery is reached, so by the time we get here the reservation is already
// billed exactly once. Trying to also finalize from here would be the double-bill
// this design was built to prevent -- finalizeReservation's `finalized` flag makes
// that safe rather than merely unlikely, but the right answer is not to reach for
// it at all.
//
// The status write is best-effort by necessity: a panic after the handler already
// called WriteHeader (mid-stream, most likely) cannot change the status, and
// net/http will log the superfluous-WriteHeader attempt. Returning a truncated
// stream is the honest outcome there; the log line is what carries the diagnosis.
func recoverPanics(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			// http.ErrAbortHandler is net/http's documented way for a handler to
			// abort a connection on purpose. It is not a fault and must not be
			// logged as one, or a deliberate abort becomes indistinguishable from
			// a real bug in the logs.
			if r == http.ErrAbortHandler {
				panic(r)
			}
			logger.Printf("PANIC recovered while handling a request: %v\n%s", r, debug.Stack())
			// Body-less: an error body could echo attacker-controlled content, and
			// there is nothing useful to say that the status does not already say.
			w.WriteHeader(http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// rateLimitedNotFound serves every unregistered path: pre-auth limits first, then
// a 404 that discloses nothing about which routes exist.
func (p *proxy) rateLimitedNotFound() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.admitPreAuth(w, r) {
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// makeHealthHandler closes over the build's commit SHA (read once at
// startup in main) so every /health response reports it without a global.
//
// draining is the shutdown flag. Once it is set, /health answers 503 with a
// Retry-After so the platform's load balancer stops sending new work here while
// the in-flight requests finish. A draining instance that keeps answering 200 is
// the reason a rolling deploy resets live connections: the balancer has no way to
// know this replica is leaving. Passed in rather than read off a package global so
// the behaviour is testable without a running server.
func makeHealthHandler(commit string, draining *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reported := ""
		if healthCommitVisible() {
			reported = commit
		}

		status := "ok"
		code := http.StatusOK
		if draining != nil && draining.Load() {
			status = "draining"
			code = http.StatusServiceUnavailable
		}

		body, err := json.Marshal(healthResponse{Status: status, Commit: reported})
		if err != nil {
			// Unreachable in practice (healthResponse is two plain
			// strings), but fail the same way a real health-check
			// failure would rather than write a malformed body.
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if code == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(code)
		w.Write(body)
	}
}

// handleChatCompletions forwards the request body to OpenRouter
// byte-for-byte -- the daemon already sets model/messages/stream/provider
// exactly as it wants them (including the ZDR provider-routing object) --
// with a server-side Authorization header added, and streams the response
// back without buffering. The body is read into memory once (bounded by
// maxRequestBodyBytes, same cap as before) rather than streamed straight
// through, so it can be peeked for max_tokens to size a quota reservation
// (see peekMaxTokens/reserveQuota) before the call is forwarded; that peek
// is the one narrow, deliberate exception to "never parsed" -- message
// content itself is still never inspected anywhere in this function.
// Request and response BODIES are never logged, at any point below: only
// method, status, and (if visible in-flight, from the response stream
// itself) the serving provider name. That omission is deliberate and is
// what keeps this proxy zero-data-retention-preserving rather than just a
// relay that happens to also see everything.
func (p *proxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p.logger.Printf("request received: %s", r.URL.Path)

	// Pre-auth admission FIRST: every authorize() call is a Supabase round trip,
	// so an unauthenticated flood would otherwise amplify into a third party.
	// This bounds that before a single lookup is made.
	if !p.admitPreAuth(w, r) {
		return
	}

	apiKeyID, ok := p.authorize(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Post-auth admission, keyed by the authenticated api_keys.id -- un-spoofable
	// (it is derived from the key hash, never from client input) and the correct
	// unit of tenancy. Rate bounds requests over time; in-flight bounds them at
	// once, which the rate limit alone does not: long streaming calls accumulate.
	if !p.keyRate.allow(apiKeyID) {
		p.logger.Printf("rate: refused (key id=%s over its request rate)", apiKeyID)
		tooManyRequests(w, "key_rate")
		return
	}
	// Second layer: TOKEN volume, which the request-rate limiter above cannot
	// see. Checked (not spent) here because a request's real token cost does not
	// exist yet -- finalizeUsage charges it once the response completes, and the
	// resulting debt is what refuses the next request.
	if !p.keyTokens.available(apiKeyID) {
		p.logger.Printf("rate: refused (key id=%s over its token rate)", apiKeyID)
		tooManyRequests(w, "token_rate")
		return
	}
	releaseSlot, admitted := p.inFlight.acquire(apiKeyID)
	if !admitted {
		p.logger.Printf("rate: refused (key id=%s at in-flight ceiling)", apiKeyID)
		tooManyRequests(w, "in_flight")
		return
	}
	defer releaseSlot()

	limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	bodyBytes, err := io.ReadAll(limitedBody)
	if err != nil {
		p.logger.Printf("reading request body failed: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Ambiguity check FIRST, before any gate reads the body. Every gate below
	// resolves a duplicate key last-wins and then this body is forwarded
	// byte-for-byte carrying both copies, so a gate that judges an ambiguous body
	// has already judged something other than what OpenRouter will read (see
	// hasDuplicateKeys). Placed here so no gate ever sees one.
	//
	// 400, not the gates' 403: this is a malformed, unanswerable request, not a
	// policy refusal of a well-formed one. Cannot fire for the shipped daemon,
	// which marshals its body from structs and so cannot emit a duplicate key.
	if hasDuplicateKeys(bodyBytes) {
		p.logger.Printf("body: refused (key id=%s -- duplicate JSON key, request is ambiguous)", apiKeyID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"duplicate_json_key"}`))
		return
	}

	// Cost authorization before anything is reserved or forwarded: quota is
	// counted in tokens, but the bill is in dollars, so the model choice -- and
	// every other field that steers what gets billed -- is itself a spending
	// decision (see costSurfaceRefusal).
	if refusal, refused := p.costSurfaceRefusal(bodyBytes); refused {
		p.logger.Printf("cost: refused (key id=%s, reason=%s)", apiKeyID, refusal)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"` + refusal + `"}`))
		return
	}

	// ZDR enforcement (F1). Before this the proxy merely FORWARDED whatever
	// routing flags the client set; enforcement was entirely client-side, so a
	// caller that dropped or weakened the zero-data-retention flags had them
	// relayed to OpenRouter unchanged. This makes the proxy the authority: it
	// REJECTS -- fail CLOSED -- any request whose provider routing object is
	// missing, malformed, or weakened, before anything is reserved or forwarded,
	// so no non-ZDR request ever reaches OpenRouter through this proxy. It is a
	// read-only peek (zdrRoutingEnforced never mutates bodyBytes), so the accept
	// path below still forwards the caller's body BYTE-FOR-BYTE -- the reason
	// Reject was chosen over Stamp (proxy/F1_ENFORCEMENT_DESIGN.md). Runs after
	// the model check, mirroring its shape and its 403 (a policy refusal of an
	// authenticated caller, distinct from 401 auth), and only ever fires for a
	// caller other than the shipped daemon, which always sends the flags
	// (daemon/provider.go's providerRouting, no field omitempty).
	if !zdrRoutingEnforced(bodyBytes) {
		p.logger.Printf("zdr: refused (key id=%s -- request lacks the required zero-data-retention routing flags)", apiKeyID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"zdr_required"}`))
		return
	}

	// Streaming is REQUIRED, because the budget ceiling below can only be enforced
	// on a stream. A non-streamed completion arrives as one buffered body, so the
	// mid-stream kill never runs and the only bound left is
	// maxNonStreamResponseBytes (16MB, ~4.2M tokens) -- a 64x escape from the
	// ceiling. Refusing here makes the ceiling enforceable on 100% of forwarded
	// traffic and leaves one response path to audit instead of two.
	//
	// This refuses a caller REQUESTING a non-streamed completion. It does not
	// affect the buffered branch further down, which still handles upstream error
	// bodies (those arrive as application/json whatever `stream` said) and keeps
	// its account-metadata scrubbing and reservation true-up.
	//
	// Cannot fire for the shipped daemon, which always sets stream:true
	// (daemon/provider.go).
	if !streamRequested(bodyBytes) {
		p.logger.Printf("stream: refused (key id=%s -- non-streamed completions are not forwarded)", apiKeyID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"stream_required"}`))
		return
	}

	// A declared max_tokens above the reservation cap is REFUSED, not clamped.
	// Clamping the reservation while forwarding the caller's larger number
	// byte-for-byte was precisely the mismatch that let one request overshoot its
	// admission: the proxy reserved 32768 and OpenRouter honoured 999999. Reject
	// rather than rewrite, for the same reason F1 chose Reject over Stamp
	// (proxy/F1_ENFORCEMENT_DESIGN.md) -- the accept path must still forward the
	// caller's bytes unmodified. Cannot fire for the shipped daemon, which sends
	// no max_tokens at all (daemon/provider.go).
	reserved := defaultReservationTokens
	if declared, ok := peekMaxTokens(bodyBytes); ok {
		if declared > maxReservationTokens {
			p.logger.Printf("max_tokens: refused (key id=%s declared %d, cap %d)", apiKeyID, declared, maxReservationTokens)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"max_tokens_too_large"}`))
			return
		}
		reserved = declared
	}

	res, ok := p.reserveQuota(r.Context(), apiKeyID, reserved)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"quota_exceeded"}`))
		return
	}
	ceiling := requestTokenCeiling(reserved, res.headroom)

	// From here on the reservation EXISTS and someone owes a correction for it.
	// Exactly one deferred finalizer discharges that debt, and every branch below
	// records its result into `outcome` instead of billing directly.
	//
	// This is a structural property, not a tidier spelling of the same thing.
	// Before it, finalizeUsage was called at four hand-placed sites and the
	// "every reservation is finalized exactly once" invariant was maintained by
	// eye: a panic after this point discharged nothing (the reservation was
	// stranded until the sweep reported it permanently over-charged), and any
	// future early return added below would silently do the same. Neither is
	// possible now -- the defer runs during panic unwinding too, and a new
	// `return` inherits it for free.
	//
	// The zero value is deliberately the safe one: actual=0, producedOutput=false
	// means "nothing was generated", i.e. a full refund. The two upstream-failure
	// branches below therefore just return and let the defer do the right thing.
	//
	// Not synchronised, and must not need to be: outcome is written only by this
	// goroutine and by streamSSE, which this goroutine calls directly. Nothing
	// here may be moved into a goroutine of its own without adding a lock.
	outcome := reservationOutcome{
		keyID:     apiKeyID,
		reserved:  reserved,
		pendingID: res.pendingID,
	}
	defer p.finalizeReservation(&outcome)

	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstreamURL, bytes.NewReader(bodyBytes))
	if err != nil {
		p.logger.Printf("building upstream request failed: %v", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	// Preserve the caller's declared length instead of leaving it at the
	// zero value, which net/http would otherwise send as
	// Transfer-Encoding: chunked -- harmless, but needlessly different
	// from what the daemon itself sent.
	upstreamReq.ContentLength = int64(len(bodyBytes))
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		p.logger.Printf("upstream call failed: %v", err)
		// Reservation was made but OpenRouter was never reached -- none
		// of it was used, so it's a full refund (see
		// QUOTA_RESERVATION_DESIGN.md §5(b)). That is the outcome zero value
		// (actual=0, producedOutput=false), so the deferred finalizer above
		// already issues exactly the refund this branch used to issue by hand.
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	p.logger.Printf("upstream responded: status=%d", resp.StatusCode)

	for _, h := range allowedResponseHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		p.streamSSE(w, resp.Body, &outcome, ceiling)
		return
	}

	// Not exercised by the daemon today (it always sets stream:true), but
	// handled the same way as the SSE path for whatever else calls this
	// proxy: buffered (bounded, maxNonStreamResponseBytes) rather than
	// streamed straight through via io.Copy, so a usage figure can be
	// peeked out of it and the reservation trued up instead of stranded
	// (see QUOTA_RESERVATION_DESIGN.md §5(c)).
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxNonStreamResponseBytes))
	if err != nil {
		p.logger.Printf("reading non-streamed response failed: %v", err)
	}
	respBytes = stripAccountMetadata(respBytes)
	if _, err := w.Write(respBytes); err != nil {
		p.logger.Printf("writing non-streamed response failed: %v", err)
	}
	actual, _ := peekUsageTotal(respBytes)
	outcome.actual = actual
	// A 2xx response with a body means upstream produced (and billed) output;
	// a missing usage figure there must not trigger a refund. A non-2xx error
	// body carries no billable output, so it stays a full refund.
	outcome.producedOutput = resp.StatusCode >= 200 && resp.StatusCode < 300 && len(respBytes) > 0
	// No finalize call here: the deferred finalizer registered right after
	// reserveQuota bills this outcome on the way out.
}

// authorize validates the caller-supplied Mochiii key ("Authorization:
// Bearer <mochi_key>") against Supabase and returns the matching
// api_keys.id on success. It fails CLOSED: a missing/empty header, an
// unconfigured Supabase, a lookup error/timeout, a non-200 response, or a
// row count other than exactly one are all treated as unauthorized --
// OpenRouter is never contacted in any of those cases. Only the auth
// outcome and, at most, the key's first keyLogPrefixLen chars are ever
// logged; the full mochi_key, the Supabase service-role key, and the
// OpenRouter key are never logged.
func (p *proxy) authorize(r *http.Request) (string, bool) {
	const bearerPrefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		p.logger.Printf("auth: rejected (no bearer token)")
		return "", false
	}
	mochiKey := strings.TrimSpace(strings.TrimPrefix(authHeader, bearerPrefix))
	if mochiKey == "" {
		p.logger.Printf("auth: rejected (empty key)")
		return "", false
	}

	if p.supabaseURL == "" || p.supabaseServiceRoleKey == "" {
		p.logger.Printf("auth: rejected (supabase not configured, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}

	hash := sha256.Sum256([]byte(mochiKey))
	hashHex := hex.EncodeToString(hash[:])

	ctx, cancel := context.WithTimeout(r.Context(), supabaseAuthTimeout)
	defer cancel()

	q := url.Values{}
	q.Set("key_hash", "eq."+hashHex)
	q.Set("active", "eq.true")
	q.Set("select", "id")
	lookupURL := strings.TrimRight(p.supabaseURL, "/") + "/rest/v1/api_keys?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		p.logger.Printf("auth: rejected (building supabase request failed, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}
	// apikey only -- deliberately no Authorization header. Supabase's new
	// sb_secret_/sb_publishable_ API keys are not JWTs: sending one via
	// Authorization: Bearer, even when it exactly matches apikey (a
	// backward-compat exception that lets the request past the gateway
	// instead of being blocked outright), still gets forwarded to the
	// database's own JWT parser and rejected there for not being a JWT --
	// see https://supabase.com/docs/guides/api/api-keys. This is a real,
	// documented header-format bug and worth keeping fixed regardless --
	// but note: removing it did NOT resolve a separate empty-row symptom
	// under investigation (see project handoff doc, Phase 3 security
	// review). A bare curl with only `apikey` set reproduces the same
	// 200 + [] result, so that deeper cause is still open and unrelated
	// to this specific header issue.
	req.Header.Set("apikey", p.supabaseServiceRoleKey)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("auth: rejected (supabase lookup failed, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.logger.Printf("auth: rejected (supabase status=%d, key prefix=%s)", resp.StatusCode, keyPrefix(mochiKey))
		return "", false
	}

	var rows []struct {
		ID string `json:"id"`
	}
	if err := decodeCappedJSON(resp, maxAuthResponseBytes, &rows); err != nil {
		p.logger.Printf("auth: rejected (decoding supabase response failed, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}

	if len(rows) != 1 {
		p.logger.Printf("auth: rejected (key prefix=%s, matches=%d)", keyPrefix(mochiKey), len(rows))
		return "", false
	}

	p.logger.Printf("auth: ok (key prefix=%s)", keyPrefix(mochiKey))
	return rows[0].ID, true
}

// reserveQuota atomically reserves `reserved` tokens against apiKeyID's
// (an already-authorized api_keys.id -- never the raw Mochiii key, see
// authorize) quota via the reserve_usage RPC (proxy/migrations/0001_reserve_usage.sql),
// closing the TOCTOU race the old checkQuota+recordUsage split had: that
// gate read tokens_used in one round trip and wrote it in a separate one
// only after the response completed, so concurrent requests could all
// observe the same stale "under limit" state before any of their siblings'
// usage had landed (confirmed live -- see QUOTA_RESERVATION_DESIGN.md §0).
// Here, the check and the write are the same atomic UPDATE statement.
//
// Deliberately separate from authorize: authorize's contract is
// identity-only and is already verified, so this is a second, independent
// gate rather than folded into it. Fails CLOSED -- returns false (not
// allowed) -- on a request-build error, a Supabase call error/timeout, a
// non-200 response, or a row count other than exactly one. A row count of
// zero specifically means either the key has no usage row or the
// reservation would exceed token_limit; both are indistinguishable here,
// the same ambiguity checkQuota already had for "row not found". Only the
// key id and token counts are ever logged -- never the raw key, never
// request content.
//
// It returns a reservation carrying both the id of the pending_corrections row
// that reserve_usage opened in the same atomic statement (proxy/migrations/0002_pending_corrections.sql)
// and the key's remaining quota headroom.
//
// The pendingID is this reservation's durable record that a correction is still
// owed, and it is what makes the crash case survivable: if this process dies
// before finalizeUsage runs, the row outlives it and the reconciliation sweep
// finds it. The id must be threaded through to correctUsage, which closes the
// row as part of applying the correction. A refusal returns a zero reservation
// and opens no row -- the INSERT is driven off the UPDATE's own returned rows,
// so there is nothing to clean up when nothing was reserved.
//
// The headroom is what makes the streaming budget ceiling free (see
// requestTokenCeiling): reserve_usage ALREADY returns tokens_used and
// token_limit, and this function already decoded both -- it just discarded them
// after a log line. Threading them out costs no extra round trip and nothing on
// the TTFT budget.
func (p *proxy) reserveQuota(ctx context.Context, apiKeyID string, reserved int) (res reservation, ok bool) {
	if p.supabaseURL == "" || p.supabaseServiceRoleKey == "" {
		p.logger.Printf("quota: refused (supabase not configured, key id=%s)", apiKeyID)
		return reservation{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, supabaseAuthTimeout)
	defer cancel()

	payload, err := json.Marshal(struct {
		KeyID    string `json:"p_key_id"`
		Reserved int    `json:"p_reserved"`
	}{KeyID: apiKeyID, Reserved: reserved})
	if err != nil {
		p.logger.Printf("quota: refused (encoding reserve_usage body failed, key id=%s): %v", apiKeyID, err)
		return reservation{}, false
	}

	rpcURL := strings.TrimRight(p.supabaseURL, "/") + "/rest/v1/rpc/reserve_usage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(payload))
	if err != nil {
		p.logger.Printf("quota: refused (building reserve_usage request failed, key id=%s): %v", apiKeyID, err)
		return reservation{}, false
	}
	// apikey only -- see authorize's comment on this same header-format
	// bug (Supabase's sb_secret_ keys aren't JWTs; Authorization: Bearer
	// gets forwarded to the DB's own JWT parser and rejected there).
	req.Header.Set("apikey", p.supabaseServiceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("quota: refused (reserve_usage call failed, key id=%s): %v", apiKeyID, err)
		return reservation{}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxAuthResponseBytes))
		p.logger.Printf("quota: refused (reserve_usage status=%d, key id=%s)", resp.StatusCode, apiKeyID)
		return reservation{}, false
	}

	var rows []struct {
		TokensUsed int64 `json:"tokens_used"`
		TokenLimit int64 `json:"token_limit"`
		PendingID  int64 `json:"pending_id"`
	}
	if err := decodeCappedJSON(resp, maxAuthResponseBytes, &rows); err != nil {
		p.logger.Printf("quota: refused (decoding reserve_usage response failed, key id=%s): %v", apiKeyID, err)
		return reservation{}, false
	}

	if len(rows) != 1 {
		p.logger.Printf("quota: refused (key id=%s, reserved=%d, matches=%d)", apiKeyID, reserved, len(rows))
		return reservation{}, false
	}

	// A zero pending_id means the reservation landed but no outbox row came
	// back with it -- the signature of a database still running the 0001
	// reserve_usage, i.e. migration 0002 has not been applied to this
	// environment. Admission is deliberately NOT failed closed on this: the
	// reservation itself is real and atomic, so quota enforcement is intact;
	// what is missing is only the crash-recovery record. Refusing the request
	// would turn a missing migration into a total outage. It is logged at every
	// occurrence rather than once, because it also means correctUsage's
	// apply_correction call is about to 404 -- see the DEPLOY ORDER note in
	// proxy/migrations/0002_pending_corrections.sql.
	if rows[0].PendingID == 0 {
		p.logger.Printf("quota: WARNING reserve_usage returned no pending_id (key id=%s) -- migration 0002 likely not applied; crash recovery is INACTIVE and corrections will fail", apiKeyID)
	}

	// tokens_used comes back POST-increment (RETURNING after UPDATE), so this is
	// what the key may still legally spend BEYOND the reservation just made.
	// Clamped at zero: reserve_usage's own WHERE clause guarantees it is
	// non-negative, so a negative here would mean the invariant broke, and a
	// budget ceiling is the wrong place to discover that by going haywire.
	headroom := rows[0].TokenLimit - rows[0].TokensUsed
	if headroom < 0 {
		headroom = 0
	}

	p.logger.Printf("quota: reserved (key id=%s, reserved=%d, new_used=%d, limit=%d, headroom=%d, pending_id=%d)", apiKeyID, reserved, rows[0].TokensUsed, rows[0].TokenLimit, headroom, rows[0].PendingID)
	return reservation{pendingID: rows[0].PendingID, headroom: headroom}, true
}

// reservation is what reserveQuota hands back on success: the durable outbox row
// this reservation must close, and the key's remaining quota headroom.
type reservation struct {
	// pendingID is the pending_corrections row opened atomically with the
	// reservation; correctUsage closes it. Zero means migration 0002 is not
	// applied (see reserveQuota).
	pendingID int64

	// headroom is token_limit - tokens_used AFTER this reservation landed --
	// i.e. what the key may still spend beyond what was just reserved. Feeds
	// requestTokenCeiling.
	headroom int64
}

// requestTokenCeiling is the hard token ceiling for one request: everything the
// key could still legally spend (the reservation plus its remaining headroom),
// capped by absoluteMaxRequestTokens so that a single request can never drain a
// large quota in one shot no matter how much headroom the key has.
//
// This is what makes admission control actually BINDING. Before it, quota was
// checked only at admission: a key with a sliver of quota left was admitted on a
// maxReservationTokens-sized reservation and could then consume the provider's
// own output ceiling, since the proxy forwards the body byte-for-byte and the
// shipped daemon declares no max_tokens at all.
func requestTokenCeiling(reserved int, headroom int64) int {
	ceiling := int64(reserved) + headroom
	if ceiling > absoluteMaxRequestTokens {
		ceiling = absoluteMaxRequestTokens
	}
	return int(ceiling)
}

// keyPrefix returns at most the first keyLogPrefixLen characters of key, for
// content-free log lines -- never the full key.
func keyPrefix(key string) string {
	if len(key) <= keyLogPrefixLen {
		return key
	}
	return key[:keyLogPrefixLen]
}

// streamSSE copies body to w line by line, flushing after every line so the
// caller sees each chunk as it arrives rather than after the full response
// completes -- the entire point of this proxy is to never buffer inference
// output the way a naive io.Copy-after-io.ReadAll would. It also
// opportunistically extracts and logs the serving provider name from a
// "provider" field on a chunk, purely for observability (mirrors the
// daemon's own onProvider callback in provider.go), without ever logging
// chunk content: extractProvider's return type is a bare string containing
// only the provider name, never the raw line.
//
// keyID is the authorized caller's api_keys.id (from authorize; never the
// raw Mochiii key -- that value is never seen this far into the call path).
// reserved is the token count reserveQuota already atomically reserved
// against keyID before this call was forwarded. Whatever usage figure this
// function ends up with (from a final usage chunk -- see extractUsage;
// requires the daemon to have set stream_options.include_usage, which it
// always does -- see daemon/provider.go -- or zero, if the stream never
// produced one) is trued up against that reservation via finalizeUsage
// (deferred, so it runs on every exit path -- see QUOTA_RESERVATION_DESIGN.md
// §5(c) for why the zero case must not be silently skipped, unlike the
// pre-reservation code this replaced) AFTER the full response has already
// been relayed to the client: a fire-and-forget call on its own detached
// context (see correctUsage) that never delays, blocks, or fails the
// client's completion.
//
// STREAMING BUDGET ENFORCEMENT. ceiling is the hard token bound for this one
// request (requestTokenCeiling). Crossing it kills the stream mid-flight rather
// than letting it run to completion, which is the ONLY control that bounds spend
// when a caller declares no max_tokens -- as the shipped daemon does not, leaving
// the provider's own (far larger) default ceiling to apply. Enforcement is
// deliberately an ESTIMATE, because a real token count only ever arrives in the
// terminal usage chunk, i.e. after all the money has already been spent. Two
// independent bounds are tracked, BOTH read only from the SSE envelope and never
// from the delta text -- a COUNT of chunks and a LENGTH in bytes. See
// budgetExceeded for what each one is for and why they are not fused.
//
// This is a CIRCUIT BREAKER, NOT AN ACCOUNTANT: it bounds worst-case spend, and
// exact accounting still comes from the terminal usage chunk whenever one arrives.
func (p *proxy) streamSSE(w http.ResponseWriter, body io.Reader, outcome *reservationOutcome, ceiling int) {
	flusher, canFlush := w.(http.Flusher)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)

	providerLogged := false
	totalTokens := 0
	sawData := false
	dataChunks := 0
	streamedBytes := 0
	// Record into the caller's outcome rather than billing here. Every `return`
	// below -- clean EOF, client write failure, budget kill -- lands on this, and
	// the handler's single deferred finalizer does the billing. Recording (not
	// billing) is what lets the invariant live in one place; it also means a
	// panic mid-stream still bills the partial result correctly, because these
	// values are already published to the caller's struct by the time the
	// handler's defer runs.
	defer func() {
		outcome.actual = totalTokens
		outcome.producedOutput = sawData
	}()

	for scanner.Scan() {
		line := scanner.Text()
		// Record that billable output flowed from upstream BEFORE the relay
		// write, so a client that disconnects mid-stream (the write below
		// erroring) is still known to have consumed real, upstream-billed
		// output -- finalizeUsage must charge, not refund, in that case. This
		// is exactly the client-disconnect-before-the-usage-chunk path that
		// QUOTA_RESERVATION_DESIGN.md §5(c) wrongly folded into "nothing used".
		if isDataChunk(line) {
			sawData = true
			// Budget signals, counted BEFORE the relay write for the same reason
			// sawData is: a client that disconnects mid-stream still consumed
			// upstream-billed output, and the ceiling must account for it.
			// Both are envelope-only -- a count and a length, never a content read.
			dataChunks++
			streamedBytes += len(line)
			// Strip account-identifying fields from the STREAMING path too. This
			// scrubbing existed only on the non-SSE branch, while the daemon always
			// sets stream:true -- so the scrubber never ran on the only path real
			// traffic uses, and OpenRouter's account fields (the "user_id" class the
			// Phase 3 review found on its bodies) were relayed verbatim to every
			// caller. Proven live before fixing: a chunk carrying user_id arrived at
			// the client byte-for-byte.
			line = stripSSEAccountMetadata(line)
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}

		if !providerLogged {
			if provider, ok := extractProvider(line); ok {
				p.logger.Printf("provider served: %s", provider)
				providerLogged = true
			}
		}
		if tokens, ok := extractUsage(line); ok {
			totalTokens = tokens
		}

		// Budget ceiling, evaluated once per relayed line. Returning here unwinds
		// to handleChatCompletions, whose existing `defer cancel()` and
		// `defer resp.Body.Close()` tear the upstream connection down -- that is
		// what actually stops the spend, so no extra plumbing is needed.
		if reason, over := budgetExceeded(dataChunks, streamedBytes, ceiling); over {
			p.logger.Printf("budget: KILLED stream (key id=%s, reason=%s, chunks=%d, bytes=%d, ceiling=%d)", outcome.keyID, reason, dataChunks, streamedBytes, ceiling)
			totalTokens = chargeForKill(totalTokens, dataChunks, outcome.reserved)
			writeBudgetExceeded(w, flusher, canFlush)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		p.logger.Printf("streaming upstream response failed: %v", err)
	}
}

// chargeForKill decides what a stream killed by the budget ceiling is charged.
//
// THE INVARIANT: a killed stream consumed everything its ceiling permitted, so
// it must never move quota in the caller's favour. Charge the LARGEST defensible
// figure of the three available:
//
//   - measured -- a real usage figure, if a terminal usage chunk already arrived.
//   - dataChunks -- the chunk count, which is what binds on a token_ceiling kill:
//     that kill fires at dataChunks > ceiling, and ceiling is
//     requestTokenCeiling(reserved, headroom) = reserved + headroom (headroom is
//     never negative -- reserveQuota only succeeds within the limit) capped at
//     absoluteMaxRequestTokens, itself 2x the maxReservationTokens cap on
//     reserved. So ceiling >= reserved always, and the chunk count is the
//     largest of the three there.
//   - reserved -- the reservation, which is what binds on a byte_guard kill,
//     where the chunk count is tiny by construction: the guard fires because a
//     FEW huge chunks crossed the byte bound, so the count is nowhere near the
//     tokens actually generated.
//
// This exists because the previous form raised the charge to dataChunks alone.
// That holds only on the token_ceiling bound; on byte_guard it handed
// finalizeUsage an `actual` far BELOW `reserved`, which took the `actual > 0`
// branch and issued a large NEGATIVE correction -- a refund of ~all of the
// reservation after streaming the maximum the ceiling permits. Repeatable free
// paid inference, and the same abort-refund class as C3 (a703978) through a path
// C3's regression test does not cover. It was worst for the keys with the least
// headroom, since a smaller ceiling makes the byte guard fire sooner while the
// refund stays proportionally larger.
//
// The charge decision lives in one named function, rather than inline at the
// kill site, so the invariant has a home and a unit test and a third bound added
// to budgetExceeded later cannot silently mean "refund".
func chargeForKill(measured, dataChunks, reserved int) int {
	return max(measured, dataChunks, reserved)
}

// budgetExceeded reports whether a stream has crossed either of its two bounds,
// and which one, for the log line.
//
// TWO INDEPENDENT BOUNDS, each compared against what it actually measures. They
// are deliberately NOT fused into a single synthetic token count:
//
//   - TOKENS: dataChunks > ceiling. For OpenAI-compatible SSE one chunk carries
//     one delta, so the chunk count IS the token proxy.
//   - BYTES: streamedBytes > ceiling * maxBytesPerChunkGuard. A runaway guard for
//     the case chunk-counting cannot see -- a provider batching an entire answer
//     into a few huge chunks.
//
// The previous version multiplied these together into one estimate
// (max(dataChunks, streamedBytes/4)) and was wrong by ~73x, because dividing a
// whole SSE LINE by four bytes-per-token measures the repeated JSON envelope, not
// the token inside it. Keeping the bounds separate is what makes each one
// checkable against reality.
//
// KNOWN RESIDUAL, stated rather than hidden: a provider that batches N tokens per
// chunk undercounts the token bound by N, so the ceiling fires late. That is the
// safe direction -- spend is still bounded by the byte guard and by quota itself,
// and it replaces a failure mode that truncated legitimate traffic.
func budgetExceeded(dataChunks, streamedBytes, ceiling int) (reason string, exceeded bool) {
	if dataChunks > ceiling {
		return "token_ceiling", true
	}
	if streamedBytes > ceiling*maxBytesPerChunkGuard {
		return "byte_guard", true
	}
	return "", false
}

// writeBudgetExceeded emits the terminal chunks of a killed stream: a
// machine-readable error chunk followed by the standard [DONE] sentinel, so a
// client can render "response truncated: budget exceeded" and, crucially, tell
// throttling apart from a crashed connection. A silent close would be
// indistinguishable from a network failure.
//
// Write errors are ignored on purpose -- this runs on a stream already being torn
// down, and the client having gone away first changes nothing about the decision
// to stop.
func writeBudgetExceeded(w io.Writer, flusher http.Flusher, canFlush bool) {
	io.WriteString(w, "data: {\"error\":\"budget_exceeded\",\"truncated\":true}\n\n")
	io.WriteString(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

// stripSSEAccountMetadata removes accountMetadataFields from one "data: {...}"
// SSE line, returning the line unchanged if it carries none (the overwhelmingly
// common case) or if it isn't a JSON object this function can safely rewrite.
//
// It preserves the two properties that make this proxy zero-data-retention-
// preserving, which is why it does not simply reuse stripAccountMetadata:
//
//   - MESSAGE CONTENT IS NEVER INSPECTED. The decode target is
//     map[string]json.RawMessage, so every value -- including choices/delta text
//     -- stays an opaque byte slice that is never parsed, never examined and
//     never logged. Only top-level KEY NAMES are compared.
//   - THE STREAM IS NEVER BUFFERED. This rewrites at most one line in place;
//     the caller still relays and flushes line by line, so a chunk that needs no
//     change is forwarded byte-for-byte at effectively zero cost and the
//     incremental delivery this proxy exists for is unaffected.
//
// Re-marshalling only happens when a field was actually deleted, so the normal
// chunk is not even re-serialized (which would otherwise perturb key order and
// spacing for no reason).
func stripSSEAccountMetadata(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return line
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" || data == "[DONE]" {
		return line
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		// Not a JSON object (or malformed): pass it through untouched rather than
		// risk mangling a chunk this proxy does not understand.
		return line
	}
	stripped := false
	for _, field := range accountMetadataFields {
		if _, ok := parsed[field]; ok {
			delete(parsed, field)
			stripped = true
		}
	}
	if !stripped {
		return line
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return line
	}
	return "data: " + string(out)
}

// isDataChunk reports whether an SSE line carries a real completion payload --
// a non-empty "data:" chunk that isn't the terminal "[DONE]" marker -- i.e.
// billable output was produced upstream. It inspects only the SSE envelope,
// never the message content (same one-field discipline as extractProvider /
// extractUsage), so it preserves this proxy's never-parse-content posture. Used
// by streamSSE to decide whether an aborted stream consumed real output and so
// must be charged rather than refunded (see finalizeUsage).
func isDataChunk(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return false
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	return data != "" && data != "[DONE]"
}

// extractProvider looks at one SSE line and, if it's a "data: {...}" chunk
// carrying OpenRouter's optional top-level "provider" field, returns it.
// The decode target only ever has a Provider field -- content/delta fields
// are deliberately not part of this struct, so message text can never end
// up in a log line even by accident.
func extractProvider(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return "", false
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" || data == "[DONE]" {
		return "", false
	}
	var peek struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(data), &peek); err != nil || peek.Provider == "" {
		return "", false
	}
	return peek.Provider, true
}

// extractUsage looks at one SSE line and, if it's a "data: {...}" chunk
// carrying OpenRouter's optional top-level "usage" field (present on the
// final chunk of a stream when the request set stream_options.include_usage
// -- see daemon/provider.go), returns the total token count. Mirrors
// extractProvider deliberately: the decode target has ONLY a
// Usage.TotalTokens field. Content/delta fields are not part of this struct,
// so message text can never end up in a log or a DB write even by accident.
func extractUsage(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return 0, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" || data == "[DONE]" {
		return 0, false
	}
	var peek struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &peek); err != nil || peek.Usage.TotalTokens == 0 {
		return 0, false
	}
	return peek.Usage.TotalTokens, true
}

// peekMaxTokens looks at the incoming request body and, if it declares a
// positive integer max_tokens, returns it -- used to size a quota
// reservation (see reserveQuota) before the call is forwarded. Mirrors
// extractUsage's one-field-only decode discipline: the target struct has
// only a MaxTokens field, so message content is never touched here either.
// This is a narrow, deliberate exception to this proxy's usual "never
// parse the body" posture -- see QUOTA_RESERVATION_DESIGN.md §3 for why.
// In practice this rarely returns true: the daemon's own request struct
// never sets max_tokens (daemon/provider.go), so reservation sizing falls
// through to defaultReservationTokens for essentially all real traffic
// today.

// topLevelFields decodes ONLY the top-level object of a request body, leaving
// every value an opaque json.RawMessage. It is the shared primitive under every
// body gate in this file, and it exists to make those gates read the body the
// same way OpenRouter does.
//
// WHY NOT STRUCT TAGS. Go's encoding/json "matches incoming object keys to the
// keys used by Marshal (either the struct field name or its tag), IGNORING CASE"
// (go doc encoding/json.Unmarshal). OpenRouter, like essentially every JSON API,
// matches keys exactly. Every gate built on struct-tag decoding therefore had a
// parser differential, and it split in the unsafe direction: a body carrying
// {"PROVIDER":{"zdr":true,...}} passed the ZDR gate -- the proxy saw the flags --
// while OpenRouter saw no `provider` key at all and applied no ZDR routing.
// {"STREAM":true} did the same to the streaming requirement, reopening the
// buffered-response escape from the budget ceiling. Reproduced directly; see
// TestZDRRoutingEnforced_RejectsCaseVariantKeys.
//
// Exact lookup fixes both in the FAIL-CLOSED direction: a case-variant key is
// simply not found, so the gate refuses rather than being satisfied by a key the
// model provider will never read.
//
// It preserves the never-parse-content posture for the same reason
// stripSSEAccountMetadata does: values -- including `messages` -- stay opaque
// byte slices that are never examined. Only top-level KEY NAMES are compared.
// hasDuplicateKeys reports whether body contains any JSON object with the same
// key twice, at any depth.
//
// WHY THIS IS A REFUSAL AND NOT A PARSING DETAIL. Every body gate reads through
// topLevelFields -> json.Unmarshal into map[string]json.RawMessage, which
// resolves duplicates LAST-WINS. The accepted body is then forwarded to
// OpenRouter BYTE-FOR-BYTE carrying both copies. So a body naming a banned model
// first and an allowed one second is admitted on the second and forwarded with
// the first -- the gate's verdict and the bytes the provider actually reads
// disagree. Reproduced: cost authorization admitted a request whose forwarded
// body still named the banned model.
//
// Whether that completes upstream depends on OpenRouter resolving duplicates
// first-wins, which RFC 8259 explicitly leaves undefined and which is not this
// proxy's to assume. Refusing removes the dependency rather than betting on it --
// the same reasoning that replaced struct-tag decoding with exact-key lookup
// (topLevelFields), one level down: the proxy's claim to be THE AUTHORITY on
// model/provider/stream/max_tokens holds only if both parsers read the same body
// the same way.
//
// Nested objects are walked too, so a duplicate inside `provider` -- where the
// ZDR flags live -- is caught as well as a top-level one.
//
// Values stay opaque exactly as in topLevelFields: json.Decoder is driven
// token-wise and only object KEY NAMES are ever compared. Message content is
// never examined, so the never-parse-content property the ZDR posture rests on
// is preserved. Stdlib only -- the proxy stays dependency-free.
func hasDuplicateKeys(body []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		// Empty or malformed: not this function's call. The gates already refuse a
		// body they cannot decode, so leave that refusal where it belongs.
		return false
	}
	duplicate, _ := scanJSONValue(dec, tok, 0)
	return duplicate
}

// maxJSONNestingDepth bounds scanJSONValue's recursion. A 4MB body (the
// MaxBytesReader cap) of "[[[[..." would otherwise recurse ~4M frames. No real
// chat-completions body comes close to this depth, so exceeding it is treated as
// a duplicate -- i.e. REFUSED, the fail-closed direction, consistent with every
// other gate here.
const maxJSONNestingDepth = 64

// scanJSONValue consumes the one JSON value whose first token is tok, descending
// into objects and arrays. It reports whether a duplicate key was found, and
// whether the scan itself completed (false on malformed input or excess depth).
func scanJSONValue(dec *json.Decoder, tok json.Token, depth int) (duplicate, ok bool) {
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return false, true // a scalar: nothing to descend into
	}
	if depth >= maxJSONNestingDepth {
		return true, false
	}

	switch delim {
	case '{':
		seen := make(map[string]bool)
		for {
			keyTok, err := dec.Token()
			if err != nil {
				return false, false
			}
			if d, isD := keyTok.(json.Delim); isD && d == '}' {
				return false, true
			}
			key, isString := keyTok.(string)
			if !isString {
				return false, false // unreachable for well-formed JSON
			}
			if seen[key] {
				return true, true
			}
			seen[key] = true

			valTok, err := dec.Token()
			if err != nil {
				return false, false
			}
			if dup, done := scanJSONValue(dec, valTok, depth+1); dup || !done {
				return dup, done
			}
		}
	case '[':
		for {
			elemTok, err := dec.Token()
			if err != nil {
				return false, false
			}
			if d, isD := elemTok.(json.Delim); isD && d == ']' {
				return false, true
			}
			if dup, done := scanJSONValue(dec, elemTok, depth+1); dup || !done {
				return dup, done
			}
		}
	}
	return false, false
}

func topLevelFields(body []byte) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// rawString / rawBool / rawInt decode one opaque field value to a concrete type,
// reporting false if it is absent or the wrong JSON type. Absent-or-wrong-type is
// deliberately indistinguishable: every caller treats both as "the gate is not
// satisfied", which is the fail-closed reading.
func rawString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok {
		return "", false
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	return v, true
}

func rawBool(fields map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := fields[key]
	if !ok {
		return false, false
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false
	}
	return v, true
}

func rawInt(fields map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := fields[key]
	if !ok {
		return 0, false
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

// streamRequested reports whether the body explicitly asks for a streamed
// completion, by EXACT key match (see topLevelFields for why that matters).
//
// Fails CLOSED in the same sense as zdrRoutingEnforced -- an unparseable body, an
// absent `stream`, a non-boolean `stream`, or a case-variant key all return false
// and the request is refused. The absent case matters: OpenAI-compatible APIs
// default `stream` to false when the field is omitted, so treating "missing" as
// "streaming" would reopen the very bypass this closes.
func streamRequested(body []byte) bool {
	fields, ok := topLevelFields(body)
	if !ok {
		return false
	}
	stream, ok := rawBool(fields, "stream")
	return ok && stream
}

// requiredDataCollection is the only data_collection value consistent with the
// zero-data-retention posture the daemon sends by default (daemon/config.go's
// resolvedProviderRouting resolves the secure default to "deny"). A request must
// carry exactly this to be forwarded.
const requiredDataCollection = "deny"

// zdrRoutingEnforced reports whether body carries a provider routing object with
// the required zero-data-retention flags (zdr:true, data_collection:"deny"). It
// is the F1 enforcement predicate: handleChatCompletions REFUSES to forward any
// request for which this returns false.
//
// It fails CLOSED. Anything short of an explicit, correct routing object --
// unparseable body, absent "provider" object, a "provider" that isn't an object,
// zdr not true, or data_collection not "deny" -- returns false and the request is
// rejected. Nothing is ever forwarded on the strength of an assumed default.
//
// It reads ONLY the two ZDR flags, with the same one-field decode discipline as
// costSurfaceRefusal/peekMaxTokens: message content is never parsed (the decode target has
// no messages field), and every OTHER field of the provider object is ignored on
// purpose. In particular the D4 "ignore"/"only" provider lists are NOT part of
// this struct, so a request carrying them is judged solely on its ZDR flags and
// is never rejected for having them -- the seam where F1 and D4 meet.
//
// The body is NOT mutated: this is a read-only peek, so the accept path forwards
// the caller's bytes byte-for-byte (Reject, not Stamp -- see
// proxy/F1_ENFORCEMENT_DESIGN.md).
// Keys are matched EXACTLY (see topLevelFields): a body carrying "PROVIDER" or
// "ZDR" is refused, because those are keys OpenRouter will not read.
func zdrRoutingEnforced(body []byte) bool {
	fields, ok := topLevelFields(body)
	if !ok {
		return false // unparseable body -> fail closed
	}
	provider, ok := fields["provider"]
	if !ok {
		return false // no routing object at all -> fail closed
	}
	routing, ok := topLevelFields(provider)
	if !ok {
		return false // "provider" present but not an object -> fail closed
	}
	zdr, zdrOK := rawBool(routing, "zdr")
	collection, collectionOK := rawString(routing, "data_collection")
	return zdrOK && zdr && collectionOK && collection == requiredDataCollection
}

// modelAllowed reports whether a caller may route to model.
//
// COST AUTHORIZATION, which quota alone does not provide. Quota is metered in
// TOKENS, but the account is billed in DOLLARS, and $/token differs by orders of
// magnitude across models (this project's own P2 work measured 3.1x variance
// between PROVIDERS of a single model; across models the spread is far wider).
// The proxy forwards the body byte-for-byte, so before this an authenticated key
// could name ANY OpenRouter model and spend far more money than its token budget
// implies — proven live: an arbitrary model was relayed upstream verbatim.
//
// The managed tier ships a decided model set (Phase 4: "Mochiii holds the key
// and ships brain+agent"), so restricting to that set is the product's own
// posture rather than a new policy. allowed is built once at startup from
// ALLOWED_MODELS when set, else the shipped tiers; an empty set means
// unrestricted and is logged loudly at startup so it can never be the silent
// default.
func (p *proxy) modelAllowed(model string) bool {
	if len(p.allowedModels) == 0 {
		return true
	}
	return p.allowedModels[model]
}

// costSurfaceRefusal is the full cost-authorization gate. It returns the error
// code to refuse with and true, or "" and false to allow.
//
// modelAllowed alone was not enough. It checked ONLY the top-level "model", while
// the proxy forwards the body byte-for-byte -- so every OTHER field that steers
// what gets billed went upstream unexamined:
//
//   - "models": OpenRouter's fallback array. A request naming an allow-listed
//     model can list arbitrary others to fall back to.
//   - "plugins": billed per use independently of tokens (web search is the
//     obvious one) -- spend that the token quota cannot see at all.
//   - "transforms": provider-side processing outside the model's own pricing.
//   - "provider.only" / "order" / "sort": cost STEERING within one model. This
//     project's own P2 work measured 3.1x cost variance between PROVIDERS of a
//     single model, so this is not hypothetical.
//
// It also closes a fail-OPEN in the predecessor it replaces: the old
// `if model, ok := peekModel(body); ok && !p.modelAllowed(model)` skipped the
// allow-list check entirely when a body parsed but carried no "model". A missing
// model is now a refusal.
//
// Same one-field decode discipline as zdrRoutingEnforced: every cost field is
// decoded as json.RawMessage purely to test for PRESENCE, never parsed, and the
// struct has no "messages" field, so content is never touched. Fails closed on an
// unparseable body.
//
// THE D4 SEAM. "provider.ignore" is deliberately NOT refused -- it is what D4
// (commit 0c5bb29) uses to exclude DeepInfra from routing, and refusing it would
// break the shipped daemon on every request. Only the cost-STEERING provider
// fields are refused; a deny-list narrows where traffic may go, which is the safe
// direction. This predicate is also kept separate from zdrRoutingEnforced rather
// than folded into it, so the F1 and D4 concerns stay independently reviewable.
// Keys are matched EXACTLY, like every other gate here (see topLevelFields).
// This gate's case-differential happened to fail SAFE -- a case-variant
// "MODELS" was over-refused rather than let through -- but it is converted
// anyway: every gate reading the body the same way OpenRouter does is itself the
// property worth having, and one predicate quietly using different matching
// rules is how the next differential gets missed.
func (p *proxy) costSurfaceRefusal(body []byte) (string, bool) {
	fields, ok := topLevelFields(body)
	if !ok {
		return "malformed_request", true // unparseable -> fail closed
	}

	for _, billable := range []string{"models", "plugins", "transforms"} {
		if _, present := fields[billable]; present {
			return "cost_surface_not_allowed", true
		}
	}
	if provider, present := fields["provider"]; present {
		if routing, isObject := topLevelFields(provider); isObject {
			for _, steering := range []string{"only", "order", "sort"} {
				if _, present := routing[steering]; present {
					return "cost_surface_not_allowed", true
				}
			}
		}
	}

	model, ok := rawString(fields, "model")
	if !ok || model == "" {
		return "model_required", true
	}
	if !p.modelAllowed(model) {
		return "model_not_allowed", true
	}
	return "", false
}

// parseAllowedModels builds the allow-list set from a comma-separated list.
func parseAllowedModels(raw string) map[string]bool {
	set := make(map[string]bool)
	for _, m := range strings.Split(raw, ",") {
		if m = strings.TrimSpace(m); m != "" {
			set[m] = true
		}
	}
	return set
}

// Keys are matched EXACTLY (see topLevelFields). A case-variant "MAX_TOKENS" is
// NOT read: sizing a reservation from a field OpenRouter will never see would
// under-reserve while the provider applied its own far larger default.
func peekMaxTokens(body []byte) (int, bool) {
	fields, ok := topLevelFields(body)
	if !ok {
		return 0, false
	}
	maxTokens, ok := rawInt(fields, "max_tokens")
	if !ok || maxTokens <= 0 {
		return 0, false
	}
	return maxTokens, true
}

// peekUsageTotal looks at a non-streamed chat-completion response body and,
// if it carries a top-level "usage.total_tokens" field, returns it. Same
// one-field-only decode discipline as extractUsage, just applied to a whole
// buffered response instead of one SSE line -- used by the non-SSE branch
// of handleChatCompletions so it can true up a reservation instead of
// leaving it stranded (see QUOTA_RESERVATION_DESIGN.md §5(c)).
func peekUsageTotal(body []byte) (int, bool) {
	var peek struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &peek); err != nil || peek.Usage.TotalTokens == 0 {
		return 0, false
	}
	return peek.Usage.TotalTokens, true
}

// finalizeUsage reconciles the reservation (reserveQuota, made before the call
// was forwarded) against what the request actually consumed. It must run on
// every exit path -- an unreconciled reservation would strand real quota
// forever (see QUOTA_RESERVATION_DESIGN.md §5) -- and it decides the correction
// three ways, NOT the old two-way "actual>0 ? true-up : full-refund":
//
//   - actual > 0: a real usage figure is in hand -> true up to it exactly.
//   - actual == 0 && producedOutput: the request produced billable output
//     upstream (OpenRouter generated and billed tokens) but no usage figure
//     ever reached us -- the classic case is a client that read the full
//     answer and disconnected just before the terminal usage chunk. Refunding
//     here would be free, repeatable, exploitable inference on the paid tier,
//     so the reservation is KEPT as the charge (delta 0 -- reserveQuota already
//     added it to tokens_used) rather than refunded. This under-meters a long
//     completion aborted late relative to its true usage, but in the SAFE
//     direction and never to zero (§5(d)/(e)); precise metering of an aborted
//     stream is a named follow-up, not this fix.
//   - actual == 0 && !producedOutput: nothing was produced (upstream never
//     reached, or a non-billable error response) -> full refund, as before.
//
// producedOutput corrects QUOTA_RESERVATION_DESIGN.md §5(c), which wrongly
// treated a client-disconnect-before-usage-chunk as "nothing used".
//
// pendingID is the outbox row reserveQuota opened for this reservation. EVERY
// branch must reach correctUsage with it, including the middle one whose
// correction is numerically zero: the delta is what the accounting needs, but
// closing the outbox row is what tells the reconciliation sweep this request
// was handled. Before the outbox existed the middle branch could simply log and
// return, since a no-op correction was genuinely nothing to do; now that same
// early return would leave a live row behind on a perfectly healthy request,
// and the sweep would report it ~20 minutes later as an abandoned reservation
// that never happened -- turning the crash alarm into noise on the one path
// most likely to fire it (clients disconnecting mid-stream).
// reservationOutcome accumulates what one request did to its reservation, so
// that billing happens at exactly one deferred site (see handleChatCompletions)
// rather than at every branch that can end a request.
//
// Its zero value is the SAFE one on purpose: actual=0 and producedOutput=false
// mean "upstream generated nothing", which finalizeUsage turns into a full
// refund. A branch that fails before reaching upstream therefore needs to record
// nothing at all.
type reservationOutcome struct {
	// Set once, at construction, and never mutated.
	keyID     string
	reserved  int
	pendingID int64

	// Recorded by whichever branch handles the response.
	actual         int
	producedOutput bool

	// finalized makes finalizeReservation idempotent. Two things rely on it:
	// nested defers (streamSSE unwinding into the handler's own defer), and the
	// panic path, where the recovery middleware and this defer both run.
	finalized bool
}

// finalizeReservation bills o exactly once, however many times it is called.
//
// Deliberately separate from finalizeUsage, which is left exactly as it was:
// finalizeUsage owns the CHARGE POLICY (how a missing usage figure is treated,
// what the rate bucket is charged, which direction the correction goes) and is
// unit-tested on that policy. This function owns only the once-ness. Keeping the
// two apart means the invariant added here cannot perturb the policy the
// quota-reservation design document specifies.
func (p *proxy) finalizeReservation(o *reservationOutcome) {
	if o == nil || o.finalized {
		return
	}
	o.finalized = true
	p.finalizeUsage(o.keyID, o.reserved, o.actual, o.producedOutput, o.pendingID)
}

func (p *proxy) finalizeUsage(keyID string, reserved, actual int, producedOutput bool, pendingID int64) {
	// Charge the token-rate bucket with the best figure available: the real usage
	// when there is one, otherwise the reservation. Never nothing -- a request
	// that produced output it could not measure must still cost the key something
	// against its rate, or "make the usage chunk go missing" becomes the way to
	// get unmetered throughput. Independent of the quota correction below: quota
	// is a cumulative budget, this is a rate.
	charged := actual
	if charged == 0 && producedOutput {
		charged = reserved
	}
	p.keyTokens.charge(keyID, float64(charged))

	switch {
	case actual > 0:
		go p.correctUsage(keyID, actual-reserved, pendingID)
	case producedOutput:
		p.logger.Printf("usage: output produced but no usage figure (key_id=%s) -- keeping reservation=%d, not refunding", keyID, reserved)
		go p.correctUsage(keyID, 0, pendingID)
	default:
		go p.correctUsage(keyID, -reserved, pendingID)
	}
}

// correctUsage adjusts keyID's tokens_used by the signed delta between what
// was reserved (reserveQuota) and what the request actually used, AND closes
// this reservation's pending_corrections outbox row, via the apply_correction
// Postgres RPC (proxy/migrations/0002_pending_corrections.sql). Those two
// effects are one atomic statement inside that RPC rather than two calls from
// here: a crash between an increment and a separate row-close would either
// double-charge the reservation on the next sweep or lose the correction
// outright, which is the exact failure class the outbox exists to eliminate.
//
// This replaced a direct increment_usage call, which had no notion of an
// outbox. delta may be negative (a
// refund: actual usage was less than reserved, or there was no usable
// response at all) or positive (a top-up: actual usage exceeded the
// reservation -- see QUOTA_RESERVATION_DESIGN.md §3, this is a common case
// for this product's active model, not a tail case). Runs on a context
// detached from the original request, same as before: the client already
// has its complete response by the time this runs (see finalizeUsage), so
// it must not be tied to a context that may already be canceled.
//
// Retries up to correctUsageMaxAttempts times on a network error or a 5xx
// response (not on 4xx -- that indicates a real request-shape problem a
// retry can't fix). This exists because a lost correction here is NOT
// uniformly safe: a lost refund leaves tokens_used too high (safe -- a
// later request is throttled slightly early), but a lost top-up leaves it
// too low (unsafe -- a later request can be admitted against quota that
// was already really spent). See QUOTA_RESERVATION_DESIGN.md §5 for the
// full direction analysis. This narrows the single-attempt failure window;
// it is not durable against a sustained outage or a process crash
// mid-retry -- if every attempt is exhausted, that is logged distinctly
// (CORRECTION LOST, below) rather than silently dropped the way the
// pre-reservation recordUsage's single failed attempt was, so it is at
// least visible rather than silent. A correction lost that way now also
// leaves its outbox row open, so the sweep reports it too: exhausted retries
// are no longer only a log line that has to be noticed in the moment.
func (p *proxy) correctUsage(keyID string, delta int, pendingID int64) {
	if p.supabaseURL == "" || p.supabaseServiceRoleKey == "" {
		return
	}

	payload, err := json.Marshal(struct {
		KeyID     string `json:"p_key_id"`
		Tokens    int    `json:"p_tokens"`
		PendingID int64  `json:"p_pending_id"`
	}{KeyID: keyID, Tokens: delta, PendingID: pendingID})
	if err != nil {
		p.logger.Printf("usage: encoding correction body failed (key_id=%s): %v", keyID, err)
		return
	}

	rpcURL := strings.TrimRight(p.supabaseURL, "/") + "/rest/v1/rpc/apply_correction"

	for attempt := 1; attempt <= correctUsageMaxAttempts; attempt++ {
		ok, retryable := p.tryCorrectUsage(rpcURL, payload, keyID, delta, attempt)
		if ok {
			p.logger.Printf("usage: corrected delta=%d for key_id=%s (attempt %d)", delta, keyID, attempt)
			return
		}
		if !retryable || attempt == correctUsageMaxAttempts {
			break
		}
		time.Sleep(correctUsageRetryBackoff[attempt-1])
	}

	p.logger.Printf("usage: CORRECTION LOST after %d attempts (key_id=%s, delta=%d) -- tokens_used may be inaccurate", correctUsageMaxAttempts, keyID, delta)
}

// tryCorrectUsage makes a single attempt at the increment_usage RPC call
// and reports whether it succeeded and, if not, whether the failure is
// worth retrying: a network error or a 5xx is treated as transient
// (retryable), a 4xx is treated as a request-shape problem that will fail
// identically on every retry (not retryable).
func (p *proxy) tryCorrectUsage(rpcURL string, payload []byte, keyID string, delta int, attempt int) (ok bool, retryable bool) {
	ctx, cancel := context.WithTimeout(context.Background(), usageUpdateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(payload))
	if err != nil {
		p.logger.Printf("usage: building correction request failed (key_id=%s, attempt=%d): %v", keyID, attempt, err)
		return false, false
	}
	// apikey only -- see authorize's comment on this same header-format
	// bug (Supabase's sb_secret_ keys aren't JWTs; Authorization: Bearer
	// gets forwarded to the DB's own JWT parser and rejected there).
	req.Header.Set("apikey", p.supabaseServiceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("usage: correction call failed (key_id=%s, delta=%d, attempt=%d): %v", keyID, delta, attempt, err)
		return false, true
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxAuthResponseBytes))

	if resp.StatusCode >= 500 {
		p.logger.Printf("usage: correction RPC returned status=%d (key_id=%s, delta=%d, attempt=%d)", resp.StatusCode, keyID, delta, attempt)
		return false, true
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		p.logger.Printf("usage: correction RPC returned status=%d (key_id=%s, delta=%d, attempt=%d)", resp.StatusCode, keyID, delta, attempt)
		return false, false
	}

	return true, false
}

// startReconciliationSweep runs the outbox sweep forever on
// reconciliationSweepInterval. Launched as a goroutine from main and never
// stopped: it is exactly as long-lived as the process, and the sweep it drives
// is only meaningful while the process is up.
//
// This is the half of QUOTA_RESERVATION_DESIGN.md §5(e)'s fix that no
// in-request code path can provide. Retries (§5(d)) cover a correction whose
// call failed; nothing in-process can cover a correction whose PROCESS died,
// because the goroutine that would have retried died with it. The durable
// record left in pending_corrections is picked up here instead, by whichever
// process is running next -- the same one after a restart, or a sibling
// replica that never crashed at all.
// ctx stops the loop on shutdown. Without it the ticker would keep firing while
// the process is draining, issuing Supabase calls on behalf of a server that is
// already gone.
func (p *proxy) startReconciliationSweep(ctx context.Context) {
	if p.supabaseURL == "" || p.supabaseServiceRoleKey == "" {
		p.logger.Printf("reconciliation: sweep disabled (supabase not configured)")
		return
	}
	p.logger.Printf("reconciliation: sweep every %s for reservations older than %dm", reconciliationSweepInterval, pendingCorrectionStaleAfterMinutes)
	ticker := time.NewTicker(reconciliationSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.logger.Print("reconciliation: sweep stopped (shutting down)")
			return
		case <-ticker.C:
			p.sweepTick()
		}
	}
}

// sweepTick runs one sweep and contains any panic inside it.
//
// The containment is the point. This loop is the only thing that ever reports a
// stranded reservation, and a panic in the tick used to take the whole goroutine
// with it: reconciliation would stop for the lifetime of the process, silently,
// while /health kept answering 200. A monitoring surface that can die without
// saying so is worse than not having one, because its silence reads as "nothing
// to report". Log loudly and let the ticker carry on.
func (p *proxy) sweepTick() {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Printf("reconciliation: PANIC in sweep, recovered so reconciliation keeps running: %v\n%s", r, debug.Stack())
		}
	}()
	p.sweepPendingCorrections()
}

// sweepPendingCorrections claims every reservation older than
// pendingCorrectionStaleAfterMinutes and logs each one loudly.
//
// It does NOT refund them, and that is the deliberate design decision rather
// than an omission -- see the sweep_pending_corrections comment in
// proxy/migrations/0002_pending_corrections.sql. The true usage of a request
// whose process died is not recoverable from anywhere: the figure only ever
// existed in the response stream that process was reading. Of the two available
// guesses, refunding is the unsafe one (it hands back quota for inference
// OpenRouter really did bill, and it is trivially repeatable by anyone who can
// make the proxy restart). Keeping the reservation charged over-meters at
// worst, in the direction that can only ever throttle a later request early.
//
// So the honest description of what this achieves: it does not fix the
// accounting, it makes the damage bounded, visible and safely-directed instead
// of silent and indefinite. The log line is the actual deliverable -- an
// operator who sees it knows a specific key was over-charged by a specific
// amount at a specific time, and can decide what to do about it.
//
// Errors are logged and the sweep returns; the rows are still there for the
// next tick, since the claim only happens on a successful DELETE ... RETURNING.
func (p *proxy) sweepPendingCorrections() {
	ctx, cancel := context.WithTimeout(context.Background(), reconciliationSweepTimeout)
	defer cancel()

	payload, err := json.Marshal(struct {
		StaleMinutes int `json:"p_stale_minutes"`
	}{StaleMinutes: pendingCorrectionStaleAfterMinutes})
	if err != nil {
		p.logger.Printf("reconciliation: encoding sweep body failed: %v", err)
		return
	}

	rpcURL := strings.TrimRight(p.supabaseURL, "/") + "/rest/v1/rpc/sweep_pending_corrections"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(payload))
	if err != nil {
		p.logger.Printf("reconciliation: building sweep request failed: %v", err)
		return
	}
	// apikey only -- see authorize's comment on this same header-format bug.
	req.Header.Set("apikey", p.supabaseServiceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("reconciliation: sweep call failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxSweepResponseBytes))
		p.logger.Printf("reconciliation: sweep returned status=%d", resp.StatusCode)
		return
	}

	var rows []struct {
		ID        int64  `json:"id"`
		KeyID     string `json:"key_id"`
		Reserved  int    `json:"reserved"`
		CreatedAt string `json:"created_at"`
	}
	if err := decodeCappedJSON(resp, maxSweepResponseBytes, &rows); err != nil {
		p.logger.Printf("reconciliation: decoding sweep response failed: %v", err)
		return
	}

	// Silent on the common case. A healthy proxy sweeps up nothing every five
	// minutes forever, and a line saying so would train operators to ignore
	// this prefix -- which is the one prefix that must stay attention-worthy.
	if len(rows) == 0 {
		return
	}

	for _, row := range rows {
		p.logger.Printf("reconciliation: ABANDONED RESERVATION swept (pending_id=%d, key_id=%s, reserved=%d, reserved_at=%s) -- no correction was ever applied, most likely a proxy crash mid-request; the reservation stays CHARGED (never refunded on missing usage data) and tokens_used for this key is overstated by at most that amount",
			row.ID, row.KeyID, row.Reserved, row.CreatedAt)
	}
	p.logger.Printf("reconciliation: swept %d abandoned reservation(s)", len(rows))
}
