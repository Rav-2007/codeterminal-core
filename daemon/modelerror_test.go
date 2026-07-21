package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// This file is the before/after evidence for Fix 9: every upstream failure
// collapsed into one string, "calling model API failed". A transient 502, an
// exhausted quota, a rejected API key, a proxy quota rejection and a
// conversation too long for the context window were indistinguishable, so the
// user could not tell "wait a second" from "top up your account" from "start a
// new session". Only the ZDR refusal kept a message of its own.
//
// Run against the pre-fix daemon, every case in TestModelError_ClassifiesEach
// FAILS with the same generic string, and the wire carried no class at all.

// upstreamReturning stands up a fake model API that answers every request with
// one status, body and header set.
func upstreamReturning(t *testing.T, status int, body string, headers map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestModelError_ClassifiesEachFailureMode walks the real failure modes through
// the real streamCompletion and requires a distinct, correct class for each.
func TestModelError_ClassifiesEachFailureMode(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantClass ModelErrorClass
		retryable bool
	}{
		{
			name: "429 plain rate limit", status: http.StatusTooManyRequests,
			body:      `{"error":{"message":"Rate limit exceeded"}}`,
			wantClass: ClassRateLimited, retryable: true,
		},
		{
			// The managed proxy signals an exhausted allowance as 429 too, so the
			// body -- not the status -- is what separates "wait" from "you are out".
			// Getting this wrong would have the retry layer back off against
			// something that can never succeed.
			name: "429 from the managed proxy's quota rejection", status: http.StatusTooManyRequests,
			body:      `{"error":"quota_exceeded"}`,
			wantClass: ClassQuotaExceeded, retryable: false,
		},
		{
			name: "402 out of credits", status: http.StatusPaymentRequired,
			body:      `{"error":{"message":"Insufficient credit"}}`,
			wantClass: ClassQuotaExceeded, retryable: false,
		},
		{
			name: "401 bad key", status: http.StatusUnauthorized,
			body:      `{"error":{"message":"Invalid API key"}}`,
			wantClass: ClassAuth, retryable: false,
		},
		{
			name: "403 forbidden", status: http.StatusForbidden,
			body:      `{"error":{"message":"Forbidden"}}`,
			wantClass: ClassAuth, retryable: false,
		},
		{
			name: "400 context length exceeded", status: http.StatusBadRequest,
			body:      `{"error":{"code":"context_length_exceeded","message":"maximum context length is 8192 tokens"}}`,
			wantClass: ClassContextTooLarge, retryable: false,
		},
		{
			name: "500 upstream blip", status: http.StatusInternalServerError,
			body:      `internal error`,
			wantClass: ClassUpstreamUnavailable, retryable: true,
		},
		{
			name: "502 bad gateway", status: http.StatusBadGateway,
			body:      `bad gateway`,
			wantClass: ClassUpstreamUnavailable, retryable: true,
		},
		{
			name: "ZDR routing refusal", status: http.StatusNotFound,
			body:      `{"error":{"message":"No endpoints found matching your data policy (Zero data retention)."}}`,
			wantClass: ClassPrivacyRefused, retryable: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := upstreamReturning(t, tc.status, tc.body, nil)
			err := streamCompletion(context.Background(), srv.URL, "k", "m", "sys", nil, "hello",
				ZDRConfig{}.resolvedProviderRouting(), func(string) error { return nil }, nil)
			if err == nil {
				t.Fatal("want an error")
			}
			me := asModelError(err)
			if me.Class != tc.wantClass {
				t.Errorf("Class = %q, want %q (detail: %s)", me.Class, tc.wantClass, me.Detail())
			}
			if me.Retryable() != tc.retryable {
				t.Errorf("Retryable() = %t, want %t", me.Retryable(), tc.retryable)
			}
			if me.Error() == "" {
				t.Error("client message is empty")
			}
		})
	}
}

