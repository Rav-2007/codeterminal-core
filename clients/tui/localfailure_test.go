package tui

import (
	"errors"
	"testing"
)

// A failed question to a model on this computer says what to check there: a
// server that has stopped, or a model it no longer has. The same failure from
// a cloud provider, and a failure with nothing local about it, keep the
// daemon's words alone.
//
// Neuter check: make localFailureHint return "" and both local cases lose
// their hint.
func TestAFailedLocalQuestionSaysWhatToCheck(t *testing.T) {
	resetLocalState(t)
	const daemonWords = "the daemon's words"
	ask := func(class string) string {
		t.Helper()
		m := newTestModel()
		m = typeText(m, "hello")
		m, _ = pressEnter(m)
		updated, _ := m.Update(streamErrMsg{err: errors.New(daemonWords), sent: true, class: class})
		m = updated.(chatModel)
		return m.turns[len(m.turns)-1].text
	}

	setLocalActive(true)
	for class, want := range map[string]string{
		"upstream_unavailable": daemonWords + " -- if the model server on this computer has stopped, start it again (for Ollama: ollama serve)",
		"unknown":              daemonWords + " -- if the model is no longer on the server, /connect local lists the ones it has now",
		"rate_limited":         daemonWords,
	} {
		if got := ask(class); got != want {
			t.Errorf("a local model, %s: the transcript says %q, want %q", class, got, want)
		}
	}
	setLocalActive(false)
	if got := ask("upstream_unavailable"); got != daemonWords {
		t.Errorf("a cloud provider's failure was given a local hint: %q", got)
	}
}
