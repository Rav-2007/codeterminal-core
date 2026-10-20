package tui

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"

	tea "github.com/charmbracelet/bubbletea"
)

// /clear STARTS A NEW CHAT ON THE DAEMON TOO (handleClearCommand), and a launch
// says what chat it is continuing (continuedChatNote). Both come from one
// report, 2026-10-11: /clear emptied the screen and told the daemon nothing, so
// /save wrote every turn since the last ctrl+n.

// fakeChatDaemon plays the three things /clear asks a daemon: "is the current
// chat saved?" (a history list), "save it" and "start a new one" (a reset).
// Like the real one, it has no current chat once it has been reset, and a chat
// it saved is no longer unsaved.
type fakeChatDaemon struct {
	mu      sync.Mutex
	current *protocol.HistoryEntry
	lists   int
	saves   int
	resets  int
	// saveFails makes every save answer with this error.
	saveFails string
	// order is every save and reset, in the order they arrived.
	order []string
}

func (f *fakeChatDaemon) saved() (saves int, order []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saves, append([]string(nil), f.order...)
}

func (f *fakeChatDaemon) counts() (lists, resets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists, f.resets
}

func serveChatDaemon(t *testing.T, current *protocol.HistoryEntry) *fakeChatDaemon {
	t.Helper()
	f := &fakeChatDaemon{current: current}
	addr := testAddress(t)
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
				var hs protocol.HandshakeRequest
				if dec.Decode(&hs) != nil {
					return
				}
				_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true,
					Features: []string{protocol.FeatureSavedChats}})
				var raw map[string]json.RawMessage
				if dec.Decode(&raw) != nil {
					return
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if _, isHistory := raw["chats"]; isHistory && string(raw["action"]) == `"`+protocol.HistorySave+`"` {
					f.saves++
					f.order = append(f.order, "save")
					if f.saveFails != "" {
						_ = enc.Encode(protocol.HistoryResponse{Error: f.saveFails})
						return
					}
					var entry protocol.HistoryEntry
					if f.current != nil {
						f.current.Unsaved, f.current.SavedAs = false, "20261012T100000Z-0000aaaa"
						entry = *f.current
					}
					_ = enc.Encode(protocol.HistoryResponse{Entry: &entry})
					return
				}
				if _, isHistory := raw["chats"]; isHistory {
					f.lists++
					var resp protocol.HistoryResponse
					if f.current != nil {
						resp.Entries = []protocol.HistoryEntry{*f.current}
					}
					_ = enc.Encode(resp)
					return
				}
				if string(raw["reset"]) == "true" {
					f.resets++
					f.order = append(f.order, "reset")
					f.current = nil
				}
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return f
}

func askedAndAnswered() chatModel {
	m := newTestModel()
	m.appendTurn(turn{role: roleUser, text: "q"})
	m.appendTurn(turn{role: roleAssistant, text: "a"})
	return m
}

// runClear types nothing: it is the command itself, and the Cmd it returns is
// the request to the daemon, if it made one.
func runClear(t *testing.T, m chatModel) (chatModel, tea.Cmd) {
	t.Helper()
	got, cmd := m.handleLocalSlash("clear", "")
	return got.(chatModel), cmd
}

// answer presses one key at the "Save or clear?" question.
func answer(m chatModel, key string) (chatModel, tea.Cmd) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if key == "esc" {
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	}
	got, cmd := m.Update(msg)
	return got.(chatModel), cmd
}

