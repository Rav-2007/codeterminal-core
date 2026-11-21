package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"mochiii/protocol"
)

// proposeEditRequest is the question on the owner's screen on 2026-11-21.
func proposeEditRequest() protocol.ToolApprovalRequest {
	return protocol.ToolApprovalRequest{
		CallID:          "call-edit",
		Server:          "builtin",
		Tool:            "propose_edit",
		Arguments:       `{"path":"bot.py"}`,
		ArgumentsSHA256: "deadbeef",
		Lane:            protocol.LaneFirstParty,
		Confined:        true,
	}
}

// THE OWNER'S CASE OVER THE REAL TRANSPORT (testAddress: a Unix socket, or a
// named pipe on Windows). The daemon asked, stopped waiting and closed the
// connection; the answer came after. On a Unix socket the write fails with the
// very error on the owner's screen ("write: broken pipe"), and what reaches the
// UI must be the lost answer -- not a stream error, which is what drew LINK
// SEVERED.
//
// The first version dialled net.Listen("unix") directly, and Windows CI failed
// it: a write to a closed AF_UNIX peer does not fail there at once. That is a
// transport the product never uses on Windows, so it proved nothing either way.
func TestAnAnswerToAClosedConnectionIsReportedAsLostNotSevered(t *testing.T) {
	addr := testAddress(t)
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	clientEnd, err := protocol.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer clientEnd.Close()
	daemonEnd := <-accepted

	sess := &daemonSession{conn: clientEnd, enc: json.NewEncoder(clientEnd), dec: json.NewDecoder(clientEnd),
		handshake: protocol.HandshakeResponse{Features: []string{protocol.FeatureApprovalWaitsForAnswer}}}
	req := proposeEditRequest()
	ch := make(chan tea.Msg, 1)
	cont := make(chan bool, 1)
	go func() { cont <- askForApproval(context.Background(), sess, req, ch) }()

	shown := (<-ch).(toolApprovalMsg)
	daemonEnd.Close() // the daemon gave up on this question and ended the turn
	shown.reply <- protocol.ApprovalApprove

	select {
	case msg := <-ch:
		lost, ok := msg.(approvalLostMsg)
		if !ok {
			t.Fatalf("the failed answer reached the UI as %T, want approvalLostMsg -- a streamErrMsg is what drew LINK SEVERED", msg)
		}
		if lost.req.CallID != req.CallID {
			t.Errorf("the lost answer names call %q, want %q", lost.req.CallID, req.CallID)
		}
		if lost.err == nil {
			t.Error("the lost answer carries no error")
		}
		if runtime.GOOS != "windows" && !errors.Is(lost.err, syscall.EPIPE) {
			t.Errorf("err = %v, want the broken pipe the owner saw (else this test is not their case)", lost.err)
		}
		if !lost.daemonWaits {
			t.Error("the daemon declared that it waits for answers, and the lost answer does not say so -- it would be called expired")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing reached the UI after the answer could not be sent")
	}
	if <-cont {
		t.Error("streaming was told to carry on over a closed connection")
	}
}

// What the owner should have read instead (their daemon gave up after five
// minutes): the approval expired and the edit was not made -- and no LINK
// SEVERED, because the link was never the problem.
func TestAnExpiredApprovalSaysTheEditWasNotMade(t *testing.T) {
	m, _ := pendingApprovalModel(t, proposeEditRequest())
	next, _ := m.answerApproval(protocol.ApprovalApprove) // "✓ approved builtin__propose_edit"
	m = next.(chatModel)

	next, _ = m.Update(approvalLostMsg{
		req:    proposeEditRequest(),
		waited: 6*time.Minute + 12*time.Second,
		err:    syscall.EPIPE,
	})
	m = next.(chatModel)

	screen := ansi.Strip(renderTranscript(m.turns, 120))
	for _, want := range []string{
		"approval expired",
		"the edit was not made",
		"builtin__propose_edit did not run",
		"5 minutes",
		"6m12s",
		"ask again",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the transcript does not say %q:\n%s", want, screen)
		}
	}
	for _, wrong := range []string{"LINK SEVERED", "no answer came back", "broken pipe"} {
		if strings.Contains(screen, wrong) {
			t.Errorf("the transcript still says %q, which blames the connection:\n%s", wrong, screen)
		}
	}
	if m.state != stateIdle || m.statusErr != "" {
		t.Errorf("state = %v, statusErr = %q; want idle with no header error -- nothing is broken", m.state, m.statusErr)
	}
	if m.streamCh != nil {
		t.Error("the turn was left open after its connection closed")
	}
	// The note is for the user's eye only, never the model's.
	for _, h := range buildHistory(m.turns) {
		if strings.Contains(h.Content, "approval expired") {
			t.Errorf("the note went into the history sent to the model: %q", h.Content)
		}
	}
}

