package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"expvar"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Counters.
//
// The log answers "what happened to this request". These answer the questions a
// log cannot: how often, and -- the one that motivated this -- is the thing that
// is supposed to be running still running?
//
// The reconciliation sweep is the case in point. It ticks every five minutes,
// logs only when it finds something (deliberately, so the prefix stays
// attention-worthy), and is therefore INDISTINGUISHABLE from a sweep that died
// twenty minutes ago. sweep_last_run_unix makes "is reconciliation alive?" a
// question with an answer.
//
// expvar, from the standard library, so the dependency property holds. Note that
// importing expvar registers /debug/vars on http.DefaultServeMux -- which this
// server NEVER serves (see newMux; every route is registered on its own mux), so
// that handler is unreachable here. The only exposure is the authed route below.

// metricSet is one process's counters.
//
// Constructed UNREGISTERED, on purpose. expvar.NewInt panics on a duplicate name,
// and the tests build many proxies in one process -- package-level expvar.NewInt
// vars would panic on the second one, and would also make every count global
// instead of per-instance, so no test could assert on a number without being
// perturbed by every other test. main publishes the one live set (publishMetrics);
// tests just read the fields.
type metricSet struct {
	// vars is what a scrape renders; started backs uptime_seconds.
	vars    *expvar.Map
	started time.Time

	// Request flow.
	requests        expvar.Int
	responses2xx    expvar.Int
	responses4xx    expvar.Int
	responses5xx    expvar.Int
	panicsRecovered expvar.Int

	// Refusals, keyed by the gate labels in logging.go -- the same strings the
	// log's gate= carries, so a count and a line name the same thing.
	refusalsByGate expvar.Map
	// Throttles, keyed by bucket (global, source, key_rate, token_rate, in_flight).
	rateLimitedByScope expvar.Map

	// The money path. openend == finalized is the invariant P1.1 made structural;
	// a gap between them means a reservation is in flight or was lost.
	reservationsOpened    expvar.Int
	reservationsFinalized expvar.Int
	reservationsStranded  expvar.Int
	correctionsLost       expvar.Int

	// Reconciliation liveness.
	sweepRuns        expvar.Int
	sweepRowsSwept   expvar.Int
	sweepLastRunUnix expvar.Int
	sweepLastErrUnix expvar.Int

	// Upstream.
	upstreamErrors expvar.Int
	budgetKills    expvar.Int

	// Auth cache. The hit RATE is the whole point: it is what says whether the
	// 91 ms saving is actually being realised in production, or whether traffic
	// is spread across enough distinct keys that every request still misses.
	authCacheHits   expvar.Int
	authCacheMisses expvar.Int

	// Admin surface.
	adminAuthFailures expvar.Int
}

func newMetrics() *metricSet {
	m := &metricSet{started: time.Now()}
	m.refusalsByGate.Init()
	m.rateLimitedByScope.Init()
	m.buildVars()
	return m
}

// countRefusal records one refusal under its gate label.
func (m *metricSet) countRefusal(gate string) {
	if m == nil {
		return
	}
	m.refusalsByGate.Add(gate, 1)
}

// countRateLimited records one throttle under its bucket.
func (m *metricSet) countRateLimited(scope string) {
	if m == nil {
		return
	}
	m.rateLimitedByScope.Add(scope, 1)
	m.refusalsByGate.Add(gateRateLimited, 1)
}

// countPanic records a contained handler fault.
func (m *metricSet) countPanic() {
	if m == nil {
		return
	}
	m.panicsRecovered.Add(1)
}

// countResponse buckets a status. Called once per request from accessLog, which is
// the only place that knows what the caller actually received.
func (m *metricSet) countResponse(status int) {
	if m == nil {
		return
	}
	m.requests.Add(1)
	switch {
	case status >= 500:
		m.responses5xx.Add(1)
	case status >= 400:
		m.responses4xx.Add(1)
	case status >= 200:
		m.responses2xx.Add(1)
	}
}

