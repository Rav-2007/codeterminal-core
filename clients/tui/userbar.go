package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// WHAT YOU SAID IS SHADED (owner, 2026-11-21, showing Cline's chat: "it should
// highlight something like this when an input from the user which is given to
// the agent ... instead of using the '>', we can use '➤', and the user's input
// should be differentiated using a shade ... the output of the agent is normal
// and the input is shaded").
//
// A question in the chat is a bar the width of the terminal, on a shade of its
// own, starting with the ➤ the prompt starts with; the answers under it are on
// the terminal's own background. It replaces the pink "You:" line: the bar says
// whose words these are without the label.
//
// THE BAR IS FILLED WITH SPACES IN THE SHADE, not with "erase to the end of the
// line": the full-screen chat's viewport pads every row with plain spaces after
// what it is given, which would paint over an erase's shade. The spaces end in
// a colour reset, so trimLineEnds, which takes plain spaces off a printed row,
// leaves them alone. At width 0 -- /copy, where nothing is cut at the window's
// edge -- there is no edge to fill to, and the bar is only as long as the words.

var (
	// userBarShade is the bar: a step lighter than a dark terminal, a step
	// darker than a light one.
	userBarShade     = lipgloss.AdaptiveColor{Light: "254", Dark: "238"}
	userBarStyle     = lipgloss.NewStyle().Background(userBarShade)
	userBarMarkStyle = lipgloss.NewStyle().Background(userBarShade).Foreground(colorBlue).Bold(true)
	userBarTextStyle = lipgloss.NewStyle().Background(userBarShade).Foreground(colorPink)
)

// renderUserBlock draws a question as its bar: "➤ " in front of the first row,
// two spaces in front of the rest, each row the width of the terminal.
func renderUserBlock(text string, width int) string {
	body := 0 // no limit
	if width > 2 {
		body = width - 2
	}
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		rows = append(rows, strings.Split(wrapToWidth(line, body), "\n")...)
	}
	for i, r := range rows {
		mark := "  "
		if i == 0 {
			mark = promptMark + " "
		}
		row := userBarMarkStyle.Render(mark) + userBarTextStyle.Render(r)
		if pad := width - lipgloss.Width(row); width > 0 && pad > 0 {
			row += userBarStyle.Render(strings.Repeat(" ", pad))
		}
		rows[i] = row
	}
	return strings.Join(rows, "\n")
}
