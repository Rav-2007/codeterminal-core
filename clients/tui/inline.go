package tui

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// THE CHAT LIVES IN THE TERMINAL'S OWN SCROLLBACK (2026-11-21).
//
// Until now the chat was drawn on the terminal's alternate screen, which has no
// scrollback. The terminal's scrollbar sat there with nothing to scroll, and a
// drag selected only the rows in the window. The owner, after /copy: "There is
// no side scroll thing, like available in the claude... add the scroll view in
// the side then be able to select and copy the content". That is the terminal's
// own scrollbar, and it works only for text that is in the terminal's normal
// screen. So the chat is now printed there, the way Claude Code does it:
//
//   - A turn that will not change any more (your prompt, a finished tool call, a
//     note, a finished answer) is printed ONCE, above the program's own lines,
//     and from then on it is the terminal's: its scrollbar and wheel move
//     through it, a drag selects any length of it, its copy key copies.
//   - What can still change is drawn below that and redrawn as it changes: the
//     answer being written, a tool call still running, a question waiting for
//     you, then the prompt box and the line under it.
//   - An answer being written is printed a paragraph at a time, at the blank
//     line after each finished one (never inside a code block), so a long one
//     is in the scrollback while it is still arriving.
//
// MOCHIII_FULLSCREEN=1 (or --fullscreen) draws the chat as before, on the
// alternate screen, for a terminal where printing above the program misbehaves.
// The tests build the model in that mode unless they say otherwise; the tests
// in inline_test.go say otherwise.
//
// WHAT IS PRINTED IS KEPT TRACK OF BY FINGERPRINT, one per turn, and not by a
// count, because the list of turns is not only ever appended to. The transcript
// bound drops turns from the front, a /task review with nothing saved takes its
// own line back, and /clear, /resume, /compact and another window's change
// replace the list. inlineBase lines the fingerprints up with the turns as they
// are now: what is still there is not printed again, and a replaced chat is
// printed below a rule.

// inlineState is inline mode's part of the model.
type inlineState struct {
	on bool
	// printed is one fingerprint per turn handed to the terminal, in order.
	printed []uint64
	// partial is how much of the turn after them -- the answer being written --
	// has been printed already, in bytes of its text.
	partial int
	// inFlight is set while a print is on its way to the terminal. The next one
	// waits for inlinePrintedMsg: each command runs on a goroutine of its own,
	// so two prints sent one after the other could arrive the other way round.
	inFlight bool
	// rule draws a line above the next print: the chat was replaced.
	rule bool
	// pending is printed after the turns, as it is (/copy in this mode).
	pending []string
	// maxHeight is how many rows the live part may take; liveLines is how
	// many it has.
	maxHeight, liveLines int
	// quitting empties the program's own lines, so leaving does not leave a
	// prompt box behind in the terminal.
	quitting bool
	// welcome is the first screen (welcome.go), up until the chat has more
	// turns than the welcomeTurns notes it opened with.
	welcome      bool
	welcomeTurns int
	// rows is how many rows of the terminal the prints have taken since it was
	// cleared: while they and the program's own lines are fewer than the
	// window's, the prompt is kept at the bottom of it (composeScreen).
	rows int
}

// WHAT WAS PRINTED BEFORE THE CHAT OPENED IS CLEARED AWAY (owner, 2026-11-21:
// "the upper part is visible, which is used to start and those stuff, clear
// the upper stuff"). In inline mode the chat starts on a clean terminal, as the
// alternate screen always did: the screen and the terminal's scrollback are
// cleared the way the clear command clears them, just before the chat is
// drawn. The command that started it, a build's output and "Starting Mochiii
// for …" go with it.
//
// A FEW OF THOSE LINES ARE NOT PROGRESS BUT NEWS -- this folder is not a
// project, another install is answering, indexing failed -- and they are said
// with note() instead of say(). note() prints the line as say() does and keeps
// it, and the chat opens with the kept lines as its first note, so clearing the
// screen loses nothing anyone needs.
var startNotes []string

// note is say for a line worth keeping: it is put in the chat as well, in
// inline mode, once the screen it was printed on is cleared.
func note(w io.Writer, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	say(w, "%s", text)
	if t := strings.TrimSpace(text); t != "" {
		startNotes = append(startNotes, t)
	}
}

