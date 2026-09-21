package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// M3 + folded render-parity guards for the TUI. The daemon already streams
// reasoning tokens and reports history/context truncation on the wire; these
// tests exercise the client's read+render path so the Fix-13 trap (a wire field
// no client renders) fails here rather than in front of a user.

func TestChat_ReasoningRendersSeparatelyAndNotInAnswer(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "explain")
	m, _ = pressEnter(m)

	// Thinking arrives before the answer.
	updated, cmd := m.Update(reasoningMsg{"Let me think. "})
	m = updated.(chatModel)
	updated, _ = m.Update(reasoningMsg{"Weighing options."})
	m = updated.(chatModel)
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after a reasoningMsg (not terminal)")
	}

	// Then the answer streams.
	updated, _ = m.Update(tokenMsg("The actual answer."))
	m = updated.(chatModel)

	last := m.turns[len(m.turns)-1]
	if last.role != roleAssistant {
		t.Fatalf("last turn role = %v, want roleAssistant", last.role)
	}
	if last.reasoning != "Let me think. Weighing options." {
		t.Errorf("turn.reasoning = %q, want both chunks accumulated", last.reasoning)
	}
	// CRITICAL: reasoning must never be spliced into the answer text -- text is
	// what carries back as history and gets parsed for edit blocks.
	if strings.Contains(last.text, "Let me think") {
		t.Errorf("answer text = %q, must NOT contain reasoning", last.text)
	}
	if last.text != "The actual answer." {
		t.Errorf("answer text = %q, want only the content tokens", last.text)
	}

	// And it must actually render (visibly), labelled as thinking.
	rendered := renderTranscript(m.turns, 80)
	if !strings.Contains(rendered, "thinking") || !strings.Contains(rendered, "Let me think") {
		t.Errorf("transcript render does not show the thinking block:\n%s", rendered)
	}
}

// A reasoning-only lead-in must still create the assistant turn, so the screen
// isn't blank while the model thinks (the dead-air bug this fixes).
func TestChat_ReasoningBeforeAnyTokenStartsTheAssistantTurn(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "explain")
	m, _ = pressEnter(m)
	before := len(m.turns)

	updated, _ := m.Update(reasoningMsg{"thinking hard"})
	m = updated.(chatModel)

	if len(m.turns) != before+1 {
		t.Fatalf("turns = %d, want %d (a reasoning chunk starts the assistant turn)", len(m.turns), before+1)
	}
	if m.state != stateStreaming {
		t.Errorf("state = %v, want stateStreaming once thinking has started", m.state)
	}
}

func TestChat_HistoryTruncationRendersAndClears(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	// Not truncated -> no notice.
	updated, _ := m.Update(historyMsg{&protocol.HistoryInfo{Turns: 3, Truncated: false}})
	m = updated.(chatModel)
	if m.historyTruncatedLabel() != "" {
		t.Error("historyTruncatedLabel non-empty for a non-truncated report")
	}

	// Truncated -> a warning notice appears in the header.
	updated, _ = m.Update(historyMsg{&protocol.HistoryInfo{Turns: 3, Truncated: true}})
	m = updated.(chatModel)
	if !m.lastHistoryTruncated {
		t.Fatal("lastHistoryTruncated not set after a truncated HistoryInfo")
	}
	if label := m.historyTruncatedLabel(); !strings.Contains(label, "history was dropped") {
		t.Errorf("historyTruncatedLabel = %q, want a dropped-history warning", label)
	}
	found := false
	for _, line := range m.noticeLines() {
		if strings.Contains(line, "history was dropped") {
			found = true
		}
	}
	if !found {
		t.Error("history-truncation notice not present in noticeLines (header would not show it)")
	}
}

// The truncation notice must be cleared at the START of a new turn so a stale
// warning never lingers against a later prompt (same lifetime as grounding).
func TestChat_HistoryTruncationClearedAtTurnStart(t *testing.T) {
	m := newTestModel() // fresh, idle
	m.lastHistoryTruncated = true

	m = typeText(m, "a new question")
	m, _ = pressEnter(m) // startTurn clears the per-turn notices

	if m.lastHistoryTruncated {
		t.Error("lastHistoryTruncated not cleared at the start of a new turn")
	}
}

func TestChat_GroundingContextTruncationRenders(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, Chunks: 5, Truncated: true}})
	m = updated.(chatModel)

	label := m.groundingLabel()
	if !strings.Contains(label, "context truncated") {
		t.Errorf("groundingLabel = %q, want it to note the context was truncated", label)
	}

	// A non-truncated grounding must NOT claim truncation.
	updated, _ = m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, Chunks: 5, Truncated: false}})
	m = updated.(chatModel)
	if strings.Contains(m.groundingLabel(), "truncated") {
		t.Errorf("groundingLabel = %q, must not claim truncation when not truncated", m.groundingLabel())
	}
}
