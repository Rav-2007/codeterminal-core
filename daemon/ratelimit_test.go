package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// A RATE LIMIT IS WAITED OUT (retry.go, 2026-10-25). MEASURED on the owner's
// NVIDIA key: an agent task's 9th call in 24 seconds got 429, both quick retries
// got 429 too, and a task the user had watched stopped as "the model provider
// failed" -- after which they replaced a key that had never been the problem.

const tooManyRequests = `{"status":429,"title":"Too Many Requests"}`

// withFastRateLimits shrinks the rate-limit waits so a test can sit through
// five of them.
func withFastRateLimits(t *testing.T) {
	t.Helper()
	prevBase, prevBudget := baseRateLimitBackoff, rateLimitBudget
	baseRateLimitBackoff, rateLimitBudget = 20*time.Millisecond, 10*time.Second
	t.Cleanup(func() { baseRateLimitBackoff, rateLimitBudget = prevBase, prevBudget })
}

func TestARateLimitIsWaitedOutNotHurried(t *testing.T) {
	withFastRateLimits(t)
	srv, requests := flakyUpstream(t, 4, http.StatusTooManyRequests, tooManyRequests)
	var waits []time.Duration
	ctx := withRetryNotice(context.Background(), func(d time.Duration) { waits = append(waits, d) })
	var tokens []string
	if _, err := streamWithRetry(ctx, srv.URL, "k", "m", buildChatMessages("sys", nil, "hello"), nil,
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger()); err != nil {
		t.Fatalf("four 429s and then an answer: want the answer, got %v", err)
	}
	if got := requests.Load(); got != maxRateLimitAttempts {
		t.Errorf("%d requests, want %d: four refused and the last one answered", got, maxRateLimitAttempts)
	}
	if len(waits) != 4 {
		t.Fatalf("%d waits announced, want one before each of the 4 retries", len(waits))
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] < waits[i-1] {
			t.Errorf("wait %d (%s) is shorter than wait %d (%s): the waits must grow", i+1, waits[i], i, waits[i-1])
		}
	}
}

// A dropped connection keeps the three quick tries, and is not announced as a
// rate limit.
func TestOnlyARateLimitGetsTheLongerWait(t *testing.T) {
	withFastRateLimits(t)
	srv, requests := flakyUpstream(t, 10, http.StatusBadGateway, "down")
	waits := 0
	ctx := withRetryNotice(context.Background(), func(time.Duration) { waits++ })
	var tokens []string
	if _, err := streamWithRetry(ctx, srv.URL, "k", "m", buildChatMessages("sys", nil, "hello"), nil,
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger()); err == nil {
		t.Fatal("ten 502s answered nothing, want an error")
	}
	if got := requests.Load(); got != maxStreamAttempts {
		t.Errorf("%d requests for a 502, want %d", got, maxStreamAttempts)
	}
	if waits != 0 {
		t.Errorf("a 502 announced %d rate-limit wait(s)", waits)
	}
}

func TestRateLimitWaitsDoubleUpToTheCapAndHonourRetryAfter(t *testing.T) {
	for attempt := 1; attempt <= maxRateLimitAttempts; attempt++ {
		exp := min(baseRateLimitBackoff<<(attempt-1), maxRateLimitBackoff)
		for i := 0; i < 50; i++ {
			if d := rateLimitBackoffFor(attempt, 0); d < exp/2 || d > exp {
				t.Fatalf("attempt %d waits %s, want between %s and %s", attempt, d, exp/2, exp)
			}
		}
	}
	if d := rateLimitBackoffFor(1, 12*time.Second); d != 12*time.Second {
		t.Errorf("Retry-After: 12 on the first wait gave %s, want the 12s the provider asked for", d)
	}
	if d := rateLimitBackoffFor(1, time.Hour); d != maxRateLimitBackoff {
		t.Errorf("Retry-After: an hour gave %s, want the %s cap", d, maxRateLimitBackoff)
	}
}

// A rate limit that never lifts still ends, inside its budget.
func TestARateLimitNeverWaitsPastItsBudget(t *testing.T) {
	prevBase, prevBudget := baseRateLimitBackoff, rateLimitBudget
	baseRateLimitBackoff, rateLimitBudget = 200*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { baseRateLimitBackoff, rateLimitBudget = prevBase, prevBudget })

	srv, requests := flakyUpstream(t, 100, http.StatusTooManyRequests, tooManyRequests)
	var tokens []string
	_, err := streamWithRetry(context.Background(), srv.URL, "k", "m", buildChatMessages("sys", nil, "hello"), nil,
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())
	var me *ModelError
	if !errors.As(err, &me) || me.Class != ClassRateLimited {
		t.Fatalf("err = %v, want the rate limit itself", err)
	}
	if got := requests.Load(); got >= maxRateLimitAttempts {
		t.Errorf("%d requests: the budget did not stop the tries", got)
	}
}

// A task the provider stopped says it was a rate limit, so nobody replaces a
// key that was never the problem.
func TestATaskStoppedByARateLimitSaysSo(t *testing.T) {
	withFastRateLimits(t)
	first := append([]string{`data: {"choices":[{"delta":{"content":"Let me look. "}}]}`},
		toolCallSSE("c1", "builtin__list_directory", `{"path":"."}`)...)
	script := [][]string{first}
	for i := 0; i < maxRateLimitAttempts; i++ {
		script = append(script, errorOnlySSE(429, "Too Many Requests"))
	}
	base, _, _ := agentUpstream(t, script...)
	// Patience off: this is the stop that comes when it has run out (patience.go).
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Budget:  MCPBudgetConfig{ProviderPatienceSeconds: -1},
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": PolicyAllow}},
	})
	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Incomplete == nil || !strings.Contains(res.Incomplete.Detail, "kept rate-limiting requests") {
		t.Fatalf("incomplete = %+v, want the stop notice to name the rate limit", res.Incomplete)
	}
}

// The user of an agent turn is told about each wait, over the real socket, in
// words that name no provider (the Gate-7 line, modelerror.go).
func TestAWaitingTurnTellsItsUser(t *testing.T) {
	withFastRateLimits(t)
	base, _, _ := agentUpstream(t, errorOnlySSE(429, "slow down"), errorOnlySSE(429, "slow down"), textSSE("the answer"))
	sockAddr, _, _ := agentSocketServer(t, base, MCPConfig{Enabled: true})
	// A real question, from a client that can answer approvals: a greeting, or a
	// client without CapToolApproval, gets the single-turn path (server.go).
	_, dec, conn := agentClient(t, sockAddr, "what is in this folder?", []string{protocol.CapToolApproval})
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var waits []protocol.ToolActivity
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("the turn ended without Done: %v", err)
		}
		if a := tok.ToolActivity; a != nil && a.Tool == protocol.ToolWaiting {
			waits = append(waits, *a)
		}
		if tok.Done {
			break
		}
	}
	if len(waits) != 2 {
		t.Fatalf("%d wait notice(s) for two rate limits, want 2: %+v", len(waits), waits)
	}
	for _, w := range waits {
		if w.Phase != protocol.ToolPhaseStep || w.CallID != "" || !strings.Contains(w.Detail, "rate-limiting") {
			t.Errorf("wait notice %+v: want a step line, with no call id, saying it is a rate limit", w)
		}
		if w.DurationMS <= 0 {
			t.Errorf("wait notice %+v carries no length: a client cannot count it down", w)
		}
		if strings.Contains(w.Detail, "127.0.0.1") || strings.Contains(w.Detail, "http") {
			t.Errorf("wait notice names the upstream: %q", w.Detail)
		}
	}
}
