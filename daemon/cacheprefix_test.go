package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// THE START OF EVERY REQUEST STAYS THE SAME, turn after turn.
//
// A provider bills a request's prefix at its cache price only when it matches
// the start of a request it saw recently, byte for byte. The system prompt and
// the tool list open every request, so anything that varies there -- a
// timestamp, a map-ordered tool list, a per-turn note placed first -- turns
// every call's whole prompt back to full price. By reading, today's code keeps
// them identical (SortTools, a day-granular date appended LAST); these tests
// hold it to that.
//
// Neuter check: make todayNote print the time to the nanosecond and the agent
// half fails; append one to the plain path's system prompt and the plain half
// does.

type requestHead struct {
	system string
	tools  string
}

func headsOf(t *testing.T, bodies [][]byte) []requestHead {
	t.Helper()
	var out []requestHead
	for _, raw := range bodies {
		var body struct {
			Messages []chatMessage   `json:"messages"`
			Tools    json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request body: %v", err)
		}
		if len(body.Messages) == 0 || body.Messages[0].Role != "system" {
			t.Fatalf("a request did not open with a system message: %s", raw)
		}
		out = append(out, requestHead{system: body.Messages[0].Content, tools: string(body.Tools)})
	}
	return out
}

func sameHeads(t *testing.T, heads []requestHead, wantTools bool) {
	t.Helper()
	for i, h := range heads[1:] {
		if h.system != heads[0].system {
			t.Errorf("request %d's system prompt differs from request 1's -- every call after a change "+
				"pays full price for its whole prompt:\nfirst: %q\nthis:  %q", i+2, heads[0].system, h.system)
		}
		if h.tools != heads[0].tools {
			t.Errorf("request %d's tool list differs from request 1's", i+2)
		}
	}
	if wantTools && heads[0].tools == "" {
		t.Error("no tool list was sent; the agent half of this test checks nothing")
	}
}

func TestAgentRequestsStartTheSameInEveryStepAndTurn(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("it says HELLO"),
		textSSE("second answer"),
	)
	sockAddr, _, _ := agentSocketServerWith(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	}, discardLogger(), defaultSystemPrompt)
	caps := []string{protocol.CapToolApproval}
	finalMessage(t, sockAddr, "what is in inside.txt?", caps)
	time.Sleep(5 * time.Millisecond) // a clock that leaks into the prompt must have moved
	finalMessage(t, sockAddr, "and then?", caps)

	if len(*bodies) != 3 {
		t.Fatalf("model calls = %d, want 3 (two steps, then the second turn)", len(*bodies))
	}
	heads := headsOf(t, *bodies)
	// The per-turn part of the system message is in there, so its stability is
	// what is being checked, not merely the embedded prompt's.
	if !strings.Contains(heads[0].system, "Today's date is") {
		t.Fatal("the agent system prompt has no date note; this test would not cover applyTurnContext")
	}
	sameHeads(t, heads, true)
}

func TestPlainRequestsStartTheSameInEveryTurn(t *testing.T) {
	base, _, bodies := agentUpstream(t, textSSE("one"), textSSE("two"))
	sockAddr, _, _ := agentSocketServerWith(t, base, MCPConfig{}, discardLogger(), defaultSystemPrompt)
	finalMessage(t, sockAddr, "why is the sky blue?", nil)
	time.Sleep(5 * time.Millisecond)
	finalMessage(t, sockAddr, "and the sea?", nil)

	if len(*bodies) != 2 {
		t.Fatalf("model calls = %d, want 2", len(*bodies))
	}
	sameHeads(t, headsOf(t, *bodies), false)
}

// ONE LOG LINE PER CALL, with the host and its cached tokens: /usage shows a
// turn's sum, and only this says which call missed the cache.
//
// Neuter check: drop the logger call in usageTally.add.
func TestEachCallsBillIsLoggedWithItsHost(t *testing.T) {
	base, _, _ := agentUpstream(t,
		[]string{
			`data: {"provider":"DigitalOcean","choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
			`data: {"provider":"DigitalOcean","choices":[],"usage":{"prompt_tokens":1234,"completion_tokens":56,"cost":0.0042,` +
				`"prompt_tokens_details":{"cached_tokens":1000},"completion_tokens_details":{"reasoning_tokens":7}}}`,
			`data: [DONE]`,
		},
		textSSE("cut off"),
	)
	var mu sync.Mutex
	var lines []string
	msgs := []chatMessage{{Role: "user", Content: "hi"}}

	ctx, tally := withUsageTally(context.Background())
	tally.logger = capturingLogger(&mu, &lines)
	if _, err := streamCompletion(ctx, base, "k", "m", msgs, nil, providerRouting{},
		func(string) error { return nil }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := streamCompletion(ctx, base, "k", "m", msgs, nil, providerRouting{},
		func(string) error { return errors.New("broken pipe") }, nil, nil, nil); err == nil {
		t.Fatal("a failed token write did not fail the call")
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "")
	for _, want := range []string{
		`model API usage: provider="DigitalOcean" prompt=1234 cached=1000 completion=56 reasoning=7 cost=$0.004200`,
		"the call ended before its usage arrived (cost unknown)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the log is missing %q:\n%s", want, joined)
		}
	}
	if n := bytes.Count([]byte(joined), []byte("model API usage:")); n != 2 {
		t.Errorf("usage lines = %d, want one per call (2)", n)
	}
}
