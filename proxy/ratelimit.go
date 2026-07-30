package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Admission control for the proxy's HTTP surface. Before this existed the proxy
// had a body-size cap and timeouts but NO rate or concurrency limiting of any
// kind: a single authenticated key could open unlimited simultaneous requests,
// each holding a goroutine plus an upstream connection for up to upstreamTimeout
// (5 minutes) and each costing real money, and every unauthenticated attempt
// still cost a Supabase round trip. Measured against the real binary during the
// endpoint-security pass: 40 simultaneous requests on one key produced 40
// concurrent upstream calls, zero throttled, plus 40 Supabase auth lookups.
//
// Deliberately implemented with the standard library only. proxy/go.mod has no
// third-party requires, which is a real supply-chain property for the one
// internet-facing component of this product, and it is worth more than the small
// amount of code below.
//
// Two independent controls, because they stop different things:
//
//	RATE (token bucket)  — bounds requests over TIME; stops sustained hammering.
//	IN-FLIGHT (semaphore) — bounds requests at ONCE; stops a burst of long-lived
//	                        streaming calls from pinning goroutines and upstream
//	                        connections, which a rate limit alone permits (a
//	                        5-minute stream started slowly still accumulates).
//
// HONEST LIMITATION, stated not buried: all of this is IN-MEMORY and therefore
// PER-INSTANCE. If the proxy is scaled to N replicas, the effective ceiling is
// N times these values. Making it exact across replicas needs shared state (a
// Postgres RPC alongside reserve_usage, or Redis), which costs a round trip on
// every request against a TTFT budget that is already ~1.45s. Per-instance
// limiting still converts "unbounded" into "bounded and predictable", which is
// the property that was missing.
const (
	// keyRatePerSecond / keyBurst bound one authenticated key over time. Sized
	// far above real single-user usage (a developer prompting from an IDE is
	// well under 1 req/s even when auto-applying) and far below what a flood
	// needs to be damaging.
	keyRatePerSecond = 2.0
	keyBurst         = 20.0

	// keyTokenRatePerSecond / keyTokenBurst are the SECOND layer: they bound one
	// key by TOKEN VOLUME, which the request-count limiter above cannot see at
	// all. Two requests per second is a trivial request rate and an unbounded
	// spend rate -- the bill is denominated in tokens, so this is the layer that
	// actually tracks money.
	//
	// 1000 tokens/s is 60k/min, far above real IDE use (a grounded turn is a few
	// thousand tokens) and far below what a scripted drain needs. The burst allows
	// ~2 minutes of accumulated headroom so a legitimate burst of long completions
	// is never throttled.
	keyTokenRatePerSecond = 1000.0
	keyTokenBurst         = 120000.0

	// maxInFlightPerKey bounds simultaneous in-progress requests for one key.
	// A human driving CLI + TUI + IDE at once is a handful; 8 leaves headroom
	// while stopping one key from parking dozens of 5-minute upstream calls.
	maxInFlightPerKey = 8

	// maxInFlightTotal is the process-wide ceiling, mirroring the daemon's own
	// Gate-5 connection ceiling (daemon/server_limits.go). Before this, the
	// local-only unix socket had a stricter bound than the internet-facing proxy.
	maxInFlightTotal = 128

	// Pre-auth limits protect the UNAUTHENTICATED surface: /health and, more
	// importantly, failed auth attempts, each of which costs a Supabase round
	// trip and so is an amplification vector against a third party.
	//
	// Per-source first, with a global backstop: the per-source bucket is keyed on
	// the client IP, which behind Railway's edge comes from X-Forwarded-For and
	// is therefore SPOOFABLE. The global bucket is what makes that unable to
	// evade the bound -- an attacker rotating forged XFF values gets fresh
	// per-source buckets but still hits the shared ceiling.
	preAuthRatePerSourcePerSecond = 5.0
	preAuthBurstPerSource         = 20.0
	preAuthRateGlobalPerSecond    = 50.0
	preAuthBurstGlobal            = 200.0

	// bucketIdleTTL is how long an unused bucket is kept before being swept.
	// Without this the bucket map grows once per distinct key/IP forever, which
	// would turn the limiter itself into a memory-exhaustion vector -- the exact
	// class of bug it exists to prevent.
	bucketIdleTTL   = 10 * time.Minute
	bucketSweepE    = 2 * time.Minute
	retryAfterValue = "1"
)

// tokenBucket is a classic lazy token bucket: tokens accrue at `rate` per second
// up to `burst`, and each admitted request spends one. Lazy refill (compute on
// access from elapsed time) means no timer goroutine per bucket.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter holds one token bucket per string key (an api_keys.id, a client
// IP, or the fixed global key). Safe for concurrent use.
type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	rate      float64
	burst     float64
	lastSweep time.Time
	now       func() time.Time // injectable for deterministic tests
}

func newRateLimiter(rate, burst float64) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    rate,
		burst:   burst,
		now:     time.Now,
	}
}

