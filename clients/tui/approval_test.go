package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

func pendingApprovalModel(t *testing.T, req protocol.ToolApprovalRequest) (chatModel, chan string) {
	t.Helper()
	reply := make(chan string, 1)
	m := newChatModel("test", "/w", "/w", nil)
	m.state = stateStreaming
	m.streamCh = make(chan tea.Msg, 4)
	m.turns = []turn{{role: roleUser, text: "go"}}

	next, _ := m.Update(toolApprovalMsg{req: req, reply: reply})
	updated := next.(chatModel)
	if updated.state != stateToolApproval {
		t.Fatalf("an approval request did not pause the turn; state = %v", updated.state)
	}
	return updated, reply
}

func laneBRequest() protocol.ToolApprovalRequest {
	args := `{"path":"/etc/passwd"}`
	return protocol.ToolApprovalRequest{
		CallID:          "call-1",
		Server:          "somebodys-server",
		Tool:            "read_anything",
		Arguments:       args,
		ArgumentsSHA256: "deadbeef",
		Lane:            protocol.LaneThirdParty,
		Confined:        false,
		Iteration:       2,
		MaxIterations:   8,
	}
}

// ONLY A LITERAL y OR a APPROVES, AND NOTHING ELSE DECIDES ANYTHING.
//
// The second half matters as much as the first. A stray keystroke from someone
// typing into what they thought was the input box must not answer a security
// question for them -- in either direction. So every key that is not one of the
// four bindings leaves the prompt exactly where it was.
func TestOnlyALiteralYesApprovesAToolCall(t *testing.T) {
	approving := map[string]string{
		"y": protocol.ApprovalApprove,
		"a": protocol.ApprovalApproveForTurn,
	}
	refusing := map[string]string{
		"n":   protocol.ApprovalDeny,
		"q":   protocol.ApprovalCancelTurn,
		"esc": protocol.ApprovalCancelTurn,
	}
	inert := []string{"Y", "A", "N", "enter", "space", "tab", "j", "1", "up", "pgdown", "ctrl+n"}

	for key, want := range approving {
		t.Run("approve/"+key, func(t *testing.T) {
			m, reply := pendingApprovalModel(t, laneBRequest())
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			if got := <-reply; got != want {
				t.Errorf("%q sent %q, want %q", key, got, want)
			}
			if next.(chatModel).state != stateStreaming {
				t.Errorf("answering did not resume the turn")
			}
		})
	}

	for key, want := range refusing {
		t.Run("refuse/"+key, func(t *testing.T) {
			m, reply := pendingApprovalModel(t, laneBRequest())
			var msg tea.KeyMsg
			if key == "esc" {
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			} else {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
			}
			m.Update(msg)
			if got := <-reply; got != want {
				t.Errorf("%q sent %q, want %q", key, got, want)
			}
		})
	}

	for _, key := range inert {
		t.Run("inert/"+key, func(t *testing.T) {
			m, reply := pendingApprovalModel(t, laneBRequest())
			var msg tea.KeyMsg
			switch key {
			case "enter":
				msg = tea.KeyMsg{Type: tea.KeyEnter}
			case "space":
				msg = tea.KeyMsg{Type: tea.KeySpace}
			case "tab":
				msg = tea.KeyMsg{Type: tea.KeyTab}
			case "up":
				msg = tea.KeyMsg{Type: tea.KeyUp}
			case "pgdown":
				msg = tea.KeyMsg{Type: tea.KeyPgDown}
			case "ctrl+n":
				msg = tea.KeyMsg{Type: tea.KeyCtrlN}
			default:
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
			}
			next, _ := m.Update(msg)

			select {
			case got := <-reply:
				t.Fatalf("%q answered the approval prompt with %q; it must decide nothing", key, got)
			default:
			}
			if next.(chatModel).state != stateToolApproval {
				t.Errorf("%q dismissed the approval prompt without answering it", key)
			}
		})
	}
}

// The panel must show what will actually run, in full, and must not soften what
// the product cannot do about it. "NOT SANDBOXED" is the honest description of
// an ordinary subprocess holding the user's own privileges, and dressing it up
// would be the single most damaging sentence here.
func TestTheApprovalPanelIsCompleteAndHonestAboutTheLane(t *testing.T) {
	req := laneBRequest()
	panel := renderApprovalPanel(req)

	if !strings.Contains(panel, req.Arguments) {
		t.Errorf("the panel does not show the arguments that will run:\n%s", panel)
	}
	if !strings.Contains(panel, req.Server) || !strings.Contains(panel, req.Tool) {
		t.Errorf("the panel does not name the tool:\n%s", panel)
	}
	if !strings.Contains(panel, "NOT SANDBOXED") {
		t.Errorf("an unconfined third-party tool was not described as unconfined:\n%s", panel)
	}
	if !strings.Contains(panel, "2") || !strings.Contains(panel, "8") {
		t.Errorf("the panel gives no sense of how far into a loop this is:\n%s", panel)
	}

	// A first-party tool must NOT carry the warning -- an alarm on everything is
	// an alarm on nothing.
	confined := req
	confined.Lane, confined.Confined, confined.Server = protocol.LaneFirstParty, true, "builtin"
	if got := renderApprovalPanel(confined); strings.Contains(got, "NOT SANDBOXED") {
		t.Errorf("a confined built-in was labelled unsandboxed:\n%s", got)
	}
}