// THE REPORT THIS ANSWERS (OWNER, 2026-10-12): "/clear is showing something
// complex… it should only ask: this chat didn't saved, confirm: save or clear?"
// /clear on an unsaved chat asks exactly that, in the prompt's box, and writes
// nothing into the transcript. "c" clears -- on the daemon too, which was the
// bug of the day before.
//
// Neuter check: make handleClearCommand call clearConversation unconditionally
// -- the chat is gone on the first /clear with nothing asked.
func TestClear_AnUnsavedChatIsAskedSaveOrClear(t *testing.T) {
	f := serveChatDaemon(t, &protocol.HistoryEntry{Current: true, Title: "q", Turns: 2, Unsaved: true})
	m, cmd := runClear(t, askedAndAnswered())
	if !m.newChatArmed || len(m.turns) != 2 || cmd != nil {
		t.Fatalf("/clear on an unsaved chat: asked=%v, %d turns, sent=%v; want the question, the chat kept, nothing sent",
			m.newChatArmed, len(m.turns), cmd != nil)
	}
	screen := plainScreen(m)
	for _, want := range []string{newChatQuestion, "s save it, then a new chat", "c clear without saving", "esc cancel"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen does not say %q:\n%s", want, screen)
		}
	}
	// The question is where the prompt was, in its box -- not a line of chat.
	rows := bottomRows(m, 5)
	if !strings.HasPrefix(rows[1], "│ "+newChatQuestion) || !strings.HasPrefix(rows[0], "╭") {
		t.Errorf("the question is not in the prompt's box:\n%s", strings.Join(rows, "\n"))
	}
	for _, old := range []string{"/clear again", "/save keeps it"} {
		if strings.Contains(screen, old) {
			t.Errorf("the screen still says %q:\n%s", old, screen)
		}
	}

	m, cmd = answer(m, "c")
	if len(m.turns) != 0 || m.newChatArmed {
		t.Fatalf("after c: %d turns, asked=%v; want an empty new chat", len(m.turns), m.newChatArmed)
	}
	if h := buildHistory(m.turns); len(h) != 0 {
		t.Errorf("the next prompt would still send %d turns of the cleared chat", len(h))
	}
	if cmd == nil {
		t.Fatal("clearing sent the daemon nothing -- the stored chat is still there for /save to write")
	}
	if msg := cmd(); msg != (resetOkMsg{}) {
		t.Fatalf("the reset = %#v, want it accepted", msg)
	}
	if saves, _ := f.saved(); saves != 0 {
		t.Errorf("clear without saving saved the chat %d time(s)", saves)
	}
	if _, resets := f.counts(); resets != 1 {
		t.Errorf("the daemon was reset %d times, want once", resets)
	}
}

// "s" SAVES FIRST AND CLEARS SECOND, and the new chat's one line says the old
// one is kept and how to get it back.
//
// Neuter check: swap the two in saveThenClear, or drop the save -- the order
// recorded by the daemon is reset-then-save, or just a reset.
func TestClear_SaveSavesTheChatThenStartsANewOne(t *testing.T) {
	f := serveChatDaemon(t, &protocol.HistoryEntry{Current: true, Title: "how do I ship this", Turns: 2, Unsaved: true})
	m, _ := runClear(t, askedAndAnswered())
	m, cmd := answer(m, "s")

	if m.newChatArmed || len(m.turns) != 1 || m.turns[0].role != roleSystem {
		t.Fatalf("after s: asked=%v, turns %+v; want a new chat holding one note", m.newChatArmed, m.turns)
	}
	note := m.turns[0].text
	for _, want := range []string{`saved "how do I ship this" (2 turns)`, "a new chat starts here", "/resume"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say %q: %q", want, note)
		}
	}
	if h := buildHistory(m.turns); len(h) != 0 {
		t.Errorf("the note about the save would be sent to the model (%d turns)", len(h))
	}
	if cmd == nil {
		t.Fatal("the chat was saved and the daemon was never told to start a new one")
	}
	cmd()
	if saves, order := f.saved(); saves != 1 || strings.Join(order, ",") != "save,reset" {
		t.Errorf("the daemon saw %v; want one save, then the reset", order)
	}
}

// A SAVE THAT FAILED CLEARS NOTHING. The user said "save" so as not to lose
// the chat; losing it because the daemon said no would be the one thing they
// declined.
func TestClear_AFailedSaveKeepsTheChat(t *testing.T) {
	f := serveChatDaemon(t, &protocol.HistoryEntry{Current: true, Title: "q", Turns: 2, Unsaved: true})
	f.saveFails = "the disk is full"
	m, _ := runClear(t, askedAndAnswered())
	m, cmd := answer(m, "s")

	if cmd != nil {
		t.Fatal("a failed save still told the daemon to start a new chat")
	}
	if len(m.turns) != 3 || m.turns[0].text != "q" {
		t.Fatalf("a failed save changed the chat: %+v", m.turns)
	}
	if last := m.turns[2].text; !strings.Contains(last, "could not be saved, so it was not cleared") || !strings.Contains(last, "the disk is full") {
		t.Errorf("the failure is not said, or not why: %q", last)
	}
	if _, order := f.saved(); strings.Join(order, ",") != "save" {
		t.Errorf("the daemon saw %v; want the one save and no reset", order)
	}
}

