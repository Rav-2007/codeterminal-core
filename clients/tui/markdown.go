package main

import (
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// AN ANSWER IS MARKDOWN, AND IT WAS DRAWN AS IF IT WERE NOT.
//
// Every model this client talks to writes markdown: "## Heading", "**this
// matters**", a pipe table. renderTurnBlock put the answer through one grey
// style, so all of it reached the screen as punctuation -- a table was rows of
// "|" and "---", and the one phrase the model marked as important looked like
// every other phrase with four asterisks round it. ASKED FOR 2026-10-06 by the
// owner, with a screenshot of another assistant's answer beside ours: headings
// and the important words in one colour, the rest in another, tables boxed.
//
// THE PALETTE IS TWO COLOURS ON THE TERMINAL'S BLACK (styles.go): blue for
// what the model marked, grey for everything else. Two blues and two greys, so
// there are four levels and no more:
//
//	blue, bold     headings, **strong**, table headers, list markers
//	steel          *emphasis*, `code`, links
//	grey           the answer's own words
//	dim grey       structure: table borders, rules, quote bars, fences
//
// WHY THIS IS NOT A LIBRARY. glamour renders markdown and renders it well. It
// was not used because of what this file has to be, each of which is pinned by
// a test rather than promised here:
//
//   - A FUNCTION OF (text, width, colour profile) AND NOTHING ELSE, because
//     the transcript cache stores a turn's rendered bytes and replays them
//     (rendercache.go), and the profile is pinned once (renderprofile.go).
//   - CHEAP ENOUGH TO RUN ON EVERY TOKEN. The turn being streamed is rendered
//     again for each token that arrives, so this is one pass over the lines
//     with no tree built.
//   - EVERY LINE CLOSES WHAT IT OPENS. The viewport shows a window of lines
//     and Bubble Tea redraws only the lines that changed, so a colour left
//     open at the end of one line colours whatever is drawn after it, and a
//     line whose colour was opened on a line now scrolled away is drawn
//     without it. No escape sequence here spans a newline.
//   - NO LINE WIDER THAN THE TERMINAL, down to a width of one. A table that
//     does not fit is drawn as a list of its rows instead.
//   - WHAT IS NOT MARKDOWN IS LEFT ALONE. A code block and an edit block are
//     drawn character for character: they are what gets copied out of the
//     terminal and what `/apply` is read against. "2 * 3 * 4", "snake_case"
//     and "**kwargs" are not emphasis.
//
// WHAT IT DOES NOT DO. A paragraph's own line breaks are kept, not joined: a
// model that writes one line per step means one line per step. A four-space
// indent is indentation, never a code block. Reference links, footnotes and
// raw HTML are drawn as written. Text that already carries colour of its own
// (the SGR the sanitiser lets through) is passed on untouched.

// assistantLabel opens every answer.
const assistantLabel = "Mochiii: "

// mdTab is what a tab is drawn as. Lip Gloss turned a tab into four spaces
// inside Style.Render, which is where an answer used to go, so Go code kept
// its shape; a raw tab has no width a wrap can count.
const mdTab = "    "

// mdUnbounded is the width used before the terminal has reported one (width
// 0): nothing wraps, as wrapToWidth leaves such content alone.
const mdUnbounded = 1 << 20

// mdPen is one way of drawing text. The pens are the whole vocabulary: a line
// is a sequence of (text, pen) and nothing else reaches the terminal.
type mdPen uint8

const (
	penBody      mdPen = iota // the answer's own words
	penStrong                 // **strong**
	penEm                     // *emphasis*
	penStrongEm               // ***both***
	penCode                   // `code`
	penCodeBlock              // a line inside a fence or an edit block
	penHeading                // a heading, a table's header row
	penLink                   // a link's text
	penStrike                 // ~~struck~~
	penQuote                  // a quoted line
	penMarker                 // a list's bullet or number
	penRule                   // structure: borders, rules, fences, addresses
	penCount
)

// mdPens are the escape sequences that turn each pen on and off under ONE
// colour profile.
//
// They are taken from Lip Gloss rather than written out, so a 16-colour
// terminal and NO_COLOR are honoured exactly as they are everywhere else in
// this client -- and then kept, because Style.Render costs several allocations
// a call and an answer is hundreds of runs.
type mdPens struct {
	profile     termenv.Profile
	open, close [penCount]string
}

// mdPenMemo holds the pens of the last profile asked for. One profile per
// process in production (it is pinned at startup); tests sweep profiles, which
// is why it is keyed and not computed once. Atomic so a render on another
// goroutine can never observe a half-written set.
var mdPenMemo atomic.Pointer[mdPens]

func currentPens() *mdPens {
	profile := lipgloss.ColorProfile()
	if p := mdPenMemo.Load(); p != nil && p.profile == profile {
		return p
	}
	p := &mdPens{profile: profile}
	for pen := range mdStyles {
		p.open[pen], p.close[pen] = sgrAround(mdStyles[pen])
	}
	mdPenMemo.Store(p)
	return p
}

// sgrAround returns what st writes before and after text. No SGR sequence
// contains an "x", so the probe cannot be mistaken for part of one.
func sgrAround(st lipgloss.Style) (open, close string) {
	s := st.Render("x")
	i := strings.IndexByte(s, 'x')
	if i < 0 {
		return "", ""
	}
	return s[:i], s[i+1:]
}

// mdRun is a stretch of text in one pen. A run whose text is "\n" is a forced
// line break (<br>).
type mdRun struct {
	text string
	pen  mdPen
}

// renderAnswer draws a model's answer at width.
func renderAnswer(text string, width int) string {
	r := mdRenderer{pens: currentPens(), width: width}
	if width <= 0 {
		r.width = mdUnbounded
	}
	r.render(text)
	out := r.b.String()
	if r.blank {
		out = out[:len(out)-1] // the document ended on a blank line
	}
	return out
}

// mdRenderer is one render in progress. A value on the stack of renderAnswer:
// nothing in it outlives the call.
type mdRenderer struct {
	b     strings.Builder
	pens  *mdPens
	width int

	labelled   bool // the "Mochiii:" label has been written
	afterLabel bool // ...and nothing else has, on its own line
	started    bool // a line has been written
	blank      bool // the last line written was empty

	flow  mdFlow
	nodes []mdNode // scratch for one line's inline parse
	runs  []mdRun  // scratch for one line's runs
}

// ---------------------------------------------------------------------------
// Blocks: one pass over the lines.

func (r *mdRenderer) render(src string) {
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		line := mdLine(lines[i])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			r.blankLine()
			continue
		}

		// A FENCE, then everything to its close, as written. Checked first:
		// nothing inside a fence is markdown, including a line of "---".
		if ch, n, ok := fenceOpen(trimmed); ok {
			r.labelOwnLine()
			r.verbatim(line, penRule)
			for i++; i < len(lines); i++ {
				l := mdLine(lines[i])
				if fenceClose(l, ch, n) {
					r.verbatim(l, penRule)
					break
				}
				r.verbatim(l, penCodeBlock)
			}
			continue
		}

		// AN EDIT BLOCK (daemon/prompts/system.txt). Its "=======" is not a
		// heading underline and its SEARCH text is not prose: it is matched
		// against the file byte for byte, so it is shown byte for byte.
		if trimmed == editSearchMarker {
			r.labelOwnLine()
			r.verbatim(line, penRule)
			for i++; i < len(lines); i++ {
				l := mdLine(lines[i])
				t := strings.TrimSpace(l)
				if t == editDivider || strings.HasPrefix(t, editReplaceMarker) {
					r.verbatim(l, penRule)
					if t != editDivider {
						break
					}
					continue
				}
				r.verbatim(l, penCodeBlock)
			}
			continue
		}

		// Colour of the text's own: passed on, not parsed. See the sanitiser.
		if strings.IndexByte(line, '\x1b') >= 0 {
			r.raw(line)
			continue
		}

		if text, ok := heading(line); ok {
			r.labelOwnLine()
			r.blankLine() // a heading stands apart from what is above it
			r.runs = r.inline(r.runs[:0], text, penHeading)
			r.flowLine("", 0, "", 0, r.runs)
			continue
		}

		if isRule(trimmed) {
			r.labelOwnLine()
			n := r.width
			if n > mdRuleMax {
				n = mdRuleMax
			}
			r.startLine()
			r.b.WriteString(r.pens.open[penRule])
			writeRepeat(&r.b, "─", n)
			r.b.WriteString(r.pens.close[penRule])
			continue
		}

		if strings.IndexByte(line, '|') >= 0 && i+1 < len(lines) {
			if aligns, ok := tableDelimiter(mdLine(lines[i+1])); ok {
				if header := splitRow(line); len(header) == len(aligns) {
					j := i + 2
					for j < len(lines) && strings.TrimSpace(lines[j]) != "" && strings.IndexByte(lines[j], '|') >= 0 {
						j++
					}
					r.labelOwnLine()
					r.table(header, aligns, lines[i+2:j])
					i = j - 1
					continue
				}
			}
		}

		if depth, rest, ok := quoteLine(line); ok {
			r.labelOwnLine()
			// Each level costs two columns. A line of forty ">" is not forty
			// levels of quotation anyone will read, and its bars alone would
			// be wider than the terminal.
			if most := r.capIndent(2*depth) / 2; depth > most {
				depth = most
			}
			if depth > mdMaxQuoteDepth {
				depth = mdMaxQuoteDepth
			}
			if depth < 1 {
				depth = 1
			}
			var prefix strings.Builder
			for d := 0; d < depth; d++ {
				prefix.WriteString(r.pens.open[penRule])
				prefix.WriteString("│")
				prefix.WriteString(r.pens.close[penRule])
				prefix.WriteByte(' ')
			}
			r.runs = r.inline(r.runs[:0], rest, penQuote)
			r.flowLine(prefix.String(), 2*depth, prefix.String(), 2*depth, r.runs)
			continue
		}

		if indent, marker, rest, ok := listItem(line); ok {
			r.labelOwnLine()
			markerW := textWidth(marker)
			// The indent and the marker together take at most half the line.
			if indent = r.capIndent(indent+markerW+1) - markerW - 1; indent < 0 {
				indent = 0
			}
			first := spaceString(indent) + r.pens.open[penMarker] + marker + r.pens.close[penMarker] + " "
			r.runs = r.inline(r.runs[:0], rest, penBody)
			r.flowLine(first, indent+markerW+1, spaceString(indent+markerW+1), indent+markerW+1, r.runs)
			continue
		}

		// A paragraph line. The first one of an answer carries the label, as
		// "Mochiii: the answer" always has.
		body := strings.TrimLeft(line, " ")
		r.runs = r.inline(r.runs[:0], body, penBody)
		if !r.labelled {
			r.labelled = true
			r.startLine()
			r.flow.begin(&r.b, r.pens, r.width, r.width, "", nil)
			r.flow.feed(assistantLabel, penBody)
			for _, run := range r.runs {
				r.flow.feed(run.text, run.pen)
			}
			r.flow.finish()
			continue
		}
		indent := r.capIndent(len(line) - len(body))
		r.flowLine(spaceString(indent), indent, spaceString(indent), indent, r.runs)
	}
	if !r.labelled {
		// Nothing but blank lines, or nothing: the label alone, as before.
		r.b.WriteString(r.pens.open[penBody])
		r.b.WriteString(assistantLabel)
		r.b.WriteString(r.pens.close[penBody])
		r.blank = false
	}
}