// TestModelError_DeadPortIsUpstreamUnavailable covers the transport half: never
// reaching the provider at all.
func TestModelError_DeadPortIsUpstreamUnavailable(t *testing.T) {
	err := streamCompletion(context.Background(), "http://127.0.0.1:1", "k", "m", "sys", nil, "hello",
		ZDRConfig{}.resolvedProviderRouting(), func(string) error { return nil }, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if me := asModelError(err); me.Class != ClassUpstreamUnavailable {
		t.Errorf("Class = %q, want %q", me.Class, ClassUpstreamUnavailable)
	}
}

// TestModelError_ClientMessagesAreDistinctAndActionable pins the point of the
// whole exercise: a user must be able to tell these apart.
func TestModelError_ClientMessagesAreDistinctAndActionable(t *testing.T) {
	seen := map[string]ModelErrorClass{}
	for class, msg := range clientMessages {
		if msg == "" {
			t.Errorf("class %q has an empty client message", class)
		}
		if prior, dup := seen[msg]; dup {
			t.Errorf("classes %q and %q share the message %q — they must be distinguishable", prior, class, msg)
		}
		seen[msg] = class
	}
}

// TestModelError_ClientMessagesLeakNoInfrastructure is the Gate-7 constraint on
// Fix 9: classifying more precisely must not disclose more. The class tells the
// user WHAT kind of problem it is; it must never tell them WHERE anything is.
func TestModelError_ClientMessagesLeakNoInfrastructure(t *testing.T) {
	forbidden := []string{"http://", "https://", "127.0.0.1", "localhost", "dial tcp", "openrouter", "/home/", "/tmp/", "x-api-key", "Bearer "}
	for class, msg := range clientMessages {
		lower := strings.ToLower(msg)
		for _, bad := range forbidden {
			if strings.Contains(lower, strings.ToLower(bad)) {
				t.Errorf("class %q message %q contains infrastructure detail %q", class, msg, bad)
			}
		}
		// A status code in the user-facing text is the other common leak.
		for _, code := range []string{"429", "500", "502", "401", "402", "http "} {
			if strings.Contains(lower, code) {
				t.Errorf("class %q message %q exposes an upstream status (%q)", class, msg, code)
			}
		}
	}
}

// TestModelError_UpstreamBodyNeverReachesTheWire drives a real prompt through
// the real socket against an upstream whose error body is full of things that
// must not travel, and greps every byte the client received.
func TestModelError_UpstreamBodyNeverReachesTheWire(t *testing.T) {
	const secretish = `{"error":{"message":"upstream host 10.1.2.3:8443 rejected key sk-live-ABCDEF"}}`
	srv := upstreamReturning(t, http.StatusInternalServerError, secretish, nil)

	s := &Server{
		apiBase:       srv.URL,
		cfg:           &Config{},
		modelOverride: "test/model",
		logger:        discardLogger(),
		workspace:     "/workspace/x",
	}

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		s.serveConn(serverConn)
		serverConn.Close()
		close(done)
	}()

	enc := json.NewEncoder(clientConn)
	dec := json.NewDecoder(clientConn)
	_ = enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"})
	var hs protocol.HandshakeResponse
	_ = dec.Decode(&hs)
	_ = enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "hello"})

	var wire strings.Builder
	var gotClass string
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("decoding token response: %v", err)
		}
		raw, _ := json.Marshal(tok)
		wire.Write(raw)
		if tok.Done {
			gotClass = tok.ErrorClass
			break
		}
	}
	<-done

	for _, bad := range []string{"10.1.2.3", "sk-live-ABCDEF", "8443", srv.URL} {
		if strings.Contains(wire.String(), bad) {
			t.Errorf("upstream detail %q reached the wire: %s", bad, wire.String())
		}
	}
	if gotClass != string(ClassUpstreamUnavailable) {
		t.Errorf("ErrorClass = %q, want %q", gotClass, ClassUpstreamUnavailable)
	}
}

// TestModelError_PreservesZDRSentinel confirms the privacy refusal still
// satisfies the errors.Is check callers already use.
func TestModelError_PreservesZDRSentinel(t *testing.T) {
	me := classifyHTTPError(http.StatusNotFound, "404 Not Found",
		`{"error":{"message":"No allowed providers are available for the selected model."}}`)
	if me.Class != ClassPrivacyRefused {
		t.Fatalf("Class = %q, want %q", me.Class, ClassPrivacyRefused)
	}
	if !errors.Is(error(me), ErrZDRRefused) {
		t.Error("errors.Is(err, ErrZDRRefused) = false; existing callers branch on this")
	}
}

// TestParseRetryAfter covers both documented header forms plus the values that
// must be ignored rather than trusted.
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
		exact  bool
	}{
		{header: "", want: 0, exact: true},
		{header: "5", want: 5 * time.Second, exact: true},
		{header: "0", want: 0, exact: true},
		{header: "-3", want: 0, exact: true},
		{header: "not-a-number", want: 0, exact: true},
		{header: time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat), want: 3 * time.Second, exact: false},
		{header: time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), want: 0, exact: true},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.header != "" {
			h.Set("Retry-After", tc.header)
		}
		got := parseRetryAfter(h)
		if tc.exact && got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.header, got, tc.want)
		}
		if !tc.exact && (got <= 0 || got > tc.want+time.Second) {
			t.Errorf("parseRetryAfter(%q) = %v, want roughly %v", tc.header, got, tc.want)
		}
	}
}
