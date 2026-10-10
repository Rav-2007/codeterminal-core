package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mochiii/protocol"
)

// sendPromptCapturingBody runs one prompt through serveConn against a fake
// provider and returns the request body the provider received.
func sendPromptCapturingBody(t *testing.T, req protocol.PromptRequest) string {
	t.Helper()
	var mu sync.Mutex
	var body string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	srv := &Server{
		apiBase:       upstream.URL,
		apiKey:        "test-key",
		cfg:           &Config{},
		modelOverride: "test/model",
		logger:        discardLogger(),
		workspace:     "/workspace/effort",
	}
	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.serveConn(serverConn)
		serverConn.Close()
		close(done)
	}()
	enc, dec := json.NewEncoder(clientConn), json.NewDecoder(clientConn)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "effort-test"}); err != nil {
		t.Fatal(err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatal(err)
	}
	req.ProtocolVersion = protocol.ProtocolVersion
	if err := enc.Encode(req); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if tok.Done {
			break
		}
	}
	clientConn.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	return body
}

// The VS Code effort picker sends reasoning_effort per prompt; it must reach the
// model call. Before this field existed the picker changed nothing at all.
//
// Neuter check: delete the override after routingFor in server.go -- the first
// case fails.
func TestAPromptsReasoningEffortReachesTheModelCall(t *testing.T) {
	body := sendPromptCapturingBody(t, protocol.PromptRequest{Prompt: "think hard", ReasoningEffort: "high"})
	if !strings.Contains(body, `"reasoning":{"effort":"high"}`) {
		t.Errorf("the prompt's effort is not on the wire:\n%s", body)
	}
}

// A value outside low/medium/high is a client bug: it is dropped, never sent to
// be refused, and the turn still runs.
func TestAnUnknownReasoningEffortIsNotSent(t *testing.T) {
	body := sendPromptCapturingBody(t, protocol.PromptRequest{Prompt: "hi", ReasoningEffort: "extreme"})
	if body == "" {
		t.Fatal("the turn did not reach the provider")
	}
	if strings.Contains(body, "extreme") || strings.Contains(body, `"reasoning"`) {
		t.Errorf("an unknown effort reached the wire:\n%s", body)
	}
}

// No effort: the body is what it was before the field existed.
func TestNoReasoningEffortSendsNone(t *testing.T) {
	body := sendPromptCapturingBody(t, protocol.PromptRequest{Prompt: "hi"})
	if strings.Contains(body, `"reasoning"`) {
		t.Errorf("no effort was asked for, yet one was sent:\n%s", body)
	}
}