// Esc keeps the chat. So does any other key -- and that key is then an
// ordinary key, typed at the prompt: "/clear" typed again must arrive whole,
// not as "clear" with its slash eaten and sent to the model as a question.
//
// Neuter check: return `m, nil` for every other key in answerNewChatQuestion
// -- the input holds "clear" and the last assertion fails.
func TestClear_AnyOtherKeyKeepsTheChatAndIsTyped(t *testing.T) {
	serveChatDaemon(t, &protocol.HistoryEntry{Current: true, Title: "q", Turns: 2, Unsaved: true})
	asked, _ := runClear(t, askedAndAnswered())

	m, cmd := answer(asked, "esc")
	if m.newChatArmed || len(m.turns) != 2 || cmd != nil || m.input.Value() != "" {
		t.Errorf("esc: asked=%v, %d turns, sent=%v, input %q; want the chat as it was", m.newChatArmed, len(m.turns), cmd != nil, m.input.Value())
	}
	if strings.Contains(plainScreen(m), newChatQuestion) {
		t.Error("the question is still on screen after esc")
	}

	m = typeText(asked, "/clear")
	if m.newChatArmed || len(m.turns) != 2 {
		t.Fatalf("typing took the chat away (asked=%v, %d turns)", m.newChatArmed, len(m.turns))
	}
	if got := m.input.Value(); got != "/clear" {
		t.Errorf("the prompt holds %q, want the whole of what was typed", got)
	}
	// And the command, entered, asks again rather than clearing.
	m, _ = pressEnter(m)
	if !m.newChatArmed || len(m.turns) != 2 {
		t.Errorf("/clear after a cancelled question: asked=%v, %d turns; want the question again", m.newChatArmed, len(m.turns))
	}
}

// Nothing to lose, nothing to ask: a chat that is saved, and a screen holding
// no chat at all, clear on the first /clear -- and still reset the daemon.
func TestClear_ASavedChatOrNoChatClearsAtOnce(t *testing.T) {
	for name, current := range map[string]*protocol.HistoryEntry{
		"saved":   {Current: true, Title: "q", Turns: 2, SavedAs: "20260928T100000Z-0000aaaa"},
		"no chat": nil,
	} {
		f := serveChatDaemon(t, current)
		m, cmd := runClear(t, askedAndAnswered())
		if len(m.turns) != 0 || cmd == nil {
			t.Errorf("%s: /clear left %d turns (reset sent: %v), want it cleared at once", name, len(m.turns), cmd != nil)
			continue
		}
		cmd()
		if _, resets := f.counts(); resets != 1 {
			t.Errorf("%s: the daemon was reset %d times, want once", name, resets)
		}
	}
}

func TestClear_WaitsForTheTurnInFlight(t *testing.T) {
	f := serveChatDaemon(t, &protocol.HistoryEntry{Current: true, Title: "q", Turns: 2, Unsaved: true})
	m := askedAndAnswered()
	m.state = stateStreaming
	m, cmd := runClear(t, m)
	if cmd != nil || len(m.turns) != 3 || !strings.Contains(m.turns[2].text, "wait for this turn") {
		t.Errorf("/clear during a turn = %+v (cmd %v), want it refused", m.turns, cmd != nil)
	}
	if lists, resets := f.counts(); lists != 0 || resets != 0 {
		t.Errorf("/clear during a turn reached the daemon (%d lists, %d resets)", lists, resets)
	}
}

// ctrl+n and /clear are one action and ask ONE question: a ctrl+n while /clear's
// question is showing is the second ctrl+n it has always taken to discard.
func TestClear_CtrlNAndClearAskTheSameQuestion(t *testing.T) {
	serveChatDaemon(t, &protocol.HistoryEntry{Current: true, Title: "q", Turns: 2, Unsaved: true})
	asked, _ := runClear(t, askedAndAnswered())
	got, cmd := asked.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m := got.(chatModel)
	if len(m.turns) != 0 || cmd == nil || m.newChatArmed {
		t.Errorf("ctrl+n after /clear asked = %d turns, reset %v, asked %v; want one new chat and nothing left waiting",
			len(m.turns), cmd != nil, m.newChatArmed)
	}

	// And the other way round: ctrl+n asks, and the same two keys answer it.
	byKey, _ := askedAndAnswered().Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if view := plainScreen(byKey.(chatModel)); !strings.Contains(view, newChatQuestion) {
		t.Fatalf("ctrl+n on an unsaved chat does not ask the question:\n%s", view)
	}
	if cleared, cmd := answer(byKey.(chatModel), "c"); len(cleared.turns) != 0 || cmd == nil {
		t.Errorf("c after ctrl+n left %d turns (reset %v)", len(cleared.turns), cmd != nil)
	}
}

// The command list says what it does now, and so does the key beside it.
func TestClear_HelpSaysItStartsANewChat(t *testing.T) {
	d := lookupSlash("clear")
	if d == nil || !strings.Contains(d.Summary, "new chat") || strings.Contains(d.Summary, "on-screen") {
		t.Errorf("/clear's summary = %+v, want it to say a new chat is started", d)
	}
	if help := formatSlashHelp(); !strings.Contains(help, "same as /clear") {
		t.Errorf("/help does not tie ctrl+n to /clear:\n%s", help)
	}
}

// --- the line a launch opens on ---

