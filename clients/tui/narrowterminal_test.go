package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A TERMINAL TOO NARROW TO DRAW IN MUST NOT BE A CRASH.
//
// bubbles/textinput.placeholderView allocates `make([]rune, m.Width+1)` with no
// guard, so a negative Width panics the entire client. The old sizing
// expression produced one for any terminal narrower than the prompt plus its
// padding -- and, MEASURED, for a host that reports no size at all: running the
// client under a pty with no window size delivers WindowSizeMsg{0, 0}, which
// gave -4 and killed it on the first render after the splash.
func TestANarrowTerminalDoesNotProduceANegativeInputWidth(t *testing.T) {
	for _, prompt := range []string{"", "> ", "much longer prompt > "} {
		for width := -5; width < 40; width++ {
			if got := inputWidthFor(width, prompt); got < 0 {
				t.Fatalf("terminal width %d with prompt %q gave input width %d, which panics "+
					"textinput.placeholderView", width, prompt, got)
			}
		}
	}
}

// A terminal wide enough to draw in must still be sized the way it always was;
// the guard is a floor, not a resize.
func TestAnOrdinaryTerminalIsSizedAsBefore(t *testing.T) {
	if got, want := inputWidthFor(120, "> "), 120-2-2; got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

// End to end through the real model: the exact message sequence that killed the
// client -- a zero-size window, then a keypress to leave the splash, then a
// render.
func TestTheClientSurvivesAZeroSizeTerminal(t *testing.T) {
	m := newChatModel("test", "/w", "/w", nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	updated, _ = updated.(chatModel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("rendering into a zero-size terminal panicked: %v", r)
		}
	}()
	if view := updated.(chatModel).View(); strings.Contains(view, "makeslice") {
		t.Fatal("the view rendered a panic message")
	}
}
