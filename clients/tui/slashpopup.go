package tui

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
)

// THE COMMAND POPUP SHOWS A WINDOW OF ITS MATCHES, not all of them.
//
// FOUND IN THE REAL APP, 2026-10-12. Typing "/" matches every command -- 37 of
// them -- and the popup drew them all, in a 24-row terminal. A frame taller
// than the terminal loses its TOP, so what scrolled away was the highlighted
// row and the first commands in the catalog: /model, /provider and /account,
// the three the owner had just said they could not find. The prompt's own
// placeholder now says "/ for commands", which is an invitation to exactly
// that screen.
//
// So the popup is bounded like the lists in the setup box (picker.go): the rows
// around the highlight, and a line above and below saying how many more there
// are. The functions are here, and renderSlashPopup in chat.go calls them, for
// the reason chatExtras gives in statusline.go.

const (
	// slashPopupMaxRows is the most commands shown at once, however tall the
	// terminal: past this a completion menu stops being a glance.
	slashPopupMaxRows = 10
	// slashPopupMinRows is the fewest it is cut to in a short terminal.
	slashPopupMinRows = 3
	// slashPopupAround is every row of the screen that is not a popup row or
	// transcript: header, the prompt in its box, the line under it, help, the
	// popup's border and its two "more" lines -- and three rows of transcript
	// kept in view.
	slashPopupAround = 13
)

// slashPopupWindow is the half-open range of matches drawn, for the highlight
// at idx among n matches in a terminal height rows tall. The window follows
// the highlight, keeping it near the middle until an end of the list is reached.
func slashPopupWindow(idx, n, height int) (first, last int) {
	rows := height - slashPopupAround
	if rows > slashPopupMaxRows {
		rows = slashPopupMaxRows
	}
	if rows < slashPopupMinRows {
		rows = slashPopupMinRows
	}
	if n <= rows {
		return 0, n
	}
	first = idx - rows/2
	if first > n-rows {
		first = n - rows
	}
	if first < 0 {
		first = 0
	}
	return first, first + rows
}

// slashPopupMarked puts the "more" lines round the rows drawn, when there are
// matches out of sight on that side. No line is added for a side with none: a
// popup that fits needs no markers, and is drawn exactly as it always was.
func slashPopupMarked(lines []string, above, below int) []string {
	if above > 0 {
		lines = append([]string{pickerHintStyle.Render(fmt.Sprintf("  ▲ %d more", above))}, lines...)
	}
	if below > 0 {
		lines = append(lines, pickerHintStyle.Render(fmt.Sprintf("  ▼ %d more — keep typing to narrow", below)))
	}
	return lines
}

// slashSummary cuts a command's summary to the popup's column. By COLUMNS, on a
// character boundary: the cut used to be a byte slice, which splits a summary
// in the middle of an "…" or a "—" and draws the halves as two broken glyphs.
func slashSummary(summary string) string {
	return ansi.Truncate(summary, 50, "...")
}
