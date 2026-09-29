package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

func cost(v float64) *float64 { return &v }

func TestUsageTally_SumsTheCallsAndKeepsTheLastContext(t *testing.T) {
	_, tally := withUsageTally(context.Background())
	first := chunkUsage{PromptTokens: 1000, CompletionTokens: 50, Cost: cost(0.002)}
	first.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 800}
	second := chunkUsage{PromptTokens: 1500, CompletionTokens: 120, Cost: cost(0.003)}
	second.CompletionTokensDetails = &struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	}{ReasoningTokens: 40}
	tally.add(first)
	tally.add(second)

	u := tally.report("a/model", 64000)
	if u == nil || u.Calls != 2 || u.PromptTokens != 2500 || u.CompletionTokens != 170 ||
		u.CachedTokens != 800 || u.ReasoningTokens != 40 || u.CostMissing {
		t.Fatalf("usage = %+v", u)
	}
	if u.ContextTokens != 1500 {
		t.Errorf("ContextTokens = %d, want the LAST call's prompt (1500), which is what the model last saw", u.ContextTokens)
	}
	if fmt.Sprintf("%.4f", u.CostUSD) != "0.0050" || u.Model != "a/model" || u.ContextWindow != 64000 {
		t.Errorf("usage = %+v", u)
	}

	tally.add(chunkUsage{PromptTokens: 10}) // a call that reported no cost
	if u := tally.report("a/model", 0); !u.CostMissing {
		t.Error("a call with no cost did not mark the total as a lower bound")
	}
}

func TestUsageTally_NothingReportedIsNoUsage(t *testing.T) {
	_, tally := withUsageTally(context.Background())
	if u := tally.report("m", 0); u != nil {
		t.Errorf("a turn with no usage reports = %+v, want nil", u)
	}
	var none *usageTally // outside a turn
	none.add(chunkUsage{PromptTokens: 1})
	if u := none.report("m", 0); u != nil {
		t.Errorf("a nil tally reported %+v", u)
	}
}

