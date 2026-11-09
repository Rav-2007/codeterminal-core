package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// settled is a model whose question has been up for a long time and whose user
// has typed nothing: the state of a person who sat down and read it. The tests
// that are about WHAT a key answers use it. The tests below are about WHEN a
// key may answer, and drive a clock of their own.
func settled(m chatModel) chatModel {
	m.questionShownAt = time.Time{}
	m.lastTypedAt = time.Time{}
	m.prevTypedAt = time.Time{}
	return m
}

// fakeClock is time that moves only when a test says so.
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// keyAtPrompt delivers one key and returns the model.
func keyAtPrompt(t *testing.T, m chatModel, msg tea.KeyMsg) chatModel {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(chatModel)
}

// promptUp is a model paused on a tool approval, on a clock the test moves.
func promptUp(t *testing.T, c *fakeClock) (chatModel, chan string) {
	t.Helper()
	reply := make(chan string, 1)
	m := newChatModel("test", "/w", "/w", nil)
	m.clock = c.now
	m.state = stateStreaming
	m.streamCh = make(chan tea.Msg, 4)
	m.turns = []turn{{role: roleUser, text: "go"}}
	next, _ := m.Update(toolApprovalMsg{req: laneBRequest(), reply: reply})
	m = next.(chatModel)
	if m.state != stateToolApproval {
		t.Fatalf("precondition: state = %v, want the approval prompt", m.state)
	}
	return m, reply
}

func answered(reply chan string) (string, bool) {
	select {
	case got := <-reply:
		return got, true
	default:
		return "", false
	}
}

// reviewUp is a model on the edit review of foo.go (and bar.go when two is
// true), on a clock the test moves.
func reviewUp(t *testing.T, c *fakeClock, two bool) (chatModel, string) {
	t.Helper()
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	writeTempFile(t, root, "bar.go", "package main\n\nfunc bar() {}\n")
	m := newTestModelWithRoot(root)
	m.clock = c.now
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	text := editBlockText("foo.go", "func old() {}", "func new_() {}")
	if two {
		text += "\n" + editBlockText("bar.go", "func bar() {}", "func bar2() {}")
	}
	updated, _ := m.Update(tokenMsg(text))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)
	if m.state != stateEditReview {
		t.Fatalf("precondition: state = %v, want the edit review", m.state)
	}
	return m, root
}

func fooChanged(t *testing.T, root string) bool {
	t.Helper()
	b, err := os.ReadFile(root + "/foo.go")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), "new_")
}

// A DELIBERATE SINGLE KEY STILL ANSWERS. The guard must not make the prompts
// unanswerable, so each answer is shown working after a person's pause.
func TestADeliberateSingleKeyStillAnswers(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"y", protocol.ApprovalApprove},
		{"n", protocol.ApprovalDeny},
		{"q", protocol.ApprovalCancelTurn},
		{"a", protocol.ApprovalApproveForTurn},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			c := newFakeClock()
			m, reply := promptUp(t, c)
			c.advance(2 * time.Second) // read it, then pressed one key
			keyAtPrompt(t, m, runeKey(tc.key))
			got, ok := answered(reply)
			if !ok || got != tc.want {
				t.Errorf("%q after a pause: answered %q (%v), want %q", tc.key, got, ok, tc.want)
			}
		})
	}
	t.Run("esc is a reflex stop and is never held", func(t *testing.T) {
		c := newFakeClock()
		m, reply := promptUp(t, c)
		keyAtPrompt(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // no time has passed at all
		if got, ok := answered(reply); !ok || got != protocol.ApprovalCancelTurn {
			t.Errorf("esc answered %q (%v), want a cancel", got, ok)
		}
	})
	t.Run("an edit review", func(t *testing.T) {
		c := newFakeClock()
		m, root := reviewUp(t, c, false)
		c.advance(2 * time.Second)
		m = keyAtPrompt(t, m, runeKey("y"))
		if !fooChanged(t, root) || m.state == stateEditReview {
			t.Errorf("a deliberate y did not apply the edit (state %v)", m.state)
		}
	})
}