// The edit block's three marker lines.
const (
	editSearchMarker  = "<<<<<<< SEARCH"
	editDivider       = "======="
	editReplaceMarker = ">>>>>>> REPLACE"
)

// mdMaxQuoteDepth is how many levels of quotation are drawn as bars.
const mdMaxQuoteDepth = 8

// mdRuleMax bounds a horizontal rule, which is otherwise as wide as the
// terminal -- and before the terminal has said how wide that is, unbounded.
const mdRuleMax = 200

// mdLine is one source line as it will be measured: no carriage return, tabs
// as the spaces they are drawn as, and every other kind of space as a space.
//
// THE LAST PART IS FOR wrapToWidth, which runs over everything the viewport is
// given. ansi.Wrap breaks a line at any Unicode space and counts that space by
// its BYTES, so a line of exactly the terminal's width holding one U+2000 (an
// en quad: one column, three bytes) measured two columns too wide there and
// was wrapped a second time -- mid-colour, which is the one thing this file
// exists to prevent. FOUND 2026-10-06 by FuzzAnAnswerDraws, thirteen seconds
// into its first run. A no-break space is left alone: ansi.Wrap does not break
// at it, and it is what a model writes to hold two words together.
func mdLine(s string) string {
	s = strings.TrimSuffix(s, "\r")
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\t' || c >= 0x80 {
			return mdNormalizeSpaces(s)
		}
	}
	return s
}

