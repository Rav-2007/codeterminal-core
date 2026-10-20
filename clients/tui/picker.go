package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// A LIST TO CHOOSE FROM: rows, a line of search text that narrows them, and one
// row highlighted. The setup screen uses it twice -- for the provider and for
// the model -- which is why it knows nothing about either (setup.go).
//
// IT HOLDS NO TEXT INPUT OF ITS OWN, deliberately. The search text is a plain
// string the caller feeds one key at a time, so there is exactly one place a
// paste could land in it, and that place refuses pastes (see typeRunes): this
// list is on screen at the very moment a new user is holding an API key on
// their clipboard, and a search box that echoes whatever is pasted into it
// would print that key in clear.

// pickerRow is one choice.
type pickerRow struct {
	// ID is what the caller gets back; it is never drawn.
	ID string
	// Label is the row's name, Hint the dimmer words after it.
	Label string
	Hint  string
	// Dim draws the whole row faint: selectable, but not recommended.
	Dim bool
	// Match is more text the search matches besides Label -- other names the
	// thing is known by.
	Match string
	// Current marks the row that is in use now: the provider prompts go to,
	// the model they are sent to. It is drawn after the label as a green dot
	// and "(current)", so a user changing one of them can see where they are
	// starting from.
	Current bool
}

// listPicker is the list and where the user is in it.
type listPicker struct {
	title  string
	rows   []pickerRow
	filter string
	// cursor indexes the rows that MATCH the filter, not rows itself, so
	// narrowing the list never leaves the highlight on a row that is hidden.
	cursor int
	// placeholder is what the search field says before anything is typed.
	placeholder string
	// compact draws the search field as one line with no border, for a
	// terminal too short to spend three rows on it (setup.go's setupBare).
	compact bool
	// airy leaves a blank line under the title. The caller grants it when the
	// terminal has rows to spare, and takes it back before taking list rows.
	airy bool
}

// pickerFilterMax bounds the search text. No provider or model is found faster
// by typing more than this, and a bound is what keeps a held-down key or a
// mistaken paste from growing a line the box cannot hold.
const pickerFilterMax = 32

func newListPicker(title string, rows []pickerRow) listPicker {
	return listPicker{title: title, rows: rows}
}

