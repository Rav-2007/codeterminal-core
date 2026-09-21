package main

import (
	"errors"
	"strings"
	"testing"

	"mochiii/protocol"
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

// The notice PROSE must NOT ride back out as conversation history: it is a
// client-side annotation, and buildHistory drops roleSystem turns (the daemon
// would refuse a non-user/assistant role anyway).
//
// Read this together with its sibling below, and never alone. On its own it
// says only "the chrome stays out", and for a long time that was the only test
// in this area -- which let the FACT stay out too. The prose is TUI-only; the
// fact is the model's, and travels as a slug on the assistant turn.
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

// THE SIBLING, and the regression test for the bug itself: the FACT that the
// answer was cut off must reach the next request, even though the notice does
// not. It rides as protocol.Turn.Incomplete on the assistant turn -- the one
// turn buildHistory keeps -- and the daemon renders the wording from it.
//
// Before this, the roleSystem notice was the only record, buildHistory dropped
// it, and the next turn re-showed the model its own truncated output as a
// finished answer.
//
// NEUTER CHECK: stop setting m.turns[m.streamAssistant].incomplete in the
// incompleteMsg handler, or drop the field from buildHistory's assistant case,
// and this fails -- measured, both ways.
func TestChat_ACutOffAnswerIsMarkedCutOffInTheNextRequestsHistory(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "explain goroutines")
	m, _ = pressEnter(m)

	// A partial answer arrives, then the daemon reports it was cut off.
	updated, _ := m.Update(tokenMsg("A goroutine is a lightweight"))
	m = updated.(chatModel)
	updated, _ = m.Update(incompleteMsg{&protocol.IncompleteInfo{
		Reason: protocol.IncompleteLength,
		Detail: "the model reached its output-length limit before finishing.",
	}})
	m = updated.(chatModel)

	history := buildHistory(m.turns)

	var assistant []protocol.Turn
	for _, turn := range history {
		if turn.Role == "assistant" {
			assistant = append(assistant, turn)
		}
	}
	// ANTI-VACUITY: with no assistant turn in history there is nothing to carry
	// the flag, and every assertion below would pass by never running.
	if len(assistant) != 1 {
		t.Fatalf("history has %d assistant turn(s), want exactly 1 to carry the flag: %+v",
			len(assistant), history)
	}

	if assistant[0].Incomplete != protocol.IncompleteLength {
		t.Errorf("assistant turn went back to the daemon with Incomplete=%q, want %q. The model "+
			"will be re-shown this truncated answer as though it were finished",
			assistant[0].Incomplete, protocol.IncompleteLength)
	}
	if !strings.Contains(assistant[0].Content, "A goroutine is a lightweight") {
		t.Errorf("the partial answer itself did not survive into history: %q", assistant[0].Content)
	}
}

// A turn that finished normally must carry no slug -- otherwise every answer
// arrives at the daemon claiming truncation, and the annotation becomes noise
// the model learns to ignore.
func TestChat_AFinishedAnswerCarriesNoCutOffFlag(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "explain goroutines")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg("A goroutine is a lightweight thread."))
	m = updated.(chatModel)

	for _, turn := range buildHistory(m.turns) {
		if turn.Incomplete != "" {
			t.Errorf("a completed turn carried Incomplete=%q", turn.Incomplete)
		}
	}
}

// L2's FIRST HALF: a stream that fails partway leaves a partial answer in the
// transcript, and it must not go back up as a finished one. The daemon cannot
// mark this case -- its error path carries no Incomplete, and a transport drop
// has no daemon left to annotate anything -- so the client marks it.
//
// NEUTER CHECK: remove the assignment in the streamErrMsg case and this fails
// -- measured.
func TestChat_APartialAnswerThatThenErroredIsMarkedInHistory(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "explain goroutines")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg("A goroutine is a lightweight"))
	m = updated.(chatModel)
	updated, _ = m.Update(streamErrMsg{err: errors.New("connection reset")})
	m = updated.(chatModel)

	var assistant []protocol.Turn
	for _, turn := range buildHistory(m.turns) {
		if turn.Role == "assistant" {
			assistant = append(assistant, turn)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("history has %d assistant turn(s), want exactly 1 to carry the flag", len(assistant))
	}
	if assistant[0].Incomplete != protocol.IncompleteProviderError {
		t.Errorf("a partial answer whose stream then FAILED went back as Incomplete=%q, want %q. "+
			"The model will read a reply that stops mid-sentence as a finished one",
			assistant[0].Incomplete, protocol.IncompleteProviderError)
	}
}

// An error before ANY token produces no partial answer, so there is nothing to
// mark. Marking an empty turn would put a "this was cut off" note on a message
// with no content -- and validTurn drops it server-side regardless.
func TestChat_AnErrorBeforeAnyTokenMarksNothing(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "explain goroutines")
	m, _ = pressEnter(m)
	updated, _ := m.Update(streamErrMsg{err: errors.New("connection refused")})
	m = updated.(chatModel)

	for _, turn := range buildHistory(m.turns) {
		if turn.Incomplete != "" {
			t.Errorf("an empty turn was marked cut off: %+v", turn)
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