// mdNormalizeSpaces is mdLine for a line that has a tab or a character outside ASCII
// in it: the same string back unless one of them is a space to rewrite.
func mdNormalizeSpaces(s string) string {
	plain := true
	for _, r := range s {
		if r == '\t' || (r >= 0x80 && r != 0xa0 && unicode.IsSpace(r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString(mdTab)
		case r == 0x3000: // an ideographic space is two columns wide
			b.WriteString("  ")
		case r >= 0x80 && r != 0xa0 && unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// startLine begins an output line.
func (r *mdRenderer) startLine() {
	if r.started {
		r.b.WriteByte('\n')
	}
	r.started, r.blank, r.afterLabel = true, false, false
}

// blankLine writes one empty line, and only one however many the source had;
// none before the first line of the answer.
func (r *mdRenderer) blankLine() {
	if !r.started || r.blank || r.afterLabel {
		return
	}
	r.b.WriteByte('\n')
	r.blank = true
}

// labelOwnLine writes the label on a line of its own, for an answer that opens
// with something a label cannot sit in front of: a heading, a table, a list.
func (r *mdRenderer) labelOwnLine() {
	if r.labelled {
		return
	}
	r.labelled = true
	r.startLine()
	r.b.WriteString(r.pens.open[penBody])
	r.b.WriteString(strings.TrimRight(assistantLabel, " "))
	r.b.WriteString(r.pens.close[penBody])
	r.afterLabel = true
}

// capIndent keeps an indent from eating the line: at most half the width.
func (r *mdRenderer) capIndent(indent int) int {
	if half := r.width / 2; indent > half {
		return half
	}
	return indent
}

// flowLine writes runs as one or more lines: first after firstPrefix, the
// rest after restPrefix. The prefixes are already styled; their widths are
// given because a styled string cannot be measured by its length.
func (r *mdRenderer) flowLine(firstPrefix string, firstW int, restPrefix string, restW int, runs []mdRun) {
	r.startLine()
	r.b.WriteString(firstPrefix)
	r.flow.begin(&r.b, r.pens, r.width-firstW, r.width-restW, restPrefix, nil)
	for _, run := range runs {
		r.flow.feed(run.text, run.pen)
	}
	r.flow.finish()
}

// verbatim writes one line exactly as it is, in pen, wrapped only where the
// terminal would otherwise cut it. An empty line stays an empty line: inside a
// code block blank lines are content, not spacing to be collapsed.
func (r *mdRenderer) verbatim(line string, pen mdPen) {
	if strings.IndexByte(line, '\x1b') >= 0 {
		r.raw(line)
		return
	}
	r.startLine()
	if line == "" {
		return
	}
	r.flow.begin(&r.b, r.pens, r.width, r.width, "", nil)
	r.flow.feed(line, pen)
	r.flow.finish()
}

// raw passes on a line that carries escape sequences of its own -- highlighted
// code, which the sanitiser deliberately lets through. It is wrapped the way
// every answer used to be and not otherwise touched: its colours are the
// model's, and parsing markdown across them would cut them.
func (r *mdRenderer) raw(line string) {
	r.labelOwnLine()
	r.startLine()
	if r.width < mdUnbounded {
		line = ansi.Wrap(line, r.width, "")
	}
	r.b.WriteString(line)
}

// ---------------------------------------------------------------------------
// Recognising a block from its first line.

// fenceOpen reports a code fence's character and length. s is trimmed: a fence
// under a list item is indented, and models indent them by whatever they like.
func fenceOpen(s string) (ch byte, n int, ok bool) {
	if len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return 0, 0, false
	}
	ch = s[0]
	for n < len(s) && s[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	// "```code```" on one line is inline code, not a fence that never closes.
	if ch == '`' && strings.IndexByte(s[n:], '`') >= 0 {
		return 0, 0, false
	}
	return ch, n, true
}

// fenceClose reports whether line closes a fence of n ch's.
func fenceClose(line string, ch byte, n int) bool {
	s := strings.TrimSpace(line)
	if len(s) < n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != ch {
			return false
		}
	}
	return true
}

// heading returns the text of an ATX heading ("## Title"). "#hashtag" and a
// bare "#" are not headings.
func heading(line string) (string, bool) {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 {
		return "", false
	}
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n == len(s) || s[n] != ' ' {
		return "", false
	}
	text := strings.TrimSpace(s[n:])
	// A closing run of #s, when it stands apart from the title.
	if t := strings.TrimRight(text, "#"); t != text && strings.HasSuffix(t, " ") {
		text = strings.TrimSpace(t)
	}
	return text, text != ""
}

// isRule reports a horizontal rule: three or more of one of - * _ and nothing
// else but spaces. s is trimmed.
func isRule(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case c:
			n++
		case ' ':
		default:
			return false
		}
	}
	return n >= 3
}

// quoteLine reports a quoted line and how deeply it is quoted.
func quoteLine(line string) (depth int, rest string, ok bool) {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 || !strings.HasPrefix(s, ">") {
		return 0, "", false
	}
	for strings.HasPrefix(s, ">") {
		depth++
		s = strings.TrimPrefix(s[1:], " ")
	}
	return depth, s, true
}

// listItem reports a list item: how far it is indented, the marker to draw and
// the item's text.
//
// The marker needs a space after it and text after that, which is what keeps
// "*emphasis*", "-flag", "3.14" and a lone "-" out. A bullet is drawn as "•"
// whichever of - * + wrote it; a number is drawn as written, because the
// answer may refer to "step 3".
func listItem(line string) (indent int, marker, rest string, ok bool) {
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent >= len(line) {
		return 0, "", "", false
	}
	end := indent
	switch c := line[indent]; {
	case c == '-' || c == '*' || c == '+':
		marker, end = "•", indent+1
	case c >= '0' && c <= '9':
		for end < len(line) && end-indent < 9 && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end >= len(line) || (line[end] != '.' && line[end] != ')') {
			return 0, "", "", false
		}
		end++
		marker = line[indent:end]
	default:
		return 0, "", "", false
	}
	if end >= len(line) || line[end] != ' ' {
		return 0, "", "", false
	}
	rest = strings.TrimLeft(line[end:], " ")
	if rest == "" {
		return 0, "", "", false
	}
	// A task list's box takes the bullet's place.
	if marker == "•" && len(rest) > 3 && rest[0] == '[' && rest[2] == ']' && rest[3] == ' ' {
		switch rest[1] {
		case ' ':
			marker, rest = "☐", strings.TrimLeft(rest[4:], " ")
		case 'x', 'X':
			marker, rest = "☑", strings.TrimLeft(rest[4:], " ")
		}
	}
	return indent, marker, rest, true
}

// ---------------------------------------------------------------------------
// Tables.

type mdAlign uint8

const (
	alignLeft mdAlign = iota
	alignCenter
	alignRight
)

// tableDelimiter reads the row under a table's header ("|---|:--:|") and
// returns each column's alignment. A row of dashes with no pipe in it is a
// rule, not a table, unless it stands under a header of one column -- which
// the caller checks by comparing counts.
func tableDelimiter(line string) ([]mdAlign, bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.IndexByte(s, '-') < 0 || strings.IndexByte(s, '|') < 0 {
		return nil, false
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '|', '-', ':', ' ':
		default:
			return nil, false
		}
	}
	cells := splitRow(s)
	aligns := make([]mdAlign, len(cells))
	for i, c := range cells {
		if strings.IndexByte(c, '-') < 0 {
			return nil, false
		}
		left, right := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":")
		switch {
		case left && right:
			aligns[i] = alignCenter
		case right:
			aligns[i] = alignRight
		}
	}
	return aligns, true
}

