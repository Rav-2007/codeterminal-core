package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"codeterminal/protocol"
)

// sseServerWithFinish emits a minimal chat-completions SSE stream whose terminal
// chunk carries the given finish_reason (empty string means: send no
// finish_reason field at all, the pre-M1 shape). It exists separately from
// sseServer because M1 is specifically about the field sseServer never sent.
func sseServerWithFinish(t *testing.T, content, finishReason string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, content))
		if finishReason == "" {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":""}}]}`+"\n\n")
		} else {
			fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"choices":[{"delta":{"content":""},"finish_reason":%q}]}`, finishReason))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// TestStreamCompletion_OnFinishReportsTerminalReason is the M1 wire-read guard:
// streamCompletion must surface the terminal SSE finish_reason exactly once via
// onFinish. Before M1 the field was never decoded, so a "length" truncation was
// indistinguishable from a clean "stop". Fails-when-neutered: revert the
// finish_reason capture in provider.go and onFinish sees "" for the length case.
func TestStreamCompletion_OnFinishReportsTerminalReason(t *testing.T) {
	cases := []struct {
		name   string
		finish string
		want   string
	}{
		{"length cutoff", "length", "length"},
		{"natural stop", "stop", "stop"},
		{"no finish_reason field", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := sseServerWithFinish(t, "some answer", tc.finish)
			defer srv.Close()

			calls := 0
			got := "<unset>"
			routing := ZDRConfig{}.resolvedProviderRouting()
			err := streamCompletion(context.Background(), srv.URL, "k", "m", "sys", nil, "hi", routing,
				func(string) error { return nil },
				nil,
				nil,
				func(reason string) { calls++; got = reason },
			)
			if err != nil {
				t.Fatalf("streamCompletion: %v", err)
			}
			if calls != 1 {
				t.Errorf("onFinish call count = %d, want exactly 1", calls)
			}
			if got != tc.want {
				t.Errorf("onFinish reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIncompleteInfoFor pins the finish_reason -> client-facing Incomplete
// mapping: a natural end (and the empty no-field case) is nil so a complete
// answer carries nothing, while a cut-off reason yields a machine-readable slug
// plus client-safe detail that discloses no host/path/provider (Gate-7).
func TestIncompleteInfoFor(t *testing.T) {
	if got := incompleteInfoFor(""); got != nil {
		t.Errorf("incompleteInfoFor(\"\") = %+v, want nil (no finish_reason = complete)", got)
	}
	if got := incompleteInfoFor("stop"); got != nil {
		t.Errorf("incompleteInfoFor(\"stop\") = %+v, want nil (natural end)", got)
	}

	length := incompleteInfoFor(protocol.IncompleteLength)
	if length == nil {
		t.Fatal("incompleteInfoFor(\"length\") = nil, want a non-nil cut-off report")
	}
	if length.Reason != protocol.IncompleteLength {
		t.Errorf("Reason = %q, want %q", length.Reason, protocol.IncompleteLength)
	}
	if length.Detail == "" {
		t.Error("Detail is empty; a client needs human-readable text to render")
	}

	// An unrecognized non-stop reason must still be surfaced (better to flag an
	// early end we can't name than to hide it), carrying the reason verbatim.
	other := incompleteInfoFor("tool_calls")
	if other == nil || other.Reason != "tool_calls" {
		t.Errorf("incompleteInfoFor(\"tool_calls\") = %+v, want a report with Reason=\"tool_calls\"", other)
	}
}