// sseServerWithUsage streams an answer, then the usage chunk a provider sends
// when stream_options.include_usage is set: no choices, only usage.
func sseServerWithUsage(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"choices":[{"delta":{"content":%q},"finish_reason":"stop"}]}`, content))
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":1234,"completion_tokens":56,"cost":0.0042,`+
			`"prompt_tokens_details":{"cached_tokens":1000},"completion_tokens_details":{"reasoning_tokens":7}}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// The usage chunk has NO choices; read after the zero-choices guard, it would
// never be seen.
func TestStreamCompletion_ReadsTheUsageChunk(t *testing.T) {
	srv := sseServerWithUsage(t, "hi")
	defer srv.Close()
	ctx, tally := withUsageTally(context.Background())
	routing := ZDRConfig{}.resolvedProviderRouting()
	if _, err := streamCompletion(ctx, srv.URL, "k", "m", buildChatMessages("sys", nil, "hi"), nil, routing,
		func(string) error { return nil }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	u := tally.report("m", 0)
	if u == nil || u.Calls != 1 || u.PromptTokens != 1234 || u.CompletionTokens != 56 || u.CachedTokens != 1000 ||
		u.ReasoningTokens != 7 || fmt.Sprintf("%.4f", u.CostUSD) != "0.0042" {
		t.Errorf("usage = %+v, want the provider's bill", u)
	}
}

// Over the real socket: a prompt's final Done message carries the turn's bill,
// with the model and the context window models.json gives it.
func TestPrompt_TheFinalMessageCarriesTheTurnsBill(t *testing.T) {
	up := sseServerWithUsage(t, "an answer")
	defer up.Close()
	s := &Server{
		apiBase: up.URL,
		cfg: &Config{Tiers: map[string]ModelTier{
			"t": {Slug: "test/model", Active: true, ContextWindow: 64000},
		}},
		modelOverride: "test/model",
		logger:        discardLogger(),
		workspace:     t.TempDir(),
	}
	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() { s.serveConn(serverConn); _ = serverConn.Close(); close(done) }()
	defer func() { _ = clientConn.Close(); <-done }()

	enc, dec := json.NewEncoder(clientConn), json.NewDecoder(clientConn)
	_ = enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"})
	var hs protocol.HandshakeResponse
	_ = dec.Decode(&hs)
	_ = enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "hello"})
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if tok.Usage != nil && !tok.Done {
			t.Errorf("usage arrived on a message that is not the final one: %+v", tok)
		}
		if tok.Done {
			u := tok.Usage
			if u == nil || u.Calls != 1 || u.PromptTokens != 1234 || u.ContextTokens != 1234 ||
				u.Model != "test/model" || u.ContextWindow != 64000 {
				t.Errorf("final message usage = %+v, want the bill, the model and its window", u)
			}
			return
		}
	}
}

func TestContextWindowIsParsedAndANegativeOneDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	cfgJSON := `{"config_version":1,"default_tier":"primary","tiers":{
		"primary":{"slug":"a/b","active":true,"context_window":1024000},
		"odd":{"slug":"c/d","active":true,"context_window":-5}}}`
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cfg.Warnings() {
		if strings.Contains(w, "unknown config key") {
			t.Errorf("context_window was reported unknown: %s", w)
		}
	}
	if cfg.Tiers["primary"].ContextWindow != 1024000 || cfg.Tiers["odd"].ContextWindow != 0 {
		t.Errorf("windows = %d, %d; want 1024000 and a negative one dropped to 0",
			cfg.Tiers["primary"].ContextWindow, cfg.Tiers["odd"].ContextWindow)
	}
	s := &Server{cfg: cfg}
	if s.contextWindowFor("a/b") != 1024000 || s.contextWindowFor("not/configured") != 0 {
		t.Error("contextWindowFor does not answer from the tiers")
	}
}

// Both shipped configs give every tier a window, so /usage can always say how
// full the context is.
func TestTheShippedConfigsGiveEveryTierAContextWindow(t *testing.T) {
	for _, f := range []string{"../models.json", "../models.agent.json"} {
		cfg, err := LoadConfig(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for name, tier := range cfg.Tiers {
			if tier.ContextWindow <= 0 {
				t.Errorf("%s: tier %s has no context_window", f, name)
			}
		}
	}
}

// withUsage appends a provider usage chunk to a scripted SSE response.
func withUsage(sse []string, prompt, completion int, dollars float64) []string {
	out := append([]string(nil), sse[:len(sse)-1]...) // everything before [DONE]
	out = append(out, fmt.Sprintf(`data: {"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"cost":%g}}`,
		prompt, completion, dollars))
	return append(out, sse[len(sse)-1])
}

// An agent turn makes several calls; its final message carries all of them.
func TestAgentTurn_TheFinalMessageCarriesTheWholeLoopsBill(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		withUsage(toolCallSSE("c1", "builtin__no_such_tool", `{}`), 1000, 20, 0.001),
		withUsage(textSSE("done"), 1500, 30, 0.002),
	)
	sockAddr, _, srv := agentSocketServer(t, base, MCPConfig{Enabled: true})
	srv.cfg.Tiers = map[string]ModelTier{"t": {Slug: "test-model", Active: true, ContextWindow: 262144}}
	_, dec, conn := agentClient(t, sockAddr, "go", []string{protocol.CapToolApproval})
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if !tok.Done {
			continue
		}
		u := tok.Usage
		if calls.Load() != 2 || u == nil || u.Calls != 2 || u.PromptTokens != 2500 || u.CompletionTokens != 50 ||
			u.ContextTokens != 1500 || fmt.Sprintf("%.3f", u.CostUSD) != "0.003" || u.ContextWindow != 262144 {
			t.Errorf("final usage = %+v after %d calls, want both calls summed and the last call's context", u, calls.Load())
		}
		return
	}
}