// visible returns the indexes of the rows the search text matches, in order.
// Every word typed must appear somewhere in the row's label, hint-free match
// text or id, ignoring case -- "open ro" finds OpenRouter.
func (p listPicker) visible() []int {
	words := strings.Fields(strings.ToLower(p.filter))
	out := make([]int, 0, len(p.rows))
	for i, row := range p.rows {
		hay := strings.ToLower(row.Label + " " + row.Match + " " + row.ID)
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

func (p *listPicker) clamp() {
	n := len(p.visible())
	if p.cursor >= n {
		p.cursor = n - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// move shifts the highlight, stopping at either end rather than wrapping: with
// a list longer than the window, wrapping reads as the list having jumped.
func (p *listPicker) move(delta int) {
	p.cursor += delta
	p.clamp()
}

// typeRunes adds typed characters to the search text and reports whether it
// took them.
//
// A PASTE IS REFUSED WHOLE. More than one rune in a single key event is a
// paste (or a burst no person types), and the likeliest thing on the clipboard
// of someone looking at "choose your provider" is the key they came to enter.
// Taking it would draw it on screen in clear, in a box labelled search. The
// caller says where the key goes instead.
func (p *listPicker) typeRunes(runes []rune, pasted bool) bool {
	if pasted || len(runes) != 1 {
		return false
	}
	r := runes[0]
	if r < ' ' || r == 0x7f {
		return false
	}
	if len([]rune(p.filter)) >= pickerFilterMax {
		return true // taken and dropped: a full box is not an error
	}
	p.filter += string(r)
	p.cursor = 0
	return true
}

func (p *listPicker) backspace() {
	r := []rune(p.filter)
	if len(r) == 0 {
		return
	}
	p.filter = string(r[:len(r)-1])
	p.cursor = 0
}

// selected returns the highlighted row, or false when nothing matches.
func (p listPicker) selected() (pickerRow, bool) {
	vis := p.visible()
	if len(vis) == 0 {
		return pickerRow{}, false
	}
	c := p.cursor
	if c < 0 {
		c = 0
	}
	if c >= len(vis) {
		c = len(vis) - 1
	}
	return p.rows[vis[c]], true
}

// selectID moves the highlight to the row with this id, if the filter shows it.
func (p *listPicker) selectID(id string) {
	for i, idx := range p.visible() {
		if p.rows[idx].ID == id {
			p.cursor = i
			return
		}
	}
}

var (
	pickerTitleStyle  = lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
	pickerSearchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	pickerHintStyle   = lipgloss.NewStyle().Foreground(colorGray)
	pickerDimStyle    = lipgloss.NewStyle().Foreground(colorGray).Faint(true)
	// pickerBarStyle is the highlighted row: a bar the whole width of the list,
	// as the owner's reference screen draws it (2026-10-11). Dark text on blue,
	// so the row reads as selected in a terminal that shows no bold.
	pickerBarStyle = lipgloss.NewStyle().Background(colorBlue).Foreground(lipgloss.Color("16")).Bold(true)
	// pickerCurrentStyle is the dot and "(current)" after the row in use.
	pickerCurrentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	// pickerFieldStyle is the border round the search field; pickerFieldLive,
	// the same once something has been typed into it.
	pickerFieldStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorDim).Padding(0, 1)
	pickerFieldLive  = pickerFieldStyle.BorderForeground(colorBlue)
)

const (
	// pickerLabelMax caps the label column WHEN ROWS HAVE HINTS, so one long
	// name does not push every hint off the right-hand edge. A list with no
	// hints gives the label the whole row: a model id is its only name, and
	// cutting "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning" at 34 columns to
	// keep room for nothing made two models look like one (seen 2026-10-12).
	pickerLabelMax = 34
	// pickerCurrentMark follows the label of the row in use.
	pickerCurrentMark = " ● (current)"
	// pickerPlaceholder is the search field's text when a caller names none.
	pickerPlaceholder = "Type to search…"
	// pickerCursor is the highlighted row's marker; the others get its width
	// in spaces.
	pickerCursor = "❯ "
)

// searchField draws the search text, in a bordered field unless compact.
//
// THE FIELD IS DRAWN, NOT AN INPUT. See the top of this file: the text in it
// is a string fed one key at a time, which is what lets a paste be refused.
func (p listPicker) searchField(width int) string {
	placeholder := p.placeholder
	if placeholder == "" {
		placeholder = pickerPlaceholder
	}
	if p.compact || width < 12 {
		if p.filter == "" {
			return pickerHintStyle.Render(ansi.Truncate(placeholder, width, "…"))
		}
		return pickerSearchStyle.Render(ansi.Truncate(p.filter, width-1, "…")) + "▏"
	}
	inner := width - 4 // the border and one column of padding, each side
	style, text := pickerFieldStyle, pickerHintStyle.Render(ansi.Truncate(placeholder, inner, "…"))
	if p.filter != "" {
		style = pickerFieldLive
		text = pickerSearchStyle.Render(ansi.Truncate(p.filter, inner-1, "…")) + "▏"
	}
	return style.Width(width - 2).Render(text)
}

// view draws the list width columns wide, showing at most maxRows rows around
// the highlight and saying how many are above and below.
//
// Every line is cut to width here rather than left to the terminal: a wrapped
// row would push the rows under it down and the highlight would no longer be
// on the line the user thinks it is.
func (p listPicker) view(width, maxRows int) string {
	if width < 8 {
		width = 8
	}
	if maxRows < 1 {
		maxRows = 1
	}
	vis := p.visible()

	var lines []string
	if p.title != "" {
		lines = append(lines, pickerTitleStyle.Render(ansi.Truncate(p.title, width, "…")))
		if p.airy && !p.compact {
			lines = append(lines, "")
		}
	}
	lines = append(lines, p.searchField(width))

	if len(vis) == 0 {
		lines = append(lines, "", pickerHintStyle.Render(ansi.Truncate("nothing matches — backspace to widen the search", width, "…")))
		return strings.Join(lines, "\n")
	}

	cursor := p.cursor
	if cursor >= len(vis) {
		cursor = len(vis) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	// The window follows the highlight, keeping it near the middle until the
	// list's own ends are reached.
	start := cursor - maxRows/2
	if start > len(vis)-maxRows {
		start = len(vis) - maxRows
	}
	if start < 0 {
		start = 0
	}
	end := start + maxRows
	if end > len(vis) {
		end = len(vis)
	}

	room := width - lipgloss.Width(pickerCursor) // what a row has after its marker
	// THE LABEL COLUMN IS SET BY THE ROWS THAT HAVE A HINT, and only they are
	// held to it. A row with nothing after its name has the whole row for the
	// name -- one model that "did not answer" at the foot of a list must not
	// cut the ids of the thirty-eight above it to make room for a note that
	// is not theirs (seen in the real app, 2026-10-12).
	labelW := 0
	for _, idx := range vis {
		if w := lipgloss.Width(p.rows[idx].Label); p.rows[idx].Hint != "" && w > labelW {
			labelW = w
		}
	}
	if labelW > pickerLabelMax {
		labelW = pickerLabelMax
	}
	if labelW > room {
		labelW = room
	}

	// ALWAYS BOTH MARKER LINES, blank when there is nothing beyond: the list
	// keeps one height as the highlight moves, so nothing under it jumps. The
	// upper one doubles as the gap under the search field.
	above, below := "", ""
	if start > 0 {
		above = pickerHintStyle.Render(ansi.Truncate(fmt.Sprintf("  ▲ %d more", start), width, "…"))
	}
	if end < len(vis) {
		below = pickerHintStyle.Render(ansi.Truncate(fmt.Sprintf("  ▼ %d more", len(vis)-end), width, "…"))
	}
	lines = append(lines, above)
	blank := strings.Repeat(" ", lipgloss.Width(pickerCursor))
	for i := start; i < end; i++ {
		row := p.rows[vis[i]]
		// The mark is kept and the label gives way to it: on a narrow list
		// "which one am I on" matters more than the end of a long name.
		mark := ""
		if row.Current && room >= lipgloss.Width(pickerCurrentMark)+4 {
			mark = pickerCurrentMark
		}
		limit := room - lipgloss.Width(mark)
		if row.Hint != "" && labelW < limit {
			limit = labelW
		}
		label := ansi.Truncate(row.Label, limit, "…")
		// The hint starts in the same column on every row that has one: the
		// mark is counted as part of what comes before it.
		pad, hint := "", ""
		if row.Hint != "" {
			pad = strings.Repeat(" ", max(0, labelW-lipgloss.Width(label)-lipgloss.Width(mark)))
			if left := room - lipgloss.Width(label) - lipgloss.Width(mark) - lipgloss.Width(pad) - 2; left > 3 {
				hint = "  " + ansi.Truncate(row.Hint, left, "…")
			}
		}
		switch {
		case i == cursor:
			// One style for the whole bar: a second colour inside it would
			// have to fight the bar's own background.
			lines = append(lines, pickerBarStyle.Width(width).Render(pickerCursor+label+mark+pad+hint))
		case row.Dim:
			lines = append(lines, pickerDimStyle.Render(blank+label+mark+pad+hint))
		default:
			lines = append(lines, unselectedItemStyle.Render(blank+label)+pickerCurrentStyle.Render(mark)+pickerHintStyle.Render(pad+hint))
		}
	}
	lines = append(lines, below)
	return strings.Join(lines, "\n")
}
