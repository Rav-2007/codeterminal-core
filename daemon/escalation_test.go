package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mochiii/protocol"
)

// withTiers gives a test server the tiers an escalation can name.
func withTiers(s *Server, tiers map[string]ModelTier) {
	s.cfg.DefaultTier = "primary"
	s.cfg.Tiers = tiers
}

// THE TIER A TASK MAY MOVE TO IS CHECKED, and refused with a reason: none of
// that name, the model already in use, a window too small to hold a segment.
func TestTheEscalationTargetIsChecked(t *testing.T) {
	s := loopServer(t, "http://127.0.0.1:1", MCPConfig{Enabled: true})
	withTiers(s, map[string]ModelTier{
		"primary": {Slug: "m-primary", Active: true},
		"strong":  {Slug: "m-strong", Active: true},
		"asleep":  {Slug: "m-asleep", Active: false},
		"small":   {Slug: "m-small", Active: true, ContextWindow: 32000},
	})
	for tier, why := range map[string]string{
		"nosuch": "no active tier", "asleep": "no active tier", "small": "context window of 32000",
	} {
		if _, _, got := s.escalationTarget(tier, "m-primary"); !strings.Contains(got, why) {
			t.Errorf("%s: %q, want %q", tier, got, why)
		}
	}
	if _, _, got := s.escalationTarget("strong", "m-strong"); !strings.Contains(got, "already runs on") {
		t.Errorf("the model in use: %q", got)
	}
	if model, _, why := s.escalationTarget("strong", "m-primary"); model != "m-strong" || why != "" {
		t.Errorf("strong: %q, %q", model, why)
	}
}

