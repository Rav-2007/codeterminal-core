package tui

import (
	"strings"
	"testing"

	"mochiii/protocol"

	tea "github.com/charmbracelet/bubbletea"
)

// LEAVING WITH A CHAT THAT IS NOT SAVED ASKS FIRST (quitchat.go). The owner,
// 2026-11-14: "a caution message must arise before the user closes the
// terminal; if the user didn't save, the session will be removed."

func ctrlC(m chatModel) (chatModel, tea.Cmd) {
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	return got.(chatModel), cmd
}

func slashExit(m chatModel) (chatModel, tea.Cmd) {
	got, cmd := m.handleLocalSlash("exit", "")
	return got.(chatModel), cmd
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, quitting := cmd().(tea.QuitMsg)
	return quitting
}

func unsavedChat() *protocol.HistoryEntry {
	return &protocol.HistoryEntry{Current: true, Title: "q", Turns: 2, Unsaved: true}
}

// The first ctrl+c on a chat that is not saved asks, in the prompt's box, and
// does not quit.
//
// Neuter check: make requestQuit return ok=false always -- ctrl+c only arms the
// footer's "press ctrl+c again", and the chat is erased on the second press
// without the user having been told.
func TestQuit_AnUnsavedChatIsAskedBeforeLeaving(t *testing.T) {
	f := serveChatDaemon(t, unsavedChat())
	m, cmd := ctrlC(askedAndAnswered())
	if !m.quitChatArmed || cmd != nil {
		t.Fatalf("ctrl+c on an unsaved chat: asked=%v, cmd=%v; want the question and nothing sent", m.quitChatArmed, cmd != nil)
	}
	if m.quitArmed {
		t.Error("the footer's own quit is armed as well as the question")
	}
	screen := plainScreen(m)
	for _, want := range []string{quitChatQuestion, "s save it, then quit", "q quit, erasing it", "esc stay"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen does not say %q:\n%s", want, screen)
		}
	}
	rows := bottomRows(m, 5)
	if !strings.HasPrefix(rows[1], "│ "+quitChatQuestion) || !strings.HasPrefix(rows[0], "╭") {
		t.Errorf("the question is not in the prompt's box:\n%s", strings.Join(rows, "\n"))
	}
	if len(m.turns) != 2 {
		t.Errorf("asking changed the chat: %d turns, want 2", len(m.turns))
	}
	if _, resets := f.counts(); resets != 0 {
		t.Errorf("the chat was erased by the question alone (%d resets)", resets)
	}
}

// "q" leaves, and the stored chat is erased on the way out.
//
// Neuter check: make quitNow ignore erase -- the program quits and the daemon
// is never reset, so the chat waits on disk for the next launch.
func TestQuit_QErasesTheChatAndQuits(t *testing.T) {
	f := serveChatDaemon(t, unsavedChat())
	m, _ := ctrlC(askedAndAnswered())
	_, cmd := answer(m, "q")
	if !isQuit(cmd) {
		t.Fatal("q did not quit")
	}
	if saves, _ := f.saved(); saves != 0 {
		t.Errorf("quit without saving saved the chat %d time(s)", saves)
	}
	if _, resets := f.counts(); resets != 1 {
		t.Errorf("the daemon was reset %d times, want once", resets)
	}
}

// "s" saves FIRST and erases second; the saved copy is what stays.
//
// Neuter check: swap the order in saveThenQuit, or drop the save -- the order
// the daemon records is reset-then-save, or just a reset.
func TestQuit_SSavesThenLeaves(t *testing.T) {
	f := serveChatDaemon(t, unsavedChat())
	m, _ := ctrlC(askedAndAnswered())
	_, cmd := answer(m, "s")
	if !isQuit(cmd) {
		t.Fatal("s did not quit")
	}
	if saves, order := f.saved(); saves != 1 || strings.Join(order, ",") != "save,reset" {
		t.Errorf("saves=%d, order=%v; want one save, then the reset", saves, order)
	}
}

// A SAVE THAT FAILED QUITS NOTHING: the user chose "save" so as not to lose it.
//
// Neuter check: have saveThenQuit ignore the error -- the program quits and
// erases a chat that was never saved.
func TestQuit_AFailedSaveStaysOpen(t *testing.T) {
	f := serveChatDaemon(t, unsavedChat())
	f.saveFails = "disk full"
	m, _ := ctrlC(askedAndAnswered())
	m, cmd := answer(m, "s")
	if cmd != nil || isQuit(cmd) {
		t.Fatal("the program quit although the save failed")
	}
	if _, resets := f.counts(); resets != 0 {
		t.Errorf("the chat was erased (%d resets) although it was not saved", resets)
	}
	last := m.turns[len(m.turns)-1]
	if last.role != roleSystem || !strings.Contains(last.text, "could not be saved") || !strings.Contains(last.text, "disk full") {
		t.Errorf("the note = %+v, want it to say why", last)
	}
	if m.quitChatArmed {
		t.Error("the question is still on screen after the failed save")
	}
}