// KEYS THAT ARRIVED BEFORE THE PROMPT WAS ON SCREEN ARE DROPPED. A person types
// ahead while the agent works; the question appears; the last of what they
// typed is delivered just after it.
func TestKeysTypedBeforeThePromptAppearedAreDropped(t *testing.T) {
	c := newFakeClock()
	reply := make(chan string, 1)
	m := newChatModel("test", "/w", "/w", nil)
	m.clock = c.now
	m.state = stateStreaming
	m.streamCh = make(chan tea.Msg, 4)
	m.turns = []turn{{role: roleUser, text: "go"}}

	// Typed ahead while the turn runs: "yes" at 100 ms intervals.
	for _, r := range "yes" {
		m = keyAtPrompt(t, m, runeKey(string(r)))
		c.advance(100 * time.Millisecond)
	}
	next, _ := m.Update(toolApprovalMsg{req: laneBRequest(), reply: reply})
	m = next.(chatModel)
	// The tail of the type-ahead is delivered 100 ms after the prompt appears,
	// after a longer pause than the quiet time: only the settle time stops it.
	c.advance(100 * time.Millisecond)
	// The person's last character is set aside here, so that only the time the
	// prompt has been up is under test. (The next step tests the typing.)
	m.lastTypedAt, m.prevTypedAt = time.Time{}, time.Time{}
	m = keyAtPrompt(t, m, runeKey("y"))
	if got, ok := answered(reply); ok {
		t.Fatalf("a key 100 ms after the prompt appeared answered %q", got)
	}
	// The dropped key counts as typing, so it is the pause after it that counts.
	c.advance(300 * time.Millisecond)
	m = keyAtPrompt(t, m, runeKey("y"))
	if got, ok := answered(reply); ok {
		t.Fatalf("a key 300 ms after the last character answered %q", got)
	}
	c.advance(time.Second)
	keyAtPrompt(t, m, runeKey("y"))
	if got, ok := answered(reply); !ok || got != protocol.ApprovalApprove {
		t.Errorf("a key after a pause answered %q (%v), want approve", got, ok)
	}
}

// A BURST OR A PASTE NEVER ANSWERS, whatever it holds, and however long the
// person had been idle before it.
func TestABurstOrAPasteNeverAnswers(t *testing.T) {
	msgs := map[string]tea.KeyMsg{
		"a burst in one message":     runeKey("yy"),
		"a burst that starts with a": runeKey("aaaa"),
		"a paste of one character":   {Type: tea.KeyRunes, Runes: []rune("y"), Paste: true},
		"a paste of a":               {Type: tea.KeyRunes, Runes: []rune("a"), Paste: true},
		"a long paste of yes and no": {Type: tea.KeyRunes, Runes: []rune(strings.Repeat("y", 500)), Paste: true},
		"alt+y":                      {Type: tea.KeyRunes, Runes: []rune("y"), Alt: true},
	}
	for name, msg := range msgs {
		t.Run("approval/"+name, func(t *testing.T) {
			c := newFakeClock()
			m, reply := promptUp(t, c)
			c.advance(time.Hour)
			m = keyAtPrompt(t, m, msg)
			if got, ok := answered(reply); ok {
				t.Errorf("%s answered the approval with %q", name, got)
			}
			if m.state != stateToolApproval {
				t.Errorf("state = %v; the prompt should still be waiting", m.state)
			}
		})
		t.Run("review/"+name, func(t *testing.T) {
			c := newFakeClock()
			m, root := reviewUp(t, c, false)
			c.advance(time.Hour)
			m = keyAtPrompt(t, m, msg)
			if fooChanged(t, root) || m.state != stateEditReview {
				t.Errorf("%s answered the edit review (state %v)", name, m.state)
			}
		})
	}
	t.Run("single keys arriving a few milliseconds apart", func(t *testing.T) {
		c := newFakeClock()
		m, reply := promptUp(t, c)
		c.advance(time.Hour)
		m = keyAtPrompt(t, m, runeKey("x")) // the burst's first character, which is no answer
		for _, r := range "aaaayyyy" {
			c.advance(5 * time.Millisecond)
			m = keyAtPrompt(t, m, runeKey(string(r)))
		}
		if got, ok := answered(reply); ok {
			t.Errorf("a burst of single keys answered %q", got)
		}
	})
}

// THE SENTENCE, TYPED AT A HUMAN PACE, even through a prompt that has been up
// for a while: every character counts as typing, so the sentence never pauses
// long enough to answer anything.
func TestTypingThroughAPromptStaysTyping(t *testing.T) {
	c := newFakeClock()
	m, reply := promptUp(t, c)
	c.advance(700 * time.Millisecond) // the prompt has been up past its settle time
	for _, r := range proseSentence {
		m = keyAtPrompt(t, m, runeKey(string(r)))
		c.advance(250 * time.Millisecond)
	}
	if got, ok := answered(reply); ok {
		t.Fatalf("%q answered the prompt with %q", proseSentence, got)
	}
	c.advance(time.Second) // they stop and look at the prompt
	keyAtPrompt(t, m, runeKey("n"))
	if got, ok := answered(reply); !ok || got != protocol.ApprovalDeny {
		t.Errorf("a deliberate n after the typing stopped answered %q (%v), want deny", got, ok)
	}
}

