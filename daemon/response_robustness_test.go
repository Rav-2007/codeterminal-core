package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// --- a real, scriptable upstream ---------------------------------------------

// scriptedUpstream serves a fixed list of SSE data lines and records the
// request bodies it received, so a test can assert both what the daemon SENT
// and what it did with what came back.
func scriptedUpstream(t *testing.T, lines []string) (*httptest.Server, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		fmt.Fprint(buf, readAllString(r))
		bodies = append(bodies, buf.String())

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func readAllString(r *http.Request) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// deltaLine builds one SSE chunk carrying the given content and/or reasoning.
func deltaLine(content, reasoning string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]string{"content": content, "reasoning": reasoning},
		}},
	})
	return string(b)
}

// runPromptTurn drives one full prompt through serveConn over a net.Pipe and
// returns every TokenResponse the daemon wrote. This is the WIRE, not a
// function call — which is the point for the empty-turn tests: what matters is
// what actually crosses the socket.
func runPromptTurn(t *testing.T, srv *Server, req protocol.PromptRequest) (protocol.HandshakeResponse, []protocol.TokenResponse) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.serveConn(serverConn)
		serverConn.Close()
		close(done)
	}()

	enc := json.NewEncoder(clientConn)
	dec := json.NewDecoder(clientConn)

	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"}); err != nil {
		t.Fatalf("encoding handshake: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("decoding handshake: %v", err)
	}

	req.ProtocolVersion = protocol.ProtocolVersion
	if err := enc.Encode(req); err != nil {
		t.Fatalf("encoding prompt: %v", err)
	}

	var responses []protocol.TokenResponse
	for {
		var tr protocol.TokenResponse
		if err := dec.Decode(&tr); err != nil {
			break
		}
		responses = append(responses, tr)
		if tr.Done {
			break
		}
	}
	clientConn.Close()
	<-done
	return hs, responses
}

func robustnessServer(t *testing.T, apiBase, workspace string, mem *MemoryStore) *Server {
	t.Helper()
	return &Server{
		apiBase:   apiBase,
		logger:    discardLogger(),
		cfg:       &Config{},
		workspace: workspace,
		memory:    mem,
	}
}

// --- Fix 14a: reject empty prompts -------------------------------------------

// TestEmptyPrompt_IsRejectedWithoutCallingTheModel: apiBase points at an
// upstream that would record any call. A billed request for a question that was
// never asked is the failure being closed.
func TestEmptyPrompt_IsRejectedWithoutCallingTheModel(t *testing.T) {
	upstream, bodies := scriptedUpstream(t, []string{deltaLine("hello", "")})
	memStore, _ := openTestMemoryStore(t)
	srv := robustnessServer(t, upstream.URL, "/workspace/empty-prompt", memStore)

	for _, prompt := range []string{"", "   ", "\n\t\n"} {
		t.Run(fmt.Sprintf("prompt=%q", prompt), func(t *testing.T) {
			before := len(*bodies)
			_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: prompt})

			if len(responses) != 1 {
				t.Fatalf("got %d responses, want exactly one refusal: %+v", len(responses), responses)
			}
			got := responses[0]
			if !got.Done || got.Error == "" {
				t.Errorf("response = %+v, want Done with an Error", got)
			}
			if got.ErrorClass != string(ClassInvalidRequest) {
				t.Errorf("ErrorClass = %q, want %q", got.ErrorClass, ClassInvalidRequest)
			}
			if n := len(*bodies) - before; n != 0 {
				t.Errorf("the model API was called %d time(s) for an empty prompt, want 0 — that call is billed", n)
			}
		})
	}

	// Nothing was persisted either.
	turns, err := memStore.LoadRecentTurns(context.Background(), "/workspace/empty-prompt", maxHistoryTurns)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("persisted %+v for empty prompts, want nothing", turns)
	}
}

func TestNonEmptyPrompt_StillReachesTheModel(t *testing.T) {
	upstream, bodies := scriptedUpstream(t, []string{deltaLine("an answer", "")})
	srv := robustnessServer(t, upstream.URL, "/workspace/ok", nil)

	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "a real question"})
	if len(*bodies) != 1 {
		t.Fatalf("model called %d time(s), want 1", len(*bodies))
	}
	last := responses[len(responses)-1]
	if !last.Done || last.Error != "" {
		t.Errorf("final response = %+v, want a clean Done", last)
	}
}

// --- Fix 14b: never persist an empty assistant turn --------------------------

// TestEmptyAnswer_IsNotPersisted is the WRITE side of the empty-turn poison.
// A zero-content upstream response must leave nothing behind.
func TestEmptyAnswer_IsNotPersisted(t *testing.T) {
	// An upstream that streams nothing at all: valid SSE, zero content.
	upstream, _ := scriptedUpstream(t, []string{deltaLine("", "")})
	memStore, _ := openTestMemoryStore(t)
	const ws = "/workspace/empty-answer"
	srv := robustnessServer(t, upstream.URL, ws, memStore)

	runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "a question that gets no answer"})

	turns, err := memStore.LoadRecentTurns(context.Background(), ws, maxHistoryTurns)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("persisted %+v after an empty answer, want nothing — an empty turn poisons every later session", turns)
	}
}

