package main

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Per-stage request timing.
//
// The proxy already emitted one whole-request latency_ms (accessLog), which can
// tell you that a request took 1.4 s and nothing whatsoever about where the time
// went. That was enough to notice a regression and never enough to locate one --
// and it is the specific reason the ~80 ms reserveQuota cost recorded on
// 2026-07-17 was still unexplained four phases later: there was no way to see
// that stage on its own in production.
//
// So each stage that can block a request records its own duration here, and
// accessLog emits them on the same line, under the same req_id, as part of the
// same record. No new log stream, no new correlation problem.
//
// Deliberate property: a stage that did NOT run is ABSENT from the line, never
// zero. A 401 never reaches reserveQuota, and logging `reserve_ms=0` for it
// would read as "the reservation was instant" rather than "there was no
// reservation" -- the exact ambiguity this instrument exists to remove.

// Stage names. Fixed labels, so the log stays greppable and a typo cannot invent
// a new stage silently.
const (
	stageAuth     = "auth_ms"     // authorize(): the Supabase api_keys lookup
	stageReserve  = "reserve_ms"  // reserveQuota(): the reserve_usage RPC
	stageUpstream = "upstream_ms" // client.Do to the provider, until response headers
	stageTTFB     = "ttfb_ms"     // until the first SSE data chunk reaches the client
)

// stageTiming is one recorded stage. A slice of these, rather than a map, keeps
// the emitted order stable (request order), which makes two log lines for the
// same path directly comparable by eye.
type stageTiming struct {
	name string
	dur  time.Duration
}

// stageTimer collects the stages of one request.
//
// The mutex is not ceremonial. A request is mostly one goroutine, but the
// streaming path and the deferred finalizer both run against the same request,
// and this module is tested under -race in CI; an unsynchronised append here
// would be found there rather than in production, but it would be found.
type stageTimer struct {
	mu     sync.Mutex
	stages []stageTiming
}

// record adds a completed stage. Recording the same stage twice appends twice
// rather than overwriting -- a retry that ran authorize() a second time really
// did spend that time, and hiding the second one would under-report the request.
func (st *stageTimer) record(name string, d time.Duration) {
	if st == nil {
		return
	}
	st.mu.Lock()
	st.stages = append(st.stages, stageTiming{name: name, dur: d})
	st.mu.Unlock()
}

// attrs returns the recorded stages as flat slog key/value pairs, in the order
// they were recorded. Milliseconds, matching latency_ms's unit so the parts and
// the whole can be compared without a conversion in the reader's head.
func (st *stageTimer) attrs() []any {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]any, 0, len(st.stages)*2)
	for _, s := range st.stages {
		out = append(out, s.name, s.dur.Milliseconds())
	}
	return out
}

// stageTimerContextKey is the context key for the current request's timer. An
// unexported struct type, so nothing outside this file can plant one.
type stageTimerContextKey struct{}

// stageTimerFrom returns the timer withStageTimer put on ctx, or nil.
//
// A nil return is not an error path: it means the caller is outside a request
// (the background sweep, a test calling a helper directly). Every method on
// *stageTimer is nil-safe precisely so those callers need no guard.
func stageTimerFrom(ctx context.Context) *stageTimer {
	if ctx == nil {
		return nil
	}
	st, _ := ctx.Value(stageTimerContextKey{}).(*stageTimer)
	return st
}

// timeStage runs fn, records how long it took under name, and returns fn's
// duration so a caller that also wants the number does not have to time it
// twice. Safe when ctx carries no timer.
func timeStage(ctx context.Context, name string, fn func()) time.Duration {
	start := time.Now()
	fn()
	d := time.Since(start)
	stageTimerFrom(ctx).record(name, d)
	return d
}

// withStageTimer attaches a fresh timer to every request.
//
// Installed alongside withRequestID and, like it, outside recoverPanics: a
// request that panics mid-stream is one whose stage timings you most want, and
// an instrument that only covers the paths that already work is an instrument
// you cannot ask about.
func withStageTimer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &stageTimer{}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), stageTimerContextKey{}, st)))
	})
}
