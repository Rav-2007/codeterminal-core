package tui

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// A TURN RENDERS THE SAME ALONE AS IT DOES IN COMPANY.
//
// renderTurnBlock was split out of renderTranscript so that one turn can be
// rendered and kept. That is only sound while the whole is exactly the
// concatenation of the parts -- the moment a block starts depending on the turn
// before it (a style that continues, a heading emitted only on a role change, a
// separator that varies) a cache of blocks stops being able to reproduce the
// transcript, and the failure is stale text on screen rather than an error.
//
// This is the property the render cache rests on, asserted directly rather than
// left to the equivalence gate to discover through randomised inputs.
//
// THE SEPARATOR MAY VARY; A BLOCK MAY NOT (2026-10-25). Since a run of tool calls
// is stacked and an answer of only whitespace draws nothing, what goes BETWEEN
// two blocks depends on the turns around it -- but only through separatorBefore,
// a function of the turn list that renderTranscript and the cache both call, and
// only ever as layout: nothing, a newline, or a blank line. Each block is still
// drawn from its own turn alone, which is what this asserts.
func TestATranscriptIsExactlyTheConcatenationOfItsTurns(t *testing.T) {
	read, _ := activityFor(call("c1", "read_file", "a.go", protocol.ToolPhaseSucceeded, 10))
	list, _ := activityFor(call("c2", "list_directory", "src", protocol.ToolPhaseSucceeded, 20))
	turns := []turn{
		{role: roleUser, text: "first question"},
		{role: roleAssistant, text: "an answer", reasoning: "some thinking"},
		{role: roleAssistant, text: "an answer with no thinking"},
		{role: roleSystem, text: "a system note"},
		{role: roleSevered, text: "never sent"},
		{role: roleUser, text: ""},
		{role: roleAssistant, text: "", reasoning: ""},
		{role: roleActivity, text: read.plain(), act: &read},
		{role: roleAssistant, text: "\n"},
		{role: roleActivity, text: list.plain(), act: &list},
	}

	for i := 1; i < len(turns); i++ {
		if sep := separatorBefore(turns, i); sep != "" && sep != "\n" && sep != turnSeparator {
			t.Fatalf("the separator before turn %d is %q: a separator is layout, never content", i, sep)
		}
	}
	for _, width := range []int{1, 2, 20, 40, 80, 200} {
		whole := renderTranscript(turns, width)

		var b strings.Builder
		for i, one := range turns {
			if i > 0 {
				b.WriteString(separatorBefore(turns, i))
			}
			b.WriteString(renderShownBlock(one, width)) // the turn alone, nothing around it
		}
		joined := b.String()

		if whole != joined {
			t.Errorf("width %d: the transcript is not the concatenation of its turns.\n"+
				"transcript: %q\njoined:     %q", width, whole, joined)
		}
	}
}

// And the empty transcript is the empty string, not a stray separator.
func TestAnEmptyTranscriptRendersEmpty(t *testing.T) {
	if got := renderTranscript(nil, 80); got != "" {
		t.Errorf("an empty transcript rendered %q", got)
	}
}