func TestContinuedChat_SaysHowMuchThereIsAndHowMuchIsShown(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local)
	started := func(daysAgo int) string {
		return now.AddDate(0, 0, -daysAgo).UTC().Format(time.RFC3339Nano)
	}
	for _, tc := range []struct {
		name          string
		e             protocol.HistoryEntry
		shown         int
		want, wantNot []string
	}{
		{"a long chat, mostly off screen, changed since its save",
			protocol.HistoryEntry{Current: true, Turns: 126, Started: started(15), SavedAs: "x", Unsaved: true}, 12,
			[]string{"from 26 Sep", "126 turns", "the last 12 shown", "/clear starts a new chat", "changed since it was saved", "/save updates it"}, nil},
		{"a short chat from today, all on screen, never saved",
			protocol.HistoryEntry{Current: true, Turns: 4, Started: started(0), Unsaved: true}, 4,
			[]string{"from earlier today", "4 turns", "not saved", "/save keeps it"}, []string{"shown"}},
		{"a saved chat from yesterday",
			protocol.HistoryEntry{Current: true, Turns: 6, Started: started(1), SavedAs: "x"}, 6,
			[]string{"from yesterday", "this one is saved"}, []string{"/save"}},
		{"a chat from another year carries the year",
			protocol.HistoryEntry{Current: true, Turns: 2, Started: started(400)}, 2,
			[]string{"2025"}, nil},
		{"a time that is not a time is left out, not shown",
			protocol.HistoryEntry{Current: true, Turns: 2, Started: "not a time"}, 2,
			[]string{"continuing your chat: 2 turns"}, []string{"from", "not a time"}},
	} {
		got := formatContinuedChat(tc.e, tc.shown, now)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", tc.name, got, w)
			}
		}
		for _, w := range tc.wantNot {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q should not say %q", tc.name, got, w)
			}
		}
	}
}

func TestContinuedChat_IsAskedOfTheDaemonAndNeverSentToTheModel(t *testing.T) {
	f := serveChatDaemon(t, &protocol.HistoryEntry{Current: true, Turns: 40, Unsaved: true,
		Started: time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339Nano)})
	m := askedAndAnswered()
	note := m.continuedChatNote(2, time.Now())
	if !strings.Contains(note, "40 turns") || !strings.Contains(note, "the last 2 shown") {
		t.Fatalf("the launch line = %q", note)
	}
	if lists, _ := f.counts(); lists != 1 {
		t.Errorf("the daemon was asked %d times, want once", lists)
	}
	m.appendTurn(turn{role: roleSystem, text: note})
	for _, h := range buildHistory(m.turns) {
		if strings.Contains(h.Content, "continuing your chat") {
			t.Errorf("the launch line would be sent to the model: %+v", h)
		}
	}
}

// No chat, or nobody to ask: a launch says nothing rather than guessing.
func TestContinuedChat_SaysNothingWhenThereIsNothingToSay(t *testing.T) {
	serveChatDaemon(t, nil)
	m := newTestModel()
	if note := m.continuedChatNote(0, time.Now()); note != "" {
		t.Errorf("with no current chat the launch line = %q", note)
	}
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	if note := m.continuedChatNote(4, time.Now()); note != "" {
		t.Errorf("with no daemon the launch line = %q", note)
	}
}

// THE CHAT THE QUESTION IS ABOUT DOES NOT CHANGE WHILE IT IS ASKED. Another
// Mochiii window on the same workspace can add to the shared chat, and this
// client catches up whenever its terminal regains focus (chatsync.go). With
// "Save or clear?" on screen that would put messages the user has not seen
// into the chat their next key saves or throws away.
//
// Neuter check: drop the newChatArmed case from canCatchUp.
func TestTheSharedChatIsNotPulledInUnderTheSaveOrClearQuestion(t *testing.T) {
	m := newTestModel()
	m.state, m.chatSync, m.chatRev = stateIdle, true, "r1"
	if !m.canCatchUp() {
		t.Fatal("setup: an idle client with a shared chat does not catch up at all")
	}

	m.newChatArmed = true
	if m.canCatchUp() {
		t.Error("the transcript may be changed while the question is waiting for its answer")
	}
	before := len(m.turns)
	next, _ := m.handleChatCaughtUp(chatCaughtUpMsg{since: "r1", resp: protocol.HistoryResponse{
		ChatRevision: "r2", Append: true, Turns: []protocol.Turn{{Role: "user", Content: "from the other window"}},
	}})
	after := next.(chatModel)
	if len(after.turns) != before || after.chatRev != "r1" {
		t.Errorf("an answer that arrived under the question was applied: %d turns (was %d), revision %q", len(after.turns), before, after.chatRev)
	}

	// Answered or cancelled, the next focus brings the chat level again.
	after.newChatArmed = false
	if !after.canCatchUp() {
		t.Error("catching up stays off after the question is gone")
	}
}