// CAPABILITY IS A PROMISE, NOT A BINARY FEATURE FLAG.
//
// The daemon runs the agentic loop only for a client that declares it can
// answer a mid-stream approval, and then suspends a turn waiting for one. So
// the declaration has to track what the CALLER can do, not what the binary
// can: an interactive chat can render a modal, a piped one-shot run has stdin
// already spent on the prompt and nobody to ask. Declaring it without a person
// there hangs every tool call until the five-minute deadline expires.
//
// This drives the three real entry points against a fake daemon and reads the
// handshake each one actually sent.
func TestOnlyThePathsThatCanAnswerDeclareTheCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
		want bool
	}{
		{
			name: "the chat streaming path",
			want: true,
			run: func(t *testing.T) {
				ch := make(chan tea.Msg, 8)
				go streamPrompt(context.Background(), "test", "/w", "hi", "", "", "", nil, nil, ch)
				waitForStreamEnd(t, ch)
			},
		},
		{
			name: "a piped one-shot run",
			want: false,
			run: func(t *testing.T) {
				runOneShotPrompt("test", "hi", oneShotIO{
					in: strings.NewReader(""), out: io.Discard, err: io.Discard, interactive: false,
				})
			},
		},
		{
			name: "an interactive one-shot run",
			want: true,
			run: func(t *testing.T) {
				runOneShotPrompt("test", "hi", oneShotIO{
					in: strings.NewReader(""), out: io.Discard, err: io.Discard, interactive: true,
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handshakes := startRecordingDaemon(t)
			tc.run(t)

			select {
			case hs := <-handshakes:
				if got := hs.HasCapability(protocol.CapToolApproval); got != tc.want {
					t.Errorf("declared %q = %t, want %t (capabilities: %v)",
						protocol.CapToolApproval, got, tc.want, hs.Capabilities)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no handshake arrived")
			}
		})
	}
}

// startRecordingDaemon stands up a fake daemon that records each handshake it
// receives and answers a prompt with an immediate Done.
func startRecordingDaemon(t *testing.T) <-chan protocol.HandshakeRequest {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath := filepath.Join(dir, "daemon.lock")

	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setLockPathForTest(t, lockPath))

	handshakes := make(chan protocol.HandshakeRequest, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
				var hs protocol.HandshakeRequest
				if err := dec.Decode(&hs); err != nil {
					return
				}
				handshakes <- hs
				_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})
				var req protocol.PromptRequest
				if err := dec.Decode(&req); err != nil {
					return
				}
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
			}()
		}
	}()
	return handshakes
}

func waitForStreamEnd(t *testing.T, ch chan tea.Msg) {
	t.Helper()
	for {
		select {
		case msg := <-ch:
			switch msg.(type) {
			case streamDoneMsg, streamErrMsg:
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the stream never finished")
		}
	}
}

// THE ANSWER MUST ECHO THE QUESTION, UNCHANGED.
//
// The daemon re-checks the call id and the argument digest before dispatching.
// A client that recomputed the digest from its own copy would be attesting to
// its own rendering rather than to the bytes it was sent, which is exactly the
// gap the binding exists to close -- so this asserts the echo is verbatim, and
// that a decline is reported as one rather than simply not answered.
func TestTheApprovalAnswerEchoesTheQuestionVerbatim(t *testing.T) {
	for _, tc := range []struct {
		decision string
		approval bool
	}{
		{protocol.ApprovalApprove, true},
		{protocol.ApprovalApproveForTurn, true},
		{protocol.ApprovalDeny, false},
		{protocol.ApprovalCancelTurn, false},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			clientEnd, daemonEnd := net.Pipe()
			defer clientEnd.Close()
			defer daemonEnd.Close()

			sess := &daemonSession{conn: clientEnd, enc: json.NewEncoder(clientEnd), dec: json.NewDecoder(clientEnd)}
			req := laneBRequest()
			ch := make(chan tea.Msg, 1)

			answered := make(chan bool, 1)
			go func() { answered <- askForApproval(context.Background(), sess, req, ch) }()

			msg := (<-ch).(toolApprovalMsg)
			if msg.req.CallID != req.CallID {
				t.Fatalf("the UI was shown a different call: %+v", msg.req)
			}
			msg.reply <- tc.decision

			var resp protocol.ToolApprovalResponse
			if err := json.NewDecoder(daemonEnd).Decode(&resp); err != nil {
				t.Fatalf("no answer reached the daemon: %v", err)
			}
			if resp.CallID != req.CallID || resp.ArgumentsSHA256 != req.ArgumentsSHA256 {
				t.Errorf("the answer does not echo the question: %+v", resp)
			}
			if resp.Decision != tc.decision {
				t.Errorf("decision = %q, want %q", resp.Decision, tc.decision)
			}
			if resp.Approval != tc.approval {
				t.Errorf("approval = %t for decision %q, want %t", resp.Approval, tc.decision, tc.approval)
			}
			if !<-answered {
				t.Error("streaming was abandoned after a successful answer")
			}
		})
	}
}