// A long task: the daemon saved it when the connection went, so the line says
// how to carry on, and the meter stops saying "running".
func TestAnExpiredApprovalInATaskSaysHowToResume(t *testing.T) {
	m, _ := pendingApprovalModel(t, proposeEditRequest())
	m.taskStatus = &protocol.TaskStatus{ID: "20261121-085651-cb9c", State: protocol.TaskStateRunning}
	next, _ := m.answerApproval(protocol.ApprovalApprove)
	next, _ = next.(chatModel).Update(approvalLostMsg{req: proposeEditRequest(), waited: 7 * time.Minute, err: syscall.EPIPE})
	m = next.(chatModel)

	screen := ansi.Strip(renderTranscript(m.turns, 120))
	if !strings.Contains(screen, "approval expired") || !strings.Contains(screen, "/task resume") {
		t.Errorf("a task's expired approval does not say it expired and how to resume:\n%s", screen)
	}
	if strings.Contains(screen, "ask again") {
		t.Errorf("a task is resumed, not asked again:\n%s", screen)
	}
	if m.taskStatus.State != protocol.TaskStateStopped {
		t.Errorf("task state = %q, want stopped", m.taskStatus.State)
	}
}

// Sooner than the daemon's wait, the question cannot have expired: the
// connection went for another reason, and the line must not claim otherwise.
func TestAnAnswerLostBeforeTheWaitDoesNotClaimItExpired(t *testing.T) {
	m, _ := pendingApprovalModel(t, laneBRequest())
	next, _ := m.answerApproval(protocol.ApprovalApprove)
	next, _ = next.(chatModel).Update(approvalLostMsg{req: laneBRequest(), waited: 10 * time.Second, err: syscall.EPIPE})
	m = next.(chatModel)

	screen := ansi.Strip(renderTranscript(m.turns, 160))
	if strings.Contains(screen, "expired") {
		t.Errorf("a 10-second-old question was called expired:\n%s", screen)
	}
	for _, want := range []string{"did not reach Mochiii", "somebodys-server__read_anything did not run", "broken pipe"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the transcript does not say %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "the edit was not made") {
		t.Errorf("a tool that is not an edit was described as one:\n%s", screen)
	}
}

// A lost answer from a turn the user already left (esc, a new prompt) changes
// nothing: every stream handler ignores a stream that is no longer current.
func TestALostAnswerFromAnAbandonedTurnIsIgnored(t *testing.T) {
	m := newTestModel()
	before := len(m.turns)
	next, _ := m.Update(approvalLostMsg{req: proposeEditRequest(), waited: 6 * time.Minute, err: syscall.EPIPE})
	if got := len(next.(chatModel).turns); got != before {
		t.Errorf("an abandoned turn's lost answer added %d line(s) to the transcript", got-before)
	}
}

// A DAEMON THAT WAITS FOR ITS ANSWER NEVER LETS A QUESTION EXPIRE, however
// long it stood (protocol.FeatureApprovalWaitsForAnswer). An answer that could
// not reach one is a connection that went -- a restart -- and calling it
// expired would send the user looking for a time limit that does not exist.
func TestAnApprovalToADaemonThatWaitsIsNeverCalledExpired(t *testing.T) {
	m, _ := pendingApprovalModel(t, proposeEditRequest())
	next, _ := m.answerApproval(protocol.ApprovalApprove)
	next, _ = next.(chatModel).Update(approvalLostMsg{
		req: proposeEditRequest(), waited: 3 * time.Hour, err: syscall.EPIPE, daemonWaits: true,
	})
	m = next.(chatModel)

	screen := ansi.Strip(renderTranscript(m.turns, 160))
	if strings.Contains(screen, "expired") || strings.Contains(screen, "5 minutes") {
		t.Errorf("a daemon that waits for answers had its question called expired:\n%s", screen)
	}
	for _, want := range []string{"did not reach Mochiii", "the edit was not made", "restarted"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the transcript does not say %q:\n%s", want, screen)
		}
	}
}

// THE HEADLINE IS DRAWN TO BE READ: in the error colour, where roleSystem's
// notices are faint grey. That is the one reason roleApprovalLost exists, and a
// test that strips colour (every other one here) cannot see it go.
func TestTheLostAnswerHeadlineIsInTheErrorColour(t *testing.T) {
	withColorProfile(t, termenv.ANSI256, func() {
		head := "✗ approval expired — the edit was not made (builtin__propose_edit did not run)"
		got := renderTranscript([]turn{{role: roleApprovalLost, text: head + "\nwhy"}}, 120)
		if want := errorStyle.Render(head); !strings.Contains(got, want) {
			t.Errorf("the headline is not drawn in the error colour:\n got %q\nwant it to contain %q", got, want)
		}
		if faint := helpStyle.Render(head); strings.Contains(got, faint) {
			t.Errorf("the headline is drawn faint, like a roleSystem notice: %q", got)
		}
	})
}
