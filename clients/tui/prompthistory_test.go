package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

func press(m chatModel, k tea.KeyType) chatModel {
	updated, _ := m.Update(tea.KeyMsg{Type: k})
	return updated.(chatModel)
}

// The reported request: up shows the last prompt, and down moves forward.
func TestUpAndDownWalkPreviousPrompts(t *testing.T) {
	m := newTestModel()
	m.rememberPrompt("first question")
	m.rememberPrompt("second question")
	m = typeText(m, "half-typ")

	m = press(m, tea.KeyUp)
	if got := m.input.Value(); got != "second question" {
		t.Fatalf("up: input = %q, want the last prompt", got)
	}
	m = press(m, tea.KeyUp)
	if got := m.input.Value(); got != "first question" {
		t.Fatalf("up again: input = %q, want the one before", got)
	}
	m = press(m, tea.KeyUp) // already the oldest: stays
	if got := m.input.Value(); got != "first question" {
		t.Errorf("up at the oldest: input = %q, want it to stay", got)
	}
	m = press(m, tea.KeyDown)
	if got := m.input.Value(); got != "second question" {
		t.Errorf("down: input = %q, want the next prompt", got)
	}
	m = press(m, tea.KeyDown)
	if got := m.input.Value(); got != "half-typ" {
		t.Errorf("down past the newest: input = %q, want the draft back", got)
	}
}

// A recalled slash command opens the popup; up must keep going back through
// history rather than start walking the popup.
func TestHistoryIsNotHijackedByARecalledSlashCommand(t *testing.T) {
	m := newTestModel()
	m.rememberPrompt("explain retry.go")
	m.rememberPrompt("/clear")
	m = press(m, tea.KeyUp)
	m = press(m, tea.KeyUp)
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
	m = press(m, tea.KeyUp)
	if got := m.input.Value(); got != "create a folder named tester" {
		t.Errorf("up after restart: input = %q, want the last saved prompt", got)
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
