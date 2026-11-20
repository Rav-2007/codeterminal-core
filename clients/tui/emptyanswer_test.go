package tui

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// AN ANSWER THAT NEVER CAME LEAVES NO "THINKING" BEHIND. Found 2026-11-19 in the
// real app: a plain-chat model that streamed nothing left "💭 thinking" on screen
// for good. endStream's final repaint ran while the turn was still in flight, and
// with no answer text that repaint draws the indicator; the switch to idle came
// after it and drew nothing. The daemon now also says what happened.
//
// Neuter check: drop the `m.state = stateIdle` line at the top of handleStreamDone.
func TestAnAnswerThatNeverCameLeavesNoThinkingBehind(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "what does this project do")
	m, _ = pressEnter(m)
	if !strings.Contains(m.View(), "💭 thinking") {
		t.Fatal("precondition: a turn in flight shows the thinking line")
	}
	const said = "the model sent back an empty answer. Ask again, or pick another model with /model."
	updated, _ := m.Update(incompleteMsg{&protocol.IncompleteInfo{Reason: protocol.IncompleteProviderError, Detail: said}})
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	v := m.View()
	if strings.Contains(v, "💭 thinking") {
		t.Errorf("the turn is over (state %v) and the screen still says it is thinking:\n%s", m.state, v)
	}
	if !strings.Contains(v, "empty answer") {
		t.Errorf("the daemon's notice is not on screen:\n%s", v)
	}
}
