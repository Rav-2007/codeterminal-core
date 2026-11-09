package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TYPED TEXT MUST NOT ANSWER A QUESTION.
//
// THE DEFECT (FOUND 2026-11-09, Phase 3 audit). A tool approval and an edit
// review are answered by one letter: y, a, n, q. While a /refactor run was in
// progress, the sentence "please also tidy the apply notes later" was typed. The
// approval prompt came up under the typing, the "a" of "please" approved the
// tool for the WHOLE TASK, and the "y" of "tidy" applied the edit review.
// doc.go was changed, and nobody had answered anything. Every letter a person
// types is also a letter that answers, and the people most likely to be typing
// are the ones waiting for the agent.
//
// THE RULE. A letter answers only if it is a deliberate single keypress:
//
//   - it is one character, not a paste and not a burst. A run of characters in
//     one message, or a bracketed paste, never answers, whatever it contains;
//   - the question has been on screen for answerSettle, so a key typed before it
//     appeared, and delivered just after, is dropped (a question that was not
//     yet visible cannot have been answered);
//   - the person was not typing: no other character arrived in the last
//     answerQuiet. Every character counts, including the ones this guard drops,
//     so a sentence typed through a prompt stays "typing" from its first
//     character to its last and a pause is what ends it. A held-down key is the
//     same thing (its repeats arrive a few milliseconds apart), so one press
//     answers one question and not the next.
//
// An answer that grants a whole task or spec ("a", "s") is held to longer
// settle and quiet times, because it is the one that cannot be taken back by
// pressing n on the next question.
//
// WHAT HAPPENS TO A KEY THAT IS NOT AN ANSWER: it is dropped. Typing at a prompt
// was already a no-op (a key that does not answer does nothing, see
// handleApprovalKey), so this is the smallest design: the text is neither held
// for the box nor replayed after the prompt. Esc and ctrl+c are not text and are
// not held to any of this: both stop, neither agrees to anything.
//
// WHAT IS NOT PREVENTED: a person who pauses, and then presses one letter that
// they meant as text, has made a deliberate-looking single keypress, and it
// answers. So does the first character of a burst that the terminal splits into
// one message per character after a pause. The first is indistinguishable from
// an answer; the second would need an answer to wait for what comes next.
const (
	answerSettle = 500 * time.Millisecond
	answerQuiet  = 600 * time.Millisecond
	grantSettle  = time.Second
	grantQuiet   = 1500 * time.Millisecond
)

// promptClockNow is the clock new models start with. The tests of this package
// replace a model's clock, not this.
var promptClockNow = time.Now

func (m chatModel) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return promptClockNow()
}

// noteKeyArrival runs first for every key, whatever the state: what was typed
// before a question appeared must still count as typing when it does.
func (m *chatModel) noteKeyArrival(msg tea.KeyMsg) {
	m.prevTypedAt = m.lastTypedAt
	if msg.Type == tea.KeyRunes {
		m.lastTypedAt = m.now()
	}
}

// noteQuestionShown runs when a question is put in front of the user: a tool
// approval, and each edit of a review.
func (m *chatModel) noteQuestionShown() {
	m.questionShownAt = m.now()
}

// mayAnswer says whether msg, a letter, may answer the question on screen.
// standing is true for an answer that grants more than this one call.
func (m chatModel) mayAnswer(msg tea.KeyMsg, standing bool) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 || msg.Paste || msg.Alt {
		return false
	}
	settle, quiet := answerSettle, answerQuiet
	if standing {
		settle, quiet = grantSettle, grantQuiet
	}
	now := m.now()
	// A zero time is "long ago": a question nobody recorded the arrival of, and
	// a person who has typed nothing, are both settled.
	if !m.questionShownAt.IsZero() && now.Sub(m.questionShownAt) < settle {
		return false
	}
	if !m.prevTypedAt.IsZero() && now.Sub(m.prevTypedAt) < quiet {
		return false
	}
	return true
}