// A user who quits while the prompt is on screen must not leave the stream
// goroutine parked on a channel nobody will ever send to. streamPrompt's
// conn-closing watcher cannot unblock a channel receive, so the context case is
// the only thing that can.
func TestQuittingDuringAnApprovalDoesNotStrandTheStream(t *testing.T) {
	clientEnd, daemonEnd := net.Pipe()
	defer clientEnd.Close()
	defer daemonEnd.Close()

	sess := &daemonSession{conn: clientEnd, enc: json.NewEncoder(clientEnd), dec: json.NewDecoder(clientEnd)}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 1)

	done := make(chan bool, 1)
	go func() { done <- askForApproval(ctx, sess, laneBRequest(), ch) }()

	<-ch // the prompt reached the UI
	cancel()

	select {
	case cont := <-done:
		if cont {
			t.Error("a cancelled turn reported that streaming should continue")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream goroutine is stranded waiting for an answer that will never come")
	}
}

// TRANSCRIPT INTEGRITY. Before agent mode the last turn was always the
// assistant's, so tokens appended to turns[len-1]. Tool-activity notices break
// that assumption, and appending an answer into a system line is how a
// transcript starts attributing the model's words to the product's own chrome.
func TestTokensAfterAToolNoticeStayInTheAssistantTurn(t *testing.T) {
	m := newChatModel("test", "/w", "/w", nil)
	m.state = stateSending
	m.streamCh = make(chan tea.Msg, 8)
	m.turns = []turn{{role: roleUser, text: "go"}}

	step := func(msg tea.Msg) {
		next, _ := m.Update(msg)
		m = next.(chatModel)
	}

	step(tokenMsg("Let me look. "))
	step(toolActivityMsg{protocol.ToolActivity{CallID: "c1", Server: "builtin", Tool: "read_file", Phase: protocol.ToolPhaseRunning}})
	step(toolActivityMsg{protocol.ToolActivity{CallID: "c1", Server: "builtin", Tool: "read_file", Phase: protocol.ToolPhaseSucceeded, ResultBytes: 42, DurationMS: 7}})
	step(tokenMsg("It says HELLO."))

	var assistant []string
	var system []string
	for _, tr := range m.turns {
		switch tr.role {
		case roleAssistant:
			assistant = append(assistant, tr.text)
		case roleSystem:
			system = append(system, tr.text)
		}
	}

	if len(assistant) != 1 {
		t.Fatalf("expected one assistant turn for one exchange, got %d: %q", len(assistant), assistant)
	}
	if assistant[0] != "Let me look. It says HELLO." {
		t.Errorf("the answer was split or corrupted by the tool notice: %q", assistant[0])
	}
	// One line per call, rewritten in place, not one per phase.
	if len(system) != 1 {
		t.Fatalf("expected one narration line for one tool call, got %d: %q", len(system), system)
	}
	if !strings.Contains(system[0], "42 bytes") {
		t.Errorf("the finished call still reads as running: %q", system[0])
	}
	if got := lastAssistantText(m.turns); got != "Let me look. It says HELLO." {
		t.Errorf("edit-block parsing would see %q", got)
	}
}

// The decision stays in the scrollback after the panel is gone, because "did I
// approve that?" is a question worth being able to answer by scrolling up.
func TestTheDecisionIsRecordedInTheTranscript(t *testing.T) {
	for decision, want := range map[string]string{
		protocol.ApprovalApprove:        "approved",
		protocol.ApprovalApproveForTurn: "rest of this task",
		protocol.ApprovalDeny:           "denied",
		protocol.ApprovalCancelTurn:     "stopped",
	} {
		m, _ := pendingApprovalModel(t, laneBRequest())
		next, _ := m.answerApproval(decision)
		transcript := renderTranscript(next.(chatModel).turns, 80)
		if !strings.Contains(transcript, want) {
			t.Errorf("%q left no %q record in the transcript:\n%s", decision, want, transcript)
		}
		if !strings.Contains(transcript, "read_anything") {
			t.Errorf("%q recorded a decision without naming the tool:\n%s", decision, transcript)
		}
	}
}
