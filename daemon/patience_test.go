package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mochiii/protocol"
)

// A BUSY PROVIDER IS WAITED OUT INSIDE THE TURN (patience.go): a rate limit
// that outlasts streamWithRetry's own tries, after the turn has done work, is a
// wait and the same step again -- not the end of the turn.
func TestARateLimitAfterWorkIsWaitedOutAndTheTurnGoesOn(t *testing.T) {
	withFastRateLimits(t)
	prev := providerWaitBase
	providerWaitBase = 50 * time.Millisecond
	t.Cleanup(func() { providerWaitBase = prev })

	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var lines []string
		switch i := n.Add(1); {
		case i == 1:
			lines = toolCallSSE("c1", "builtin__list_directory", `{"path":"."}`)
		case i <= 1+maxRateLimitAttempts:
			http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
			return
		default:
			lines = textSSE("done")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range lines {
			_, _ = w.Write([]byte(line + "\n\n"))
		}
	}))
	t.Cleanup(srv.Close)
	s := loopServer(t, srv.URL, MCPConfig{Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": PolicyAllow}}})
	sink := &proposalSink{}
	registry, _ := s.buildRegistry(context.Background(), s.logger, sink, "auto")
	t.Cleanup(func() { _ = registry.Close() })

	var waits []protocol.ToolActivity
	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "what is here?"}}, providerRouting{}, nil,
		func(string) error { return nil }, func(a protocol.ToolActivity) {
			if a.Tool == protocol.ToolWaiting {
				waits = append(waits, a)
			}
		}, nil, nil, func(protocol.Degradation) {}, nil, nil)
	if err != nil || res.Incomplete != nil || res.FinalText != "done" {
		t.Fatalf("the turn did not go on after the wait: err=%v incomplete=%+v final=%q", err, res.Incomplete, res.FinalText)
	}
	if len(waits) != 1 || waits[0].Tool != protocol.ToolWaiting || !strings.Contains(waits[0].Detail, "rate-limiting") {
		t.Errorf("the wait was not shown as one: %+v", waits)
	}
	if res.Iterations != 2 {
		t.Errorf("the refused call was counted as a step: %d iterations, want 2", res.Iterations)
	}
}

// Patience is bounded: switched off, the same failure ends the turn as before
// -- with its work kept -- and a first request that fails is not waited on.
func TestPatienceIsBoundedAndOnlyForWorkDone(t *testing.T) {
	prev := providerWaitBase // the real minute: the deadline case below is about its length
	providerWaitBase = time.Minute
	t.Cleanup(func() { providerWaitBase = prev })
	s := &Server{cfg: &Config{MCP: MCPConfig{Budget: MCPBudgetConfig{ProviderPatienceSeconds: -1}}}, logger: quietLogger()}
	turn := &agentTurn{calls: 3, toolNames: []string{"builtin__read_file"}}
	bud := &budget{deadline: time.Now().Add(time.Hour)}
	limited := &ModelError{Class: ClassRateLimited}
	if s.waitOutProvider(context.Background(), turn, bud, limited, "", nil) {
		t.Error("waited with patience switched off")
	}
	s.cfg.MCP.Budget.ProviderPatienceSeconds = 0 // the default: five minutes
	if s.waitOutProvider(context.Background(), &agentTurn{calls: 1}, bud, limited, "", nil) {
		t.Error("waited on a turn that had done no work")
	}
	if s.waitOutProvider(context.Background(), turn, bud, limited, "Let me look", nil) {
		t.Error("waited after the step had shown the user words")
	}
	if s.waitOutProvider(context.Background(), turn, bud, &ModelError{Class: ClassQuotaExceeded}, "", nil) {
		t.Error("waited on a failure no wait can cure")
	}
	if s.waitOutProvider(context.Background(), turn, &budget{deadline: time.Now().Add(40 * time.Second)}, limited, "", nil) {
		t.Error("waited past the turn's own deadline")
	}
}

// However short the wait, a turn waits at most maxProviderWaits times: a
// provider that never comes back still ends the turn, with its work kept.
func TestAProviderThatNeverComesBackStillEndsTheTurn(t *testing.T) {
	withFastRateLimits(t)
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, line := range toolCallSSE("c1", "builtin__list_directory", `{"path":"."}`) {
				_, _ = w.Write([]byte(line + "\n\n"))
			}
			return
		}
		http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	s := loopServer(t, srv.URL, MCPConfig{Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": PolicyAllow}}})
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "auto")
	t.Cleanup(func() { _ = registry.Close() })
	waits := 0
	// Bounded here too, so that without the cap this fails rather than hangs.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.runAgentLoop(ctx, time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "what is here?"}}, providerRouting{}, nil,
		func(string) error { return nil }, func(a protocol.ToolActivity) {
			if a.Tool == protocol.ToolWaiting && strings.Contains(a.Detail, "still rate-limiting") {
				waits++
			}
		}, nil, nil, func(protocol.Degradation) {}, nil, nil)
	if err != nil || res.Incomplete == nil || res.Incomplete.Reason != protocol.IncompleteProviderError {
		t.Fatalf("a provider that never comes back did not end the turn with its work kept: err=%v %+v", err, res.Incomplete)
	}
	if waits != maxProviderWaits {
		t.Errorf("waited %d times, want %d", waits, maxProviderWaits)
	}
}
