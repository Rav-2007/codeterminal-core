package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
	"mochiii/protocol"
)

func TestQuestionsAboutMochiiiAreRecognised(t *testing.T) {
	for _, p := range []string{"what is your name", "What's your name?", "who are you", "what can you do",
		"who made you", "what model are you", "are you an AI?", "tell me about yourself", "how do you work",
		"what can you help me with", "who are youuu"} {
		if !isAboutAssistant(p) {
			t.Errorf("isAboutAssistant(%q) = false, want true", p)
		}
	}
}

// Anything that names code, or is not addressed to Mochiii, is not about it.
func TestCodeQuestionsAreNotAboutMochiii(t *testing.T) {
	for _, p := range []string{"", "what can you tell me about retry.go", "can you fix it",
		"what does maxStreamAttempts do", "what is the model", "can you build it", "what is `your` name",
		"who are you calling in handleConn", "can you help me with the retry bug", "what is your plan"} {
		if isAboutAssistant(p) {
			t.Errorf("isAboutAssistant(%q) = true, want false", p)
		}
	}
}

// Neuter check: drop the isAboutAssistant skip in gatherContext.
func TestAQuestionAboutMochiiiGetsNoRetrievedCode(t *testing.T) {
	s := &Server{
		logger:             discardLogger(),
		embedder:           &fakeEmbedder{dim: embedDim},
		store:              fixedStore{chunks: []Chunk{lineChunk("config.go", 1, 20)}},
		retrievalTopK:      defaultK,
		contextBudgetChars: 1 << 20,
	}
	if got := s.gatherContext(t.Context(), "what does the config loader do"); got.Skipped || len(got.Chunks) == 0 {
		t.Fatalf("control: a real question got no code (%+v); the harness proves nothing", got)
	}
	for _, q := range []string{"what is your name", "who are you", "what can you do"} {
		got := s.gatherContext(t.Context(), q)
		if !got.Skipped || len(got.Chunks) != 0 || !strings.Contains(got.Reason, "Mochiii") {
			t.Errorf("gatherContext(%q) = %d chunk(s), reason %q; a question about Mochiii must get none", q, len(got.Chunks), got.Reason)
		}
	}
}

// promptOnce sends one prompt through serveConn and returns what the client
// was told about grounding, the first request the model received, and what
// the embedder was asked to search for.
func promptOnce(t *testing.T, agent bool, prompt string) (*protocol.GroundingInfo, string, []string) {
	t.Helper()
	root, err := editapply.ResolveRealWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for n := 1; n <= 20; n++ {
		lines = append(lines, fmt.Sprintf("config.go line %d", n))
	}
	if err := os.WriteFile(filepath.Join(root, "config.go"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	base, _, bodies := agentUpstream(t, textSSE("an answer"))
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
	_ = enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: prompt})
	var grounding *protocol.GroundingInfo
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
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
		t.Fatalf("no grounding message (%v) or no model call (%d)", grounding, len(*bodies))
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return grounding, string((*bodies)[0]), append([]string(nil), rec.queries...)
}

// AN AGENT TURN SEARCHES FOR ITSELF: nothing is searched on its behalf before
// the first call, and what the prompt points at exactly still goes in.
//
// Neuter check: send agent turns back through gatherContext in serveConn and
// the first half fails; make gatherDirectRefs skip unconditionally and the
// second half does.
func TestAnAgentTurnSearchesForItself(t *testing.T) {
	grounding, body, queries := promptOnce(t, true, "how does the config loader pick its defaults")
	if len(queries) != 0 || strings.Contains(body, "retrieved_context") {
		t.Errorf("agent turn: searched %q or attached code before the model asked", queries)
	}
	if grounding.Grounded || grounding.Reason != agentSearchReason {
		t.Errorf("agent turn: grounding %+v, want skipped with %q", grounding, agentSearchReason)
	}

	grounding, body, queries = promptOnce(t, true, "why does config.go:3 say that")
	if !grounding.Grounded || !strings.Contains(body, "config.go line 3") {
		t.Errorf("agent turn: the file:line the prompt named was not attached (grounding %+v)", grounding)
	}
	if len(queries) != 0 {
		t.Errorf("agent turn: an exact reference also ran a search: %q", queries)
	}

	// Control: a turn without tools still gets code searched and attached.
	grounding, body, queries = promptOnce(t, false, "how does the config loader pick its defaults")
	if !grounding.Grounded || !strings.Contains(body, "config.go line 1") || len(queries) != 1 {
		t.Errorf("plain turn: no code searched and attached (grounding %+v, queries %q)", grounding, queries)
	}
}
