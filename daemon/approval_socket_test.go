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
func agentSocketServer(t *testing.T, apiBase string, cfg MCPConfig) (sockAddr protocol.Address, auditPath string, srv *Server) {
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

	sockAddr = testAddress(t)
	ln, err := protocol.Listen(sockAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go srv.Serve(ln)
	return sockAddr, auditPath, srv
}

// agentClient dials, handshakes with the given capabilities, and sends prompt.
func agentClient(t *testing.T, sockAddr protocol.Address, prompt string, caps []string) (*json.Encoder, *json.Decoder, net.Conn) {
	t.Helper()
	conn, err := protocol.Dial(sockAddr)
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
	sockAddr, auditPath, srv := agentSocketServer(t, base, MCPConfig{Enabled: true})
	enc, dec, conn := agentClient(t, sockAddr, "what is in inside.txt?", []string{protocol.CapToolApproval})

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
	sockAddr, auditPath, _ := agentSocketServer(t, base, MCPConfig{Enabled: true})
	_, dec, conn := agentClient(t, sockAddr, "hello", nil)

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

// H5 -- A HOSTILE OR BUGGY CLIENT ON THE SHARED DECODER.
//
// The approval reply and the prompt stream travel the same connection and the
// daemon reads answers off one json.Decoder. The idempotence guards that stop a
// client answering twice are client-side (the VS Code webview's `answered` flag,
// the CLI's one-shot reader), which means they are exactly the guards a hostile
// client does not have. Registered in the 2026-08-01 gate and never run.
//
// The failure shape worth caring about is not a rejected answer, it is a
// DESYNCHRONISED one: a leftover reply sitting in the decoder's buffer, read
// later as consent for a call the user was never shown.

// Two answers for one ask. The second must not become consent for the next
// call, which is a different call with a different id.
//
// Fails if verifyApproval stops checking the call id.
func TestASecondAnswerDoesNotApproveTheNextCall(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("done"),
	)
	sockAddr, auditPath, _ := agentSocketServer(t, base, MCPConfig{Enabled: true})
	enc, dec, conn := agentClient(t, sockAddr, "read it twice", []string{protocol.CapToolApproval})
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}

	var asks []protocol.ToolApprovalRequest
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.ToolApproval != nil {
			asks = append(asks, *tok.ToolApproval)
			answer := protocol.ToolApprovalResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Approval:        true,
				CallID:          tok.ToolApproval.CallID,
				ArgumentsSHA256: tok.ToolApproval.ArgumentsSHA256,
				Decision:        protocol.ApprovalApprove,
			}
			// Answer, then answer AGAIN. The duplicate is what a buggy client
			// with a double-fired keypress sends, and what a hostile one sends
			// on purpose.
			if len(asks) == 1 {
				for i := 0; i < 2; i++ {
					if err := enc.Encode(answer); err != nil {
						t.Fatalf("sending approval %d: %v", i, err)
					}
				}
			} else {
				if err := enc.Encode(answer); err != nil {
					t.Fatalf("sending approval: %v", err)
				}
			}
		}
		if tok.Done {
			break
		}
	}

	// THE PROPERTY: the second call was asked about, on its own merits. If the
	// leftover answer had been consumed as its consent, there would be one ask.
	if len(asks) != 2 {
		t.Fatalf("%d approval prompt(s) for 2 calls. A duplicate answer to call 1 must not be read "+
			"as consent for call 2 -- the user would have authorised a call they never saw", len(asks))
	}
	if asks[0].CallID == asks[1].CallID {
		t.Fatal("the harness sent the same call id twice, so this proves nothing")
	}

	events := readAudit(t, auditPath)
	if len(events) != 2 {
		t.Fatalf("expected 2 audit records, got %d: %+v", len(events), events)
	}
	for i, e := range events {
		if e.Source != auditUserApprove {
			t.Errorf("event %d ran via %q rather than a user approval", i, e.Source)
		}
	}
}

// An approval sent BEFORE any question, with a guessed call id, is not consent.
//
// This is the pipelining attack in its most favourable form for the attacker:
// the reply is already sitting in the daemon's decoder when the first ask goes
// out, so it is the very next thing read. The digest and the call id are what
// make it fail, and a denial rather than an error is what makes the turn
// continue safely.
//
// Fails if verifyApproval stops checking the arguments digest.
func TestAnApprovalSentBeforeTheQuestionIsNotConsent(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("I could not read it."),
	)
	sockAddr, auditPath, _ := agentSocketServer(t, base, MCPConfig{Enabled: true})
	enc, dec, conn := agentClient(t, sockAddr, "read it", []string{protocol.CapToolApproval})
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}

	// Sent immediately after the prompt, before the daemon has decided anything.
	// The call id is a guess -- and it is the RIGHT guess, because this test
	// scripts the upstream and knows it is "c1". That is the strongest version
	// of the attack: only the digest stands in the way.
	if err := enc.Encode(protocol.ToolApprovalResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Approval:        true,
		CallID:          "c1",
		ArgumentsSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		Decision:        protocol.ApprovalApproveForTurn,
	}); err != nil {
		t.Fatalf("pipelining an approval: %v", err)
	}

	var sawAsk bool
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.ToolApproval != nil {
			sawAsk = true
		}
		if tok.Done {
			break
		}
	}

	if !sawAsk {
		t.Fatal("the daemon never asked, so the pre-sent answer was taken as consent outright")
	}
	events := readAudit(t, auditPath)
	if len(events) != 1 {
		t.Fatalf("expected 1 audit record, got %d: %+v", len(events), events)
	}
	if events[0].Outcome != auditOutcomeRefused {
		t.Errorf("a pre-sent approval with a forged digest ran the tool: %+v", events[0])
	}
	if events[0].DenyCause != denyByMismatch {
		t.Errorf("deny cause %q; a wrong digest is a mismatch, and it must be one so the audit "+
			"distinguishes 'the user said no' from 'somebody answered a different question'",
			events[0].DenyCause)
	}
}