// clearTerminal is what the clear command writes: cursor home, the screen
// cleared, the scrollback cleared.
const clearTerminal = "\x1b[H\x1b[2J\x1b[3J"

// openInline gets the terminal and the model ready for an inline chat: the
// notes kept from before it opened go into the chat, the first screen is the
// lotus and the prompt rather than the splash (welcome.go), and the terminal is
// cleared, if it is one.
func openInline(model *chatModel, out io.Writer, isTerminal bool) {
	if len(startNotes) > 0 {
		model.appendTurn(turn{role: roleSystem, text: strings.Join(startNotes, "\n")})
		startNotes = nil
	}
	if model.state == stateSplash {
		model.state = stateIdle
		model.input.Focus() // its blink is started by Init
	}
	model.inline.welcome = true
	for _, t := range model.turns {
		if t.role != roleSystem {
			model.inline.welcome = false // a chat carried on: it is printed, not welcomed
		}
	}
	model.inline.welcomeTurns = len(model.turns)
	if isTerminal {
		_, _ = io.WriteString(out, clearTerminal)
	}
}

// helpInline makes /help describe scrolling as it is in inline mode. Set once,
// by main.go, beside chatModel.inline.on: /help's text is built without a model.
var helpInline bool

// inlineSpacerRows is the blank row composeScreen puts between the printed chat
// and the program's own lines.
const inlineSpacerRows = 1

// inlinePrintedMsg says the last print has reached the program's renderer.
type inlinePrintedMsg struct{}

// fullscreenRequested reports whether the chat should be drawn the old way.
func fullscreenRequested(flagValue bool) bool {
	return flagValue || os.Getenv("MOCHIII_FULLSCREEN") == "1"
}

// Update is the program's Update with the printing done after it.
//
// It hands back update's own model when nothing here changed it. Putting the
// model back into a tea.Model is an allocation, this runs for every streamed
// token, and that second one took a repaint at the transcript's ceiling past
// its allocation ceiling under -race (62, over 60:
// TestRepaintAllocationsAtTheCeilingAreBounded).
func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if _, ok := msg.(inlinePrintedMsg); ok {
		m.inline.inFlight = false
		m.printFinished(&cmd)
		return m, cmd
	}
	rowsBefore := m.promptRows()
	var next tea.Model
	if am, isQuestion := msg.(toolApprovalMsg); isQuestion && m.autoCovers(am.req) {
		next, cmd = m.autoAnswer(am) // answered before it is drawn (autoapprove.go)
	} else {
		next, cmd = m.update(msg)
	}
	cm, ok := next.(chatModel)
	if !ok {
		return next, cmd
	}
	if cm.afterUpdate(m.welcoming(), rowsBefore, &cmd) {
		return cm, cmd
	}
	return next, cmd
}

// afterUpdate is what Update does once update has run, and reports whether it
// changed m.
func (m *chatModel) afterUpdate(wasWelcoming bool, rowsBefore int, cmd *tea.Cmd) bool {
	changed := m.autoApplyEdits(cmd) // while auto-approve is on (autoapprove.go)
	if m.promptRows() != rowsBefore {
		// A long prompt grew or shrank a row (promptinput.go): the chat above it
		// gets the rows it gave up, or gives up the ones it took. ITS HEIGHT
		// ONLY: its text has not changed, and drawing a long chat's text again
		// for a paste took a 1 MB paste at the transcript's ceiling from half a
		// millisecond to fifty (repaintceiling_test.go).
		bottom := m.viewport.AtBottom()
		m.resizeViewport()
		if bottom {
			m.viewport.GotoBottom()
		}
		changed = true
	}
	if !m.inline.on {
		return changed
	}
	if m.inline.welcome && len(m.turns) != m.inline.welcomeTurns {
		m.inline.welcome, changed = false, true // something was said: the first screen is over
	}
	if m.welcoming() != wasWelcoming {
		m.syncInputWidth() // the prompt changed width (promptWidth)
		changed = true
	}
	return m.printFinished(cmd) || changed
}

// View is the program's View, and nothing at all once it is quitting.
func (m chatModel) View() string {
	if m.inline.quitting {
		return ""
	}
	return m.view()
}

