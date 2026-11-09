package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// THE SENTENCE FROM THE PHASE 3 AUDIT (2026-11-09), typed at a prompt.
//
// While a /refactor run was in progress, "please also tidy the apply notes
// later" was typed. The tool approval came up under it, the "a" of "please"
// approved the tool for the whole task, and the "y" of "tidy" applied the edit
// review: doc.go changed and nobody had answered anything.
//
// These two tests use nothing but what the TUI had before the fix (no clock, no
// helper from promptguard_test.go), so they can be run against the code as it
// was: they fail there, and that is how the defect was reproduced. The keys go
// in 2 ms apart, as fast as the audit's harness typed them and faster than any
// person; the guard's timing does not rely on being slow.
const proseSentence = "please also tidy the apply notes later"

func typeProse(m chatModel, s string) chatModel {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(chatModel)
		time.Sleep(2 * time.Millisecond)
	}
	return m
}

func TestProseTypedAtAToolApprovalAnswersNothing(t *testing.T) {
	reply := make(chan string, 1)
	m := newChatModel("test", "/w", "/w", nil)
	m.state = stateStreaming
	m.streamCh = make(chan tea.Msg, 4)
	m.turns = []turn{{role: roleUser, text: "go"}}
	next, _ := m.Update(toolApprovalMsg{req: laneBRequest(), reply: reply})
	m = next.(chatModel)
	if m.state != stateToolApproval {
		t.Fatalf("precondition: state = %v, want the approval prompt", m.state)
	}

	m = typeProse(m, proseSentence)

	select {
	case got := <-reply:
		t.Fatalf("typing %q answered the approval prompt with %q", proseSentence, got)
	default:
	}
	if m.state != stateToolApproval {
		t.Errorf("state = %v after typing; the prompt must still be waiting for a deliberate answer", m.state)
	}
}

func TestProseTypedAtAnEditReviewChangesNothing(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText("foo.go", "func old() {}", "func new_() {}")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)
	if m.state != stateEditReview {
		t.Fatalf("precondition: state = %v, want the edit review", m.state)
	}

	m = typeProse(m, proseSentence)

	if got := readFileString(t, root+"/foo.go"); got != original {
		t.Errorf("typing %q changed foo.go to %q", proseSentence, got)
	}
	if m.state != stateEditReview {
		t.Errorf("state = %v after typing; the review must still be waiting for a deliberate answer", m.state)
	}
}
