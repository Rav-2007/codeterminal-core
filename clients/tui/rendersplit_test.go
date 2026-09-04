package main

import (
	"strings"
	"testing"
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
func TestATranscriptIsExactlyTheConcatenationOfItsTurns(t *testing.T) {
	turns := []turn{
		{role: roleUser, text: "first question"},
		{role: roleAssistant, text: "an answer", reasoning: "some thinking"},
		{role: roleAssistant, text: "an answer with no thinking"},
		{role: roleSystem, text: "a system note"},
		{role: roleSevered, text: "never sent"},
		{role: roleUser, text: ""},
		{role: roleAssistant, text: "", reasoning: ""},
	}

	for _, width := range []int{1, 2, 20, 40, 80, 200} {
		whole := renderTranscript(turns, width)

		parts := make([]string, 0, len(turns))
		for _, one := range turns {
			parts = append(parts, renderTurnBlock(one, width))
		}
		joined := strings.Join(parts, turnSeparator)

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