// publish exposes m under one expvar name, as a map of everything.
//
// Called exactly once, from main. A second call would panic on the duplicate
// name, which is the correct outcome: two live metric sets in one process would
// mean two proxies serving, and every number would be a half-truth.
// vars is the set rendered to a scrape, built once in newMetrics.
//
// Deliberately NOT registered with expvar.Publish, and the admin route
// deliberately does NOT serve expvar.Handler(). That handler renders the DEFAULT
// registry, which the expvar package populates with `cmdline` and `memstats` --
// so a scrape disclosed the binary's full filesystem path and several hundred
// lines of heap internals, with the counters somewhere underneath. Found by
// scraping the real binary rather than by reading the code.
//
// Keeping our own map also removes the duplicate-name panic entirely: nothing is
// global, so a test can build as many proxies as it likes.
func (m *metricSet) buildVars() {
	m.vars = new(expvar.Map).Init()
	m.vars.Set("requests_total", &m.requests)
	m.vars.Set("responses_2xx", &m.responses2xx)
	m.vars.Set("responses_4xx", &m.responses4xx)
	m.vars.Set("responses_5xx", &m.responses5xx)
	m.vars.Set("panics_recovered_total", &m.panicsRecovered)
	m.vars.Set("refusals_by_gate", &m.refusalsByGate)
	m.vars.Set("rate_limited_by_scope", &m.rateLimitedByScope)
	m.vars.Set("reservations_opened_total", &m.reservationsOpened)
	m.vars.Set("reservations_finalized_total", &m.reservationsFinalized)
	m.vars.Set("reservations_stranded_total", &m.reservationsStranded)
	m.vars.Set("corrections_lost_total", &m.correctionsLost)
	m.vars.Set("sweep_runs_total", &m.sweepRuns)
	m.vars.Set("sweep_rows_swept_total", &m.sweepRowsSwept)
	m.vars.Set("sweep_last_run_unix", &m.sweepLastRunUnix)
	m.vars.Set("sweep_last_error_unix", &m.sweepLastErrUnix)
	m.vars.Set("upstream_errors_total", &m.upstreamErrors)
	m.vars.Set("budget_kills_total", &m.budgetKills)
	m.vars.Set("admin_auth_failures_total", &m.adminAuthFailures)
	m.vars.Set("auth_cache_hits_total", &m.authCacheHits)
	m.vars.Set("auth_cache_misses_total", &m.authCacheMisses)

	// Runtime numbers a soak test and a memory-leak question need, chosen one by
	// one rather than by handing over all of memstats. No paths, no command line.
	m.vars.Set("uptime_seconds", expvar.Func(func() any {
		return int64(time.Since(m.started).Seconds())
	}))
	m.vars.Set("goroutines", expvar.Func(func() any { return runtime.NumGoroutine() }))
	m.vars.Set("heap_alloc_bytes", expvar.Func(func() any {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}))
}

// bindDraining exposes the shutdown flag live.
//
// Read through an expvar.Func rather than mirrored into an Int: a mirror has to be
// updated by whoever flips the flag, and the flag is already authoritative
// elsewhere. A scrape then shows whether this replica is on its way out without
// having to interpret a 503 from /health.
func (m *metricSet) bindDraining(draining *atomic.Bool) {
	m.vars.Set("draining", expvar.Func(func() any { return atomicBoolToInt(draining) }))
}

// bindLimiterBuckets exposes the live total of rate-limiter buckets held across
// every limiter.
//
// The rate limiters hold one bucket per distinct key -- a client IP on the
// pre-auth surface, an api_keys.id after it -- so this is the one dimension of
// the proxy that grows with the number of DISTINCT callers rather than with
// concurrency. sweepLocked reclaims idle ones; whether it keeps up was
// previously only answerable by inferring from RSS, which moves for a dozen
// unrelated reasons. The soak test asserts on this directly.
//
// Read through an expvar.Func for the same reason as draining: the limiters are
// authoritative, and a mirrored counter would need updating by every caller that
// creates a bucket.
func (m *metricSet) bindLimiterBuckets(count func() int) {
	m.vars.Set("rate_limiter_buckets", expvar.Func(func() any { return count() }))
}

// writeTo renders the counters as JSON.
func (m *metricSet) writeTo(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, m.vars.String())
}

// adminTokenEnv names the credential for the counters route.
const adminTokenEnv = "PROXY_ADMIN_TOKEN"

// minAdminTokenLen is the shortest token that may be configured.
//
// Fail-fast rather than warn-and-serve, matching the daemon's Tier-4 C1 config
// discipline: a four-character token on an internet-facing operational endpoint is
// a misconfiguration whose consequence is silent, and the correct time to find out
// is at startup rather than in an access log afterwards.
const minAdminTokenLen = 24

// adminMetricsPath is the counters route. Registered only when a token is set.
const adminMetricsPath = "/admin/metrics"

