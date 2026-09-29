package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mochiii/editapply"
	"mochiii/protocol"
)

// The follow-ups the 2026-09-29 audit measured each drawing ~30 KB of
// unrelated code, and others like them.
func TestFollowUpsAreRecognised(t *testing.T) {
	for _, p := range []string{"continue", "go on", "yes do it", "ok fix it", "try again", "why?",
		"thanks! now add a test for that", "explain more", "keep going", "do the same for the other one",
		"that's wrong, fix it", "show me", "and then?", "ok apply it", "continueeee", "Redo it please"} {
		if !isFollowUp(p) {
			t.Errorf("isFollowUp(%q) = false, want true", p)
		}
	}
}

// Anything that names something is a request in its own right and keeps its
// code search: an identifier, a file, a code word, or simply length.
func TestRealQuestionsAreNotFollowUps(t *testing.T) {
	for _, p := range []string{"", "fix the retry bug", "how does streamWithRetry work", "add a test for prepareHistory",
		"explain the agent loop", "why does the index go stale", "what is maxHistoryTurns", "fix config.go",
		"show me the history code", "try the proxy", "add logging", "go test ./... fails", "explain `gatherContext`",
		"ok now do it and then fix it and add a test for it too please", "fix it in a.go", "show me `that`"} {
		if isFollowUp(p) {
			t.Errorf("isFollowUp(%q) = true, want false", p)
		}
	}
}

func TestRetrievalQueryForAFollowUp(t *testing.T) {
	history := []chatMessage{
		{Role: "user", Content: "how does the retry path decide to try again"},
		{Role: "assistant", Content: "It retries only before the first token."},
		{Role: "user", Content: "continue"},
		{Role: "assistant", Content: "And it backs off with jitter."},
	}
	cases := []struct {
		name, prompt string
		history      []chatMessage
		agent        bool
		query, skip  string
	}{
		{"agent mode: no code", "ok fix it", history, true, "", followUpSkipReason},
		{"plain: searched with the question it follows", "ok fix it", history, false,
			"how does the retry path decide to try again\nok fix it", ""},
		{"nothing to follow", "continue", nil, true, "continue", ""},
		{"a real question", "explain gatherContext", history, true, "explain gatherContext", ""},
		{"small talk keeps its own path", "thanks", history, true, "thanks", ""},
	}
	for _, c := range cases {
		q, skip := retrievalQueryFor(c.prompt, c.history, c.agent)
		if q != c.query || skip != c.skip {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, q, skip, c.query, c.skip)
		}
	}
}

// queryRecorder is an embedder that remembers what it was asked to search for.
type queryRecorder struct {
	fakeEmbedder
	mu      sync.Mutex
	queries []string
}

func (r *queryRecorder) EmbedQuery(ctx context.Context, texts []string) ([][]float32, error) {
	r.mu.Lock()
	r.queries = append(r.queries, texts...)
	r.mu.Unlock()
	return r.fakeEmbedder.Embed(ctx, texts)
}

// Through the prompt path: in agent mode a follow-up reaches the model with no
// code and says why; without tools it is searched with the question it follows.
func TestAFollowUpOverTheSocket(t *testing.T) {
	for _, agent := range []bool{true, false} {
		root, err := editapply.ResolveRealWorkspaceRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// On disk as well as in the store, so the hit is current code.
		var lines []string
		for n := 1; n <= 20; n++ {
			lines = append(lines, fmt.Sprintf("config.go line %d", n))
		}
		if err := os.WriteFile(filepath.Join(root, "config.go"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		base, _, bodies := agentUpstream(t, textSSE("fixed"))
		rec := &queryRecorder{fakeEmbedder: fakeEmbedder{dim: embedDim}}
		s := &Server{
			apiBase: base, apiKey: "k", modelOverride: "test-model", logger: discardLogger(), workspace: root,
			cfg:                &Config{MCP: MCPConfig{Enabled: agent}},
			embedder:           rec,
			store:              fixedStore{chunks: []Chunk{lineChunk("config.go", 1, 20)}},
			retrievalTopK:      defaultK,
			contextBudgetChars: 1 << 20,
		}
		clientConn, serverConn := net.Pipe()
		done := make(chan struct{})
		go func() { s.serveConn(serverConn); _ = serverConn.Close(); close(done) }()
		enc, dec := json.NewEncoder(clientConn), json.NewDecoder(clientConn)
		_ = enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test",
			Capabilities: []string{protocol.CapToolApproval}})
		var hs protocol.HandshakeResponse
		_ = dec.Decode(&hs)
		_ = enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "ok fix it",
			History: []protocol.Turn{{Role: "user", Content: "how does the retry path decide to try again"},
				{Role: "assistant", Content: "It retries early."}}})
		var grounding *protocol.GroundingInfo
		for {
			var tok protocol.TokenResponse
			if err := dec.Decode(&tok); err != nil {
				t.Fatalf("agent=%v: reading the stream: %v", agent, err)
			}
			if tok.Grounding != nil {
				grounding = tok.Grounding
			}
			if tok.Done {
				break
			}
		}
		_ = clientConn.Close()
		<-done
		if grounding == nil || len(*bodies) == 0 {
			t.Fatalf("agent=%v: no grounding message (%v) or no model call (%d)", agent, grounding, len(*bodies))
		}
		body := string((*bodies)[0])
		if agent {
			if grounding.Grounded || grounding.Reason != followUpSkipReason {
				t.Errorf("agent mode: grounding %+v, want skipped with %q", grounding, followUpSkipReason)
			}
			if strings.Contains(body, "retrieved_context") || len(rec.queries) != 0 {
				t.Errorf("agent mode: code was searched (%q) or attached", rec.queries)
			}
			continue
		}
		if !grounding.Grounded || !strings.Contains(body, "config.go line 1") {
			t.Errorf("plain: no code attached (grounding %+v)", grounding)
		}
		if len(rec.queries) != 1 || !strings.Contains(rec.queries[0], "how does the retry path") {
			t.Errorf("plain: searched for %q, want the question it follows", rec.queries)
		}
	}
}