// splitRow splits a table row into its cells. A pipe written "\|" belongs to
// its cell.
func splitRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	if strings.HasSuffix(s, "|") && !strings.HasSuffix(s, `\|`) {
		s = s[:len(s)-1]
	}
	var cells []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '|':
			cells = append(cells, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(cells, strings.TrimSpace(s[start:]))
}

// mdMark is one laid-out line of a table cell: where it sits in the scratch
// buffer and how wide it is.
type mdMark struct{ start, end, w int }

// mdCell is one cell: its runs, and then the lines they were laid out as.
type mdCell struct {
	runFrom, runTo   int
	markFrom, markTo int
}

// mdWordFloor bounds how wide a column has to be on account of one word. A
// column is never drawn narrower than its longest word -- a cell that breaks
// "governance" into "governan" and "ce" is not worth its box -- but a URL in a
// cell does not get to demand its whole length.
const mdWordFloor = 16

// table draws a table: boxed when it fits the width, as a list of its rows
// when it does not.
func (r *mdRenderer) table(header []string, aligns []mdAlign, body []string) {
	ncol := len(header)
	nrow := 1 + len(body)

	// Every cell's runs, in one slice; each column's natural width (its widest
	// cell, unwrapped) and its floor (its longest word).
	var runs []mdRun
	cells := make([]mdCell, nrow*ncol)
	natural := make([]int, ncol)
	floor := make([]int, ncol)
	for row := 0; row < nrow; row++ {
		src, pen := header, penHeading
		if row > 0 {
			src, pen = splitRow(mdLine(body[row-1])), penBody
		}
		for col := 0; col < ncol; col++ {
			cell := &cells[row*ncol+col]
			cell.runFrom = len(runs)
			if col < len(src) && strings.IndexByte(src[col], '\x1b') < 0 {
				runs = r.inline(runs, src[col], pen)
			}
			cell.runTo = len(runs)
			line, word := runsWidth(runs[cell.runFrom:cell.runTo])
			if line > natural[col] {
				natural[col] = line
			}
			if word > floor[col] {
				floor[col] = word
			}
		}
	}
	needed := 0
	for col := range natural {
		if natural[col] < 1 {
			natural[col] = 1
		}
		if floor[col] > mdWordFloor {
			floor[col] = mdWordFloor
		}
		if floor[col] < 1 {
			floor[col] = 1
		}
		needed += floor[col]
	}

	// "│ cell │ cell │": three columns of frame per cell, and one to close.
	avail := r.width - 3*ncol - 1
	if needed > avail {
		r.tableAsRows(cells, runs, ncol, nrow)
		return
	}
	widths := fitColumns(natural, floor, avail)

	// Lay every cell out at its column's width, into one scratch buffer.
	var scratch strings.Builder
	var marks []mdMark
	for i := range cells {
		cell := &cells[i]
		cell.markFrom = len(marks)
		r.flow.begin(&scratch, r.pens, widths[i%ncol], widths[i%ncol], "", &marks)
		for _, run := range runs[cell.runFrom:cell.runTo] {
			r.flow.feed(run.text, run.pen)
		}
		r.flow.finish()
		cell.markTo = len(marks)
	}
	laid := scratch.String()

	r.tableBorder("┌", "┬", "┐", widths)
	for row := 0; row < nrow; row++ {
		if row > 0 {
			r.tableBorder("├", "┼", "┤", widths)
		}
		height := 1
		for col := 0; col < ncol; col++ {
			if c := cells[row*ncol+col]; c.markTo-c.markFrom > height {
				height = c.markTo - c.markFrom
			}
		}
		for li := 0; li < height; li++ {
			r.startLine()
			for col := 0; col < ncol; col++ {
				r.tableBar()
				r.b.WriteByte(' ')
				line, w := "", 0
				if c := cells[row*ncol+col]; c.markFrom+li < c.markTo {
					m := marks[c.markFrom+li]
					line, w = laid[m.start:m.end], m.w
				}
				pad := widths[col] - w
				if pad < 0 {
					pad = 0
				}
				left := 0
				switch aligns[col] {
				case alignRight:
					left = pad
				case alignCenter:
					left = pad / 2
				}
				r.b.WriteString(spaceString(left))
				r.b.WriteString(line)
				r.b.WriteString(spaceString(pad - left))
				r.b.WriteByte(' ')
			}
			r.tableBar()
		}
	}
	r.tableBorder("└", "┴", "┘", widths)
}

