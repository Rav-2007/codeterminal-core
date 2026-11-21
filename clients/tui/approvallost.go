package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// AN ANSWER THAT COULD NOT BE SENT is reported as what it is: the call did not
// run, and -- when an older daemon's wait had run out -- the question expired.
//
// FOUND 2026-11-21 on the owner's screen: "✓ approved builtin__propose_edit",
// then LINK SEVERED, "write unix …sock: write: broken pipe" and "the prompt
// was sent, but no answer came back". Every word of that pointed at the
// connection. The daemon was fine. What had happened, read off its log:
//
//  1. The approval panel waits for a key with no time limit, and while it
//     waits nothing reads the connection (askForApproval runs inside the
//     stream's read loop, stream.go).
//  2. The daemon waited five minutes for the answer, took silence as a no, and
//     carried on with the turn.
//  3. With nobody reading, its output fills the socket buffer (a few hundred
//     tokens), a write blocks, and at its 60-second write deadline the daemon
//     takes the client for gone, ends the turn and closes the connection.
//  4. The answer, given after that, is written to a closed socket.
//
// Nothing on the screen said the one thing the user needed to know: the edit
// they had just approved was not made.
//
// A daemon with protocol.FeatureApprovalWaitsForAnswer no longer gives up
// (daemon/toolapproval.go), so step 2 cannot happen there -- but a TUI can be
// talking to an older daemon that is still running, and the connection can
// still go for other reasons (the daemon restarted while the question was up).
//
// The daemon cannot be told after the fact, so this is the client's to say, and
// it says only what it knows. WHY: the question expired only if the daemon is
// one that gives up AND it had been open for that daemon's whole wait;
// otherwise the connection went for some other reason, and "expired" would be
// untrue.

// olderDaemonApprovalWait is how long a daemon without
// protocol.FeatureApprovalWaitsForAnswer waited for an answer before it took
// silence as a no.
const olderDaemonApprovalWait = 5 * time.Minute

// approvalLostMsg is an approval answer the stream could not send.
type approvalLostMsg struct {
	req protocol.ToolApprovalRequest
	// waited runs from the question reaching the screen to the failed send.
	waited time.Duration
	err    error
	// daemonWaits is whether the daemon declared
	// protocol.FeatureApprovalWaitsForAnswer: if it did, the question cannot
	// have expired.
	daemonWaits bool
}

// roleApprovalLost is the transcript's record of that answer. Display-only,
// like every role but roleUser and roleAssistant, so buildHistory drops it.
// Its own role because its first line must be read: roleSystem is drawn faint.
const roleApprovalLost turnRole = roleActivity + 1

// handleApprovalLost ends the turn whose answer could not be sent.
//
// The bookkeeping is interruptTurn's, because the daemon treated this turn as
// one the user stopped: a failed write is how it learns of a stop
// (daemon/agentturn.go), so it held the bill for the next Done and did not
// save the exchange.
func (m chatModel) handleApprovalLost(msg approvalLostMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a turn already abandoned: nothing more of it is shown
	}
	m.endStream()
	// Whatever streamed before the question goes back to the model as a turn
	// that did not finish, as on every other failure (handleStreamErr).
	if m.streamAssistant >= 0 && m.streamAssistant < len(m.turns) &&
		strings.TrimSpace(m.turns[m.streamAssistant].text) != "" {
		m.turns[m.streamAssistant].incomplete = protocol.IncompleteProviderError
	}
	task := m.taskRunning()
	m.appendTurn(turn{role: roleApprovalLost, text: approvalLostText(msg.req, msg.waited, msg.err, msg.daemonWaits, !task)})
	m.stopsInChat++
	m.chatRev, m.chatQuiet = "", true
	if task {
		m.taskStatus.State = protocol.TaskStateStopped
		m.appendTurn(turn{role: roleSystem, text: "the task is saved — /task resume carries on, " +
			"/task review shows its changes so far"})
	}
	m.state = stateIdle
	m.statusErr = ""
	m.resizeViewport()
	m.refreshViewport()
	return m, m.input.Focus()
}

// approvalLostText says that req did not run and why: one headline line, then
// the detail. daemonWaits is approvalLostMsg's. retryHint adds what to do next;
// a long task says it on its own line instead (handleApprovalLost).
func approvalLostText(req protocol.ToolApprovalRequest, waited time.Duration, err error, daemonWaits, retryHint bool) string {
	name := req.Server + "__" + req.Tool
	outcome := name + " did not run"
	if req.Server == "builtin" && req.Tool == "propose_edit" {
		outcome = "the edit was not made (" + name + " did not run)"
	}
	var head, why string
	if !daemonWaits && waited >= olderDaemonApprovalWait {
		head = "✗ approval expired — " + outcome
		why = fmt.Sprintf("this version of Mochiii waits %s for an answer; this one came after %s, when the turn had already ended",
			waitText(olderDaemonApprovalWait), waited.Round(time.Second))
	} else {
		head = "✗ your answer did not reach Mochiii — " + outcome
		why = "the connection closed while the question was open (was Mochiii restarted?)"
		if err != nil {
			// The cause only ("broken pipe"): the rest of a socket error is the
			// socket's path, a terminal line long, and says nothing more.
			cause := err.Error()
			if i := strings.LastIndex(cause, ": "); i >= 0 {
				cause = cause[i+2:]
			}
			why += " — " + cause
		}
	}
	text := head + "\n" + why
	if retryHint {
		text += "\nask again to have it done"
	}
	// Tool and server names are chosen by an MCP server, not us.
	return sanitizeText(text)
}

// waitText is a wait as people say it: "5 minutes", not "5m0s".
func waitText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if d == time.Minute {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.Round(time.Second).String()
}

// renderApprovalLost draws the headline in the error colour, where the eye
// looks for what went wrong, and the detail under it in the quiet colour.
func renderApprovalLost(text string) string {
	head, rest, _ := strings.Cut(text, "\n")
	var b strings.Builder
	b.WriteString(errorStyle.Render(head))
	for _, line := range strings.Split(rest, "\n") {
		if line != "" {
			b.WriteString("\n" + helpStyle.Render("  "+line))
		}
	}
	return b.String()
}
