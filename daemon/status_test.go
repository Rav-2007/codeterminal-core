package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// statusOverSocket drives a real Server over a real socketpair through the
// full production path -- handshake, then the request-discriminator peek in
// serveConn -- rather than calling handleStatus directly. That is the door a
// client actually comes through, and it is what proves the peek routes
// {"status":true} to status instead of letting it fall through to the prompt
// path (where it previously came back "prompt is empty").
func statusOverSocket(t *testing.T, s *Server) protocol.StatusResponse {
	t.Helper()

	client, server := net.Pipe()
	go func() {
		s.serveConn(server)
		server.Close()
	}()
	defer client.Close()

	enc := json.NewEncoder(client)
	dec := json.NewDecoder(bufio.NewReader(client))

	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"}); err != nil {
		t.Fatalf("handshake encode: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake decode: %v", err)
	}
	if !hs.Ok {
		t.Fatalf("handshake refused: %s", hs.Error)
	}

	if err := enc.Encode(protocol.StatusRequest{ProtocolVersion: protocol.ProtocolVersion, Status: true}); err != nil {
		t.Fatalf("status encode: %v", err)
	}
	var resp protocol.StatusResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("status decode: %v", err)
	}
	return resp
}

func statusServer() *Server {
	return &Server{
		logger:       log.New(io.Discard, "", 0),
		cfg:          &Config{ConfigVersion: 1, DefaultTier: "primary", Tiers: map[string]ModelTier{"primary": {Slug: "a/b", Active: true}}},
		workspace:    "/tmp/ws",
		apiKey:       "k",
		systemPrompt: "sys",
	}
}

func TestStatusRoutesThroughTheRequestDiscriminator(t *testing.T) {
	s := statusServer()
	resp := statusOverSocket(t, s)

	if resp.DaemonVersion != daemonVersion {
		t.Errorf("daemon_version = %q, want %q", resp.DaemonVersion, daemonVersion)
	}
	if resp.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", resp.PID, os.Getpid())
	}
	if resp.Workspace != "/tmp/ws" {
		t.Errorf("workspace = %q, want /tmp/ws", resp.Workspace)
	}
	if !resp.APIKeyConfigured {
		t.Error("api_key_configured = false, want true")
	}
}

// The state that motivated the whole cluster must be distinguishable in the
// status output: semantic up, lexical down.
func TestStatusReportsRetrievalTiersSeparately(t *testing.T) {
	s := statusServer()
	s.embedder = &fakeEmbedder{}
	s.store = fixedStore{chunks: []Chunk{{FilePath: "a.go"}, {FilePath: "b.go"}}}
	s.lexicalStore = nil
	s.retrievalTopK = 5
	s.contextBudgetChars = 8000

	resp := statusOverSocket(t, s)

	if !resp.Retrieval.Enabled {
		t.Error("retrieval.enabled = false, want true (the semantic tier is up)")
	}
	if resp.Retrieval.Lexical {
		t.Error("retrieval.lexical = true, want false — a single retrieval boolean would re-hide exactly this state")
	}
	if resp.Retrieval.IndexedChunks != 2 {
		t.Errorf("indexed_chunks = %d, want 2", resp.Retrieval.IndexedChunks)
	}
	var sawLexical bool
	for _, d := range resp.Degraded {
		if d.Component == protocol.DegradedLexicalRetrieval {
			sawLexical = true
		}
	}
	if !sawLexical {
		t.Errorf("degraded = %v, want it to include lexical_retrieval — the pulled and pushed views must agree", resp.Degraded)
	}
}

func TestStatusReportsRetrievalOffWithItsSpecificReason(t *testing.T) {
	s := statusServer()
	s.retrievalDisabledReason = reasonNoIndex

	resp := statusOverSocket(t, s)

	if resp.Retrieval.Enabled {
		t.Error("retrieval.enabled = true, want false")
	}
	if resp.Retrieval.Reason != reasonNoIndex {
		t.Errorf("reason = %q, want the specific cause %q", resp.Retrieval.Reason, reasonNoIndex)
	}
}

