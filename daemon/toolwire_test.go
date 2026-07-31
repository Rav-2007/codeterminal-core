package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// THE REGRESSION THIS FILE EXISTS FOR.
//
// Adding tool support to chatCompletionRequest touched the exact bytes the
// daemon POSTs for EVERY turn, including the overwhelming majority that will
// never use a tool. The ZDR enforcement on that body was verified live on
// 2026-07-27 by wire probe (403 zdr_required on the negative path), and F1's
// whole posture is that the body states its privacy constraints explicitly
// rather than relying on a server-side default.
//
// So a stray non-omitempty tag here would not merely be untidy: it would change
// the request every existing user sends, against an enforcement path whose
// proof is a probe of the old bytes. This pins the old bytes.
func TestRequestBodyIsByteIdenticalWithoutTools(t *testing.T) {
	// Exactly what the single-turn path builds: buildChatMessages output, no
	// tools, real ZDR routing.
	routing := (&ZDRConfig{}).resolvedProviderRouting()
	req := chatCompletionRequest{
		Model:         "deepseek/deepseek-v4-flash",
		Messages:      buildChatMessages("you are an assistant", []chatMessage{{Role: "user", Content: "earlier"}}, "hello"),
		Stream:        true,
		Provider:      routing,
		StreamOptions: streamOptions{IncludeUsage: true},
	}

	got, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Written out in full rather than assembled, so a change to any tag shows
	// up here as a literal diff a reviewer can read.
	want := `{"model":"deepseek/deepseek-v4-flash",` +
		`"messages":[{"role":"system","content":"you are an assistant"},` +
		`{"role":"user","content":"earlier"},` +
		`{"role":"user","content":"hello"}],` +
		`"stream":true,` +
		`"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":false},` +
		`"stream_options":{"include_usage":true}}`

	if string(got) != want {
		t.Errorf("the request body changed for a turn that uses no tools.\n got: %s\nwant: %s\n\n"+
			"Every tool field must be omitempty. This body is what the live ZDR verification "+
			"(2026-07-27) probed; changing it for non-agent turns invalidates that proof and "+
			"alters what every existing user sends.", got, want)
	}

	// The three tool fields must all vanish from a message that does not use
	// them, checked by name so a renamed tag cannot slip past the literal above.
	for _, key := range []string{"tool_calls", "tool_call_id", "name", "tools"} {
		if strings.Contains(string(got), `"`+key+`"`) {
			t.Errorf("key %q appears in a tool-free request body: %s", key, got)
		}
	}
}

// And the converse: when tools ARE supplied they must actually reach the wire,
// or agent mode would silently degrade to ordinary completion.
func TestToolsReachTheWireWhenSupplied(t *testing.T) {
	req := chatCompletionRequest{
		Model:    "m",
		Messages: []chatMessage{{Role: "user", Content: "hi"}},
		Tools: []toolSpec{{
			Type: "function",
			Function: toolSpecFunction{
				Name:        "read_file",
				Description: "Read a file.",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		}},
		Stream: true,
	}
	got, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"tools":[`, `"name":"read_file"`, `"parameters":{"type":"object"}`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// A tool message round-trips with the fields the provider needs to pair a
// result with the call that produced it. Getting ToolCallID wrong makes the
// model read one tool's output as another's.
func TestToolMessagesCarryTheirPairingFields(t *testing.T) {
	messages := []chatMessage{
		{Role: "assistant", ToolCalls: []toolCall{{
			ID: "call_1", Type: "function",
			Function: toolCallFunction{Name: "read_file", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Name: "read_file", Content: "package main"},
	}

	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []chatMessage
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(messages, back) {
		t.Errorf("tool messages did not round-trip.\n sent: %+v\n got: %+v\nwire: %s", messages, back, encoded)
	}
}

// rawSSEServer replays fixed SSE lines verbatim, so a truncated stream can be
// produced exactly rather than approximately. Distinct from provider_test.go's
// sseServer, which builds well-formed chunks from content -- the point here is
// to send bytes a well-formed builder could not produce.
func rawSSEServer(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range lines {
			if _, err := w.Write([]byte(line + "\n\n")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamFrom(t *testing.T, srv *httptest.Server) ([]toolCall, error) {
	t.Helper()
	return streamCompletion(t.Context(), srv.URL, "k", "m",
		[]chatMessage{{Role: "user", Content: "hi"}}, nil, providerRouting{},
		func(string) error { return nil }, nil, nil, nil)
}

// Tool calls arrive as fragments that are only valid JSON once concatenated.
// This is the assembly the whole loop depends on.
func TestToolCallAccumulatorRebuildsFragmentedArguments(t *testing.T) {
	// The shape observed live on 2026-07-31: name and id on the first fragment
	// with empty arguments, then arguments in pieces.
	srv := rawSSEServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)

	calls, err := streamFrom(t, srv)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1: %+v", len(calls), calls)
	}
	want := toolCall{ID: "call_abc", Type: "function",
		Function: toolCallFunction{Name: "read_file", Arguments: `{"path":"a.go"}`}}
	if !reflect.DeepEqual(calls[0], want) {
		t.Errorf("call = %+v, want %+v", calls[0], want)
	}
}

func TestToolCallAccumulatorHandlesParallelCalls(t *testing.T) {
	// Two calls interleaved by index, which is how a provider sends parallel
	// tool calls. Assembling them into one, or pairing an index with the wrong
	// arguments, would dispatch a tool with another tool's input.
	srv := rawSSEServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"read_file","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","function":{"name":"list_directory","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"path\":\"b\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a\"}"}}]}}]}`,
		`data: [DONE]`,
	)

	calls, err := streamFrom(t, srv)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2: %+v", len(calls), calls)
	}
	// Returned in provider index order, regardless of arrival order.
	if calls[0].Function.Name != "read_file" || calls[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("call 0 = %+v", calls[0])
	}
	if calls[1].Function.Name != "list_directory" || calls[1].Function.Arguments != `{"path":"b"}` {
		t.Errorf("call 1 = %+v", calls[1])
	}
}

