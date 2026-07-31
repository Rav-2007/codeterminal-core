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
			_, err := streamCompletion(context.Background(), srv.URL, "k", "m", buildChatMessages("sys", nil, "hi"), nil, routing,
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

	// "tool_calls" is a COMPLETE stream, not a truncated one: the model stopped
	// on purpose, having said what it wanted to do next.
	//
	// This case used to assert the opposite, because tool_calls was then just a
	// convenient example of a reason we did not recognise. Once tools went on
	// the wire that stopped being harmless: every tool-calling response would
	// have been reported to the user as "this answer may be incomplete" —
	// alarming, and wrong, since nothing was cut off.
	if got := incompleteInfoFor(finishReasonToolCalls); got != nil {
		t.Errorf("incompleteInfoFor(%q) = %+v, want nil — a stream that ends in tool_calls is "+
			"complete, and reporting it as truncated tells the user their answer was cut off "+
			"when the model simply asked to call a tool", finishReasonToolCalls, got)
	}

	// An unrecognized non-stop reason must still be surfaced (better to flag an
	// early end we can't name than to hide it), carrying the reason verbatim.
	// Uses a reason no provider actually sends, so this case cannot quietly
	// become meaningful later the way tool_calls did.
	other := incompleteInfoFor("some_future_reason")
	if other == nil || other.Reason != "some_future_reason" {
		t.Errorf("incompleteInfoFor(\"some_future_reason\") = %+v, want a report carrying the reason verbatim", other)
	}
}
