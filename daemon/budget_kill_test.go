package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// proxyBudgetKillChunk is the EXACT byte sequence proxy/main.go's
// writeBudgetExceeded emits when the per-request spend ceiling kills a stream.
// Copied verbatim rather than referenced, because the proxy is a separate module
// this one cannot import (proxy/Dockerfile copies only *.go and go.mod, with no
// go.sum and no sibling modules -- see TestSeam_ProxyBudgetKillReachesTheUser,
// which pins the two together by running the real binary).
const proxyBudgetKillChunk = `data: {"error":"budget_exceeded","truncated":true}` + "\n\n" +
	"data: [DONE]\n\n"

// The P1-1 regression, at the parser. The proxy's kill chunk carries an `error`
// and NO choices. chatCompletionChunk had no Error field, so it decoded to an
// entirely empty struct, the zero-choices guard skipped it, "[DONE]" arrived,
// and incompleteInfoFor("") returned nil -- a stream cut off mid-answer was
// reported to the user as a complete, successful response. Strictly worse than
// the silent close the chunk was written to avoid: indistinguishable from
// SUCCESS rather than from a network failure.
//
// Neuter check: remove the errorSlug check from streamCompletion's loop (or move
// it below the `len(chunk.Choices) == 0` guard, which is the subtler way to get
// this wrong) and onFinish reports "" again.
func TestStreamCompletion_BudgetKillIsReportedAsIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Real content first: a budget kill happens mid-answer, so there is
		// always output the user should keep.
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial answer"}}]}`+"\n\n")
		fmt.Fprint(w, proxyBudgetKillChunk)
	}))
	defer srv.Close()

	var got string
	var content strings.Builder
	routing := ZDRConfig{}.resolvedProviderRouting()
	err := streamCompletion(context.Background(), srv.URL, "k", "m", "sys", nil, "hi", routing,
		func(tok string) error { content.WriteString(tok); return nil },
		nil, nil,
		func(reason string) { got = reason },
	)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}

	if got != protocol.IncompleteBudgetExceeded {
		t.Errorf("onFinish reason = %q, want %q. The proxy killed this stream for "+
			"crossing its spending ceiling and the daemon reported nothing, so the "+
			"answer reaches the user as a complete success that is silently truncated",
			got, protocol.IncompleteBudgetExceeded)
	}
	// The partial answer must survive: it is real output the user paid for.
	if content.String() != "partial answer" {
		t.Errorf("content delivered = %q, want %q -- a budget kill must not discard "+
			"the output that was already streamed", content.String(), "partial answer")
	}

	info := incompleteInfoFor(got)
	if info == nil {
		t.Fatal("incompleteInfoFor returned nil for a budget kill -- nothing reaches the client")
	}
	if info.Reason != protocol.IncompleteBudgetExceeded || info.Detail == "" {
		t.Errorf("IncompleteInfo = %+v, want reason %q with non-empty client-facing detail",
			info, protocol.IncompleteBudgetExceeded)
	}
}

// The reason chatCompletionChunk.Error is json.RawMessage and not string.
// OpenRouter surfaces mid-stream errors as an OBJECT. Typed as a string, the
// whole chunk would fail to unmarshal and hit streamCompletion's `continue`,
// silently discarding any content that chunk also carried -- turning a
// nice-to-have signal into a content-loss bug.
func TestStreamCompletion_ObjectShapedErrorDoesNotDiscardContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"error":{"message":"upstream hiccup","code":502},`+
			`"choices":[{"delta":{"content":"kept"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var content strings.Builder
	routing := ZDRConfig{}.resolvedProviderRouting()
	err := streamCompletion(context.Background(), srv.URL, "k", "m", "sys", nil, "hi", routing,
		func(tok string) error { content.WriteString(tok); return nil },
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	if content.String() != "kept" {
		t.Errorf("content = %q, want %q -- an object-shaped `error` field made the "+
			"chunk undecodable and its content was dropped. This is what typing "+
			"Error as `string` instead of json.RawMessage costs", content.String(), "kept")
	}
}

func TestErrorSlug(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"absent", ``, "", false},
		{"bare string", `"budget_exceeded"`, "budget_exceeded", true},
		{"empty string", `""`, "", false},
		{"object with code", `{"message":"nope","code":"budget_exceeded"}`, "budget_exceeded", true},
		// Unrecognized shapes must yield nothing rather than a guess -- the caller
		// treats "no slug" as "handle this chunk normally".
		{"object without code", `{"message":"nope"}`, "", false},
		{"object with numeric code", `{"code":502}`, "", false},
		{"null", `null`, "", false},
		{"number", `502`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.raw != "" {
				raw = json.RawMessage(tt.raw)
			}
			got, ok := errorSlug(raw)
			if ok != tt.ok || got != tt.want {
				t.Errorf("errorSlug(%s) = (%q, %v), want (%q, %v)", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// An error slug the daemon does not recognize must not be invented into a
// truncation report: the stream is reported exactly as it would have been
// before, so an unknown mid-stream error keeps falling through to the existing
// generic handling rather than acquiring a wrong label.
func TestStreamCompletion_UnknownErrorSlugIsNotReportedAsBudgetKill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"error":"some_unrelated_thing"}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	got := "<unset>"
	routing := ZDRConfig{}.resolvedProviderRouting()
	err := streamCompletion(context.Background(), srv.URL, "k", "m", "sys", nil, "hi", routing,
		func(string) error { return nil }, nil, nil,
		func(reason string) { got = reason },
	)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	if got != "stop" {
		t.Errorf("onFinish reason = %q, want \"stop\" -- an unrecognized error slug "+
			"must not overwrite the provider's own finish_reason", got)
	}
}