// printFinished prints what has become final since the last print, and runs
// cmd after it -- after, so a quit cannot overtake the last lines of the chat.
// It reports whether it changed m.
func (m *chatModel) printFinished(cmd *tea.Cmd) bool {
	if m.inline.inFlight {
		return false
	}
	before := m.inline.mark()
	text, changed := m.takePrintable()
	if changed {
		m.refreshViewport() // the live part now starts lower down
	}
	if text == "" {
		// Nothing printed, but the record may still have been lined up with
		// the turns (inlineBase).
		return changed || m.inline.mark() != before
	}
	m.inline.inFlight = true
	m.inline.rows += rowsTaken(text, m.width)
	*cmd = tea.Sequence(tea.Println(text), func() tea.Msg { return inlinePrintedMsg{} }, *cmd)
	return true
}

// inlineMark is what taking a print can move in inlineState, so a take that
// printed nothing can say whether it changed anything. Every rewrite of
// printed changes its length (inlineBase), so its length and where it starts
// tell.
type inlineMark struct {
	printed, partial, pending int
	printedAt                 *uint64
	rule                      bool
}

func (s *inlineState) mark() inlineMark {
	k := inlineMark{printed: len(s.printed), partial: s.partial, pending: len(s.pending), rule: s.rule}
	if len(s.printed) > 0 {
		k.printedAt = &s.printed[0]
	}
	return k
}

// takePrintable takes what can be printed now, and moves the record of what
// has been printed past it. changed is true when the live part moved.
func (m *chatModel) takePrintable() (text string, changed bool) {
	// Nothing is printed above a screen that fills the terminal.
	if !m.ready || m.state == stateSplash || m.setupActive() || m.welcoming() || m.viewport.Width <= 0 {
		return "", false
	}
	base := m.inlineBase()
	w := m.viewport.Width
	var b strings.Builder
	add := func(sep, block string) {
		if block == "" {
			return
		}
		switch {
		case b.Len() > 0 && sep == "":
			b.WriteByte('\n')
		case b.Len() > 0:
			b.WriteString(sep)
		case sep != "":
			// The terminal has already ended the line above: one newline fewer.
			b.WriteString(sep[1:])
		}
		b.WriteString(block)
	}
	if m.inline.rule {
		m.inline.rule, changed = false, true
		b.WriteString(helpStyle.Render(strings.Repeat("─", w)))
	}

	// Whole turns, for as long as they are final.
	k := base
	for k < len(m.turns) && m.inlineFinal(k) {
		t := m.turns[k]
		if k == base && m.inline.partial > 0 {
			if m.inline.partial <= len(t.text) {
				add(turnSeparator, renderAnswerContinued(t.text[m.inline.partial:], w))
			}
		} else {
			add(separatorBefore(m.turns, k), renderShownBlock(t, w))
		}
		m.inline.printed = append(m.inline.printed, inlineFP(t))
		k++
	}
	if k > base {
		m.inline.partial, changed = 0, true
	}

	// The answer being written: its finished paragraphs.
	if k < len(m.turns) && m.streamCh != nil && k == m.streamAssistant {
		t := m.turns[k]
		from := m.inline.partial
		if !t.plainAnswer() && strings.TrimSpace(t.text) != "" && from <= len(t.text) {
			if split := answerSplit(t.text, from); split > from {
				if from == 0 {
					add(separatorBefore(m.turns, k), renderAnswer(t.text[:split], w))
				} else {
					add(turnSeparator, renderAnswerContinued(t.text[from:split], w))
				}
				m.inline.partial, changed = split, true
			}
		}
	}

	out := trimLineEnds(wrapToWidth(b.String(), w))
	if b.Len() == 0 {
		out = ""
	}
	// /copy's text is printed as it is: wrapping it here would undo it.
	for _, p := range m.inline.pending {
		if out != "" {
			out += "\n"
		}
		out += trimLineEnds(p)
	}
	m.inline.pending = nil
	return out, changed
}

// inlineFinal reports whether turn i will not change any more.
func (m chatModel) inlineFinal(i int) bool {
	if m.streamCh != nil && i == m.streamAssistant {
		return false // the answer being written
	}
	if t := m.turns[i]; t.role == roleActivity && t.act != nil && t.act.state == activityRunning {
		return false // a call still running: its line changes when it ends
	}
	return true
}