// TestPersistedEmptyTurn_NeverReachesTheWire is the READ side, tested at the
// wire as required: a row written BEFORE the write-side guard existed (or by
// any other route) must not re-hydrate into a client's transcript, and must not
// be sent to the model when the client hands it back as history.
func TestPersistedEmptyTurn_NeverReachesTheWire(t *testing.T) {
	upstream, bodies := scriptedUpstream(t, []string{deltaLine("fresh answer", "")})
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/poisoned"

	// Simulate the already-poisoned database: a real exchange, then an empty
	// assistant turn of exactly the shape the old code wrote.
	for _, tt := range []struct{ role, content string }{
		{"user", "first question"},
		{"assistant", "first answer"},
		{"user", "second question"},
		{"assistant", ""},
	} {
		if err := memStore.AppendTurn(ctx, ws, tt.role, tt.content); err != nil {
			t.Fatalf("AppendTurn: %v", err)
		}
	}

	srv := robustnessServer(t, upstream.URL, ws, memStore)

	// (1) The handshake must not hand the empty turn to the client.
	hs, _ := runPromptTurn(t, srv, protocol.PromptRequest{
		Prompt: "third question",
		// (2) ...and even if a client sends one anyway, it must not be relayed.
		History: []protocol.Turn{
			{Role: "user", Content: "first question"},
			{Role: "assistant", Content: ""},
		},
	})

	for _, turn := range hs.PersistedHistory {
		if strings.TrimSpace(turn.Content) == "" {
			t.Errorf("an empty turn was re-hydrated into the client's transcript: %+v", hs.PersistedHistory)
		}
	}
	if len(hs.PersistedHistory) != 3 {
		t.Errorf("PersistedHistory has %d turns, want 3 (the empty one filtered out): %+v", len(hs.PersistedHistory), hs.PersistedHistory)
	}

	// The wire body the daemon actually sent upstream must contain no empty
	// message content.
	if len(*bodies) != 1 {
		t.Fatalf("model called %d time(s), want 1", len(*bodies))
	}
	var sent chatCompletionRequest
	if err := json.Unmarshal([]byte((*bodies)[0]), &sent); err != nil {
		t.Fatalf("decoding the request the daemon sent: %v", err)
	}
	for i, m := range sent.Messages {
		if strings.TrimSpace(m.Content) == "" {
			t.Errorf("message %d (role=%q) reached the model with empty content: %+v", i, m.Role, sent.Messages)
		}
	}
}

// --- Fix 14c: history truncation is visible ----------------------------------

func TestHistoryTruncation_IsVisibleOnTheWire(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{deltaLine("ok", "")})
	srv := robustnessServer(t, upstream.URL, "/workspace/trunc", nil)

	var many []protocol.Turn
	for i := 0; i < maxHistoryTurns+4; i++ {
		many = append(many, protocol.Turn{Role: "user", Content: fmt.Sprintf("turn %d", i)})
	}

	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "what was my first question?", History: many})

	var info *protocol.HistoryInfo
	for _, r := range responses {
		if r.History != nil {
			info = r.History
		}
	}
	if info == nil {
		t.Fatal("no HistoryInfo on the wire — truncation is still invisible to the client")
	}
	if !info.Truncated {
		t.Errorf("HistoryInfo = %+v, want Truncated:true", info)
	}
	if info.Turns != maxHistoryTurns {
		t.Errorf("HistoryInfo.Turns = %d, want %d", info.Turns, maxHistoryTurns)
	}
}

func TestHistoryInfo_AbsentWhenThereIsNoHistory(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{deltaLine("ok", "")})
	srv := robustnessServer(t, upstream.URL, "/workspace/nohist", nil)

	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "first ever question"})
	for _, r := range responses {
		if r.History != nil {
			t.Errorf("HistoryInfo = %+v on a first turn, want it absent", r.History)
		}
	}
}

// --- Fix 14d: delta.reasoning ------------------------------------------------

// TestReasoningTokens_AreReadAndSurfacedSeparately is the delta.reasoning
// acceptance. Reasoning must reach the client (it used to be decoded and
// dropped, leaving the user watching an empty screen) and must NOT be folded
// into the answer, since the answer is what gets parsed for edit blocks and
// written to memory.
func TestReasoningTokens_AreReadAndSurfacedSeparately(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{
		deltaLine("", "let me look at the parser"),
		deltaLine("", " ... the marker scan is bounded"),
		deltaLine("The bug is in ", ""),
		deltaLine("scanUntil.", ""),
	})
	memStore, _ := openTestMemoryStore(t)
	const ws = "/workspace/reasoning"
	srv := robustnessServer(t, upstream.URL, ws, memStore)

	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "where is the bug?"})

	var reasoning, answer strings.Builder
	for _, r := range responses {
		reasoning.WriteString(r.Reasoning)
		answer.WriteString(r.Token)
	}

	if reasoning.String() != "let me look at the parser ... the marker scan is bounded" {
		t.Errorf("reasoning on the wire = %q, want the streamed thinking tokens", reasoning.String())
	}
	if answer.String() != "The bug is in scanUntil." {
		t.Errorf("answer = %q, want only the content tokens", answer.String())
	}
	if strings.Contains(answer.String(), "let me look") {
		t.Error("reasoning leaked into the answer stream — it would be parsed for edit blocks and persisted")
	}

	// And memory holds the answer only.
	turns, err := memStore.LoadRecentTurns(context.Background(), ws, maxHistoryTurns)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("persisted %+v, want the user turn and the assistant answer", turns)
	}
	if turns[1].Content != "The bug is in scanUntil." {
		t.Errorf("persisted answer = %q, want the content tokens only (no reasoning)", turns[1].Content)
	}
}