func (r *mdRenderer) tableBar() {
	r.b.WriteString(r.pens.open[penRule])
	r.b.WriteString("│")
	r.b.WriteString(r.pens.close[penRule])
}

func (r *mdRenderer) tableBorder(left, mid, right string, widths []int) {
	r.startLine()
	r.b.WriteString(r.pens.open[penRule])
	r.b.WriteString(left)
	for col, w := range widths {
		if col > 0 {
			r.b.WriteString(mid)
		}
		writeRepeat(&r.b, "─", w+2)
	}
	r.b.WriteString(right)
	r.b.WriteString(r.pens.close[penRule])
}

// tableAsRows draws a table the terminal is too narrow to box: each row as
// "header: value" lines, a blank line between rows. Nothing is dropped.
func (r *mdRenderer) tableAsRows(cells []mdCell, runs []mdRun, ncol, nrow int) {
	for row := 1; row < nrow; row++ {
		if row > 1 {
			r.blankLine()
		}
		for col := 0; col < ncol; col++ {
			cell := cells[row*ncol+col]
			if cell.runFrom == cell.runTo {
				continue
			}
			r.startLine()
			r.flow.begin(&r.b, r.pens, r.width, r.width-2, "  ", nil)
			head := cells[col]
			for _, run := range runs[head.runFrom:head.runTo] {
				r.flow.feed(run.text, run.pen)
			}
			if head.runFrom != head.runTo {
				r.flow.feed(": ", penRule)
			}
			for _, run := range runs[cell.runFrom:cell.runTo] {
				r.flow.feed(run.text, run.pen)
			}
			r.flow.finish()
		}
	}
	if nrow == 1 { // a header and no rows: the header is all there is to show
		r.startLine()
		r.flow.begin(&r.b, r.pens, r.width, r.width, "", nil)
		for col := 0; col < ncol; col++ {
			if col > 0 {
				r.flow.feed(" · ", penRule)
			}
			for _, run := range runs[cells[col].runFrom:cells[col].runTo] {
				r.flow.feed(run.text, run.pen)
			}
		}
		r.flow.finish()
	}
}

// runsWidth measures a cell: the width of its widest line unwrapped, and of
// its longest word. A word may change pen midway ("**bold**,"), so it is
// measured across runs.
func runsWidth(runs []mdRun) (line, word int) {
	lineW, wordW := 0, 0
	for _, run := range runs {
		s := run.text
		for len(s) > 0 {
			switch s[0] {
			case '\n':
				lineW, wordW = 0, 0
				s = s[1:]
			case ' ':
				lineW++
				wordW = 0
				s = s[1:]
			default:
				j := strings.IndexAny(s, " \n")
				if j < 0 {
					j = len(s)
				}
				w := textWidth(s[:j])
				lineW += w
				wordW += w
				s = s[j:]
			}
			if lineW > line {
				line = lineW
			}
			if wordW > word {
				word = wordW
			}
		}
	}
	return line, word
}

// fitColumns shares avail columns among columns that want natural[i] each and
// must have at least floor[i]. The caller has checked the floors fit.
//
// It is a water level: every column is as wide as it wants up to the level,
// and the level is as high as the width allows. So a column that needs little
// gets what it needs, one long column is the one that wraps, and three short
// ones beside it are not squeezed to pay for it.
func fitColumns(natural, floor []int, avail int) []int {
	at := func(level int) (widths []int, total int) {
		widths = make([]int, len(natural))
		for c, n := range natural {
			w := level
			if w > n {
				w = n
			}
			if w < floor[c] {
				w = floor[c]
			}
			widths[c] = w
			total += w
		}
		return widths, total
	}
	top := 0
	for _, n := range natural {
		if n > top {
			top = n
		}
	}
	if widths, total := at(top); total <= avail {
		return widths // everything fits as it is
	}
	lo, hi := 0, top // the highest level that fits is in [lo, hi)
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if _, total := at(mid); total <= avail {
			lo = mid
		} else {
			hi = mid
		}
	}
	widths, total := at(lo)
	// What the level left over goes, one column each, to those still short.
	for c := range widths {
		if total >= avail {
			break
		}
		if widths[c] == lo && natural[c] > lo {
			widths[c]++
			total++
		}
	}
	return widths
}

// ---------------------------------------------------------------------------
// Laying runs out as lines.

// mdFlow lays (text, pen) runs out as lines of a given width.
//
// It breaks between words, breaks inside a word only when the word is wider
// than a whole line (a URL, a path -- wrapToWidth's reason for using ansi.Wrap
// and not Wordwrap), and closes the open pen before every line break. That
// last part is the reason it exists rather than ansi.Wrap over styled text:
// ansi.Wrap keeps the escape sequences but leaves a colour open across the
// break.
//
// It writes straight into the answer, or -- for a table cell, whose lines are
// placed side by side with other cells' -- into a scratch buffer with each
// line's extent recorded in marks.
type mdFlow struct {
	out    *strings.Builder
	pens   *mdPens
	avail  int    // columns left on this line, counted from its start
	rest   int    // columns on every later line
	prefix string // written at the start of every later line
	marks  *[]mdMark

	w         int // columns used on this line
	lineStart int // where this line began in out (marks only)
	pen       mdPen
	open      bool

	word     []mdSeg // the word being gathered: it may change pen midway
	wordW    int
	spaces   int // spaces waiting to see whether a word follows on this line
	spacePen mdPen
}

// mdSeg is a piece of a word in one pen, with the width it was measured at so
// it is measured once.
type mdSeg struct {
	text string
	pen  mdPen
	w    int
}

func (f *mdFlow) begin(out *strings.Builder, pens *mdPens, first, rest int, prefix string, marks *[]mdMark) {
	if first < 1 {
		first = 1
	}
	if rest < 1 {
		rest = 1
	}
	word := f.word[:0]
	*f = mdFlow{out: out, pens: pens, avail: first, rest: rest, prefix: prefix, marks: marks, word: word}
	f.lineStart = out.Len()
}

