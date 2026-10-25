package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// WHERE THE ANSWER GOES (transcriptlayout.go, 2026-10-25). A reasoning model
// streams "\n\n" before its first tool call; that opened the answer above every
// call of the run, and "Done: …" was drawn above the work it reported.

func activityStep(m chatModel, a protocol.ToolActivity) chatModel {
	u, _ := m.Update(toolActivityMsg{a})
	return u.(chatModel)
}

func TestAnAnswerThatBeganAsWhitespaceIsDrawnBelowItsCalls(t *testing.T) {
	m := deliverToken(streamingModelWithBacklog(t), "\n\n")
	m = activityStep(m, call("c1", "read_file", "a.go", protocol.ToolPhaseRunning, 0))
	m = deliverToken(m, "Done: it reads one line.")
	// The call's line is rewritten AFTER the move: it must still be its own.
	m = activityStep(m, call("c1", "read_file", "a.go", protocol.ToolPhaseSucceeded, 10))

	got := ansi.Strip(renderTranscript(m.turns, 100))
	callAt, answerAt := strings.Index(got, "● Read   a.go · 10 bytes"), strings.Index(got, "Done: it reads one line.")
	if callAt < 0 || answerAt < 0 || callAt > answerAt {
		t.Errorf("want the call, finished, above the answer:\n%s", got[max(0, len(got)-300):])
	}
	if n := strings.Count(got, "a.go"); n != 1 {
		t.Errorf("a.go appears %d times: the late rewrite missed its line after the move", n)
	}
	if last := m.turns[len(m.turns)-1]; last.role != roleAssistant || !strings.Contains(last.text, "Done") {
		t.Errorf("the answer is not the last turn: %+v", last)
	}
	if h := buildHistory(m.turns); h[len(h)-1].Role != "assistant" || !strings.Contains(h[len(h)-1].Content, "Done") {
		t.Errorf("history after the move ends %+v, want the answer", h[len(h)-1])
	}
}

// An answer that said something before its calls stays where it said it.
func TestAnAnswerThatSaidSomethingFirstStaysPut(t *testing.T) {
	m := deliverToken(streamingModelWithBacklog(t), "Let me look. ")
	m = activityStep(m, call("c1", "read_file", "a.go", protocol.ToolPhaseSucceeded, 10))
	m = deliverToken(m, "Done.")
	got := ansi.Strip(renderTranscript(m.turns, 100))
	if strings.Index(got, "Let me look.") > strings.Index(got, "● Read   a.go") {
		t.Errorf("an answer that had already begun was moved:\n%s", got[max(0, len(got)-300):])
	}
}

// "THERE IS SOMETHING STILL VISIBLE?" (2026-10-25): under a stopped turn, among
// the rest, "Mochiii:" over nothing at all. The model had streamed a newline or
// two before its first tool call; an answer of only whitespace is not drawn.
func TestAnAnswerOfOnlyWhitespaceDrawsNothing(t *testing.T) {
	v, _ := activityFor(call("c1", "list_directory", "/home/u/proj", protocol.ToolPhaseSucceeded, 487))
	turns := []turn{
		{role: roleUser, text: "create a login page"},
		{role: roleAssistant, text: "\n\n"},
		{role: roleActivity, text: v.plain(), act: &v},
		{role: roleSystem, text: "■ stopped — you interrupted this turn"},
	}
	got := ansi.Strip(renderTranscript(turns, 80))
	if strings.Contains(got, assistantLabel) {
		t.Errorf("an answer of only whitespace drew its label:\n%s", got)
	}
	want := "You: create a login page\n\n● List   /home/u/proj · 487 bytes\n\n■ stopped — you interrupted this turn"
	if got != want {
		t.Errorf("transcript:\n%q\nwant:\n%q", got, want)
	}
	var cache transcriptCache
	if cached := ansi.Strip(cache.render(turns, 80)); cached != got {
		t.Errorf("the cache lays it out differently:\n%q\nfresh:\n%q", cached, got)
	}
}

// While the only thing streamed is whitespace, the turn is still waiting for its
// answer: "thinking" stays up, and no empty label takes its place.
func TestWhitespaceIsNotYetAnAnswer(t *testing.T) {
	m := deliverToken(streamingModelWithBacklog(t), "\n\n")
	if !m.awaitingAnswer() {
		t.Error("two newlines counted as the answer having started")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "thinking") {
		t.Errorf("no thinking indicator while only whitespace has arrived:\n%s", view)
	}
}

// A turn that draws nothing leaves no gap: not at the top of the chat, and not
// inside a run of calls, which stays stacked across it.
func TestAHiddenTurnLeavesNoGap(t *testing.T) {
	a, _ := activityFor(call("c1", "read_file", "a.go", protocol.ToolPhaseSucceeded, 10))
	b, _ := activityFor(call("c2", "read_file", "b.go", protocol.ToolPhaseSucceeded, 20))
	turns := []turn{
		{role: roleAssistant, text: " \n"},
		{role: roleActivity, text: a.plain(), act: &a},
		{role: roleAssistant, text: "\n"},
		{role: roleActivity, text: b.plain(), act: &b},
	}
	got := ansi.Strip(renderTranscript(turns, 80))
	if want := "● Read   a.go · 10 bytes\n● Read   b.go · 20 bytes"; got != want {
		t.Errorf("transcript:\n%q\nwant:\n%q", got, want)
	}
}
