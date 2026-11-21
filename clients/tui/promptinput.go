package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/mattn/go-runewidth"
)

// A LONG PROMPT GROWS DOWNWARDS, IT DOES NOT SCROLL SIDEWAYS (owner, 2026-11-21:
// "the prompt is in single [line] and can't be able to see the content what i
// typed"). The input was one row that scrolled left as it filled, so of a long
// question only its last eighty-odd characters were ever on screen.
//
// Now a prompt longer than its row is drawn over as many rows as it needs,
// wrapped at spaces, up to inputRowsMax; past that the rows around the cursor
// are shown. The text input still owns the text, the cursor and every key --
// only the drawing is here -- and a prompt that fits on its row is drawn by the
// input itself, exactly as before. The prompt's rows change as it is typed into
// or sent, so the frame is re-measured whenever they do (Update, inline.go).
//
// Not for a hidden entry (a key being pasted): that stays one masked row.

// inputRowsMax is the most rows a prompt takes: a quarter of the window, never
// more than six, so the chat above it keeps the room.
func (m chatModel) inputRowsMax() int {
	return max(1, min(6, m.height/4))
}

// inputShown reports whether the prompt's place holds the input, rather than a
// question standing in for it (an approval, an edit review, save-or-quit).
func (m chatModel) inputShown() bool {
	switch {
	case m.state == stateEditReview && m.reviewPrepared != nil,
		m.state == stateToolApproval && m.pendingApproval != nil,
		m.newChatArmed, m.quitChatArmed:
		return false
	}
	return true
}

// oneInputRow is the layout of a prompt that is not wrapped. Shared, because
// the frame asks for the prompt's rows on every message -- every streamed token
// among them -- and a fresh slice each time showed in the allocations per token
// (renderbench_test.go).
var oneInputRow = [][2]int{{0, 0}}

// inputLayout wraps the prompt's text into rows of rune ranges [start, end) and
// says which row the cursor is on. One row means it fits and is drawn as before.
func (m chatModel) inputLayout() (rows [][2]int, cursorRow int) {
	if !m.promptBoxed() || !m.inputShown() || m.input.EchoMode != textinput.EchoNormal || m.input.Value() == "" {
		return oneInputRow, 0
	}
	rs := []rune(m.input.Value())
	cols := m.promptWidth() - runewidth.StringWidth(m.input.Prompt)
	if cols < 4 {
		return [][2]int{{0, len(rs)}}, 0
	}
	rows, lastWidth := wrapRunes(rs, cols)
	pos := min(max(m.input.Position(), 0), len(rs))
	// The cursor after the last character of a full row starts the next one.
	if pos == len(rs) && lastWidth >= cols {
		rows = append(rows, [2]int{len(rs), len(rs)})
	}
	for i, r := range rows {
		if pos >= r[0] && (pos < r[1] || i == len(rows)-1) {
			return rows, i
		}
	}
	return rows, len(rows) - 1
}

// wrapRunes breaks rs into rows of at most cols columns, after the last space
// that fits, or mid-word when a word is longer than the row. It returns the
// last row's width too.
func wrapRunes(rs []rune, cols int) (rows [][2]int, lastWidth int) {
	start := 0
	for start < len(rs) {
		w, end, lastSpace, wAtSpace := 0, start, -1, 0
		for end < len(rs) {
			r, cw := rs[end], 1 // printable ASCII is one column: the common case, done without a lookup
			if r < 0x20 || r >= 0x7f {
				cw = runewidth.RuneWidth(r)
			}
			if w+cw > cols {
				break
			}
			w += cw
			if r == ' ' {
				lastSpace, wAtSpace = end, w
			}
			end++
		}
		if end < len(rs) && lastSpace >= start {
			end, w = lastSpace+1, wAtSpace
		}
		if end == start {
			end = start + 1 // a character wider than the row
		}
		rows = append(rows, [2]int{start, end})
		lastWidth = w
		start = end
	}
	if len(rows) == 0 {
		rows = oneInputRow
	}
	return rows, lastWidth
}

// inputRows is how many rows the input takes in the prompt.
func (m chatModel) inputRows() int {
	rows, _ := m.inputLayout()
	return min(len(rows), m.inputRowsMax())
}

// inputView is the input as drawn in the prompt: the input's own row when it
// fits, else its wrapped rows -- the prompt's mark on the first, two spaces in
// front of the rest, the cursor where it is.
func (m chatModel) inputView() string {
	rows, cur := m.inputLayout()
	if len(rows) <= 1 {
		return m.input.View()
	}
	rs := []rune(m.input.Value())
	pos := min(max(m.input.Position(), 0), len(rs))
	show := m.inputRowsMax()
	first := 0
	if len(rows) > show {
		first = min(max(cur-show+1, 0), len(rows)-show)
	}
	text := m.input.TextStyle.Inline(true).Render
	lines := make([]string, 0, show)
	for i := first; i < len(rows) && i < first+show; i++ {
		r := rows[i]
		prefix := strings.Repeat(" ", runewidth.StringWidth(m.input.Prompt))
		if i == 0 {
			prefix = m.input.PromptStyle.Render(m.input.Prompt)
		}
		if i != cur {
			lines = append(lines, prefix+text(string(rs[r[0]:r[1]])))
			continue
		}
		c := m.input.Cursor
		char, after := " ", ""
		if pos < r[1] {
			char, after = string(rs[pos]), string(rs[pos+1:r[1]])
		}
		c.SetChar(char)
		lines = append(lines, prefix+text(string(rs[r[0]:pos]))+c.View()+text(after))
	}
	return strings.Join(lines, "\n")
}
