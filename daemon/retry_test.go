package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This file is the before/after evidence for Fix 10: there was no retry or
// backoff anywhere in the request path, so one transient 502 ended the turn and
// the user retyped their prompt. Run against the pre-fix daemon,
// TestRetry_TransientFailureThenSuccessRecovers FAILS -- the first 500 is
// simply the answer.
//
// The tests that matter most here are the ones about NOT retrying. A retry
// layer that is too eager is worse than none: it duplicates streamed output,
// and it burns time and metered money against failures that can never succeed.

// flakyUpstream answers the first failCount requests with the given status and
// body, then streams a normal SSE completion. It counts every request it saw.
func flakyUpstream(t *testing.T, failCount int, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requests.Add(1)
		if int(n) <= failCount {
			w.WriteHeader(status)
			w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func collectTokens(tokens *[]string) func(string) error {
	return func(tok string) error {
		*tokens = append(*tokens, tok)
		return nil
	}
}

// TestRetry_TransientFailureThenSuccessRecovers is the review's repro: a
// transient 500 followed by success must be invisible to the user.
func TestRetry_TransientFailureThenSuccessRecovers(t *testing.T) {
	srv, requests := flakyUpstream(t, 1, http.StatusInternalServerError, "temporary glitch")

	var tokens []string
	err := streamWithRetry(context.Background(), srv.URL, "k", "m", "sys", nil, "hello",
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())
	if err != nil {
		t.Fatalf("want recovery, got: %v", err)
	}
	if got := strings.Join(tokens, ""); got != "recovered" {
		t.Errorf("tokens = %q, want %q", got, "recovered")
	}
	if requests.Load() != 2 {
		t.Errorf("upstream saw %d requests, want 2 (one failure, one success)", requests.Load())
	}
}

// TestRetry_PersistentFailureTerminatesCleanly pins the bound: a failure that
// never clears must stop, with the real error, rather than loop.
func TestRetry_PersistentFailureTerminatesCleanly(t *testing.T) {
	srv, requests := flakyUpstream(t, 1000, http.StatusBadGateway, "still down")

	start := time.Now()
	var tokens []string
	err := streamWithRetry(context.Background(), srv.URL, "k", "m", "sys", nil, "hello",
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want an error after the attempts are exhausted")
	}
	if me := asModelError(err); me.Class != ClassUpstreamUnavailable {
		t.Errorf("Class = %q, want %q — the last real error must survive", me.Class, ClassUpstreamUnavailable)
	}
	if got := requests.Load(); got != maxStreamAttempts {
		t.Errorf("upstream saw %d requests, want exactly %d", got, maxStreamAttempts)
	}
	if elapsed > totalRetryBudget {
		t.Errorf("took %s, want under the %s budget", elapsed, totalRetryBudget)
	}
	if len(tokens) != 0 {
		t.Errorf("tokens = %v, want none", tokens)
	}
}

// TestRetry_NonRetryableClassesAreNotRetried is the money-and-time guarantee.
// Each of these can never succeed on a second attempt, so the upstream must see
// exactly one request.
func TestRetry_NonRetryableClassesAreNotRetried(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantClass ModelErrorClass
	}{
		{"auth", http.StatusUnauthorized, `{"error":{"message":"Invalid API key"}}`, ClassAuth},
		{"quota exhausted", http.StatusPaymentRequired, `{"error":{"message":"Insufficient credit"}}`, ClassQuotaExceeded},
		{"proxy quota (429!)", http.StatusTooManyRequests, `{"error":"quota_exceeded"}`, ClassQuotaExceeded},
		{"context too large", http.StatusBadRequest, `{"error":{"message":"maximum context length exceeded"}}`, ClassContextTooLarge},
		{"privacy refusal", http.StatusNotFound, `{"error":{"message":"No endpoints found matching your data policy (Zero data retention)."}}`, ClassPrivacyRefused},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := flakyUpstream(t, 1000, tc.status, tc.body)

			var tokens []string
			err := streamWithRetry(context.Background(), srv.URL, "k", "m", "sys", nil, "hello",
				ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())
			if err == nil {
				t.Fatal("want an error")
			}
			if me := asModelError(err); me.Class != tc.wantClass {
				t.Errorf("Class = %q, want %q", me.Class, tc.wantClass)
			}
			if got := requests.Load(); got != 1 {
				t.Errorf("upstream saw %d requests, want exactly 1 — %s must not be retried", got, tc.wantClass)
			}
		})
	}
}

