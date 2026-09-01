package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"codeterminal/protocol"
)

// startedTurn is a model with a turn in flight and a cancellable context in
// place of the one startTurn made, so a test can observe the cancel actually
// firing rather than only that the field was cleared.
func startedTurn(t *testing.T) (chatModel, context.Context) {
	t.Helper()
	m := newTestModel()
	m = typeText(m, "do the thing")
	m, _ = pressEnter(m)
	if m.state != stateSending {
		t.Fatalf("state = %v, want stateSending after Enter", m.state)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	return m, ctx
}

// The headline of this feature: a turn can be stopped without ending the
// session. Before it existed the only key that ended a wait was ctrl+c, and
// that quit -- so escaping a turn that had gone somewhere useless cost the
// whole conversation.
func TestEscStopsTheTurnAndKeepsTheSession(t *testing.T) {
	m, ctx := startedTurn(t)
	updated, _ := m.Update(tokenMsg("partial answ"))
	m = updated.(chatModel)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	if ctx.Err() == nil {
		t.Error("esc did not cancel the request context, so nothing actually stopped")
	}
	if cmd != nil {
		if _, quitting := cmd().(tea.QuitMsg); quitting {
			t.Fatal("esc quit the program; it must only stop the turn")
		}
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle so the user can type again", m.state)
	}
	if m.streamCh != nil || m.streamCancel != nil {
		t.Error("the stream was not released")
	}
	if got := lastAssistantText(m.turns); got != "partial answ" {
		t.Errorf("assistant text = %q, want the streamed text kept verbatim", got)
	}
	if note := lastSystemText(m.turns); !strings.Contains(note, "stopped") {
		t.Errorf("transcript note = %q, want a visible record that the turn was stopped", note)
	}
}

// The partial answer must go back to the model MARKED as stopped. Without
// this the next prompt re-shows the model its own half-finished reply as a
// conclusion it reached -- the same loss the streamErrMsg path guards against,
// through a different door.
func TestAnInterruptedAnswerTravelsBackAsUserCancelled(t *testing.T) {
	m, _ := startedTurn(t)
	updated, _ := m.Update(tokenMsg("half an answer"))
	m = updated.(chatModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	history := buildHistory(m.turns)
	var assistant *protocol.Turn
	for i := range history {
		if history[i].Role == "assistant" {
			assistant = &history[i]
		}
	}
	if assistant == nil {
		t.Fatalf("history carried no assistant turn: %#v", history)
	}
	if assistant.Incomplete != protocol.IncompleteUserCancelled {
		t.Errorf("Incomplete = %q, want %q", assistant.Incomplete, protocol.IncompleteUserCancelled)
	}
}

// Stopping before a single token arrived is the common case (a turn that is
// obviously going nowhere gets stopped early). There is no partial answer to
// mark, and nothing may be invented to mark instead.
func TestStoppingBeforeAnyTokenMarksNoAnswer(t *testing.T) {
	m, ctx := startedTurn(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	if ctx.Err() == nil {
		t.Error("esc did not cancel the request context")
	}
	for _, tn := range m.turns {
		if tn.role == roleAssistant {
			t.Errorf("an assistant turn was invented for a turn that never spoke: %#v", tn)
		}
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle", m.state)
	}
}

func pressCtrlC(m chatModel) (chatModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	return updated.(chatModel), cmd
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// THE REPORTED BUG, as a test: "when I press esc / ctrl+c it exits".
//
// The stop key worked and the turn ended; the next ctrl+c, pressed a beat later
// out of the same habit, quit the program. One press must never end the session
// -- not the one that stops a turn, and not the one after it.
func TestNoSingleCtrlCEverQuits(t *testing.T) {
	m, ctx := startedTurn(t)

	m, cmd := pressCtrlC(m) // 1: stops the turn
	if ctx.Err() == nil {
		t.Error("the first ctrl+c did not cancel the request context")
	}
	if quits(cmd) {
		t.Fatal("the ctrl+c that stopped the turn also quit")
	}
	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle after the stop", m.state)
	}

	m, cmd = pressCtrlC(m) // 2: nothing left to stop, so it only asks
	if quits(cmd) {
		t.Fatal("a single ctrl+c with nothing to stop quit without asking")
	}
	if !m.quitArmed {
		t.Fatal("the press neither quit nor armed a quit, so ctrl+c did nothing at all")
	}
	if view := m.View(); !strings.Contains(view, "press ctrl+c again") {
		t.Errorf("the footer does not say a second press quits:\n%s", view)
	}

	if _, cmd = pressCtrlC(m); !quits(cmd) { // 3: confirmed
		t.Error("a second consecutive ctrl+c did not quit")
	}
}

// Arming has to be forgettable. Anything else the user does means they were not
// trying to quit, and the next stray ctrl+c must ask again rather than exit.
func TestAnyOtherKeyDisarmsTheQuit(t *testing.T) {
	m := newTestModel()
	m, _ = pressCtrlC(m)
	if !m.quitArmed {
		t.Fatal("ctrl+c did not arm a quit")
	}

	m = typeText(m, "c")
	if m.quitArmed {
		t.Fatal("typing left the quit armed")
	}
	if view := m.View(); strings.Contains(view, "press ctrl+c again") {
		t.Errorf("the confirm hint outlived the keypress that cancelled it:\n%s", view)
	}
	if _, cmd := pressCtrlC(m); quits(cmd) {
		t.Error("ctrl+c quit after being disarmed; it must ask again")
	}
}

// A stopped turn re-arms nothing: the press that stops is spent on stopping.
func TestStoppingATurnDoesNotLeaveAQuitArmed(t *testing.T) {
	m, _ := startedTurn(t)
	m, _ = pressCtrlC(m)
	if m.quitArmed {
		t.Error("the ctrl+c that stopped a turn also armed a quit, so the next one would exit")
	}
}

// esc pressed a beat after the answer landed must be harmless. This is the
// whole reason esc is not also a quit key: it is pressed by reflex.
func TestEscWhileIdleDoesNothing(t *testing.T) {
	m := newTestModel()
	before := len(m.turns)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	if cmd != nil {
		if _, quitting := cmd().(tea.QuitMsg); quitting {
			t.Fatal("esc quit an idle session")
		}
	}
	if len(m.turns) != before {
		t.Errorf("turns = %d, want unchanged %d", len(m.turns), before)
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle", m.state)
	}
}

// Whatever the abandoned stream had already put in flight must not land in the
// transcript after the user has stopped it.
func TestMessagesFromAStoppedTurnAreIgnored(t *testing.T) {
	m, _ := startedTurn(t)
	updated, _ := m.Update(tokenMsg("kept"))
	m = updated.(chatModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	for _, msg := range []tea.Msg{
		tokenMsg(" stray"),
		reasoningMsg{text: "stray thinking"},
		toolActivityMsg{activity: protocol.ToolActivity{CallID: "c1", Server: "s", Tool: "t", Phase: "running"}},
		streamDoneMsg{},
	} {
		updated, _ = m.Update(msg)
		m = updated.(chatModel)
	}

	if got := lastAssistantText(m.turns); got != "kept" {
		t.Errorf("assistant text = %q, want %q -- a stopped turn kept writing", got, "kept")
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle", m.state)
	}
}

// The footer is where someone looks when they want it to stop, so that is
// where the key has to be named.
func TestTheFooterNamesTheStopKeyWhileATurnRuns(t *testing.T) {
	m, _ := startedTurn(t)
	if view := m.View(); !strings.Contains(view, "stop") {
		t.Errorf("no stop hint in the footer while a turn is in flight:\n%s", view)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)
	if view := m.View(); !strings.Contains(view, helpText) {
		t.Errorf("the idle footer did not come back after the turn was stopped:\n%s", view)
	}
}

// At an approval prompt the turn is stopped through the approval channel, so
// the daemon is told WHY rather than being hung up on mid-question -- and
// ctrl+c stays in the session like every other stop key.
func TestCtrlCAtAnApprovalStopsTheTaskWithoutQuitting(t *testing.T) {
	m := newTestModel()
	m.state = stateToolApproval
	req := protocol.ToolApprovalRequest{CallID: "c1", Server: "shell", Tool: "run", ArgumentsSHA256: "abc"}
	m.pendingApproval = &req
	reply := make(chan string, 1)
	m.approvalReply = reply

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(chatModel)

	select {
	case decision := <-reply:
		if decision != protocol.ApprovalCancelTurn {
			t.Errorf("decision = %q, want %q", decision, protocol.ApprovalCancelTurn)
		}
	default:
		t.Fatal("no decision reached the waiting stream")
	}
	if cmd != nil {
		if _, quitting := cmd().(tea.QuitMsg); quitting {
			t.Error("ctrl+c at an approval quit instead of stopping the task")
		}
	}
}

// fakeDaemonStreamingForever completes a handshake and then writes tokens until
// the client goes away -- a stand-in for a turn in full flow when the user hits
// stop.
func fakeDaemonStreamingForever(t *testing.T) (lockPath string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath = filepath.Join(dir, "daemon.lock")

	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}
	data, err := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err != nil {
		t.Fatalf("marshal lockfile: %v", err)
	}
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatalf("writing lockfile: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		dec := json.NewDecoder(conn)
		enc := json.NewEncoder(conn)
		var hsReq protocol.HandshakeRequest
		if err := dec.Decode(&hsReq); err != nil {
			return
		}
		if err := enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true}); err != nil {
			return
		}
		var promptReq protocol.PromptRequest
		if err := dec.Decode(&promptReq); err != nil {
			return
		}
		for {
			if err := enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: "tok "}); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	return lockPath, func() { ln.Close(); <-done }
}

// THE LEAK AN INTERRUPT KEY WOULD OTHERWISE CREATE. ch is unbuffered and the UI
// only drains it while it still cares, so once a turn is stopped a bare send in
// streamPrompt blocks forever: one parked goroutine per interrupt, for the life
// of a session. Cancellation used to happen only on the way out of the process,
// which is why nothing caught this before there was a stop key.
func TestStreamPromptExitsWhenAStoppedUIQuitsReading(t *testing.T) {
	lockPath, cleanup := fakeDaemonStreamingForever(t)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg) // unbuffered, exactly as startTurn makes it

	returned := make(chan struct{})
	go func() {
		streamPrompt(ctx, "test-client", "", "hello", "", "", "", nil, nil, ch)
		close(returned)
	}()

	select {
	case <-ch: // one token read, so the stream is genuinely flowing
	case <-time.After(2 * time.Second):
		t.Fatal("the fake daemon never streamed anything")
	}

	// From here nobody reads ch again -- the UI has stopped the turn. The pause
	// is what makes this test about the SEND rather than the read: it lets the
	// daemon fill the socket buffer, so streamPrompt is certainly parked handing
	// a decoded token to a UI that has gone, not waiting on the wire, by the
	// time the cancel lands. Without it the goroutine is usually inside Decode
	// and the test passes whether or not sends are cancellable.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("streamPrompt parked on a send nobody will ever receive")
	}
}
