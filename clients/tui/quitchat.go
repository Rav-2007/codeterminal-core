package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// LEAVING WITH A CHAT THAT IS NOT SAVED ASKS FIRST, and erases it on the way out.
//
// THE OWNER'S WORDS (2026-11-14): "a caution message must arise before the user
// closes the terminal; if the user didn't save, the session will be removed."
//
// A launch starts a new chat (freshchat.go), so a chat that is not saved is
// gone the moment its user leaves. ctrl+c and /exit therefore ask, in the
// prompt's box and with one-key answers, exactly as /clear and ctrl+n do for the
// same chat (clear.go):
//
//	s    save the chat, then quit
//	q    quit; the chat is erased
//	esc  stay
//
// ctrl+c again, with the question showing, is "quit": it is what that key has
// always meant twice running, and the question is on screen saying what it
// costs. Any other key takes the question away and is then an ordinary key.
//
// A SAVED CHAT, OR AN EMPTY ONE, LEAVES AT ONCE as before: there is nothing to
// lose. Closing the terminal WINDOW cannot ask -- the process gets SIGHUP -- so
// that exit leaves the chat on the daemon and the next launch erases it.
//
// ONLY WHILE THE PROMPT IS WAITING. The question is for an idle or failed turn.
// A turn in flight or paused at an approval is stopped by ctrl+c, not left
// (handleCtrlC), and the dialogs (key entry, setup, edit review) keep their own
// two-press quit; the next launch still erases what they leave.
const quitChatQuestion = "This chat is not saved and will be erased. Save or quit?"

// quitChatHelpText is the footer under the question, naming the keys.
const quitChatHelpText = "s save it, then quit · q quit, erasing it · esc stay"

// canAskBeforeQuit reports whether the quit question is the right answer to a
// request to leave now.
func (m chatModel) canAskBeforeQuit() bool {
	return m.state == stateIdle || m.state == stateError
}

// requestQuit is /exit, and the first ctrl+c that has nothing to stop: leave
// now if there is nothing to lose, otherwise ask. ok is false when it only
// asked, so handleCtrlC can fall back to its own armed-footer quit for the
// states that cannot show the question.
func (m chatModel) requestQuit() (tea.Model, tea.Cmd, bool) {
	if m.canAskBeforeQuit() && m.currentChatUnsaved() {
		m.quitArmed = false
		m.quitChatArmed = true
		return m, nil, true
	}
	return m, nil, false
}

// quitNow ends the program. erase also asks the daemon to drop the stored chat
// first, bounded (freshChatTimeout) so leaving can never hang; a failed erase
// is not an error worth stopping for, because the next launch erases it again.
func (m chatModel) quitNow(erase bool) (tea.Model, tea.Cmd) {
	if m.streamCancel != nil {
		m.streamCancel()
	}
	if erase {
		return m, eraseThenQuit(m.clientName)
	}
	return m, tea.Quit
}

// eraseThenQuit is the Cmd that erases the stored chat and then quits. It
// returns tea.QuitMsg, which Bubble Tea treats exactly as tea.Quit.
func eraseThenQuit(clientName string) tea.Cmd {
	return func() tea.Msg {
		_, _ = resetWithin(clientName, freshChatTimeout)
		return tea.QuitMsg{}
	}
}

// answerQuitQuestion takes the key that answers the quit question.
func (m chatModel) answerQuitQuestion(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.quitChatArmed = false
	switch msg.String() {
	case "s", "S":
		return m.saveThenQuit()
	case "q", "Q":
		return m.quitNow(true)
	case "esc":
		return m, nil
	}
	return m.handleKey(msg)
}

// saveThenQuit is the "save" answer: the chat is saved, and only once it is
// does the program quit. A SAVE THAT FAILED QUITS NOTHING, for saveThenClear's
// reason: the user chose "save" so as not to lose this chat.
func (m chatModel) saveThenQuit() (tea.Model, tea.Cmd) {
	if _, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistorySave, Workspace: m.workspaceRoot, Spec: m.activeSpec,
	}); err != nil {
		m.appendTurn(turn{role: roleSystem, text: "the chat could not be saved, so it was not closed: " + err.Error()})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	// Saved: the copy in /history is the one that stays, so the stored current
	// chat goes like any other quit's.
	return m.quitNow(true)
}