// sentModel is the model a captured request asked for, and whether it offered
// any tools.
func sentModel(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var req struct {
		Model string            `json:"model"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Model, len(req.Tools) > 0
}

// A FAILURE THAT KEEPS COMING BACK MOVES THE TASK TO THE NAMED TIER, and the
// judge asks the model -- once, with no tools -- why it keeps failing, keeping
// the answer with the failure. Without a tier named, nothing moves.
//
// Neuter checks: drop the escalate call in control's second ladder step; drop
// the reflect call in runLongTask.
func TestTheLadderMovesToTheNamedTierAndAsksWhy(t *testing.T) {
	_, dir := threeTests(t)
	base, _, bodies := agentUpstream(t,
		toolCallSSE("t1", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("e1", "builtin__propose_edit", `{"path":"m.go","search":"func C()","replace":"// one\nfunc C()"}`),
		toolCallSSE("t2", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("e2", "builtin__propose_edit", `{"path":"m.go","search":"// one","replace":"// two"}`),
		toolCallSSE("t3", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		textSSE("A returns 0 and every attempt edited comments; fix A next."),
		toolCallSSE("x", "builtin__finish_task", `{"status":"blocked","summary":"Handing over."}`),
	)
	cfg := taskPolicies()
	cfg.Builtin.Tools["sandbox_exec"] = PolicyAllow
	cfg.Correction = MCPCorrectionConfig{Mode: correctionOn, Attempts: 2, EscalateTo: "strong"}
	s := loopServer(t, base, cfg)
	s.workspace = dir
	withTiers(s, map[string]ModelTier{"primary": {Slug: "test-model", Active: true}, "strong": {Slug: "m-strong", Active: true}})
	run, _, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 7 || run.escalated != "strong" || run.reflections != 1 {
		t.Fatalf("%d calls, escalated %q, %d reflection(s)", len(*bodies), run.escalated, run.reflections)
	}
	for i, want := range []string{"test-model", "test-model", "test-model", "test-model", "test-model", "m-strong", "m-strong"} {
		if got, _ := sentModel(t, (*bodies)[i]); got != want {
			t.Errorf("call %d went to %q, want %q", i+1, got, want)
		}
	}
	if _, tools := sentModel(t, (*bodies)[5]); tools {
		t.Error("the reflection call offered tools")
	}
	frames := run.ledger.Correction.Frames
	if got := frames[len(frames)-1].Reflection; got != "A returns 0 and every attempt edited comments; fix A next." {
		t.Errorf("the frame's reflection = %q", got)
	}
	if opening := segmentOpening(t, (*bodies)[6]); !strings.Contains(opening, "You said then: A returns 0") {
		t.Errorf("the next segment does not carry the reflection:\n%s", opening)
	}
}

// failingFor answers 503 to every request for one model and streams the given
// replies, in order, to requests for any other.
func failingFor(t *testing.T, failing string, replies ...[]string) (string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var models []string
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		if req.Model == failing {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("overloaded"))
			return
		}
		mu.Lock()
		reply := []string{`data: {"choices":[{"delta":{"content":"unscripted"},"finish_reason":"stop"}]}`, `data: [DONE]`}
		if n < len(replies) {
			reply = replies[n]
		}
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range reply {
			_, _ = fmt.Fprint(w, line+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &models
}

// A PROVIDER THAT KEEPS FAILING MOVES THE TASK TO THE NAMED TIER rather than
// ending it -- once its retries are spent. With no tier named it fails, saved,
// as before.
//
// Neuter check: drop the escalation from the provider-failure path.
func TestAProviderFailureMovesTheTaskOn(t *testing.T) {
	for _, named := range []string{"strong", ""} {
		base, models := failingFor(t, "test-model",
			toolCallSSE("x", "builtin__finish_task", `{"status":"blocked","summary":"Moved over."}`))
		cfg := taskPolicies()
		cfg.Correction = MCPCorrectionConfig{Mode: correctionOn, EscalateTo: named}
		s := loopServer(t, base, cfg)
		withTiers(s, map[string]ModelTier{"primary": {Slug: "test-model", Active: true}, "strong": {Slug: "m-strong", Active: true}})
		run, _, runIt := directTaskRun(t, s, modeFix)
		_, _ = runIt(context.Background())
		want := protocol.TaskStateBlocked
		if named == "" {
			want = protocol.TaskStateFailed
		}
		if run.ledger.State != want {
			t.Errorf("escalate_to %q: state %q (%s), want %q; models asked: %v", named, run.ledger.State, run.ledger.Detail, want, *models)
		}
	}
}

// NO EXTRA CALL WHEN THE MODEL ALREADY SAID WHY: its own words after the
// failure are the reflection, and asking again would only cost a call.
func TestNoReflectionCallWhenTheModelSaidWhy(t *testing.T) {
	s := loopServer(t, "http://127.0.0.1:1", MCPConfig{Enabled: true}) // any call would fail the assertion below
	run := &taskRun{ledger: &taskLedger{Mode: modeFix, State: protocol.TaskStateRunning,
		Correction: &taskCorrection{Frames: []errorFrame{{Check: 3, Reflection: "A returns 0."}}}},
		judge: newCorrectionJudge(correctionOn), budget: resolveTaskBudget(nil, nil)}
	run.judge.wantReflection = true
	run.start, run.deadline = time.Now(), time.Now().Add(time.Hour) // room for the call: only the guard stops it
	s.reflect(context.Background(), run, "S", nil, 2, "m", providerRouting{})
	if run.reflections != 0 || run.judge.wantReflection {
		t.Errorf("reflections %d, still wanted %v", run.reflections, run.judge.wantReflection)
	}
}

// ONLY A FAILURE ANOTHER MODEL MIGHT NOT HAVE MOVES THE TASK: a rejected key or
// a spent allowance would meet the stronger model too, on the same key.
//
// Neuter check: let every provider error escalate.
func TestOnlyProviderTroubleEscalates(t *testing.T) {
	for class, want := range map[ModelErrorClass]bool{
		ClassRateLimited: true, ClassUpstreamUnavailable: true, ClassContextTooLarge: true, ClassUnavailableTier: true,
		ClassAuth: false, ClassQuotaExceeded: false, ClassSpendLimit: false, ClassPrivacyRefused: false, ClassUnknown: false,
	} {
		if got := providerTrouble(&ModelError{Class: class}); got != want {
			t.Errorf("%s: %v, want %v", class, got, want)
		}
	}
}

// THE STATUS SAYS WHAT THE JUDGE DID, in mode "on" -- and nothing otherwise.
//
// Neuter check: drop the correcting condition from taskRun.status.
func TestTheStatusSaysWhatTheJudgeDid(t *testing.T) {
	run := &taskRun{ledger: &taskLedger{Mode: modeFix, State: protocol.TaskStateRunning}, judge: newCorrectionJudge(correctionOn),
		restores: 1, rechecks: 2, escalated: "strong", budget: resolveTaskBudget(nil, nil)}
	if got := run.status(3).Judge; got != "1 restore · 2 re-checks · moved to strong" {
		t.Errorf("on: %q", got)
	}
	run.judge.mode = correctionShadow
	if got := run.status(3).Judge; got != "" {
		t.Errorf("shadow: %q", got)
	}
}

// WHEN EVERY MODEL FAILS, THE TASK ENDS. The stronger tier is tried once, not
// again and again: a provider down for every model fails the task, saved,
// rather than holding it until its time runs out.
//
// Neuter check: let escalate move the task more than once.
func TestEveryModelFailingEndsTheTask(t *testing.T) {
	var asked atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	cfg := taskPolicies()
	cfg.Correction = MCPCorrectionConfig{Mode: correctionOn, EscalateTo: "strong"}
	s := loopServer(t, srv.URL, cfg)
	withTiers(s, map[string]ModelTier{"primary": {Slug: "test-model", Active: true}, "strong": {Slug: "m-strong", Active: true}})
	run, _, runIt := directTaskRun(t, s, modeFix)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = runIt(ctx)
	if run.ledger.State != protocol.TaskStateFailed || run.escalated != "strong" {
		t.Errorf("state %q (%s), escalated %q after %d requests; want failed, once moved to strong",
			run.ledger.State, run.ledger.Detail, run.escalated, asked.Load())
	}
}