// feed adds text in pen.
func (f *mdFlow) feed(s string, pen mdPen) {
	for len(s) > 0 {
		switch s[0] {
		case ' ':
			n := 1
			for n < len(s) && s[n] == ' ' {
				n++
			}
			f.flushWord()
			if f.spaces == 0 {
				f.spacePen = pen
			}
			f.spaces += n
			s = s[n:]
		case '\n':
			f.flushWord()
			f.newline()
			s = s[1:]
		default:
			j := strings.IndexAny(s, " \n")
			if j < 0 {
				j = len(s)
			}
			w := textWidth(s[:j])
			f.word = append(f.word, mdSeg{s[:j], pen, w})
			f.wordW += w
			s = s[j:]
		}
	}
}

// flushWord places the gathered word: on this line if it fits after the
// waiting spaces, otherwise on the next.
func (f *mdFlow) flushWord() {
	if len(f.word) == 0 {
		return
	}
	switch {
	case f.w > 0 && f.w+f.spaces+f.wordW > f.avail:
		f.newline()
	case f.spaces > 0:
		// Spaces at the very start of a flow are indentation (a code line's)
		// and are kept, short of pushing the text off the line.
		n := f.spaces
		if room := f.avail - f.w - 1; n > room {
			n = room
		}
		if n > 0 {
			f.put(spaceString(n), f.spacePen, n)
		}
	}
	f.spaces = 0
	if f.w+f.wordW <= f.avail {
		for _, seg := range f.word {
			f.put(seg.text, seg.pen, seg.w)
		}
	} else {
		// Wider than a whole line: it is broken where each line ends.
		for _, seg := range f.word {
			f.putLong(seg.text, seg.pen)
		}
	}
	f.word, f.wordW = f.word[:0], 0
}

// put writes s (w columns) in pen, switching pens if it has to.
func (f *mdFlow) put(s string, pen mdPen, w int) {
	if !f.open || f.pen != pen {
		f.closePen()
		f.out.WriteString(f.pens.open[pen])
		f.pen, f.open = pen, true
	}
	f.out.WriteString(s)
	f.w += w
}

// putLong writes s, breaking it across lines where it is wider than what is
// left of this one.
//
// EACH LINE READS ONLY ITS OWN CHARACTERS. The first version measured all of
// what remained before every line, and a Chinese or Japanese paragraph is one
// "word" with no space in it: the work grew with the square of its length.
// FOUND 2026-10-06 by FuzzAnAnswerDraws -- 3.4 KB took 1.09 s to draw.
func (f *mdFlow) putLong(s string, pen mdPen) {
	for s != "" {
		head, w, tail := takeWidth(s, f.avail-f.w)
		if head == "" {
			if f.w > 0 {
				f.newline()
				continue
			}
			// Not one character fits an empty line (a wide character at
			// width 1). It is drawn anyway: a line cannot hold less.
			cluster, rest, cw, _ := ansi.FirstGraphemeCluster(s, -1)
			if cluster == "" {
				cluster, rest, cw = s, "", 0
			}
			head, w, tail = cluster, cw, rest
		}
		f.put(head, pen, w)
		if tail == "" {
			return
		}
		f.newline()
		s = tail
	}
}

func (f *mdFlow) closePen() {
	if f.open {
		f.out.WriteString(f.pens.close[f.pen])
		f.open = false
	}
}

func (f *mdFlow) newline() {
	f.closePen()
	if f.marks != nil {
		*f.marks = append(*f.marks, mdMark{f.lineStart, f.out.Len(), f.w})
		f.lineStart = f.out.Len()
	} else {
		f.out.WriteByte('\n')
		f.out.WriteString(f.prefix)
	}
	f.w, f.avail, f.spaces = 0, f.rest, 0
}

// finish ends the flow. Spaces still waiting are trailing spaces and are
// dropped.
func (f *mdFlow) finish() {
	f.flushWord()
	f.closePen()
	if f.marks != nil {
		*f.marks = append(*f.marks, mdMark{f.lineStart, f.out.Len(), f.w})
	}
}

// textWidth is how many columns s takes. Plain ASCII, which is nearly
// everything, is its own length; the rest is measured.
func textWidth(s string) int {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f {
			return ansi.StringWidth(s)
		}
	}
	return len(s)
}

// takeWidth splits s after the longest start of it that is no wider than room,
// and reports how wide that start is. It reads no further than the cut, and
// never cuts inside a character: a letter and the accent that combines with
// it, or the parts of one emoji, stay together.
func takeWidth(s string, room int) (head string, w int, tail string) {
	if room <= 0 {
		return "", 0, s
	}
	// Plain ASCII is a column a byte. The byte after the cut has to be ASCII
	// as well, or it could be an accent belonging to the letter before it.
	n := 0
	for n < len(s) && s[n] >= 0x20 && s[n] < 0x7f {
		if n == room {
			return s[:n], n, s[n:]
		}
		n++
	}
	if n == len(s) {
		return s, n, ""
	}
	rest, state := s, -1
	for rest != "" {
		cluster, after, cw, next := ansi.FirstGraphemeCluster(rest, state)
		if cluster == "" || w+cw > room {
			break
		}
		w += cw
		rest, state = after, next
	}
	return s[:len(s)-len(rest)], w, rest
}

const mdSpaces = "                                                                "

// spaceString returns n spaces.
func spaceString(n int) string {
	if n <= 0 {
		return ""
	}
	if n <= len(mdSpaces) {
		return mdSpaces[:n]
	}
	return strings.Repeat(" ", n)
}

func writeRepeat(b *strings.Builder, s string, n int) {
	for ; n > 0; n-- {
		b.WriteString(s)
	}
}

// ---------------------------------------------------------------------------
// Inline: one line's text into runs.

// What a stretch of text has been marked as.
const (
	mdBold uint8 = 1 << iota
	mdItalic
	mdStrike
	mdCodeSpan
	mdLinkText
	mdFaint // a link's address, shown after its text
)

// mdNode is a piece of a line on the way to being a run: text, or a run of
// emphasis characters that may yet turn out to open or close something.
type mdNode struct {
	text  string
	flags uint8

	delim    byte // '*', '_' or '~'; 0 for text
	count    int  // characters of the run not yet used
	orig     int
	canOpen  bool
	canClose bool
}

// mdInlineSpecial are the characters that can start inline markup. A line with
// none of them -- most lines -- is one run and is not parsed.
const mdInlineSpecial = "*_`[\\<~&"

