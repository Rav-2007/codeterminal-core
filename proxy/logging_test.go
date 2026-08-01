package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Sentinels chosen so a single grep over the captured log settles the question.
// Each one stands in for a real secret the proxy holds or handles.
const (
	sentinelOpenRouterKey  = "sk-or-v1-SENTINEL-openrouter-server-key"
	sentinelServiceRoleKey = "sb_secret_SENTINEL-supabase-service-role"
	sentinelMochiKey       = "mk_live_SENTINEL-the-callers-full-key"
	sentinelPromptText     = "SENTINEL-USER-PROMPT-CONTENT"
	sentinelResponseText   = "SENTINEL-MODEL-RESPONSE-CONTENT"
)

// TestLogNeverContainsSecrets is the assertion behind a rule that was, until now,
// enforced only by comments: "the full mochi_key, the Supabase service-role key,
// and the OpenRouter key are never logged", plus "request and response BODIES are
// never logged, at any point".
//
// Every branch below is driven through the real handler with sentinel secrets in
// place, and the whole captured log is then searched. The positive assertion at the
// end matters as much as the negative ones: it proves the log was actually
// populated, so this cannot pass by logging nothing at all.
func TestLogNeverContainsSecrets(t *testing.T) {
	keyID := "key-id-for-secret-scan"
	store := &fakeUsageStore{tokenLimit: 1_000_000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// The upstream echoes a sentinel in its streamed content, so the response
	// body has something identifiable to leak.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\""+sentinelResponseText+"\"}}]}\n\n")
		io.WriteString(w, "data: {\"usage\":{\"total_tokens\":11}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	var logs syncBuffer
	p := newProxy(sentinelOpenRouterKey, upstream.URL, supabase.URL,
		sentinelServiceRoleKey, log.New(&logs, "", 0), parseAllowedModels("good/model"))
	h := wrapMiddleware(log.New(&logs, "", 0), p.metrics, http.HandlerFunc(p.handleChatCompletions))

	good := `{"model":"good/model","messages":[{"role":"user","content":"` + sentinelPromptText +
		`"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`

	// Every branch that logs, so the scan covers the refusal paths too -- those are
	// the ones that log the most about the key.
	cases := []struct {
		name string
		auth string
		body string
	}{
		{"success", "Bearer " + sentinelMochiKey, good},
		{"no bearer", "", good},
		{"empty bearer", "Bearer ", good},
		{"model not allowed", "Bearer " + sentinelMochiKey,
			`{"model":"expensive/model","stream":true,"provider":{"zdr":true,"data_collection":"deny"},"messages":[{"role":"user","content":"` + sentinelPromptText + `"}]}`},
		{"zdr flags missing", "Bearer " + sentinelMochiKey,
			`{"model":"good/model","stream":true,"messages":[{"role":"user","content":"` + sentinelPromptText + `"}]}`},
		{"not streamed", "Bearer " + sentinelMochiKey,
			`{"model":"good/model","provider":{"zdr":true,"data_collection":"deny"},"messages":[{"role":"user","content":"` + sentinelPromptText + `"}]}`},
		{"max_tokens too large", "Bearer " + sentinelMochiKey,
			`{"model":"good/model","stream":true,"max_tokens":9999999,"provider":{"zdr":true,"data_collection":"deny"},"messages":[]}`},
		{"duplicate JSON key", "Bearer " + sentinelMochiKey,
			`{"model":"good/model","model":"other/model","stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, chatCompletionsPath, strings.NewReader(tc.body))
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	got := logs.String()

	for _, secret := range []struct {
		name  string
		value string
	}{
		{"the OpenRouter server key", sentinelOpenRouterKey},
		{"the Supabase service-role key", sentinelServiceRoleKey},
		{"the caller's full Mochiii key", sentinelMochiKey},
		{"user prompt content", sentinelPromptText},
		{"model response content", sentinelResponseText},
	} {
		if strings.Contains(got, secret.value) {
			t.Errorf("the log contains %s (%q). This proxy's zero-data-retention "+
				"claim depends on it not.\nLog:\n%s", secret.name, secret.value, got)
		}
	}

	// A truncated key prefix IS logged on purpose -- it is how an operator
	// correlates a user's report with a specific key without holding the key. Its
	// presence also proves the scan above ran against a populated log.
	wantPrefix := keyPrefix(sentinelMochiKey)
	if !strings.Contains(got, "key_prefix="+wantPrefix) {
		t.Errorf("log carries no key_prefix=%s, so the assertions above may have "+
			"passed against an empty log.\nLog:\n%s", wantPrefix, got)
	}
	if !strings.Contains(got, "gate=") {
		t.Errorf("log carries no gate= label, so no refusal was recorded.\nLog:\n%s", got)
	}
}

// TestAccessLogPreservesFlushing guards the trap this middleware could most
// easily introduce.
//
// streamSSE decides whether it can stream by type-asserting w.(http.Flusher). A
// ResponseWriter wrapper that does not forward Flush makes that assertion fail,
// canFlush goes false, and every SSE response silently becomes one buffered blob
// delivered when the handler returns. Nothing else in the suite would notice: the
// bytes are all correct, only the timing is destroyed.
func TestAccessLogPreservesFlushing(t *testing.T) {
	t.Run("the wrapped writer is still an http.Flusher", func(t *testing.T) {
		var sawFlusher bool
		h := wrapMiddleware(log.New(io.Discard, "", 0), newMetrics(),
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, sawFlusher = w.(http.Flusher)
			}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

		if !sawFlusher {
			t.Error("the handler cannot see an http.Flusher through the middleware; " +
				"streamSSE would fall back to buffering every stream")
		}
	})

	// The behavioural half, over a real connection: the client must receive the
	// first chunk while the handler is still running. A buffering wrapper makes
	// this time out instead of failing on a value, which is why the deadline is
	// short and its own failure message says what it means.
	t.Run("a chunk reaches the client before the handler returns", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(wrapMiddleware(log.New(io.Discard, "", 0), newMetrics(),
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, "data: first\n\n")
				w.(http.Flusher).Flush()
				// Hold the handler open until the client confirms it got the chunk.
				<-release
				io.WriteString(w, "data: last\n\n")
			})))
		defer srv.Close()
		defer close(release)

		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		// Bound to a local and passed to the goroutine as an argument rather than
		// captured: bodyclose cannot follow a body that escapes into a closure and
		// reports this (correctly closed) body as a leak. Handing it over
		// explicitly keeps the linter a hard gate instead of one with an exception.
		body := resp.Body
		defer body.Close()

		type readResult struct {
			n   int
			err error
		}
		done := make(chan readResult, 1)
		go func(rc io.Reader) {
			buf := make([]byte, len("data: first\n\n"))
			n, err := io.ReadFull(rc, buf)
			done <- readResult{n: n, err: err}
		}(body)

		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("reading the first chunk: %v", got.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no chunk arrived while the handler was still running: the " +
				"middleware is buffering the stream instead of forwarding Flush")
		}
	})
}

// TestStatusRecorderReportsWhatTheCallerGot covers the access line's own values,
// including the case that is easy to get wrong: a handler that writes a body
// without ever calling WriteHeader still sent a 200.
func TestStatusRecorderReportsWhatTheCallerGot(t *testing.T) {
	cases := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus string
		wantBytes  string
	}{
		{
			name: "explicit status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTeapot)
				w.Write([]byte("xy"))
			},
			wantStatus: "status=418", wantBytes: "bytes=2",
		},
		{
			name: "implicit 200 from a bare Write",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("hello"))
			},
			wantStatus: "status=200", wantBytes: "bytes=5",
		},
		{
			// Deliberately reported as 0, not guessed at as 200: a handler that
			// wrote nothing is itself the interesting fact.
			name:       "handler wrote nothing",
			handler:    func(w http.ResponseWriter, r *http.Request) {},
			wantStatus: "status=0", wantBytes: "bytes=0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs syncBuffer
			h := accessLog(log.New(&logs, "", 0), newMetrics(), tc.handler)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/p", nil))

			got := logs.String()
			for _, want := range []string{"msg=request", "method=GET", "path=/p", tc.wantStatus, tc.wantBytes, "latency_ms="} {
				if !strings.Contains(got, want) {
					t.Errorf("access line missing %q; got %q", want, got)
				}
			}
		})
	}
}

// TestLogLevelFromEnv pins the fail-loud default: a typo must not silently
// quieten the proxy below the level everything safety-relevant is logged at.
func TestLogLevelFromEnv(t *testing.T) {
	cases := map[string]string{
		"":            "INFO",
		"debug":       "DEBUG",
		"DEBUG":       "DEBUG",
		"  warn  ":    "WARN",
		"warning":     "WARN",
		"error":       "ERROR",
		"chatty":      "INFO",
		"informative": "INFO",
	}
	for value, want := range cases {
		t.Setenv(logLevelEnv, value)
		if got := logLevelFromEnv().String(); got != want {
			t.Errorf("%s=%q -> %s, want %s", logLevelEnv, value, got, want)
		}
	}
}

// TestSlogWritesThroughTheInjectedLogger pins the seam itself: records must go
// through the *log.Logger they were derived from, which is what serializes them
// against everything else writing to that destination (see logWriterAdapter) and
// what lets an existing test capture slog output in the buffer it already passes.
func TestSlogWritesThroughTheInjectedLogger(t *testing.T) {
	var logs syncBuffer
	sl := slogFromLogger(log.New(&logs, "test-prefix: ", 0))
	sl.Info("hello", "req_id", "0123456789abcdef")

	got := logs.String()
	if !strings.HasPrefix(got, "test-prefix: ") {
		t.Errorf("record did not go through the injected logger (no prefix): %q", got)
	}
	if !strings.Contains(got, "req_id=0123456789abcdef") {
		t.Errorf("record lost its attrs: %q", got)
	}
	// One line, not two: the adapter must not double the newline log.Logger adds.
	if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 0 {
		t.Errorf("one record produced %d extra newlines: %q", n, got)
	}
}
