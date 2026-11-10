package tui

import (
	"fmt"

	"mochiii/protocol"
)

// noteRejections puts what the daemon refused or withheld in the transcript, one
// line each, the way a refused block is already shown (chat.go): a TUI-only
// system turn. buildHistory drops those, so a notice is read by the person and
// never sent to the model, and the daemon does not store it either.
func (m *chatModel) noteRejections(rejections []protocol.EditRejectionWire) {
	if len(rejections) == 0 {
		return
	}
	for _, r := range rejections {
		m.appendTurn(turn{role: roleSystem, text: rejectionNote(r)})
	}
	m.refreshViewport()
}

// rejectionNote is one rejection as a line. The reason is the daemon's text and
// carries a file name the model chose, so it is sanitised like every other
// string from there. Line 0 means "not about a line of the answer" (plan mode's
// withheld edit); otherwise it is the line, as a refused block already says.
func rejectionNote(r protocol.EditRejectionWire) string {
	reason := sanitizeText(r.Reason)
	if r.Line > 0 {
		return fmt.Sprintf("(edit block at line %d refused: %s)", r.Line, reason)
	}
	return "(" + reason + ")"
}
