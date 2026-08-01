package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// THE SEAM THE UNIT TESTS DO NOT COVER.
//
// Everything else about consent is tested against runAgentLoop directly, with
// an approver handed in. That leaves the part production actually depends on
// untested: serveConn deciding this client may be asked, handing its decoder
// and its limitedConn to the turn, and connApprover reading an answer back off
// the same connection the tokens are streaming down.
//
// Getting that wiring wrong fails in the two worst possible ways -- a daemon
// that hangs for five minutes per call because it is reading from the wrong
// place, or one that never asks at all -- and neither is visible from either
// side alone.
func agentSocketServer(t *testing.T, apiBase string, cfg MCPConfig) (sockPath, auditPath string, srv *Server) {
	t.Helper()

	root, err := editapply.ResolveRealWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("resolving workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("HELLO"), 0600); err != nil {
		t.Fatal(err)
	}

	auditPath = filepath.Join(t.TempDir(), "toolcalls.jsonl")
	srv = &Server{
		logger:        discardLogger(),
		workspace:     root,
		modelOverride: "test-model",
		apiBase:       apiBase,
		apiKey:        "k",
		cfg:           &Config{MCP: cfg},
		counters:      &counters{},
		toolAudit:     newToolAuditSink(auditPath),
	}

	sockPath = filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go srv.Serve(ln)
	return sockPath, auditPath, srv
}

// agentClient dials, handshakes with the given capabilities, and sends prompt.
func agentClient(t *testing.T, sockPath, prompt string, caps []string) (*json.Encoder, *json.Decoder, net.Conn) {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	if err := enc.Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientName:      "socket-test",
		Capabilities:    caps,
	}); err != nil {
		t.Fatalf("handshake send: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake recv: %v", err)
	}
	if !hs.Ok {
		t.Fatalf("handshake refused: %s", hs.Error)
	}
	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: prompt}); err != nil {
		t.Fatalf("prompt send: %v", err)
	}
	return enc, dec, conn
}

// A whole agent turn over a real socket: the daemon asks, a client answers on
// the same connection, the tool runs, and the turn finishes normally.
func TestApprovalRoundTripOverTheRealSocket(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("It says HELLO."),
	)
	sockPath, auditPath, srv := agentSocketServer(t, base, MCPConfig{Enabled: true})
	enc, dec, conn := agentClient(t, sockPath, "what is in inside.txt?", []string{protocol.CapToolApproval})

	// Nothing should take anywhere near this; it exists so a wiring bug shows up
	// as a failed test rather than a five-minute hang.
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}

	var asked *protocol.ToolApprovalRequest
	var answer string
	var done protocol.TokenResponse
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.ToolApproval != nil {
			asked = tok.ToolApproval
			// Answering here, mid-stream, on the same connection, is the whole
			// property under test.
			if err := enc.Encode(protocol.ToolApprovalResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Approval:        true,
				CallID:          asked.CallID,
				ArgumentsSHA256: asked.ArgumentsSHA256,
				Decision:        protocol.ApprovalApprove,
			}); err != nil {
				t.Fatalf("sending approval: %v", err)
			}
			answer = protocol.ApprovalApprove
		}
		if tok.Token != "" {
			// nothing to assert per token; the Done message carries the outcome
			_ = tok.Token
		}
		if tok.Done {
			done = tok
			break
		}
	}

	if asked == nil {
		t.Fatal("the daemon ran an agent turn without ever asking; policy 'ask' was not honoured over the wire")
	}
	if answer == "" {
		t.Fatal("no answer was sent; the test's own premise is broken")
	}
	if asked.Arguments != `{"path":"inside.txt"}` {
		t.Errorf("the prompt on the wire showed %q", asked.Arguments)
	}
	if done.Error != "" {
		t.Fatalf("the turn ended in an error: %s", done.Error)
	}
	if done.Incomplete != nil {
		t.Errorf("an approved turn came back incomplete: %+v", done.Incomplete)
	}
	if got := srv.counters.snapshot().ToolCallsApproved; got != 1 {
		t.Errorf("ToolCallsApproved = %d over the socket path, want 1", got)
	}

	events := readAudit(t, auditPath)
	if len(events) != 1 || events[0].Source != auditUserApprove {
		t.Fatalf("the socket path did not audit the approval: %+v", events)
	}
}

// A CLIENT THAT CANNOT BE ASKED IS NEVER ASKED.
//
// agentModeEngaged requires the capability, so a client that did not declare it
// gets the single-turn path -- byte-identical to what it got before agent mode
// existed. That is what makes it safe to ship the loop before every client can
// render a prompt, and it is the reason the capability is a promise rather than
// a hint: a client that declared it and could not answer would leave every call
// hanging until the human deadline expired.
func TestAClientWithoutTheCapabilityGetsNoToolsAndNoQuestions(t *testing.T) {
	base, calls, bodies := agentUpstream(t, textSSE("plain answer"))
	sockPath, auditPath, _ := agentSocketServer(t, base, MCPConfig{Enabled: true})
	_, dec, conn := agentClient(t, sockPath, "hello", nil)

	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.ToolApproval != nil {
			t.Fatalf("the daemon asked a client that never said it could answer: %+v", tok.ToolApproval)
		}
		if tok.Done {
			break
		}
	}

	if calls.Load() != 1 {
		t.Errorf("a non-agent client caused %d model calls, want 1", calls.Load())
	}
	// The request body is the ZDR-verified one: no tools field at all.
	if len(*bodies) != 1 {
		t.Fatalf("expected exactly one upstream request, got %d", len(*bodies))
	}
	var body map[string]any
	if err := json.Unmarshal((*bodies)[0], &body); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if _, present := body["tools"]; present {
		t.Errorf("a client without the capability still had tools put on the wire: %s", (*bodies)[0])
	}
	if events := readAudit(t, auditPath); len(events) != 0 {
		t.Errorf("a non-agent turn wrote tool audit records: %+v", events)
	}
}
