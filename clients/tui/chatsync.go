package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// KEEPING UP WITH THE SHARED CHAT (daemon/chatsync.go).
//
// The chat is one conversation per workspace, and the VS Code panel writes to
// it too. This client used to read it once, at startup, and from then on show
// only what it had itself been part of -- so a question asked in the editor
// was missing from this screen, and from the history this client sent with its
// next question (FOUND 2026-10-08).
//
// It now catches up at three moments, which between them cover how two windows
// are actually used:
//
//   - when this terminal gets focus: coming back from the editor is exactly when
//     the editor may have added something (catchUpCmd, asynchronous);
//   - before a question is sent, so what the other window added is on screen
//     ABOVE the new question and in the history sent with it (catchUpNow);
//   - after a turn the daemon says was not level with the chat -- the other
//     window wrote while this one was answering (chatRevMsg).
//
// The daemon is the backstop for the second: a question sent from a copy that
// is behind is answered from the stored chat whatever this client sent.
//
// What the other window added is APPENDED when the chat has only grown, so this
// client's own turns stay exactly as they were typed and shown. Only a chat that
// was replaced -- a new chat, a resume, a compact in the other window -- is
// redrawn whole.

// chatRevMsg is where the shared chat stands after this client's turn, from the
// turn's Done (protocol.TokenResponse.ChatRevision/ChatBehind). Delivered just
// before streamDoneMsg.
type chatRevMsg struct {
	rev    string
	behind bool
}

// chatCaughtUpMsg is the answer to an asynchronous catch-up (catchUpCmd).
// since is the revision it was asked from: an answer to a question the client
// has since moved past is dropped rather than applied.
type chatCaughtUpMsg struct {
	since string
	resp  protocol.HistoryResponse
	err   error
}

// canCatchUp reports whether the transcript may be changed under the user now:
// the daemon keeps a shared chat, no ctrl+n is still on its way to it, and
// nothing is mid-flight on screen -- a stream, a review, an approval, the key
// dialog all hold indexes into the transcript or the user's attention.
func (m chatModel) canCatchUp() bool {
	if !m.chatSync || m.resetPending {
		return false
	}
	return m.state == stateIdle || m.state == stateError || m.state == stateSplash
}

// catchUpCmd asks the daemon, off the UI goroutine, what the chat has gained
// since this client last looked.
func (m chatModel) catchUpCmd() tea.Cmd {
	since, clientName, root := m.chatRev, m.clientName, m.workspaceRoot
	return func() tea.Msg {
		resp, err := sendHistory(clientName, protocol.HistoryRequest{
			Action: protocol.HistoryCurrent, Workspace: root, Since: since,
		})
		return chatCaughtUpMsg{since: since, resp: resp, err: err}
	}
}

func (m chatModel) handleChatCaughtUp(msg chatCaughtUpMsg) (tea.Model, tea.Cmd) {
	// A failure says nothing worth interrupting anyone for: the daemon being
	// away is reported by the next thing that needs it, and the next focus or
	// question tries again.
	if msg.err != nil || msg.since != m.chatRev || !m.canCatchUp() {
		return m, nil
	}
	m.adoptChat(msg.resp)
	return m, nil
}

// catchUpNow brings the transcript level with the shared chat before a question
// is sent, synchronously -- the question is built from the transcript on the
// very next line, so it cannot wait for a message. One request over the local
// socket, which /history's commands already make from here. A failure leaves
// the transcript as it is; the daemon still answers from the stored chat when
// this copy is behind.
func (m *chatModel) catchUpNow() {
	if !m.canCatchUp() {
		return
	}
	resp, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistoryCurrent, Workspace: m.workspaceRoot, Since: m.chatRev,
	})
	if err == nil {
		m.adoptChat(resp)
	}
}

// adoptChat applies an answer to HistoryCurrent: the turns the chat gained are
// appended, or a chat that was replaced is shown whole.
func (m *chatModel) adoptChat(resp protocol.HistoryResponse) {
	if resp.ChatRevision == "" || resp.ChatRevision == m.chatRev {
		return
	}
	quiet := m.chatQuiet
	m.chatRev, m.chatQuiet = resp.ChatRevision, false
	added := turnsFromProtocol(resp.Turns)
	switch {
	case resp.Append && len(added) == 0:
		return
	case resp.Append:
		if !quiet {
			m.appendTurn(turn{role: roleSystem, text: fmt.Sprintf("↓ %d message%s from another Mochiii window on this workspace",
				len(added), plural(len(added)))})
		}
		for _, t := range added {
			m.appendTurn(t)
		}
	default:
		m.turns = added
		m.chatIDs = nil
		m.lastGrounding, m.lastRedactions, m.lastDegraded = nil, nil, nil
		m.lastProvider, m.lastHistoryTruncated = "", false
		if !quiet {
			note := "↻ this chat was changed in another Mochiii window -- showing it as it is now"
			if len(added) == 0 {
				note = "↻ this chat was cleared in another Mochiii window"
			}
			m.appendTurn(turn{role: roleSystem, text: note})
		}
	}
	m.resizeViewport()
	m.refreshViewport()
}

// handleChatRev records where the chat stands after this client's own turn.
// Level means the transcript (with the exchange it just showed) IS the chat at
// that revision. Not level -- the other window wrote first, or this turn was
// sent without knowing where the chat stood -- means the chat must be fetched
// whole, which handleStreamDone starts once the turn is over.
func (m chatModel) handleChatRev(msg chatRevMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	if msg.rev != "" {
		if msg.behind || m.turnChatRev == "" {
			m.chatRev = ""
		} else {
			m.chatRev = msg.rev
		}
	}
	return m, waitForNext(m.streamCh)
}