// TestRetry_NeverRetriesOnceTokensHaveStreamed is the idempotency boundary. The
// upstream streams real content and then dies mid-stream; retrying would re-emit
// text the user has already read, spliced onto what they got the first time.
func TestRetry_NeverRetriesOnceTokensHaveStreamed(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n")
		w.(http.Flusher).Flush()
		// Die without [DONE]: a truncated stream, the shape a dropped upstream
		// connection produces.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer srv.Close()

	var tokens []string
	_ = streamWithRetry(context.Background(), srv.URL, "k", "m", "sys", nil, "hello",
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())

	if got := requests.Load(); got != 1 {
		t.Errorf("upstream saw %d requests, want exactly 1 — a retry after streaming would duplicate output", got)
	}
	if joined := strings.Join(tokens, ""); strings.Count(joined, "partial answer") > 1 {
		t.Errorf("DUPLICATED OUTPUT: tokens = %q", joined)
	}
}

// TestRetry_HonoursRetryAfter confirms an upstream's own requested delay is
// respected rather than ignored in favour of our own backoff.
func TestRetry_HonoursRetryAfter(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	start := time.Now()
	var tokens []string
	if err := streamWithRetry(context.Background(), srv.URL, "k", "m", "sys", nil, "hello",
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger()); err != nil {
		t.Fatalf("want recovery, got: %v", err)
	}
	// Our own first backoff is well under a second; only Retry-After explains a
	// wait of two.
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Errorf("waited %s, want at least the 2s the upstream asked for", elapsed)
	}
}

// TestRetry_CancelledContextAbortsImmediately confirms a cancelled request does
// not sit out a pending backoff.
func TestRetry_CancelledContextAbortsImmediately(t *testing.T) {
	srv, _ := flakyUpstream(t, 1000, http.StatusBadGateway, "down")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	var tokens []string
	err := streamWithRetry(ctx, srv.URL, "k", "m", "sys", nil, "hello",
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())
	if err == nil {
		t.Fatal("want an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; a cancelled context must not wait out the backoff", elapsed)
	}
}

// TestBackoffFor_IsBoundedAndJittered pins the backoff shape: never zero, never
// above the cap, and genuinely varying so a fleet of clients does not
// synchronise into a thundering herd.
func TestBackoffFor_IsBoundedAndJittered(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 200 {
		for attempt := 1; attempt <= maxStreamAttempts; attempt++ {
			d := backoffFor(attempt, 0)
			if d <= 0 {
				t.Fatalf("backoffFor(%d) = %v, want a positive delay", attempt, d)
			}
			if d > maxRetryBackoff {
				t.Fatalf("backoffFor(%d) = %v, want at most %v", attempt, d, maxRetryBackoff)
			}
			seen[d] = true
		}
	}
	if len(seen) < 10 {
		t.Errorf("only %d distinct delays across 600 draws — backoff does not look jittered", len(seen))
	}
}

// TestBackoffFor_CapsAnAbsurdRetryAfter confirms a provider naming a huge
// Retry-After cannot hold the request open for it.
func TestBackoffFor_CapsAnAbsurdRetryAfter(t *testing.T) {
	if d := backoffFor(1, time.Hour); d > maxRetryBackoff {
		t.Errorf("backoffFor(1, 1h) = %v, want it capped at %v", d, maxRetryBackoff)
	}
}
