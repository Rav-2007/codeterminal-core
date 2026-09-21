package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// LONG ANSWERS MUST NOT BE INVISIBLE PAST THE RIGHT EDGE.
//
// bubbles' viewport.SetContent splits on "\n" and nothing else; View() then
// clips each line to Width. A model's paragraph is ONE logical line, so every
// answer wider than the terminal was readable up to the edge and gone after it
// -- with no ellipsis, no scrollbar and nothing on screen to say text had been
// cut.
func TestALongAnswerIsWrappedRatherThanClipped(t *testing.T) {
	const width = 40
	long := "I can't determine today's date from the provided context because those code " +
		"snippets don't contain any date or today information, and I would need to check " +
		"the system clock to answer that."

	wrapped := wrapToWidth(long, width)
	if !strings.Contains(wrapped, "\n") {
		t.Fatal("the text was not wrapped, so everything past the terminal edge is unreachable")
	}
	for i, line := range strings.Split(wrapped, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("line %d is %d cells wide against a width of %d: %q", i, w, width, line)
		}
	}
	// Nothing may be dropped: wrapping reflows, it does not truncate.
	if got := strings.Join(strings.Fields(wrapped), " "); got != strings.Join(strings.Fields(long), " ") {
		t.Error("wrapping changed the words, not just where the lines break")
	}
}

// A WORD LONGER THAN THE TERMINAL must still be broken. ansi.Wordwrap leaves it
// intact, which is why it is the wrong function here: a file path, a URL or a
// base64 blob -- exactly what a coding assistant emits -- would still run off
// the edge and still be invisible.
func TestAnOverlongWordIsBrokenRatherThanOverflowing(t *testing.T) {
	const width = 20
	path := "/home/ravi-kiran/Desktop/Neww/daemon/mcp/sandbox.go:265:WrapCommand"

	for i, line := range strings.Split(wrapToWidth(path, width), "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Fatalf("line %d is %d cells wide against %d: %q -- an unbroken token still overflows",
				i, w, width, line)
		}
	}
}

// Styling must survive. The transcript is wrapped AFTER userStyle/assistantStyle
// have rendered it, so a wrap that cut an escape sequence in half would either
// bleed colour down the screen or print raw escapes.
func TestWrappingPreservesStyling(t *testing.T) {
	styled := assistantStyle.Render("Mochiii: " + strings.Repeat("word ", 40))
	wrapped := wrapToWidth(styled, 30)

	if strings.Contains(wrapped, "\x1b[") != strings.Contains(styled, "\x1b[") {
		t.Error("wrapping added or removed escape sequences")
	}
	if !strings.Contains(ansi.Strip(wrapped), "Mochiii:") {
		t.Error("the visible text did not survive wrapping")
	}
}

// Before the first WindowSizeMsg the width is 0, and content must pass through
// untouched rather than being wrapped to nothing.
//
// ansi.Wrap already returns its input for limit < 1, so wrapToWidth's own guard
// is BELT AND BRACES and a neuter of it does not fail this test. Kept anyway:
// the guard is what makes the contract local and readable, rather than a
// property of a dependency that could change under us. This pins the behaviour
// at the boundary either way.
func TestAnUnknownTerminalWidthLeavesContentAlone(t *testing.T) {
	const s = "some text that must not be destroyed"
	for _, w := range []int{0, -1} {
		if got := wrapToWidth(s, w); got != s {
			t.Errorf("width %d returned %q", w, got)
		}
	}
}

// END TO END through the real model, asserting what the bug actually costs:
// TEXT DISAPPEARS. A width assertion on viewport.View() cannot catch this --
// View() clips to Width, so its lines are never too wide whether or not the
// content was wrapped. The clipping IS the bug, and it hides itself from
// exactly the measurement you would reach for first.
//
// So this asserts the end of a long turn is still on screen somewhere.
func TestNoneOfALongAnswerIsLostOffTheRightEdge(t *testing.T) {
	const (
		width = 60
		tail  = "THE-VERY-LAST-WORDS"
	)
	m := newChatModel("test", "/w", "/w", nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	cm := updated.(chatModel)
	cm.turns = append(cm.turns, turn{
		role: roleAssistant,
		text: strings.Repeat("a long sentence that keeps going and going ", 6) + tail,
	})
	cm.refreshViewport()

	rendered := ansi.Strip(cm.viewport.View())
	if !strings.Contains(rendered, tail) {
		t.Fatalf("the end of the answer never reaches the screen: everything past column %d "+
			"is clipped away with no ellipsis and no scrollbar.\nrendered:\n%s", width, rendered)
	}
	for i, line := range strings.Split(cm.viewport.View(), "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("rendered line %d is %d cells wide against a %d-wide viewport", i, w, width)
		}
	}
}

// ---------------------------------------------------------------------------
// Header notices: nothing hidden, and the row arithmetic still exact.
// ---------------------------------------------------------------------------

// THE INVARIANT headerLineCount DEPENDS ON: one element is one terminal row.
// A notice that soft-wraps consumes a row nobody counted, which desyncs the
// viewport height and pushes content off-screen.
func TestEveryNoticeLineIsExactlyOneTerminalRow(t *testing.T) {
	const width = 50
	m := newChatModel("test", "/w", "/w", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	cm := updated.(chatModel)

	cm.lastDegraded = []protocol.Degradation{{
		Component: protocol.DegradedProviderRouting,
		Detail: "provider routing permits fallbacks outside the configured " +
			"zero-data-retention constraints, so a request may be served by a non-ZDR endpoint",
	}}
	cm.lastProvider = "DigitalOcean"

	lines := cm.noticeLines()
	if len(lines) < 2 {
		t.Fatalf("a notice far longer than %d columns produced %d row(s); it cannot fit", width, len(lines))
	}
	for i, line := range lines {
		if strings.Contains(line, "\n") {
			t.Errorf("notice element %d contains a newline, so it is more than one row", i)
		}
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("notice element %d is %d cells wide against %d, so it will soft-wrap uncounted", i, w, width)
		}
	}

	// headerLineCount must agree with what renderHeader actually draws.
	drawn := len(strings.Split(cm.renderHeader(), "\n"))
	if drawn != cm.headerLineCount() {
		t.Errorf("renderHeader drew %d rows, headerLineCount says %d", drawn, cm.headerLineCount())
	}
}

// THE END OF A DISCLOSURE MUST NOT BE THROWN AWAY. The longest notice is the ZDR
// degradation, whose entire purpose is to say which guarantee was weakened --
// truncating it disclosed that something was wrong while hiding what.
func TestTheDegradationNoticeIsNotCutOffMidSentence(t *testing.T) {
	m := newChatModel("test", "/w", "/w", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	cm := updated.(chatModel)
	cm.lastDegraded = []protocol.Degradation{{
		Component: protocol.DegradedProviderRouting,
		Detail: "provider routing permits fallbacks outside the configured " +
			"zero-data-retention constraints, so a request may be served by a non-ZDR endpoint",
	}}

	joined := ansi.Strip(strings.Join(cm.noticeLines(), " "))
	if strings.Contains(joined, "…") {
		t.Errorf("the notice was truncated with an ellipsis: %q", joined)
	}
	// The LAST word, not a phrase: a hyphen is always a wrap breakpoint, so
	// "non-ZDR" legitimately splits across rows and reassembling with a space
	// gives "non- ZDR". That is correct typography, and asserting on a phrase
	// that spans a possible break point tests the reassembly rather than the
	// disclosure.
	if !strings.HasSuffix(strings.TrimSpace(joined), "endpoint") {
		t.Errorf("the end of the disclosure never reaches the screen: %q", joined)
	}
}
