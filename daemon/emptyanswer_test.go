package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// AN EMPTY ANSWER IS SAID, NOT LEFT AS SILENCE. Found 2026-11-19 in the real app:
// in plain chat (tools off) a model that streamed nothing for a question got no
// word to the user at all; only the daemon's log said "not persisting turn".
// A greeting already had this after three empty tries (smalltalk.go); any
// other question now gets it after one, since plain chat does not ask again.
//
// Neuter check: drop the `else if incomplete == nil && full.Len() == 0` branch
// after emptyGreeting in server.go.
func TestAnEmptyAnswerToAQuestionIsSaid(t *testing.T) {
	api := sseServerWithFinish(t, "", "stop")
	defer api.Close()
	got := planTurnResponse(t, api.URL, "", "what does this project do")
	inc := got.Incomplete
	if !got.Done || inc == nil || inc.Reason != protocol.IncompleteProviderError || !strings.Contains(inc.Detail, "empty answer") {
		t.Fatalf("an empty answer reached the client as done=%v incomplete=%+v error=%q, want an empty-answer notice", got.Done, inc, got.Error)
	}

	// And an answer with text carries no such notice.
	api2 := sseServerWithFinish(t, "It indexes a repository.", "stop")
	defer api2.Close()
	if got := planTurnResponse(t, api2.URL, "", "what does this project do"); got.Incomplete != nil {
		t.Errorf("a complete answer was marked incomplete: %+v", got.Incomplete)
	}
}