// esc stays; any other key takes the question away and is then an ordinary key.
func TestQuit_EscStaysAndOtherKeysAreTyped(t *testing.T) {
	f := serveChatDaemon(t, unsavedChat())
	m, _ := ctrlC(askedAndAnswered())
	m, cmd := answer(m, "esc")
	if m.quitChatArmed || cmd != nil || len(m.turns) != 2 {
		t.Fatalf("esc: asked=%v, cmd=%v, %d turns; want the chat as it was", m.quitChatArmed, cmd != nil, len(m.turns))
	}

	m, _ = ctrlC(m)
	m, cmd = answer(m, "z")
	if m.quitChatArmed || isQuit(cmd) {
		t.Fatalf("another key left the question up or quit: asked=%v", m.quitChatArmed)
	}
	if got := m.input.Value(); got != "z" {
		t.Errorf("the key was swallowed: the prompt holds %q, want \"z\"", got)
	}
	if _, resets := f.counts(); resets != 0 {
		t.Errorf("a chat that was kept was erased (%d resets)", resets)
	}
}

// ctrl+c again with the question showing is "quit", and erases what the
// question said it would.
func TestQuit_ASecondCtrlCQuitsAndErases(t *testing.T) {
	f := serveChatDaemon(t, unsavedChat())
	m, _ := ctrlC(askedAndAnswered())
	_, cmd := ctrlC(m)
	if !isQuit(cmd) {
		t.Fatal("the second ctrl+c did not quit")
	}
	if _, resets := f.counts(); resets != 1 {
		t.Errorf("the daemon was reset %d times, want once", resets)
	}
}

// /exit asks too: it used to "quit outright", when quitting kept the chat.
func TestQuit_SlashExitAsksOnAnUnsavedChat(t *testing.T) {
	serveChatDaemon(t, unsavedChat())
	m, cmd := slashExit(askedAndAnswered())
	if !m.quitChatArmed || cmd != nil {
		t.Fatalf("/exit on an unsaved chat: asked=%v, cmd=%v; want the question", m.quitChatArmed, cmd != nil)
	}
}

// A SAVED CHAT, OR NO CHAT, LEAVES AS BEFORE: nothing to lose, nothing asked, and
// nothing erased on the way out (the next launch starts empty anyway).
func TestQuit_ASavedOrEmptyChatLeavesAtOnce(t *testing.T) {
	saved := &protocol.HistoryEntry{Current: true, Turns: 2, SavedAs: "20261114T100000Z-0000aaaa"}
	for name, current := range map[string]*protocol.HistoryEntry{"saved": saved, "none": nil} {
		t.Run(name, func(t *testing.T) {
			f := serveChatDaemon(t, current)
			m, cmd := ctrlC(askedAndAnswered())
			if m.quitChatArmed || !m.quitArmed || cmd != nil {
				t.Fatalf("first ctrl+c: asked=%v, armed=%v; want only the footer's arm", m.quitChatArmed, m.quitArmed)
			}
			if !strings.Contains(m.View(), "press ctrl+c again") {
				t.Error("the footer does not say a second ctrl+c quits")
			}
			_, cmd = ctrlC(m)
			if !isQuit(cmd) {
				t.Fatal("the second ctrl+c did not quit")
			}
			if _, resets := f.counts(); resets != 0 {
				t.Errorf("a chat with nothing to lose was reset on quit (%d)", resets)
			}
			_, cmd = slashExit(askedAndAnswered())
			if !isQuit(cmd) {
				t.Error("/exit did not quit at once")
			}
		})
	}
}

// THE QUESTION IS FOR A PROMPT THAT IS WAITING. In a dialog the key still does
// what it always did there; the next launch erases what it leaves.
func TestQuit_OnlyAsksWhileThePromptWaits(t *testing.T) {
	serveChatDaemon(t, unsavedChat())
	for _, st := range []chatState{stateEditReview, stateConnect, stateSetup} {
		m := askedAndAnswered()
		m.state = st
		got, _ := ctrlC(m)
		if got.quitChatArmed || !got.quitArmed {
			t.Errorf("state %d: asked=%v, armed=%v; want the footer's own quit", st, got.quitChatArmed, got.quitArmed)
		}
	}
	m := askedAndAnswered()
	m.state = stateError
	if got, _ := ctrlC(m); !got.quitChatArmed {
		t.Error("after a failed turn the prompt is waiting, and ctrl+c did not ask")
	}
}

// THE CHAT THE QUESTION IS ABOUT DOES NOT CHANGE WHILE IT IS ASKED, as for the
// "Save or clear?" question (canCatchUp).
//
// Neuter check: drop quitChatArmed from canCatchUp.
func TestQuit_TheSharedChatIsNotPulledInUnderTheQuestion(t *testing.T) {
	m := askedAndAnswered()
	m.chatSync, m.chatRev = true, "r1"
	if !m.canCatchUp() {
		t.Fatal("setup: an idle, synced chat should be able to catch up")
	}
	m.quitChatArmed = true
	if m.canCatchUp() {
		t.Error("the chat can change under the quit question")
	}
}

// Leaving never hangs on a daemon that is not there: the erase fails quietly
// and the program still quits.
func TestQuit_ALostDaemonDoesNotStopTheQuit(t *testing.T) {
	t.Cleanup(setLockPathForTest(t, t.TempDir()+"/absent.lock"))
	if !isQuit(eraseThenQuit("test-client")) {
		t.Fatal("with no daemon the erase did not return a quit")
	}
}