// inline appends s's runs to dst, drawn in base where nothing marks them.
func (r *mdRenderer) inline(dst []mdRun, s string, base mdPen) []mdRun {
	if !strings.ContainsAny(s, mdInlineSpecial) {
		return append(dst, mdRun{s, base})
	}
	r.nodes = tokenize(r.nodes[:0], s, 0)
	resolveEmphasis(r.nodes)
	for _, n := range r.nodes {
		text := n.text
		if n.delim != 0 {
			text = text[:n.count] // what was not used as markup is text
		}
		if text == "" {
			continue
		}
		dst = append(dst, mdRun{text, penFor(base, n.flags)})
	}
	return dst
}

// penFor is the pen for text marked with flags inside a block drawn in base.
func penFor(base mdPen, flags uint8) mdPen {
	switch {
	case flags&mdCodeSpan != 0:
		return penCode
	case flags&mdFaint != 0:
		return penRule
	case flags&mdLinkText != 0:
		return penLink
	case flags&mdStrike != 0:
		return penStrike
	}
	if base != penBody {
		// A heading is all heading. A quote keeps its voice, but what is
		// marked strong inside it still stands out.
		if base == penQuote && flags&mdBold != 0 {
			return penStrong
		}
		return base
	}
	switch flags & (mdBold | mdItalic) {
	case mdBold | mdItalic:
		return penStrongEm
	case mdBold:
		return penStrong
	case mdItalic:
		return penEm
	}
	return penBody
}

// Bounds on how far a link is looked for. A line of ten thousand "[" would
// otherwise be scanned to its end ten thousand times, on every token.
const (
	mdMaxLinkText = 1000
	mdMaxLinkDest = 2000
)

// tokenize splits s into nodes, each carrying flags.
func tokenize(nodes []mdNode, s string, flags uint8) []mdNode {
	start := 0 // start of the plain text not yet made a node
	text := func(end int) {
		if end > start {
			nodes = append(nodes, mdNode{text: s[start:end], flags: flags})
		}
	}
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '\\':
			if i+1 < len(s) && isASCIIPunct(s[i+1]) {
				text(i)
				start = i + 1 // the escaped character is ordinary text
				i += 2
				continue
			}
		case '`':
			n := runLength(s, i)
			if j := closingBackticks(s, i+n, n); j >= 0 {
				text(i)
				nodes = append(nodes, mdNode{text: codeSpanText(s[i+n : j]), flags: flags | mdCodeSpan})
				i = j + n
				start = i
				continue
			}
			i += n
			continue
		case '*', '_':
			n := runLength(s, i)
			canOpen, canClose := flanking(c, runeBefore(s, i), runeAfter(s, i+n))
			text(i)
			nodes = append(nodes, mdNode{text: s[i : i+n], flags: flags, delim: c, count: n, orig: n, canOpen: canOpen, canClose: canClose})
			i += n
			start = i
			continue
		case '~':
			n := runLength(s, i)
			if n == 2 {
				text(i)
				nodes = append(nodes, mdNode{text: s[i : i+n], flags: flags, delim: c, count: n, orig: n,
					canOpen: !unicode.IsSpace(runeAfter(s, i+n)), canClose: !unicode.IsSpace(runeBefore(s, i))})
				i += n
				start = i
				continue
			}
			i += n
			continue
		case '[':
			if label, dest, end, ok := linkAt(s, i); ok {
				from := i
				if i > start && s[i-1] == '!' {
					from = i - 1 // an image: its alt text and where it is
				}
				text(from)
				nodes = tokenize(nodes, label, flags|mdLinkText)
				if dest != "" && dest != label && !strings.HasPrefix(dest, "#") {
					nodes = append(nodes,
						mdNode{text: " (", flags: flags | mdFaint},
						mdNode{text: dest, flags: flags | mdFaint},
						mdNode{text: ")", flags: flags | mdFaint})
				}
				i = end
				start = i
				continue
			}
		case '<':
			if n := brTag(s[i:]); n > 0 {
				text(i)
				nodes = append(nodes, mdNode{text: "\n", flags: flags})
				i += n
				start = i
				continue
			}
			if url, end, ok := autolinkAt(s, i); ok {
				text(i)
				nodes = append(nodes, mdNode{text: url, flags: flags | mdLinkText})
				i = end
				start = i
				continue
			}
		case '&':
			if rep, n := entity(s[i:]); n > 0 {
				text(i)
				nodes = append(nodes, mdNode{text: rep, flags: flags})
				i += n
				start = i
				continue
			}
		}
		i++
	}
	text(len(s))
	return nodes
}

// resolveEmphasis pairs the emphasis runs and marks what lies between each
// pair. It is CommonMark's algorithm: each closer takes the nearest opener of
// its own character, two characters from each making strong and one making
// emphasis, and whatever lies unmatched between them is left as text.
func resolveEmphasis(nodes []mdNode) {
	// For each kind of closer, the index below which no opener for it exists,
	// so a line of closers with no opener is not scanned once per closer.
	var bottom [3][2][3]int
	for ci := range nodes {
		c := &nodes[ci]
		if c.delim == 0 || !c.canClose {
			continue
		}
		kind := 0
		switch c.delim {
		case '_':
			kind = 1
		case '~':
			kind = 2
		}
		opens := 0
		if c.canOpen {
			opens = 1
		}
		floor := &bottom[kind][opens][c.orig%3]
		for c.count > 0 {
			oi := -1
			for k := ci - 1; k >= *floor; k-- {
				o := &nodes[k]
				if o.delim != c.delim || !o.canOpen || o.count == 0 {
					continue
				}
				// The rule of three: "*a**b*" is not "<em>a</em><em>b</em>".
				if c.delim != '~' && (o.canClose || c.canOpen) && (o.orig+c.orig)%3 == 0 && (o.orig%3 != 0 || c.orig%3 != 0) {
					continue
				}
				oi = k
				break
			}
			if oi < 0 {
				*floor = ci
				break
			}
			o := &nodes[oi]
			use, flag := 1, mdItalic
			switch {
			case c.delim == '~':
				use, flag = 2, mdStrike
			case o.count >= 2 && c.count >= 2:
				use, flag = 2, mdBold
			}
			// "__init__" IS A NAME. CommonMark makes it a bold "init", and a
			// coding assistant's prose says "the __init__ method" without
			// backticks often enough that losing the underscores is the worse
			// mistake. "__two words__" is still strong.
			if c.delim == '_' && use == 2 && isIdentifier(nodes[oi+1:ci]) {
				o.canOpen, c.canClose = false, false
				break
			}
			for k := oi + 1; k < ci; k++ {
				nodes[k].flags |= flag
				if nodes[k].delim != 0 {
					nodes[k].canOpen, nodes[k].canClose = false, false
				}
			}
			o.count -= use
			c.count -= use
		}
	}
}