// allow spends one token for key, reporting whether the request may proceed.
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		// A brand-new key starts full, so a first request is never throttled.
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	l.refillLocked(b, now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// available reports whether key's bucket has anything left to spend, WITHOUT
// spending it. It is the admission half of post-hoc token limiting: at admission
// time the request's real token cost is unknowable (it only exists once the
// response is complete), so the gate can only ask whether the previous requests
// have already exhausted the budget.
//
// Deliberately a >= 1 test rather than > 0, matching allow's own threshold, so a
// bucket in DEBT (see charge) keeps refusing until it refills past one whole
// token.
func (l *rateLimiter) available(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		return true // never seen: a full bucket by construction
	}
	l.refillLocked(b, now)
	return b.tokens >= 1
}

// charge spends n tokens against key AFTER the fact, and is allowed to drive the
// bucket NEGATIVE.
//
// Debt is the point, not a defect. Real usage is only known once a response
// completes, so a single large completion can legitimately exceed what was in the
// bucket when it was admitted. Clamping at zero would make that overrun free;
// letting the bucket go negative makes the NEXT request wait for the refill that
// pays it off, which is what converts a per-request overrun into a bounded
// sustained rate.
//
// Debt is floored at one full burst so a single pathological request cannot lock
// a key out for an unbounded stretch -- the punishment stays proportional.
func (l *rateLimiter) charge(key string, n float64) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	l.refillLocked(b, now)

	b.tokens -= n
	if b.tokens < -l.burst {
		b.tokens = -l.burst
	}
}

// refillLocked accrues elapsed-time tokens into b, capped at burst. Extracted
// from allow so available and charge apply the identical refill rule -- three
// copies of this arithmetic would be three chances to drift.
func (l *rateLimiter) refillLocked(b *tokenBucket, now time.Time) {
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
}

// sweepLocked drops buckets untouched for bucketIdleTTL, bounding memory. A
// swept bucket is equivalent to a fresh one (both start full), so dropping it
// can never be stricter than keeping it -- only more forgiving, which is the
// safe direction for a false positive.
//
// This holds for a bucket in DEBT too (see charge), and sweeping does not forgive
// anything the refill would not have: at keyTokenRatePerSecond, bucketIdleTTL of
// elapsed time accrues far more than keyTokenBurst, so any debt within the floor
// charge enforces has already been paid off by the time a bucket is old enough to
// be swept.
func (l *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < bucketSweepE {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > bucketIdleTTL {
			delete(l.buckets, k)
		}
	}
}

// inFlightLimiter is a counting semaphore with both a per-key and a process-wide
// ceiling. Non-blocking: over the cap it refuses immediately rather than
// queueing, so an overloaded proxy sheds load instead of growing an invisible
// backlog of requests whose clients have already given up.
type inFlightLimiter struct {
	mu        sync.Mutex
	perKey    map[string]int
	total     int
	maxPerKey int
	maxTotal  int
}

func newInFlightLimiter(maxPerKey, maxTotal int) *inFlightLimiter {
	return &inFlightLimiter{
		perKey:    make(map[string]int),
		maxPerKey: maxPerKey,
		maxTotal:  maxTotal,
	}
}

// acquire takes a slot for key. The returned release MUST be called (defer) when
// the request finishes; ok is false when either ceiling is already reached.
func (l *inFlightLimiter) acquire(key string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.total >= l.maxTotal || l.perKey[key] >= l.maxPerKey {
		return func() {}, false
	}
	l.total++
	l.perKey[key]++

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.total--
			if n := l.perKey[key] - 1; n <= 0 {
				delete(l.perKey, key) // don't retain a zero entry per key seen
			} else {
				l.perKey[key] = n
			}
		})
	}, true
}

// clientSource identifies the caller for PRE-AUTH limiting. Behind Railway the
// TCP peer is the edge, so X-Forwarded-For carries the real client -- but that
// header is caller-supplied and therefore spoofable, which is precisely why the
// global pre-auth bucket exists alongside this one. Only the FIRST XFF entry is
// used (the original client per the convention); the rest are hops.
func clientSource(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// refusalBody is the one shape every JSON refusal this proxy writes takes.
//
// RequestID is what makes a refusal reportable: the caller can quote it and it
// resolves to exactly one request's log trail (see reqid.go). It is safe to echo
// because it is either minted here or validated to lowercase hex -- see
// validRequestID, which explains why that charset specifically.
//
// Scope is set only by the 429s, naming which bucket refused.
type refusalBody struct {
	Error     string `json:"error"`
	Scope     string `json:"scope,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// writeRefusal writes a refusal as JSON, carrying the request id.
//
// Marshalled rather than concatenated: the bodies it replaces were built by
// string concatenation, which was fine while every value was a compile-time
// constant, and stops being fine the moment one of them comes off the wire.
func writeRefusal(w http.ResponseWriter, code int, errCode, reqID string) {
	writeRefusalBody(w, code, refusalBody{Error: errCode, RequestID: reqID})
}

func writeRefusalBody(w http.ResponseWriter, code int, body refusalBody) {
	// Cannot fail: refusalBody is three plain strings.
	encoded, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(encoded)
}

// tooManyRequests writes the shared 429. Always JSON with a Retry-After, never a
// silent drop: a caller must be able to tell throttling apart from a failure.
func tooManyRequests(w http.ResponseWriter, reason, reqID string) {
	w.Header().Set("Retry-After", retryAfterValue)
	writeRefusalBody(w, http.StatusTooManyRequests, refusalBody{
		Error:     "rate_limited",
		Scope:     reason,
		RequestID: reqID,
	})
}
