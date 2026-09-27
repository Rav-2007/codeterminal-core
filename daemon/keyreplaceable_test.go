package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"mochiii/protocol"
)

// A SPENT OR REVOKED KEY MUST LEAD SOMEWHERE. The reported experience: the key's
// limit ran out, every prompt failed, and nothing the client showed led to a new
// key -- the error said to check "the key this daemon was started with". The
// daemon now says, beside a key failure, whether a key pasted through Connect
// would be the one the next request uses, so a client can ask for it there and
// then. These pin who decides that, and that it reaches the wire.
func TestKeyReplaceableOnlyForKeyFailuresTheStoredKeyCanFix(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   map[string]string
		class ModelErrorClass
		want  bool
	}{
		{"BYOK, rejected key", nil, ClassAuth, true},
		{"BYOK, spent key", nil, ClassQuotaExceeded, true},
		// A new key fixes neither of these, and offering one would be a lie.
		{"BYOK, rate limit", nil, ClassRateLimited, false},
		{"BYOK, provider down", nil, ClassUpstreamUnavailable, false},
		{"BYOK, unknown", nil, ClassUnknown, false},
		// The environment outranks a stored key, so a pasted one is not used.
		{"env key", map[string]string{"MOCHIII_API_KEY": "sk-from-env"}, ClassAuth, false},
		// Proxy mode authenticates with a different secret altogether.
		{"proxy mode", map[string]string{"MOCHIII_USE_PROXY": "true"}, ClassQuotaExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MOCHIII_API_KEY", "")
			t.Setenv("MOCHIII_USE_PROXY", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := keyReplaceable(tc.class); got != tc.want {
				t.Errorf("keyReplaceable(%s) = %v, want %v", tc.class, got, tc.want)
			}
		})
	}
}

// The same judgement, carried on the Done message of a real failed turn.
func TestARejectedKeyIsReportedAsReplaceableOnTheWire(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"User not found.","code":401}}`, http.StatusUnauthorized)
	}))
	defer provider.Close()

	for _, tc := range []struct {
		name   string
		envKey string
		want   bool
	}{
		{"stored key", "", true},
		{"key from the environment", "sk-from-env", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MOCHIII_API_KEY", tc.envKey)
			t.Setenv("MOCHIII_USE_PROXY", "")
			srv := &Server{
				apiKey:        "sk-a-revoked-key",
				apiBase:       provider.URL,
				cfg:           &Config{},
				modelOverride: "test/model-slug",
				logger:        log.New(io.Discard, "", 0),
			}
			done := doneOfOneTurn(t, srv)
			if done.ErrorClass != string(ClassAuth) {
				t.Fatalf("ErrorClass = %q, want %q (error %q)", done.ErrorClass, ClassAuth, done.Error)
			}
			if done.KeyReplaceable != tc.want {
				t.Errorf("KeyReplaceable = %v, want %v", done.KeyReplaceable, tc.want)
			}
		})
	}
}

// doneOfOneTurn sends one prompt over a pipe and returns the terminal message.
func doneOfOneTurn(t *testing.T, srv *Server) protocol.TokenResponse {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	finished := make(chan struct{})
	go func() {
		srv.serveConn(serverConn)
		serverConn.Close()
		close(finished)
	}()
	defer func() { clientConn.Close(); <-finished }()

	enc, dec := json.NewEncoder(clientConn), json.NewDecoder(clientConn)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake response: %v", err)
	}
	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the turn: %v", err)
		}
		if tok.Done {
			return tok
		}
	}
}
