package main

import (
	"strings"
	"testing"

	"codeterminal/protocol"
)

// M1 client-render guard for the TUI: the daemon reports a cut-off answer via
// TokenResponse.Incomplete (a "length" finish, say), and the client must render
// that as a visibly incomplete state -- not silently, byte-identical to a
// finished answer. These tests fail here (the Fix-13 trap) rather than leaving a
// user to discover a truncated reply looked complete.

func TestChat_IncompleteMsgAppendsCutOffNotice(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	before := len(m.turns)
	updated, cmd := m.Update(incompleteMsg{&protocol.IncompleteInfo{
		Reason: protocol.IncompleteLength,
		Detail: "the model reached its output-length limit before finishing — this answer is cut off.",
	}})
	m = updated.(chatModel)

	if len(m.turns) != before+1 {
		t.Fatalf("turns count = %d, want %d (one cut-off notice appended)", len(m.turns), before+1)
	}
	last := m.turns[len(m.turns)-1]
	if last.role != roleSystem {
		t.Errorf("notice role = %v, want roleSystem (TUI chrome, dropped from history)", last.role)
	}
	if !strings.Contains(last.text, "answer cut off") {
		t.Errorf("notice text = %q, want it to say the answer was cut off", last.text)
	}
	if !strings.Contains(last.text, "output-length limit") {
		t.Errorf("notice text = %q, want it to carry the daemon's client-safe detail", last.text)
	}
	// Not terminal: streamDoneMsg follows on the same channel, so the handler
	// must re-issue waitForNext to keep draining.
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after an incompleteMsg (streamDoneMsg still to come)")
	}
}

// The cut-off notice must NOT ride back out as conversation history: it is a
// client-side annotation, and buildHistory drops roleSystem turns (the daemon
// would refuse a non-user/assistant role anyway).
func TestChat_IncompleteNoticeIsNotSentAsHistory(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(incompleteMsg{&protocol.IncompleteInfo{Reason: protocol.IncompleteLength, Detail: "cut off"}})
	m = updated.(chatModel)

	for _, turn := range buildHistory(m.turns) {
		if strings.Contains(turn.Content, "answer cut off") {
			t.Errorf("history carried the cut-off notice %q; it must be TUI-only chrome", turn.Content)
		}
	}
}

func TestIncompleteText(t *testing.T) {
	// Prefers the daemon's Detail prose.
	if got := incompleteText(&protocol.IncompleteInfo{Reason: "length", Detail: "hit the ceiling"}); got != "hit the ceiling" {
		t.Errorf("incompleteText with detail = %q, want the detail verbatim", got)
	}
	// Falls back to the reason when detail is empty.
	if got := incompleteText(&protocol.IncompleteInfo{Reason: "length"}); !strings.Contains(got, "length") {
		t.Errorf("incompleteText without detail = %q, want it to name the reason", got)
	}
	// Never empty, even for a nil info.
	if got := incompleteText(nil); got == "" {
		t.Error("incompleteText(nil) = empty, want a non-empty generic notice")
	}
}
