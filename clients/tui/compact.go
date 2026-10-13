package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /compact, AS A SUMMARY WRITTEN BY THE DAEMON.
//
// THE DEFECT (open since the daemon learned to summarise, 9eb2bc5; FOUND again
// 2026-10-12). This command kept the last eight turns of the transcript on
// screen and asked the daemon for nothing: on a long chat it threw the earlier
// context away with nothing in its place, and the chat the daemon held -- the
// one the other window and the next start see -- was not compacted at all. The
// extension's /compact has summarised through the daemon all along.
//
// NOW the daemon is asked (HistoryCompact): it saves the whole chat to history
// first, folds the older part into a summary written by the model in use, keeps
// the most recent exchanges word for word, and that becomes the chat here and
// everywhere.
//
// WHEN THE SUMMARY CANNOT BE WRITTEN, NOTHING IS DROPPED. A model that is rate
// limited or out of quota is a reason to try again later, not a reason to
// discard the turns the user asked to have summarised. Only a daemon too old to
// summarise at all gets the old behaviour, and the answer says that is what
// happened.

// compactKeepLocal is how many turns the old, local compact keeps.
const compactKeepLocal = 8

// compactDoneMsg carries the daemon's answer back to the UI goroutine.
type compactDoneMsg struct {
	resp protocol.HistoryResponse
	err  error
}

// startCompact asks the daemon to compact the chat. The call includes one model
// request, so it runs off the UI goroutine, and the keyboard is held until it
// answers: an exchange started meanwhile would be appended to a chat that is
// about to be replaced (the daemon holds its own lock for the same reason).
func (m chatModel) startCompact() (tea.Model, tea.Cmd) {
	say := func(text string) (tea.Model, tea.Cmd) {
		m.appendTurn(turn{role: roleSystem, text: text})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	switch {
	case m.state == stateSending || m.state == stateStreaming:
		return say("wait for this turn to finish (or press esc), then compact the chat")
	case m.compacting:
		return say("already compacting -- one moment")
	case len(buildHistory(m.turns)) == 0:
		return say("nothing to compact: this chat is empty")
	}
	m.compacting = true
	m.appendTurn(turn{role: roleSystem, text: "compacting: the whole chat is saved to /history, then its earlier part is summarised…"})
	m.resizeViewport()
	m.refreshViewport()

	clientName, root, spec, tier := m.clientName, m.workspaceRoot, m.activeSpec, m.preferredTier
	return m, func() tea.Msg {
		resp, err := sendHistory(clientName, protocol.HistoryRequest{
			Action: protocol.HistoryCompact, Workspace: root, Spec: spec, Tier: tier,
		})
		return compactDoneMsg{resp: resp, err: err}
	}
}

// daemonCannotCompact reports whether err is a daemon that does not know the
// summarising compact: one from before /history, or one that has /history and
// answers "unknown history action" for this one.
func daemonCannotCompact(err error) bool {
	return errors.Is(err, errDaemonPredatesHistory) ||
		(err != nil && strings.HasPrefix(err.Error(), "unknown history action"))
}

// handleCompactDone shows the compacted chat, or says why there is none.
func (m chatModel) handleCompactDone(msg compactDoneMsg) (tea.Model, tea.Cmd) {
	m.compacting = false
	var note string
	switch {
	case daemonCannotCompact(msg.err):
		note = m.compactLocally()
	case msg.err != nil:
		note = "not compacted: " + sanitizeText(msg.err.Error()) + " -- nothing was removed; the chat is as it was"
	default:
		m.turns = turnsFromProtocol(msg.resp.Turns)
		m.chatIDs = nil // the chat is a new one in history; every number may have moved
		// The compacted chat IS the shared chat now, as of this revision.
		m.chatRev, m.chatQuiet = msg.resp.ChatRevision, false
		m.lastGrounding, m.lastRedactions, m.lastDegraded = nil, nil, nil
		m.lastProvider, m.lastHistoryTruncated = "", false
		note = fmt.Sprintf("compacted: %d earlier message%s summarised -- the full chat is saved in /history",
			msg.resp.Compacted, plural(msg.resp.Compacted))
	}
	m.appendTurn(turn{role: roleSystem, text: note})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// compactLocally is what /compact did before the daemon could summarise: keep
// the last few turns of this transcript. Used only against a daemon that
// cannot, and it says so -- nothing is summarised and the daemon's own copy of
// the chat is not changed.
func (m *chatModel) compactLocally() string {
	const why = "the running daemon is older than the summarising /compact (restart it to get a summary)"
	// The last few turns OF THE CONVERSATION: this command's own progress line,
	// and any other note on screen, is not one of them.
	cut, kept := len(m.turns), 0
	for cut > 0 && kept < compactKeepLocal {
		cut--
		if r := m.turns[cut].role; r == roleUser || r == roleAssistant {
			kept++
		}
	}
	if cut == 0 {
		return "nothing was compacted: " + why + ", and this chat is already short"
	}
	// A re-slice of turns that appendTurn already sanitized; nothing new enters
	// the transcript here.
	m.turns = append([]turn(nil), m.turns[cut:]...)
	return fmt.Sprintf("kept the last %d turns on this screen and summarised nothing: %s", compactKeepLocal, why)
}