// inlineBase lines the record of what was printed up with the turns as they
// are now, and returns how many of them are already in the terminal.
func (m *chatModel) inlineBase() int {
	p := m.inline.printed
	n := len(p)
	if n == 0 {
		return 0
	}
	// Turn j is the last one printed, and the one before it agrees -- or is
	// the transcript bound's marker, which takes the place of what it dropped.
	at := func(j int) bool {
		if j < 0 || j >= len(m.turns) || inlineFP(m.turns[j]) != p[n-1] {
			return false
		}
		if n < 2 || j < 1 || inlineFP(m.turns[j-1]) == p[n-2] {
			return true
		}
		before := m.turns[j-1]
		return j == 1 && before.role == roleSystem && strings.HasPrefix(before.text, evictionMarkerPrefix)
	}
	// Appended to, or not touched: the usual case, and the cheap one.
	if at(n - 1) {
		return n
	}
	// Trimmed at the front (the transcript bound, /compact without a summary):
	// the last turn printed has moved up.
	for j := min(len(m.turns), n) - 1; j >= 0; j-- {
		if at(j) {
			m.inline.printed = m.inline.printed[:0]
			for _, t := range m.turns[:j+1] {
				m.inline.printed = append(m.inline.printed, inlineFP(t))
			}
			return j + 1
		}
	}
	// Trimmed at the end (a line taken back): what is left was all printed. It
	// stays in the scrollback; the terminal's lines are not this program's to
	// take back.
	if len(m.turns) > 0 && len(m.turns) < n {
		same := true
		for j, t := range m.turns {
			if inlineFP(t) != p[j] {
				same = false
				break
			}
		}
		if same {
			m.inline.printed = p[:len(m.turns)]
			return len(m.turns)
		}
	}
	// Replaced: a new chat, a resumed one, a summary, another window's copy.
	m.inline.printed, m.inline.partial, m.inline.rule = nil, 0, true
	return 0
}

// inlineFP is a turn's fingerprint: what it says and how it is drawn. The text
// is hashed by its length and its two ends, which is enough to tell turns apart
// and costs the same for a long answer as for a short one -- it runs on every
// update.
func inlineFP(t turn) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d|%t|%t|%d|", t.role, t.local, t.verbatim, len(t.text)) // a hash's Write never fails, here and below
	if len(t.text) > 512 {
		_, _ = io.WriteString(h, t.text[:256])
		_, _ = io.WriteString(h, t.text[len(t.text)-256:])
	} else {
		_, _ = io.WriteString(h, t.text)
	}
	if t.act != nil {
		_, _ = fmt.Fprintf(h, "|%+v", *t.act)
	}
	return h.Sum64()
}

// answerSplit is where an answer being written can be cut so that the part
// before renders printed on its own as it would in the whole: just after the
// last blank line past from, outside a code fence, with something written
// before it. from itself is such a place. It returns from when there is none.
func answerSplit(text string, from int) int {
	best, pos := from, from
	inFence, content := false, false
	var fch byte
	var fn int
	for pos < len(text) {
		nl := strings.IndexByte(text[pos:], '\n')
		if nl < 0 {
			break // the last line is still being written
		}
		line := mdLine(text[pos : pos+nl])
		pos += nl + 1
		trimmed := strings.TrimSpace(line)
		switch {
		case inFence:
			if fenceClose(line, fch, fn) {
				inFence = false
			}
		case trimmed == "":
			if content {
				best = pos
			}
		default:
			if ch, n, ok := fenceOpen(trimmed); ok {
				inFence, fch, fn = true, ch, n
			}
			content = true
		}
	}
	return best
}

// renderAnswerContinued draws the rest of an answer whose beginning is
// already printed: as renderAnswer does, without the "Mochiii:" label.
func renderAnswerContinued(text string, width int) string {
	r := mdRenderer{pens: currentPens(), width: width, labelled: true}
	if width <= 0 {
		r.width = mdUnbounded
	}
	r.render(text)
	out := r.b.String()
	if r.blank {
		out = out[:len(out)-1]
	}
	return out
}