// adminMetricsHandler serves the counters to an authenticated operator.
//
// Three properties, each deliberate:
//
//  1. Pre-auth admission FIRST, like every other route. The endpoint-security pass
//     found /health serving 200 unauthenticated requests with zero throttling, and
//     the lesson generalizes: an unthrottled route is an unthrottled route whether
//     or not it needs a credential.
//  2. Constant-time comparison over SHA-256 DIGESTS of both sides, so neither the
//     token's bytes nor its LENGTH leaks through timing.
//  3. A wrong token gets the same 401 shape as everything else, and is counted.
//     Nothing in the response distinguishes "no such route" from "wrong token" --
//     when no token is configured the route genuinely does not exist, and the
//     catch-all answers instead.
func (p *proxy) adminMetricsHandler(token string) http.HandlerFunc {
	want := sha256.Sum256([]byte(token))
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.admitPreAuth(w, r) {
			return
		}
		reqID := requestIDFrom(r.Context())
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(strings.TrimSpace(presented)))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			p.metrics.adminAuthFailures.Add(1)
			p.log.Warn("admin metrics rejected", "req_id", reqID, "gate", gateAdminAuth,
				"source", clientSource(r))
			writeRefusal(w, http.StatusUnauthorized, "unauthorized", reqID)
			return
		}
		p.metrics.writeTo(w)
	}
}

// adminAuthCacheFlushPath drops every cached authorization. Registered only when
// a token is set, exactly like the counters route.
const adminAuthCacheFlushPath = "/admin/auth-cache/flush"

// adminAuthCacheFlushHandler makes revocation immediate.
//
// It exists because authCacheTTL is a real, if small, security cost: a key
// revoked in the database keeps working for up to 30 seconds. Thirty seconds is
// a defensible default; thirty seconds with no way to shorten it in an incident
// is not. This is the lever, and it is why the cache was acceptable to add.
//
// POST only: it changes server state, and a GET that mutates is a GET something
// will eventually issue by accident -- a link checker, a prefetch, a retry.
//
// The auth shape is deliberately identical to adminMetricsHandler's rather than
// merely similar: pre-auth admission first, constant-time comparison over
// SHA-256 digests of both sides so neither the token's bytes nor its length
// leaks, and the same 401 for a wrong token as for anything else.
func (p *proxy) adminAuthCacheFlushHandler(token string) http.HandlerFunc {
	want := sha256.Sum256([]byte(token))
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.admitPreAuth(w, r) {
			return
		}
		reqID := requestIDFrom(r.Context())
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRefusal(w, http.StatusMethodNotAllowed, "method_not_allowed", reqID)
			return
		}
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(strings.TrimSpace(presented)))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			p.metrics.adminAuthFailures.Add(1)
			p.log.Warn("admin auth-cache flush rejected", "req_id", reqID, "gate", gateAdminAuth,
				"source", clientSource(r))
			writeRefusal(w, http.StatusUnauthorized, "unauthorized", reqID)
			return
		}
		n := p.authCache.flush()
		p.log.Warn("auth cache flushed by operator", "req_id", reqID, "entries_dropped", n)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"flushed\":%d}\n", n)
	}
}

// adminTokenFromEnv reads and validates the admin token at startup.
//
// Returns "" when unset, which means the route is not registered at all -- absent
// configuration cannot accidentally expose the counters. A token that is set but
// too short is fatal, per minAdminTokenLen.
func adminTokenFromEnv(logger *log.Logger) string {
	token := strings.TrimSpace(os.Getenv(adminTokenEnv))
	if token == "" {
		return ""
	}
	if len(token) < minAdminTokenLen {
		logger.Fatalf("%s is set but only %d characters; use at least %d (it guards the "+
			"operational counters on a public listener)", adminTokenEnv, len(token), minAdminTokenLen)
	}
	return token
}

// markSweepRun records a sweep tick and its outcome. Split out so both the
// success and the error paths update liveness: a sweep that is running and
// failing is a different state from one that is not running at all, and
// sweep_last_run_unix alone cannot tell them apart.
func (m *metricSet) markSweepRun(rowsSwept int, failed bool) {
	if m == nil {
		return
	}
	now := time.Now().Unix()
	m.sweepRuns.Add(1)
	m.sweepLastRunUnix.Set(now)
	if failed {
		m.sweepLastErrUnix.Set(now)
		return
	}
	if rowsSwept > 0 {
		m.sweepRowsSwept.Add(int64(rowsSwept))
		m.reservationsStranded.Add(int64(rowsSwept))
	}
}

// atomicBoolToInt renders a flag as the 0/1 a scrape expects.
func atomicBoolToInt(b *atomic.Bool) int64 {
	if b != nil && b.Load() {
		return 1
	}
	return 0
}
