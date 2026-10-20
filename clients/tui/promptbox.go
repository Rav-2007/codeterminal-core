package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// THE PROMPT IS DRAWN IN A BOX.
//
// WHY (OWNER, 2026-10-12): "can you highlight the prompt like the images I
// added". The prompt was one line of text directly under the transcript, in the
// same grey as an answer's last line, and on a full screen nothing said where
// the conversation ended and the place to type began. The owner was shown two
// ways to mark it -- a rule above and below, or a box round it -- and chose the
// box.
//
// IT IS THE WHOLE BOTTOM LINE THAT IS BOXED, whatever is on it: the prompt, and
// also the keys of a tool approval or an edit review, which take the prompt's
// place while they are waited for. Boxing only the prompt would make the frame
// two rows shorter the moment a question replaced it, and the transcript would
// jump down and back with every approval.
//
// THE BORDER SAYS WHOSE TURN IT IS. Blue while the client is waiting for the
// user -- a prompt to type, a key to press -- and grey while a turn is running
// and typing would do nothing.

const (
	// promptBoxMinHeight and promptBoxMinWidth are the smallest terminal the
	// box is drawn in. Under them the prompt is the one bare line it always
	// was: two rows of border are two rows of the answer being read, and in a
	// terminal this small they are worth more as answer.
	promptBoxMinHeight = 12
	promptBoxMinWidth  = 24
	// promptBoxCols is what the box takes from the width of what is inside it:
	// a border and one column of padding, each side.
	promptBoxCols = 4
)

var (
	promptBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBlue).Padding(0, 1)
	promptBoxQuiet = promptBoxStyle.BorderForeground(colorDim)
)

// promptBoxed reports whether the prompt is drawn in its box. It depends on the
// terminal's size ALONE, so the viewport -- sized when the terminal is -- and
// the frame drawn afterwards always agree about how many rows the prompt takes.
func (m chatModel) promptBoxed() bool {
	return m.height >= promptBoxMinHeight && m.width >= promptBoxMinWidth
}

// promptRows is how many rows the bottom line takes: three in its box, one bare.
func (m chatModel) promptRows() int {
	if m.promptBoxed() {
		return 3
	}
	return 1
}

// promptCols is the width the prompt's text has to fit in: the terminal's, less
// the box when there is one.
func (m chatModel) promptCols() int {
	if m.promptBoxed() {
		return m.width - promptBoxCols
	}
	return m.width
}

// promptBoxBorder is the box for this moment: blue when the user is waited
// for, grey while a turn is running.
func (m chatModel) promptBoxBorder() lipgloss.Style {
	if m.turnInFlight() {
		return promptBoxQuiet
	}
	return promptBoxStyle
}

// promptBox draws content -- the prompt, or what has taken its place -- in the
// box, the whole width of the terminal.
//
// ALWAYS EXACTLY THREE ROWS. The content is cut to the inside of the box before
// it is drawn: left to itself the box would wrap a long line onto a second one,
// and a frame one row taller than the terminal scrolls its top row away.
func (m chatModel) promptBox(content string) string {
	if !m.promptBoxed() {
		return content
	}
	inner := m.width - promptBoxCols
	return m.promptBoxBorder().Width(m.width - 2).Render(ansi.Truncate(content, inner, "…"))
}