// transcriptOf is what the transcript part of the screen shows: the whole
// chat on the alternate screen, and in inline mode only what is not printed.
//
// A POINTER RECEIVER, because the transcript cache fills as it renders: on a
// copy of the model every block it cached was thrown away with the copy, and
// every repaint re-rendered the whole chat (caught by the allocation ceilings
// in renderbench_test.go).
func (m *chatModel) transcriptOf(turns []turn) string {
	if !m.inline.on {
		return m.transcript.render(turns, m.viewport.Width)
	}
	base := min(len(m.inline.printed), len(turns))
	live := turns[base:]
	var b strings.Builder
	for i, t := range live {
		var block string
		if i == 0 && m.inline.partial > 0 && m.inline.partial <= len(t.text) {
			block = renderAnswerContinued(t.text[m.inline.partial:], m.viewport.Width)
		} else {
			block = renderShownBlock(t, m.viewport.Width)
		}
		if block == "" {
			continue
		}
		if b.Len() > 0 {
			if sep := separatorBefore(live, i); sep != "" {
				b.WriteString(sep)
			} else {
				b.WriteByte('\n')
			}
		}
		b.WriteString(block)
	}
	return b.String()
}

// setLive gives the transcript part its content, and in inline mode its
// height: as many rows as it has, up to what the terminal leaves it.
func (m *chatModel) setLive(content string) {
	m.viewport.SetContent(content)
	if !m.inline.on {
		return
	}
	m.inline.liveLines = 0
	if content != "" {
		m.inline.liveLines = strings.Count(content, "\n") + 1
	}
	m.viewport.Height = m.fitLive()
}

// liveHeight is resizeViewport's height for the transcript part: all of it on
// the alternate screen, and in inline mode no more than its content needs.
func (m *chatModel) liveHeight(max int) int {
	if !m.inline.on {
		return max
	}
	m.inline.maxHeight = max - inlineSpacerRows
	if m.inline.maxHeight < 0 {
		m.inline.maxHeight = 0
	}
	return m.fitLive()
}

// fitPanelRows sizes the transcript part again when the MCP panel opens or
// closes. In inline mode that part is only as high as what is still being
// written, and setLive sizes it when that changes; the panel opening is not a
// change to it, so the part kept its old height -- none on a fresh launch --
// and /mcp opened a panel with no rows to draw it in (FOUND 2026-11-24).
func (m *chatModel) fitPanelRows() {
	if m.inline.on {
		m.viewport.Height = m.fitLive()
	}
}

func (m chatModel) fitLive() int {
	if m.mcpPanel != nil {
		return m.inline.maxHeight // the panel is drawn where the transcript is, at full height
	}
	return min(m.inline.maxHeight, m.inline.liveLines)
}

// composeScreen puts the screen together.
//
// In inline mode the order is not the alternate screen's. What is still being
// written comes first, straight under the printed chat it continues, after one
// blank row; the header ("✿ Mochiii  ⠋ streaming…", a question waiting) comes
// after it, just above the prompt, where it says what the program is doing
// without cutting an answer in two. An empty transcript part takes no row.
//
// THE PROMPT IS AT THE BOTTOM OF THE WINDOW (owner, 2026-11-21, showing Cline:
// "the prompt bar is now sticking in the top"). While the chat printed so far
// is shorter than the window, blank rows between it and the header take up the
// rest, as many as the prints have not; once it is longer, the terminal's own
// scrolling keeps the prompt there. The rows are never more than the window
// has, so nothing printed is pushed into the scrollback early.
func (m chatModel) composeScreen(header, transcript, bottomLine, status string) string {
	if !m.inline.on {
		return header + "\n" + transcript + "\n" + bottomLine + "\n" + status
	}
	if m.welcoming() {
		return m.welcomeScreen(bottomLine, status)
	}
	var b strings.Builder
	b.WriteByte('\n')
	if m.viewport.Height > 0 {
		b.WriteString(transcript)
		b.WriteByte('\n')
	}
	rest := header + "\n" + bottomLine + "\n" + status
	if pad := m.height - m.inline.rows - (strings.Count(b.String()+rest, "\n") + 1); pad > 0 {
		b.WriteString(strings.Repeat("\n", pad))
	}
	b.WriteString(rest)
	return b.String()
}

// rowsTaken is how many rows of a terminal width columns wide text takes when
// printed: one per line, and more for a line longer than the terminal (/copy's
// are not wrapped).
func rowsTaken(text string, width int) int {
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		w := ansi.StringWidth(line)
		if width <= 0 || w <= width {
			rows++
			continue
		}
		rows += (w + width - 1) / width
	}
	return rows
}