func TestStatusReportsMemoryAndConfigWarnings(t *testing.T) {
	s := statusServer()
	s.memory = nil
	s.cfg.warnings = []string{`unknown config key "retreival" is being IGNORED`}

	resp := statusOverSocket(t, s)

	if resp.MemoryAvailable {
		t.Error("memory_available = true, want false")
	}
	if len(resp.ConfigWarnings) != 1 || !strings.Contains(resp.ConfigWarnings[0], "retreival") {
		t.Errorf("config_warnings = %v, want the startup warning carried through", resp.ConfigWarnings)
	}
}

// Gate 7 removed the provider host from everything crossing the socket. A
// status surface is no reason to put it back.
func TestStatusCarriesNoAPIBase(t *testing.T) {
	s := statusServer()
	s.apiBase = "https://secret-provider.example.com/v1"

	resp := statusOverSocket(t, s)

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret-provider.example.com", "https://"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("status response contains %q: %s", leak, raw)
		}
	}
}

// A prompt must still be a prompt: adding the status discriminator must not
// have captured ordinary traffic.
func TestPromptStillRoutesToThePromptPath(t *testing.T) {
	s := statusServer()

	client, server := net.Pipe()
	go func() {
		s.serveConn(server)
		server.Close()
	}()
	defer client.Close()

	enc := json.NewEncoder(client)
	dec := json.NewDecoder(bufio.NewReader(client))
	enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"})
	var hs protocol.HandshakeResponse
	dec.Decode(&hs)

	// An empty prompt is refused by the prompt path with a known error; a
	// StatusResponse has no such field, so this distinguishes the two routes.
	enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: ""})
	var tok protocol.TokenResponse
	if err := dec.Decode(&tok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tok.Error != "prompt is empty" {
		t.Errorf("error = %q, want the prompt path's empty-prompt refusal", tok.Error)
	}
}

func TestIsStatusRequestDoesNotCaptureOtherMessages(t *testing.T) {
	tests := map[string]bool{
		`{"status":true}`:                 true,
		`{"status":false}`:                true, // present-but-false is still a StatusRequest shape
		`{"prompt":"hello"}`:              false,
		`{"undo":true}`:                   false,
		`{"search":true,"query":"x"}`:     false,
		`{"edit":{"file_path":"a.go"}}`:   false,
		`{"prompt":"what is my status?"}`: false,
	}
	for raw, want := range tests {
		if got := isStatusRequest(json.RawMessage(raw)); got != want {
			t.Errorf("isStatusRequest(%s) = %v, want %v", raw, got, want)
		}
	}
}

// --log-file must capture what stderr captures, and must not replace it.
func TestLogWriterTeesToFileAndStderr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")

	w, closeLog, err := newLogWriter(path)
	if err != nil {
		t.Fatalf("newLogWriter: %v", err)
	}
	log.New(w, "", 0).Print("degraded: component=memory")
	closeLog()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	if !strings.Contains(string(body), "degraded: component=memory") {
		t.Errorf("log file = %q, want the logged line", body)
	}
	assertOwnerOnly(t, path, "a daemon log carries workspace paths and prompt sizes")
}

func TestLogWriterDefaultsToStderrWhenUnset(t *testing.T) {
	w, closeLog, err := newLogWriter("")
	defer closeLog()
	if err != nil {
		t.Fatalf("newLogWriter(\"\") = %v, want nil", err)
	}
	if w != os.Stderr {
		t.Error("an unset --log-file must be exactly stderr, changing nothing")
	}
}

// Bounded growth: the file must rotate rather than grow forever, or this
// would trade one operational problem for another.
func TestRotatingFileBoundsGrowth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")

	rf, err := openRotatingFile(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 64) + "\n")
	for i := 0; i < 40; i++ {
		if _, err := rf.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	rf.Close()

	active, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("expected a rotated .1 backup: %v", err)
	}
	if total := active.Size() + backup.Size(); total > 2*256+int64(len(line)) {
		t.Errorf("total on-disk size %d exceeds the ~2x bound", total)
	}
}
