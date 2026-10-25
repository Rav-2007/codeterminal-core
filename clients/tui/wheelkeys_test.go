package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// UP AND DOWN ARE THE WHEEL'S (2026-10-25).
//
// With mouse capture off -- so that a drag selects in the terminal and its copy
// key copies, the owner's choice -- the terminal sends each wheel notch as up or
// down keys. So those keys must scroll the chat, must never touch what is typed
// in the prompt, and must work during a turn as well: reading back through an
// answer while it arrives is exactly what the wheel is for.

// idleChatWithBacklog is an idle chat far taller than the screen, with a prompt
// in history, so that both a scroll and a recall would show.
func idleChatWithBacklog(t *testing.T) chatModel {
	t.Helper()
	m := newTestModel()
	for i := 0; i < 40; i++ {
		m.appendTurn(turn{role: roleUser, text: fmt.Sprintf("question %d", i)})
		m.appendTurn(turn{role: roleAssistant, text: "an answer"})
	}
	m.rememberPrompt("an earlier prompt")
	m.refreshViewport()
	if !m.viewport.AtBottom() || m.viewport.TotalLineCount() <= m.viewport.Height {
		t.Fatalf("want a chat taller than the screen, shown from its bottom (%d lines, %d rows, at bottom %v)",
			m.viewport.TotalLineCount(), m.viewport.Height, m.viewport.AtBottom())
	}
	return m
}

func TestUpAndDownScrollTheChatAndLeaveThePromptAlone(t *testing.T) {
	m := idleChatWithBacklog(t)
	m = typeText(m, "half-typ")
	bottom := m.viewport.YOffset

	for i := 0; i < 3; i++ { // one wheel notch: VTE sends three
		m = press(m, tea.KeyUp)
	}
	if m.viewport.YOffset != bottom-3 {
		t.Errorf("three ups scrolled to YOffset %d, want %d", m.viewport.YOffset, bottom-3)
	}
	if got := m.input.Value(); got != "half-typ" {
		t.Errorf("up changed the prompt to %q: the wheel would be overwriting what was typed", got)
	}
	if m.historyIdx != -1 {
		t.Error("up started walking prompt history; that is ctrl+up now")
	}

	for i := 0; i < 3; i++ {
		m = press(m, tea.KeyDown)
	}
	if m.viewport.YOffset != bottom {
		t.Errorf("three downs scrolled to YOffset %d, want back at the bottom, %d", m.viewport.YOffset, bottom)
	}
}

func TestUpScrollsTheChatDuringATurn(t *testing.T) {
	m := streamingModelWithBacklog(t)
	before := m.viewport.YOffset
	m = press(m, tea.KeyUp)
	if m.viewport.YOffset != before-1 {
		t.Errorf("up during a turn moved YOffset %d -> %d, want one line up", before, m.viewport.YOffset)
	}
	if !m.turnInFlight() {
		t.Error("up stopped the turn")
	}
}

// The slash popup keeps up and down: there they walk the highlighted command.
func TestUpAndDownStillWalkTheSlashPopup(t *testing.T) {
	m := idleChatWithBacklog(t)
	m = typeText(m, "/")
	before := m.viewport.YOffset
	m = press(m, tea.KeyDown)
	if m.autocompleteIdx != 1 {
		t.Errorf("down with the popup open: highlighted row %d, want 1", m.autocompleteIdx)
	}
	if m.viewport.YOffset != before {
		t.Error("down with the popup open scrolled the chat instead of walking the popup")
	}
}
