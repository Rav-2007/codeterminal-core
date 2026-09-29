package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// Every call carries an output cap: the default when the tier sets none, the
// tier's own when it does, and never more than the managed proxy accepts.
func TestEveryCallCarriesAnOutputCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	cfgJSON := `{"config_version":1,"default_tier":"plain","tiers":{
		"plain":{"slug":"a/b","active":true},
		"small":{"slug":"c/d","active":true,"max_output_tokens":8000},
		"huge":{"slug":"e/f","active":true,"max_output_tokens":100000},
		"odd":{"slug":"g/h","active":true,"max_output_tokens":-1}}}`
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for tier, want := range map[string]int{"plain": 32768, "small": 8000, "huge": 32768, "odd": 32768, "not-configured": 32768} {
		if got := cfg.routingFor(tier).maxTokens; got != want {
			t.Errorf("tier %s: max tokens %d, want %d", tier, got, want)
		}
	}
	warned := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{"tiers.huge.max_output_tokens 100000", "tiers.odd.max_output_tokens -1"} {
		if !strings.Contains(warned, want) {
			t.Errorf("no warning about %q in:\n%s", want, warned)
		}
	}
	if strings.Contains(warned, "unknown config key") {
		t.Errorf("max_output_tokens was reported unknown:\n%s", warned)
	}

	// On the wire, as the request's top-level max_tokens.
	var body []byte
	base := rawSSEServerFunc(t, func(b []byte) []string {
		body = b
		return []string{`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `data: [DONE]`}
	})
	if _, err := streamCompletion(context.Background(), base, "k", "a/b", buildChatMessages("", nil, "hi"), nil,
		cfg.routingFor("small"), func(string) error { return nil }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["max_tokens"] != float64(8000) {
		t.Errorf("the request carried max_tokens=%v, want 8000", sent["max_tokens"])
	}
}

// A tool call the cap cuts off is one billed call and a cut-off answer -- not
// an error the retry loop repeats three times, each stopping at the same place.
func TestAToolCallCutOffByTheOutputCapIsNotRetried(t *testing.T) {
	cutOff := []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"builtin__read_file","arguments":"{\"path\":\"a.t"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	}
	base, requests, _ := agentUpstream(t, cutOff, cutOff, cutOff)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})
	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("a call cut off by the output cap failed the turn: %v", err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("made %d model calls, want 1 -- the same request stops at the same cap every time", n)
	}
	if res.Incomplete == nil || res.Incomplete.Reason != protocol.IncompleteLength {
		t.Errorf("Incomplete = %+v, want reason %q (the user can ask it to continue)", res.Incomplete, protocol.IncompleteLength)
	}
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseRunning {
			t.Errorf("a half-received call ran: %+v", a)
		}
	}
}

// Without the cap's finish reason, a truncated call is still what it always was:
// a broken stream, refused rather than run.
func TestATruncatedCallWithoutTheCapIsStillRefused(t *testing.T) {
	var calls []toolCall
	acc := newToolCallAccumulator()
	var chunk chatCompletionChunk
	_ = json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"x","arguments":"{\"a"}}]}}]}`), &chunk)
	acc.ingest(chunk)
	calls, err := finishStream(acc, "", nil)
	if err == nil || calls != nil {
		t.Errorf("a truncated call with no length finish returned calls=%v err=%v, want a refusal", calls, err)
	}
}
