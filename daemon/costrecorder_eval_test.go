//go:build eval

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A provider that streams its answer, then its usage chunk, and holds the
// connection a moment before closing -- as real ones do. The client is done at
// [DONE]; the recorder is not done until the body ends.
func lingeringUpstream(t *testing.T, linger time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"provider":"FastHost","choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"provider":"FastHost","choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":300,` +
			`"cost":0.00042,"prompt_tokens_details":{"cached_tokens":1000},"completion_tokens_details":{"reasoning_tokens":250}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		f.Flush()
		time.Sleep(linger)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The bill is read from the usage chunk -- dollars, cached and reasoning
// tokens, who served it -- and a call still being drained when the client has
// finished is waited for, not missed (the race the recorder's doc describes).
func TestCostRecorderReadsTheBillAndMissesNoCall(t *testing.T) {
	rec := newCostRecorder(t, lingeringUpstream(t, 300*time.Millisecond).URL)
	_, err := streamCompletion(context.Background(), rec.base(), "k", "m",
		buildChatMessages("s", nil, "p"), nil, providerRouting{}, func(string) error { return nil }, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := rec.detail()
	if got.calls != 1 || got.prompt != 1200 || got.completion != 300 {
		t.Fatalf("counted %+v, want 1 call of 1200+300 tokens", got)
	}
	if got.costUSD != 0.00042 || got.cached != 1000 || got.reasoning != 250 {
		t.Errorf("bill = $%v cached %d reasoning %d, want $0.00042, 1000, 250", got.costUSD, got.cached, got.reasoning)
	}
	if got.providers["FastHost"] != 1 {
		t.Errorf("providers = %v, want FastHost once", got.providers)
	}
	rec.reset()
	if after := rec.detail(); after.calls != 0 || after.costUSD != 0 || len(after.providers) != 0 {
		t.Errorf("reset left %+v", after)
	}
}
