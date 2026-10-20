package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// sizedApprovalModel is pendingApprovalModel on a terminal of a known size:
// what is visible is the whole subject of these tests, and a model that was
// never sized has no screen to hide anything behind.
func sizedApprovalModel(t *testing.T, width, height int, req protocol.ToolApprovalRequest) (chatModel, chan string) {
	t.Helper()
	reply := make(chan string, 1)
	m := newChatModel("test", "/w", "/w", nil)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = sized.(chatModel)
	m.state = stateStreaming
	m.streamCh = make(chan tea.Msg, 4)
	m.turns = []turn{{role: roleUser, text: "go"}}

	next, _ := m.Update(toolApprovalMsg{req: req, reply: reply})
	updated := next.(chatModel)
	if updated.state != stateToolApproval {
		t.Fatalf("an approval request did not pause the turn; state = %v", updated.state)
	}
	return updated, reply
}

// longLaneBRequest is a third-party call whose arguments wrap to far more lines
// than a 24-row terminal has: the path that matters first, then padding.
func longLaneBRequest() protocol.ToolApprovalRequest {
	var pad strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&pad, "filler-%03d ", i)
	}
	req := laneBRequest()
	req.Tool = "write_anything"
	req.Arguments = `{"path":"/home/u/.bashrc","content":"` + pad.String() + `LAST-WORD"}`
	return req
}

func pressKey(t *testing.T, m chatModel, key tea.KeyType) chatModel {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: key})
	return next.(chatModel)
}

func pressRune(t *testing.T, m chatModel, r rune) chatModel {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return next.(chatModel)
}

// A QUESTION TALLER THAN THE SCREEN IS SHOWN FROM ITS BEGINNING.
//
// FOUND 2026-10-12 by reading: the panel was appended to the transcript and
// the view jumped to the bottom, so a call whose arguments wrapped past the
// screen was asked about with its tool name and its first arguments scrolled
// out of sight -- "y yes" under the tail of some padding. For a third-party
// server that prompt is the only thing between the call and a program running
// with the user's full privileges.
func TestALongApprovalQuestionIsShownFromItsBeginning(t *testing.T) {
	m, _ := sizedApprovalModel(t, 80, 24, longLaneBRequest())
	screen := ansi.Strip(m.View())

	for _, want := range []string{"somebodys-server/write_anything", "/home/u/.bashrc"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen asking for approval does not show %q:\n%s", want, screen)
		}
	}
}

// A QUESTION THAT WAS NOT ALL SHOWN CANNOT BE ANSWERED YES.
//
// Refusing stays one key away throughout: the gate is on agreeing to what has
// not been on screen, never on declining it.
func TestAnApprovalIsNotGrantedBeforeItWasAllShown(t *testing.T) {
	for _, key := range []rune{'y', 'a'} {
		t.Run(string(key), func(t *testing.T) {
			m, reply := sizedApprovalModel(t, 80, 24, longLaneBRequest())
			m = pressRune(t, m, key)
			select {
			case got := <-reply:
				t.Fatalf("%q answered %q while most of the question had never been on screen", key, got)
			default:
			}
			if m.state != stateToolApproval {
				t.Fatalf("%q dismissed the question without answering it", key)
			}
			// All of it, at 80 columns: how much is unread, and the way out.
			for _, want := range []string{"more line", "n no", "esc stop"} {
				if screen := ansi.Strip(m.View()); !strings.Contains(screen, want) {
					t.Errorf("the key line does not show %q at 80 columns:\n%s", want, screen)
				}
			}
		})
	}

	t.Run("n still refuses", func(t *testing.T) {
		m, reply := sizedApprovalModel(t, 80, 24, longLaneBRequest())
		pressRune(t, m, 'n')
		if got := <-reply; got != protocol.ApprovalDeny {
			t.Errorf("n sent %q, want %q", got, protocol.ApprovalDeny)
		}
	})
}

// READING TO THE END, A PAGE AT A TIME, IS WHAT UNLOCKS THE ANSWER -- and the
// end is really the end: the last word of the arguments has been on screen.
func TestReadingAnApprovalToItsEndAllowsTheAnswer(t *testing.T) {
	m, reply := sizedApprovalModel(t, 80, 24, longLaneBRequest())

	sawLast := false
	for i := 0; i < 200 && m.approvalUnread() > 0; i++ {
		m = pressKey(t, m, tea.KeyPgDown)
		sawLast = sawLast || strings.Contains(ansi.Strip(m.View()), "LAST-WORD")
	}
	if m.approvalUnread() != 0 {
		t.Fatalf("paging down never reached the end of the question; %d line(s) unread", m.approvalUnread())
	}
	if !sawLast {
		t.Fatalf("the question counts as read, but its last word was never on screen")
	}
	if screen := ansi.Strip(m.View()); !strings.Contains(screen, "y yes") {
		t.Errorf("the keys are not offered once the question has been read:\n%s", screen)
	}

	pressRune(t, m, 'y')
	select {
	case got := <-reply:
		if got != protocol.ApprovalApprove {
			t.Errorf("y sent %q, want %q", got, protocol.ApprovalApprove)
		}
	default:
		t.Fatalf("y did nothing after the whole question had been shown")
	}
}

// JUMPING PAST THE MIDDLE IS NOT READING IT. A mouse wheel or a resize can put
// the end on screen without the lines between ever having been there.
func TestSkippingTheMiddleOfAnApprovalDoesNotCountAsReadingIt(t *testing.T) {
	m, reply := sizedApprovalModel(t, 80, 24, longLaneBRequest())
	m.viewport.GotoBottom()
	m.noteApprovalShown()

	if m.approvalUnread() == 0 {
		t.Fatalf("jumping to the end marked the whole question as read")
	}
	pressRune(t, m, 'y')
	select {
	case got := <-reply:
		t.Fatalf("y answered %q after a jump to the end", got)
	default:
	}
}

// A SHORT QUESTION IS ANSWERED AS BEFORE: one line, one key.
func TestAShortApprovalNeedsNoScrolling(t *testing.T) {
	m, reply := sizedApprovalModel(t, 80, 24, laneBRequest())
	if n := m.approvalUnread(); n != 0 {
		t.Fatalf("a one-line question reports %d unread line(s)", n)
	}
	pressRune(t, m, 'y')
	if got := <-reply; got != protocol.ApprovalApprove {
		t.Errorf("y sent %q, want %q", got, protocol.ApprovalApprove)
	}
}

// A QUESTION THAT ARRIVES WHILE THE USER IS SCROLLED BACK IS BROUGHT INTO VIEW.
// The view follows the stream only for someone at the bottom, which is right
// for an answer and wrong for a question: the keys would be live under text
// that has nothing to do with what they answer.
func TestAnApprovalArrivingWhileScrolledBackIsBroughtIntoView(t *testing.T) {
	reply := make(chan string, 1)
	m := newChatModel("test", "/w", "/w", nil)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = sized.(chatModel)
	m.state = stateStreaming
	m.streamCh = make(chan tea.Msg, 4)
	for i := 0; i < 60; i++ {
		m.turns = append(m.turns, turn{role: roleUser, text: fmt.Sprintf("earlier question %d", i)})
	}
	m.refreshViewport()
	m.viewport.GotoTop()

	next, _ := m.Update(toolApprovalMsg{req: laneBRequest(), reply: reply})
	m = next.(chatModel)
	if screen := ansi.Strip(m.View()); !strings.Contains(screen, "somebodys-server/read_anything") {
		t.Errorf("the question is not on the screen that asks for its answer:\n%s", screen)
	}
}
