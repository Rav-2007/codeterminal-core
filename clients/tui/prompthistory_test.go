package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

func press(m chatModel, k tea.KeyType) chatModel {
	updated, _ := m.Update(tea.KeyMsg{Type: k})
	return updated.(chatModel)
}

// The reported request: a key shows the last prompt, and its pair moves
// forward -- ctrl+up and ctrl+down since 2026-10-25, when up and down became the
// wheel's (handleKey).
func TestCtrlUpAndDownWalkPreviousPrompts(t *testing.T) {
	m := newTestModel()
	m.rememberPrompt("first question")
	m.rememberPrompt("second question")
	m = typeText(m, "half-typ")

	m = press(m, tea.KeyCtrlUp)
	if got := m.input.Value(); got != "second question" {
		t.Fatalf("ctrl+up: input = %q, want the last prompt", got)
	}
	m = press(m, tea.KeyCtrlUp)
	if got := m.input.Value(); got != "first question" {
		t.Fatalf("ctrl+up again: input = %q, want the one before", got)
	}
	m = press(m, tea.KeyCtrlUp) // already the oldest: stays
	if got := m.input.Value(); got != "first question" {
		t.Errorf("ctrl+up at the oldest: input = %q, want it to stay", got)
	}
	m = press(m, tea.KeyCtrlDown)
	if got := m.input.Value(); got != "second question" {
		t.Errorf("ctrl+down: input = %q, want the next prompt", got)
	}
	m = press(m, tea.KeyCtrlDown)
	if got := m.input.Value(); got != "half-typ" {
		t.Errorf("ctrl+down past the newest: input = %q, want the draft back", got)
	}
}

// A recalled slash command opens the popup; ctrl+up must keep going back
// through history rather than start walking the popup.
func TestHistoryIsNotHijackedByARecalledSlashCommand(t *testing.T) {
	m := newTestModel()
	m.rememberPrompt("explain retry.go")
	m.rememberPrompt("/clear")
	m = press(m, tea.KeyCtrlUp)
	m = press(m, tea.KeyCtrlUp)
	if got := m.input.Value(); got != "explain retry.go" {
		t.Errorf("input = %q, want the prompt before /clear", got)
	}
}

// Prompts sent in earlier sessions come back after a restart.
func TestHistoryIsSeededFromTheSavedConversation(t *testing.T) {
	m := newChatModel("test", "/ws", "/ws", []protocol.Turn{
		{Role: "user", Content: "hi"}, {Role: "assistant", Content: "Hi!"},
		{Role: "user", Content: "create a folder named tester"}, {Role: "assistant", Content: "done"},
	})
	m.state = stateIdle
	m = press(m, tea.KeyCtrlUp)
	if got := m.input.Value(); got != "create a folder named tester" {
		t.Errorf("ctrl+up after restart: input = %q, want the last saved prompt", got)
	}
}

// Submitting records the prompt; repeats are not stacked.
func TestSubmittedPromptsAreRemembered(t *testing.T) {
	m := newTestModel()
	for _, p := range []string{"/help", "/help"} {
		m = typeText(m, p)
		m, _ = pressEnter(m)
	}
	if len(m.promptHistory) != 1 || m.promptHistory[0] != "/help" {
		t.Errorf("history = %q, want one /help", m.promptHistory)
	}
}

// alt+up and alt+down recall too: a Mac gives ctrl+up and ctrl+down to Mission
// Control, so there those never arrive.
func TestAltUpAndDownWalkPreviousPromptsToo(t *testing.T) {
	m := newTestModel()
	m.rememberPrompt("first question")
	m.rememberPrompt("second question")
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	m = u.(chatModel)
	if got := m.input.Value(); got != "second question" {
		t.Fatalf("alt+up: input = %q, want the last prompt", got)
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown, Alt: true})
	m = u.(chatModel)
	if got := m.input.Value(); got != "" {
		t.Errorf("alt+down past the newest: input = %q, want the empty draft back", got)
	}
}