// TestReasoningTokens_AbsentWhenTheModelSendsNone guards the default path: a
// non-reasoning model's stream is byte-for-byte what it always was.
func TestReasoningTokens_AbsentWhenTheModelSendsNone(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{deltaLine("plain answer", "")})
	srv := robustnessServer(t, upstream.URL, "/workspace/plain", nil)

	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "hi"})
	for _, r := range responses {
		if r.Reasoning != "" {
			t.Errorf("Reasoning = %q on a non-reasoning stream, want empty", r.Reasoning)
		}
	}
}

// providerLine builds one SSE chunk carrying a top-level "provider" field, the
// shape OpenRouter emits to name the upstream that served the request (and the
// shape daemon/provider.go's chatCompletionChunk.Provider decodes).
func providerLine(provider, content string) string {
	b, _ := json.Marshal(map[string]any{
		"provider": provider,
		"choices": []map[string]any{{
			"delta": map[string]string{"content": content},
		}},
	})
	return string(b)
}

// TestProvider_IsSurfacedOnTheWire (E1): the daemon already observed and logged
// the serving provider; this proves the same value now crosses the socket to a
// client, on its own message, so the client-surfacing gap is actually closed
// (the Fix-13 trap: a value the daemon has but no client can see). It rides its
// OWN TokenResponse (not the pre-token grounding message) because the provider
// is not known until the response begins.
func TestProvider_IsSurfacedOnTheWire(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{
		providerLine("DeepInfra", ""),
		deltaLine("hello", ""),
	})
	srv := robustnessServer(t, upstream.URL, "/workspace/provider", nil)

	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "hi"})

	var providers []string
	for _, r := range responses {
		if r.Provider != "" {
			providers = append(providers, r.Provider)
		}
	}
	if len(providers) != 1 || providers[0] != "DeepInfra" {
		t.Errorf("provider(s) on the wire = %v, want exactly one %q", providers, "DeepInfra")
	}
}

// TestProvider_AbsentWhenUpstreamOmitsIt guards the non-error absence path:
// OpenRouter does not guarantee the field, so a stream without one must leave
// TokenResponse.Provider empty on every message (omitempty drops it), never a
// placeholder — the response stays byte-identical to before this existed.
func TestProvider_AbsentWhenUpstreamOmitsIt(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{deltaLine("plain answer", "")})
	srv := robustnessServer(t, upstream.URL, "/workspace/noprovider", nil)

	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{Prompt: "hi"})
	for _, r := range responses {
		if r.Provider != "" {
			t.Errorf("Provider = %q on a stream that named none, want empty", r.Provider)
		}
	}
}

// TestStreamCompletion_ReadsDeltaReasoning tests the plumbing directly, so the
// tier being inactive today doesn't leave this unverified.
func TestStreamCompletion_ReadsDeltaReasoning(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{
		deltaLine("", "thinking..."),
		deltaLine("answer", "more thinking"),
	})

	var tokens, reasoning []string
	_, err := streamCompletion(context.Background(), upstream.URL, "k", "m", buildChatMessages("sys", nil, "hello"), nil,
		ZDRConfig{}.resolvedProviderRouting(),
		func(tok string) error { tokens = append(tokens, tok); return nil },
		nil,
		func(r string) { reasoning = append(reasoning, r) },
		nil,
	)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	if strings.Join(reasoning, "|") != "thinking...|more thinking" {
		t.Errorf("reasoning = %v, want both reasoning deltas", reasoning)
	}
	if strings.Join(tokens, "|") != "answer" {
		t.Errorf("tokens = %v, want only the content delta", tokens)
	}
}

// TestStreamCompletion_NilReasoningCallbackIsSafe: a caller that doesn't care
// about reasoning must not crash on a model that emits it.
func TestStreamCompletion_NilReasoningCallbackIsSafe(t *testing.T) {
	upstream, _ := scriptedUpstream(t, []string{deltaLine("answer", "thinking")})
	_, err := streamCompletion(context.Background(), upstream.URL, "k", "m", buildChatMessages("sys", nil, "hello"), nil,
		ZDRConfig{}.resolvedProviderRouting(),
		func(string) error { return nil }, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("streamCompletion with a nil onReasoning: %v", err)
	}
}
