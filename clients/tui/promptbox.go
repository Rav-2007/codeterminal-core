package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// THE PROMPT IS MARKED OFF FROM THE CHAT.
//
// WHY (OWNER, 2026-10-12): "can you highlight the prompt like the images I
// added". The prompt was one line of text directly under the transcript, in the
// same grey as an answer's last line, and on a full screen nothing said where
// the conversation ended and the place to type began. The owner was shown two
// ways to mark it -- a rule above and below, or a box round it -- and chose the
// box.
//
// A RULE ABOVE AND BELOW, SINCE 2026-11-21. The owner, showing Cline's screen:
// "i attached a photo which looks good, so do the ui like that ... the prompt
// section should change like that". So the prompt is the line between two
// rules the width of the terminal, after a mark, as there; the rows are the
// same three the box had, so nothing that counts them changed. The mark is ➤
// (owner, same day: "instead of using the '>', we can use '➤'"), the one each
// question carries in the chat (userbar.go).
//
// IT IS THE WHOLE BOTTOM LINE THAT IS MARKED, whatever is on it: the prompt,
// and also the keys of a tool approval or an edit review, which take the
// prompt's place while they are waited for. Marking only the prompt would make
// the frame two rows shorter the moment a question replaced it, and the
// transcript would jump down and back with every approval.
//
// THE RULES SAY WHOSE TURN IT IS. Blue while the client is waiting for the
// user -- a prompt to type, a key to press -- and grey while a turn is running
// and typing would do nothing.

const (
	// promptBoxMinHeight and promptBoxMinWidth are the smallest terminal the
	// rules are drawn in. Under them the prompt is the one bare line it always
	// was: two rows of rule are two rows of the answer being read, and in a
	// terminal this small they are worth more as answer.
	promptBoxMinHeight = 12
	promptBoxMinWidth  = 24
	// promptRule is what the two rules are drawn with.
	promptRule = "─"
	// promptMark is in front of what is typed, and of each question in the chat.
	promptMark = "➤"
)

var (
	promptRuleStyle = lipgloss.NewStyle().Foreground(colorBlue)
	promptRuleQuiet = lipgloss.NewStyle().Foreground(colorDim)
	// promptChevronStyle is the ➤ in front of what is typed.
	promptChevronStyle = lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
)

// promptBoxed reports whether the prompt is drawn between its rules. It depends
// on the terminal's size ALONE, so the viewport -- sized when the terminal is --
// and the frame drawn afterwards always agree about how many rows the prompt
// takes.
func (m chatModel) promptBoxed() bool {
	return m.height >= promptBoxMinHeight && m.width >= promptBoxMinWidth
}

// promptRows is how many rows the bottom line takes: the rules and the input's
// rows between them (one, or more for a long prompt: promptinput.go), or one
// bare. It changes as a long prompt is typed, and the frame is re-measured when
// it does (Update, inline.go), so the viewport and the frame still agree.
func (m chatModel) promptRows() int {
	if m.promptBoxed() {
		return 2 + m.inputRows()
	}
	return 1
}

// promptCols is the width the prompt's text has to fit in: the terminal's, or
// on the first screen of an inline chat the narrower prompt under the lotus
// (welcome.go).
func (m chatModel) promptCols() int {
	if m.promptBoxed() {
		return m.promptWidth()
	}
	return m.width
}

// promptWidth is how wide the rules are drawn.
func (m chatModel) promptWidth() int {
	if m.welcoming() {
		return m.welcomeWidth()
	}
	return m.width
}

// promptRuleFor is the rules' colour for this moment: blue when the user is
// waited for, grey while a turn is running.
func (m chatModel) promptRuleFor() lipgloss.Style {
	if m.turnInFlight() {
		return promptRuleQuiet
	}
	return promptRuleStyle
}

// promptBox draws content -- the prompt, or what has taken its place --
// between the rules, the whole width of the terminal.
//
// THE RULES AND ONE ROW PER LINE OF CONTENT -- one, or a long prompt's rows
// (promptinput.go) -- EACH EXACTLY THE RULES' WIDTH. Each line is cut to that
// width before it is drawn: a line that wrapped would make the frame a row
// taller than counted, and its top row would scroll away.
func (m chatModel) promptBox(content string) string {
	if !m.promptBoxed() {
		return content
	}
	w := m.promptWidth()
	rule := m.promptRuleFor().Render(strings.Repeat(promptRule, w))
	rows := strings.Split(content, "\n")
	for i, r := range rows {
		rows[i] = padToWidth(r, w)
	}
	return rule + "\n" + strings.Join(rows, "\n") + "\n" + rule
}
