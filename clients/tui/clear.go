package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /clear. In a file of its own, beside history.go and connect.go, like the
// other commands that are more than a line of chat.go's switch.

// newChatQuestion is what /clear and ctrl+n ask on a chat that is not saved. It
// is drawn in the prompt's box, in the prompt's place, and the footer under it
// names the keys (newChatConfirmHelpText, chat.go).
//
// THE OWNER'S WORDS (2026-10-12): "when I ask to /clear it is showing
// something complex… it should only ask: this chat didn't saved, confirm: save
// or clear?" What it showed was a sentence in the transcript -- "this chat is
// not saved -- /save keeps it; /clear again discards it" -- which answered a
// yes-or-no moment with two commands to go and type. So it is a question now,
// and each answer is one key.
const newChatQuestion = "This chat is not saved. Save or clear?"

// handleClearCommand is /clear: A NEW CHAT, exactly what ctrl+n starts.
//
// IT USED TO EMPTY THE SCREEN AND NOTHING ELSE. There are three copies of the
// current chat -- the transcript on screen, the history built from it and sent
// with each prompt, and the daemon's stored chat, which is what /save writes
// and what a relaunch brings back. /clear emptied the first two and never told
// the daemon, so the three disagreed from then on: the model had forgotten the
// chat, and the next /save wrote all of it. REPORTED BY THE OWNER 2026-10-11
// ("it is saving everything which are /clear cleaned"); their saved chat held
// 116 turns from eleven days of sittings. One meaning for "start again", in
// one place (clearConversation), is the fix: there is no longer a way to empty
// the screen that leaves the stored chat behind.
//
// IT ASKS ON A CHAT THAT IS NOT SAVED, as ctrl+n does and for ctrl+n's reason
// -- only what the user saves is kept, so clearing discards it for good. The
// owner's decision, 2026-10-11; the question and its one-key answers are the
// owner's too, the day after (newChatQuestion). A saved chat, or an empty one,
// clears at once, and a cleared screen is the only confirmation: a reply would
// put a line straight back on the screen the user just asked to empty.
func (m chatModel) handleClearCommand() (tea.Model, tea.Cmd) {
	if m.state == stateSending || m.state == stateStreaming {
		m.appendTurn(turn{role: roleSystem, text: "wait for this turn to finish (or press esc), then /clear"})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	if !m.currentChatUnsaved() {
		return m.clearConversation()
	}
	m.newChatArmed = true
	return m, nil
}

// answerNewChatQuestion takes the key that answers "Save or clear?" -- every
// key but ctrl+n, which handleCtrlN still takes as "clear", the habit it has
// always been.
//
//	s    save the chat, then start a new one
//	c    start a new one; this chat is discarded
//	esc  nothing happens, the chat stays
//
// ANY OTHER KEY TAKES THE QUESTION AWAY AND IS THEN AN ORDINARY KEY. The prompt
// comes back with that key typed in it. Swallowing it instead looked tidier and
// was a trap: someone used to "/clear again" types it again, the "/" is eaten
// as "cancel", and what reaches the prompt is "clear" -- sent to the model as a
// question the moment they press Enter.
func (m chatModel) answerNewChatQuestion(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.newChatArmed = false
	switch msg.String() {
	case "s", "S":
		return m.saveThenClear()
	case "c", "C":
		return m.clearConversation()
	case "esc":
		return m, nil
	}
	return m.handleKey(msg)
}

// saveThenClear is the "save" answer: the chat is saved, and only once it is
// does the new chat start.
//
// A SAVE THAT FAILED CLEARS NOTHING. The user chose "save" precisely so as not
// to lose this chat; discarding it because the daemon could not be reached
// would be doing the one thing they said no to. The chat stays, and the line
// says why.
func (m chatModel) saveThenClear() (tea.Model, tea.Cmd) {
	resp, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistorySave, Workspace: m.workspaceRoot, Spec: m.activeSpec,
	})
	if err != nil {
		m.appendTurn(turn{role: roleSystem, text: "the chat could not be saved, so it was not cleared: " + err.Error()})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	m.chatIDs = nil // the list's numbers move with a new or updated save

	saved := "saved"
	if e := resp.Entry; e != nil {
		saved = fmt.Sprintf("saved %q (%d turns)", e.Title, e.Turns)
	}
	if resp.Pruned > 0 {
		saved += fmt.Sprintf(" · the oldest %d saved chat(s) were removed to keep history within its size limit", resp.Pruned)
	}
	next, cmd := m.clearConversation()
	cm := next.(chatModel)
	// One line on the new chat's empty screen, because it is the only record
	// that the old one still exists and of how to get back to it.
	cm.appendTurn(turn{role: roleSystem, text: saved + " · a new chat starts here · /resume brings a saved chat back"})
	cm.resizeViewport()
	cm.refreshViewport()
	return cm, cmd
}