// "a" APPROVES THE WHOLE TASK, and it is held to more than "y" is.
func TestApprovingForTheWholeTaskNeedsAMoreDeliberateKey(t *testing.T) {
	t.Run("the same moment that lets y through does not let a", func(t *testing.T) {
		c := newFakeClock()
		m, reply := promptUp(t, c)
		c.advance(900 * time.Millisecond)
		m = keyAtPrompt(t, m, runeKey("x")) // a character of something typed
		c.advance(900 * time.Millisecond)
		keyAtPrompt(t, m, runeKey("a")) // prompt up 1.8 s, last character 900 ms ago
		if got, ok := answered(reply); ok {
			t.Errorf("a answered %q 900 ms after typing", got)
		}
		keyAtPrompt(t, m, runeKey("y"))
		if got, ok := answered(reply); !ok || got != protocol.ApprovalApprove {
			t.Errorf("y at the same moment answered %q (%v), want approve", got, ok)
		}
	})
	t.Run("a prompt that has only just appeared", func(t *testing.T) {
		c := newFakeClock()
		m, reply := promptUp(t, c)
		c.advance(800 * time.Millisecond) // past y's wait, short of a's
		keyAtPrompt(t, m, runeKey("a"))
		if got, ok := answered(reply); ok {
			t.Errorf("a answered %q 800 ms after the prompt appeared", got)
		}
	})
	t.Run("after a real pause it works", func(t *testing.T) {
		c := newFakeClock()
		m, reply := promptUp(t, c)
		c.advance(3 * time.Second)
		keyAtPrompt(t, m, runeKey("a"))
		if got, ok := answered(reply); !ok || got != protocol.ApprovalApproveForTurn {
			t.Errorf("a after a pause answered %q (%v), want approve-for-turn", got, ok)
		}
	})
	t.Run("the spec grant is held the same way", func(t *testing.T) {
		c := newFakeClock()
		m, reply := promptUp(t, c)
		m.pendingApproval.SpecGrant = "go test ./..."
		c.advance(800 * time.Millisecond)
		keyAtPrompt(t, m, runeKey("s"))
		if got, ok := answered(reply); ok {
			t.Errorf("s answered %q 800 ms after the prompt appeared", got)
		}
	})
}

// A HELD-DOWN KEY ANSWERS ONE QUESTION, NOT THE NEXT. The edit review shows its
// edits one at a time; holding y must not apply them all.
func TestAHeldKeyAppliesOneEditNotTheNext(t *testing.T) {
	c := newFakeClock()
	m, root := reviewUp(t, c, true)
	c.advance(2 * time.Second)
	m = keyAtPrompt(t, m, runeKey("y")) // the press
	if !fooChanged(t, root) {
		t.Fatal("the first edit was not applied by a deliberate y")
	}
	if m.state != stateEditReview {
		t.Fatalf("state = %v; the second edit should be waiting", m.state)
	}
	for i := 0; i < 20; i++ { // the repeats, 30 ms apart
		c.advance(30 * time.Millisecond)
		m = keyAtPrompt(t, m, runeKey("y"))
	}
	if m.state != stateEditReview {
		t.Errorf("held y went on to answer the second edit (state %v)", m.state)
	}
	// And the second edit is still answerable by a deliberate key.
	c.advance(time.Second)
	m = keyAtPrompt(t, m, runeKey("y"))
	if m.state == stateEditReview {
		t.Errorf("a deliberate y did not answer the second edit")
	}
}

// THE REVIEW TAKES THE SAME GUARD: typed text answers neither y, n nor q.
func TestTextTypedAtAnEditReviewAnswersNothing(t *testing.T) {
	c := newFakeClock()
	m, root := reviewUp(t, c, false)
	c.advance(700 * time.Millisecond)
	for _, r := range proseSentence + " yes no quit" {
		m = keyAtPrompt(t, m, runeKey(string(r)))
		c.advance(250 * time.Millisecond)
	}
	if fooChanged(t, root) || m.state != stateEditReview || m.reviewSkipped != 0 {
		t.Errorf("typed text answered the review: changed=%v state=%v skipped=%d", fooChanged(t, root), m.state, m.reviewSkipped)
	}
}
