package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// promptBetweenRules is the rows of the screen between the prompt's two rules.
func promptBetweenRules(t *testing.T, m chatModel) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	rule := strings.Repeat(promptRule, m.promptWidth())
	var at []int
	for i, l := range lines {
		if strings.TrimSpace(l) == rule {
			at = append(at, i)
		}
	}
	if len(at) < 2 {
		t.Fatalf("no prompt rules on screen:\n%s", strings.Join(lines, "\n"))
	}
	return lines[at[len(at)-2]+1 : at[len(at)-1]]
}

// A LONG PROMPT GROWS DOWNWARDS (owner, 2026-11-21: "the prompt is in single
// [line] and can't be able to see the content what i typed"): every word typed
// is on screen, wrapped at spaces under the ➤, the frame still exactly the
// window's height, and it shrinks back to one row when sent.
//
// Neuter checks: return m.input.View() from inputView -- the first word scrolls
// away; return 3 from promptRows -- the frame is taller than the window.
func TestALongPromptGrowsDownwards(t *testing.T) {
	for _, inline := range []bool{false, true} {
		m := newTestModel()
		m.inline.on = inline
		next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
		m = next.(chatModel)
		q := "FIRSTWORD a single layer perceptron cannot solve XOR because the classes are not linearly separable, " +
			"while a multilayer perceptron with a hidden layer and a nonlinear activation can draw the boundary LASTWORD"
		m = typeText(m, q)

		rows := promptBetweenRules(t, m)
		if len(rows) < 3 {
			t.Fatalf("inline=%t: a %d-character prompt takes %d row(s):\n%s", inline, len(q), len(rows), strings.Join(rows, "\n"))
		}
		joined := strings.Join(rows, " ")
		for _, w := range strings.Fields(q) {
			if !strings.Contains(joined, w) {
				t.Errorf("inline=%t: %q is not on screen:\n%s", inline, w, strings.Join(rows, "\n"))
			}
		}
		if !strings.HasPrefix(rows[0], promptMark+" FIRSTWORD") || !strings.HasPrefix(rows[1], "  ") {
			t.Errorf("inline=%t: the rows are not the ➤ and its indented continuation:\n%s", inline, strings.Join(rows, "\n"))
		}
		for i, r := range rows {
			if w := lipgloss.Width(r); w > m.width {
				t.Errorf("inline=%t: row %d is %d wide", inline, i, w)
			}
		}
		if !inline {
			if n := len(strings.Split(m.View(), "\n")); n != m.height {
				t.Errorf("the frame is %d rows in a %d-row window", n, m.height)
			}
		} else if n := len(strings.Split(m.View(), "\n")); n > m.height {
			t.Errorf("inline: the program's lines (%d) are taller than the window (%d)", n, m.height)
		}

		// Sent, it is one row again.
		m, _ = pressEnter(m)
		if got := m.promptRows(); got != 3 {
			t.Errorf("inline=%t: after sending, the prompt takes %d rows", inline, got)
		}
	}
}

// PAST inputRowsMax THE ROWS AROUND THE CURSOR ARE SHOWN: typing at the end
// shows the end; with the cursor taken back to the start, the start.
func TestAVeryLongPromptShowsWhereTheCursorIs(t *testing.T) {
	m := newTestModel()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = next.(chatModel)
	m = typeText(m, "START "+strings.Repeat("filler words go here ", 40)+"END")
	rows := promptBetweenRules(t, m)
	if len(rows) != m.inputRowsMax() {
		t.Fatalf("a very long prompt takes %d rows, want the most, %d", len(rows), m.inputRowsMax())
	}
	if v := strings.Join(rows, "\n"); !strings.Contains(v, "END") || strings.Contains(v, "START") {
		t.Errorf("typing at the end does not show the end:\n%s", v)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(chatModel)
	if v := strings.Join(promptBetweenRules(t, m), "\n"); !strings.Contains(v, "START") {
		t.Errorf("with the cursor at the start, the start is not shown:\n%s", v)
	}
}

// What fits on one row is drawn by the input itself, as before; a hidden entry
// stays one masked row however long; wrapping breaks at spaces.
func TestAShortOrHiddenPromptStaysOneRow(t *testing.T) {
	m := typeText(newTestModel(), "short question")
	if got := len(promptBetweenRules(t, m)); got != 1 {
		t.Errorf("a short prompt takes %d rows", got)
	}
	m = newTestModel()
	m.input.EchoMode = textinput.EchoPassword
	m = typeText(m, strings.Repeat("k", 200))
	if got := m.inputRows(); got != 1 {
		t.Errorf("a hidden entry takes %d rows", got)
	}
	// "aaa bbbb" in 6 columns: cut at the edge it would be "aaa bb" + "bb"; at
	// the space it is "aaa " + "bbbb".
	rows, _ := wrapRunes([]rune("aaa bbbb"), 6)
	if len(rows) != 2 || rows[0] != [2]int{0, 4} {
		t.Errorf("wrapping did not break after the space: %v", rows)
	}
	if rows, _ := wrapRunes([]rune(strings.Repeat("x", 10)), 4); len(rows) != 3 {
		t.Errorf("a word longer than the row was not cut: %v", rows)
	}
}