// isIdentifier reports whether nodes spell one programming name: letters,
// digits and underscores, with nothing else between them.
func isIdentifier(nodes []mdNode) bool {
	if len(nodes) == 0 {
		return false
	}
	for _, n := range nodes {
		if n.delim == '_' {
			continue
		}
		if n.delim != 0 || n.flags&mdCodeSpan != 0 || n.text == "" {
			return false
		}
		for i := 0; i < len(n.text); i++ {
			if !isASCIIWord(rune(n.text[i])) {
				return false
			}
		}
	}
	return true
}

// flanking reports whether a run of c between prev and next can open and can
// close emphasis.
//
// CommonMark's rules, and one more that is not CommonMark's: a run with a
// letter or digit on BOTH sides does neither. CommonMark says that of "_" and
// not of "*", and a coding assistant's prose is full of "*" that is not
// emphasis -- "5*3 = 15 and 2*4 = 8" would lose both stars and gain an italic
// "3 = 15 and 2". ASCII only, so "这是**重点**内容", which has no spaces to
// stand on, still works.
func flanking(c byte, prev, next rune) (canOpen, canClose bool) {
	prevSpace, nextSpace := unicode.IsSpace(prev), unicode.IsSpace(next)
	prevPunct, nextPunct := isPunct(prev), isPunct(next)
	left := !nextSpace && (!nextPunct || prevSpace || prevPunct)
	right := !prevSpace && (!prevPunct || nextSpace || nextPunct)
	if isASCIIWord(prev) && isASCIIWord(next) {
		return false, false
	}
	if c == '_' {
		return left && (!right || prevPunct), right && (!left || nextPunct)
	}
	return left, right
}

func isPunct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

func isASCIIWord(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func isASCIIPunct(c byte) bool {
	return c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}

// runLength is how many times the character at s[i] repeats from there.
func runLength(s string, i int) int {
	n := 1
	for i+n < len(s) && s[i+n] == s[i] {
		n++
	}
	return n
}

// runeBefore and runeAfter are the characters either side of a position; the
// ends of the line count as a space, which is what makes "**word**" at the
// start of a line open.
func runeBefore(s string, i int) rune {
	if i <= 0 {
		return ' '
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r
}

func runeAfter(s string, i int) rune {
	if i >= len(s) {
		return ' '
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r
}

// closingBackticks finds the run of exactly n backticks that closes a code
// span opened before from, or -1.
func closingBackticks(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		m := runLength(s, i)
		if m == n {
			return i
		}
		i += m
	}
	return -1
}

// codeSpanText is a code span's content: as written, less the one space each
// side that "“ `a` “" needs to hold a backtick.
func codeSpanText(s string) string {
	if len(s) > 2 && s[0] == ' ' && s[len(s)-1] == ' ' && strings.TrimSpace(s) != "" {
		return s[1 : len(s)-1]
	}
	return s
}

// linkAt reads "[label](destination)" at s[i] == '['.
//
// Two things a link is not, both of them code written without backticks:
// "handlers[0](req)" is an index and a call (a link does not start in the
// middle of a name), and "[x](y)" with a destination that is no address or
// path is not worth losing the brackets for.
func linkAt(s string, i int) (label, dest string, end int, ok bool) {
	if prev := runeBefore(s, i); isASCIIWord(prev) || prev == ')' || prev == ']' {
		return "", "", 0, false
	}
	depth, j := 0, i+1
	for ; j < len(s) && j-i <= mdMaxLinkText; j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			depth++
		case ']':
			if depth == 0 {
				goto closed
			}
			depth--
		}
	}
	return "", "", 0, false
closed:
	if j+1 >= len(s) || s[j+1] != '(' {
		return "", "", 0, false
	}
	depth = 0
	k := j + 2
	for ; k < len(s) && k-j <= mdMaxLinkDest; k++ {
		switch s[k] {
		case '\\':
			k++
		case '(':
			depth++
		case ')':
			if depth == 0 {
				dest = strings.TrimSpace(s[j+2 : k])
				// A title after the address ("url \"title\"") is not shown.
				if sp := strings.IndexByte(dest, ' '); sp >= 0 {
					dest = dest[:sp]
				}
				dest = strings.TrimSuffix(strings.TrimPrefix(dest, "<"), ">")
				if !strings.ContainsAny(dest, "/.:#") {
					return "", "", 0, false
				}
				return s[i+1 : j], dest, k + 1, true
			}
			depth--
		}
	}
	return "", "", 0, false
}

// autolinkAt reads "<https://...>" or "<mailto:...>" at s[i] == '<'.
func autolinkAt(s string, i int) (url string, end int, ok bool) {
	rest := s[i+1:]
	if !strings.HasPrefix(rest, "http://") && !strings.HasPrefix(rest, "https://") && !strings.HasPrefix(rest, "mailto:") {
		return "", 0, false
	}
	j := strings.IndexAny(rest, "> ")
	if j < 0 || j > mdMaxLinkDest || rest[j] != '>' {
		return "", 0, false
	}
	return rest[:j], i + 1 + j + 1, true
}

// brTag returns the length of a <br> tag at the start of s, or 0. Models put
// them in table cells, where markdown has no other line break.
func brTag(s string) int {
	for _, tag := range [...]string{"<br>", "<br/>", "<br />"} {
		if len(s) >= len(tag) && strings.EqualFold(s[:len(tag)], tag) {
			return len(tag)
		}
	}
	return 0
}

// entity decodes the handful of HTML entities models actually write. Anything
// else is left as it is: a full table would be a few thousand names to draw
// "&amp;" correctly.
func entity(s string) (string, int) {
	for _, e := range [...]struct{ name, text string }{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`},
		{"&#39;", "'"}, {"&apos;", "'"}, {"&nbsp;", " "},
	} {
		if strings.HasPrefix(s, e.name) {
			return e.text, len(e.name)
		}
	}
	return "", 0
}
