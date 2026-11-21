package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// THE FIRST SCREEN OF AN INLINE CHAT IS THE LOTUS AND THE PROMPT (owner,
// 2026-11-21, showing Cline's first screen beside Mochiii's: "the prompt bar is
// now sticking in the top, i attached a photo which looks good, so do the ui
// like that ... add the lotus in the first then the prompt section should
// change like that").
//
// Until the first question the screen is the lotus, the name, what to do, and
// the prompt under them, all in the middle of the window -- in place of the splash, which
// asked for a key press before the prompt was there at all. Typing goes into
// the prompt from the start. The first thing said (a question, a command's
// answer) ends it for good: from then on the chat is printed into the terminal
// and the prompt sits at the bottom of the window (composeScreen, inline.go).
//
// The notes the chat opened with -- another install answering, the folder not
// being a project -- are shown here, under the words, and printed as the
// chat's first lines when it ends: nothing is printed above this screen.
//
// Only in inline mode. The full-screen chat (--fullscreen) keeps its splash.

const (
	// welcomeMaxWidth is the widest the prompt on this screen is drawn: wide
	// enough for the key line under it (helpText) and the command list above it
	// to fit without being cut.
	welcomeMaxWidth = 76
	welcomeTitle    = "What can I do for you?"
	welcomeHint     = "/ for commands · /help for the keys · ctrl+c to quit"
)

var (
	welcomeTitleStyle = lipgloss.NewStyle().Bold(true)
	welcomeHintStyle  = lipgloss.NewStyle().Foreground(colorGray).Faint(true).Italic(true)
)

// welcoming reports whether the first screen is up: an inline chat in which
// nothing has been said since it opened, waiting at the prompt.
func (m chatModel) welcoming() bool {
	return m.inline.on && m.inline.welcome && len(m.turns) == m.inline.welcomeTurns &&
		m.ready && m.state == stateIdle && !m.setupActive() && m.mcpPanel == nil
}

// welcomeWidth is how wide the first screen's prompt is: welcomeMaxWidth, or
// wider by as much as the keys line needs to carry who is running the client at
// its right end, as it does under the chat -- never wider than the window.
func (m chatModel) welcomeWidth() int {
	w := welcomeMaxWidth
	if m.whoami != "" {
		w = max(w, lipgloss.Width(helpText)+whoamiGap+lipgloss.Width(m.whoami)+rightEdgeGap)
	}
	return min(m.width, w)
}

// welcomeScreen draws the first screen: the lotus, the words, the notes, then
// the prompt (and the command list, when one is open) and the lines under it,
// in a column welcomeWidth wide in the middle of the window. The lotus is left
// out when the window is too small for it.
func (m chatModel) welcomeScreen(bottomLine, status string) string {
	w := m.welcomeWidth()
	words := []string{
		lipgloss.PlaceHorizontal(w, lipgloss.Center, brandStyle.Render(brandName)),
		"",
		lipgloss.PlaceHorizontal(w, lipgloss.Center, welcomeTitleStyle.Render(welcomeTitle)),
		lipgloss.PlaceHorizontal(w, lipgloss.Center, welcomeHintStyle.Render(ansi.Truncate(welcomeHint, w, "…"))),
	}
	var notes []string
	for _, t := range m.turns {
		if block := renderShownBlock(t, w); block != "" {
			notes = append(notes, strings.Split(trimLineEnds(wrapToWidth(block, w)), "\n")...)
		}
	}
	if len(notes) > 0 {
		words = append(words, "")
		words = append(words, notes...)
	}

	// The rows under the prompt come drawn the prompt's width (withStatusLine),
	// so the bottom right corner is the column's.
	below := append(strings.Split(bottomLine, "\n"), strings.Split(status, "\n")...)

	column := func(withLogo bool) string {
		var rows []string
		if withLogo {
			for _, l := range strings.Split(renderLogo(), "\n") {
				rows = append(rows, lipgloss.PlaceHorizontal(w, lipgloss.Center, l))
			}
			rows = append(rows, "")
		}
		rows = append(rows, words...)
		rows = append(rows, "")
		rows = append(rows, below...)
		for i, r := range rows {
			rows[i] = padToWidth(r, w)
		}
		return strings.Join(rows, "\n")
	}
	block := column(w >= setupLogoMinWidth)
	if lipgloss.Height(block) > m.height {
		block = column(false)
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, block)
}

// padToWidth cuts s to w columns, or fills it out to w with spaces, so that
// every row of a column is the same width and lipgloss.Place moves the column
// as one piece instead of centring each row on its own.
func padToWidth(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	if short := w - lipgloss.Width(s); short > 0 {
		s += strings.Repeat(" ", short)
	}
	return s
}