// THE TRUNCATION RULE (D5).
//
// A stream cut mid-fragment leaves arguments that are a truncated prefix of a
// JSON object. Executing that would mean acting on something the model did not
// finish saying -- and the arguments are precisely what a consent prompt shows
// a human, so a half-built call is a half-shown decision.
//
// It is refused, not repaired. There is no "close the brace and hope".
func TestTruncatedToolCallIsRefusedNotRepaired(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			"arguments cut mid-object",
			[]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read_file","arguments":""}}]}}]}`,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\": \"very-long-pa"}}]}}]}`,
				// stream ends here: no [DONE], no closing brace
			},
			"incomplete arguments",
		},
		{
			"call never received a function name",
			[]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a\"}"}}]}}]}`,
			},
			"without a function name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := streamFrom(t, rawSSEServer(t, tc.lines...))
			if err == nil {
				t.Fatalf("a truncated tool call was accepted and returned %+v; it must end the turn "+
					"as an error rather than be executed or guessed at", calls)
			}
			if calls != nil {
				t.Errorf("an error return also produced %d call(s); a caller must never be handed "+
					"a partial call to be tempted by", len(calls))
			}
			// Detail(), not Error(): a truncated stream is classified as an
			// upstream failure, so the socket caller gets ModelError's
			// client-safe text (Gate 7) while the diagnostic goes to the local
			// log. Asserting on Error() here would be asserting that we leak.
			modelErr := asModelError(err)
			if !strings.Contains(modelErr.Detail(), tc.want) {
				t.Errorf("detail %q should explain that the stream ended mid-call (want %q)",
					modelErr.Detail(), tc.want)
			}
			// Classified retryable on purpose: a stream cut mid-call is exactly
			// the transient failure retry exists for, and streamWithRetry's
			// own rule still refuses to retry once content has reached the user.
			if !modelErr.Retryable() {
				t.Errorf("a truncated stream should be retryable, got class %q", modelErr.Class)
			}
		})
	}
}

// A tool that legitimately takes no arguments must not be mistaken for a
// truncated one.
func TestToolCallWithNoArgumentsIsValid(t *testing.T) {
	srv := rawSSEServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"env_names","arguments":""}}]}}]}`,
		`data: [DONE]`,
	)

	calls, err := streamFrom(t, srv)
	if err != nil {
		t.Fatalf("a no-argument tool call was refused: %v", err)
	}
	if len(calls) != 1 || calls[0].Function.Arguments != "{}" {
		t.Errorf("calls = %+v; a tool taking no arguments should arrive as a valid empty object, "+
			"so the tool layer never has to special-case an empty string", calls)
	}
}

// An ordinary text response must still produce no calls at all -- the loop
// decides whether to continue on exactly this.
func TestPlainResponseYieldsNoToolCalls(t *testing.T) {
	srv := rawSSEServer(t,
		`data: {"choices":[{"delta":{"content":"just talking"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)

	calls, err := streamFrom(t, srv)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("a plain text response produced %d tool call(s): %+v", len(calls), calls)
	}
}
