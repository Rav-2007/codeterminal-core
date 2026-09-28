package main

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

// fakeHistoryDaemon answers every HistoryRequest with respond, recording the
// requests. Same handshake as fakeDaemonServingTiers.
type fakeHistoryDaemon struct {
	mu       sync.Mutex
	requests []protocol.HistoryRequest
}

func (f *fakeHistoryDaemon) seen() []protocol.HistoryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.HistoryRequest(nil), f.requests...)
}

func serveHistory(t *testing.T, respond func(protocol.HistoryRequest) protocol.HistoryResponse) *fakeHistoryDaemon {
	t.Helper()
	return serveHistoryWith(t, []string{protocol.FeatureSavedChats}, respond)
}

// serveHistoryWith chooses the handshake's Features: nil plays a daemon from
// before /history.
func serveHistoryWith(t *testing.T, features []string, respond func(protocol.HistoryRequest) protocol.HistoryResponse) *fakeHistoryDaemon {
	t.Helper()
	f := &fakeHistoryDaemon{}
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath := filepath.Join(dir, "daemon.lock")
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
				_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true, Features: features})
				var req protocol.HistoryRequest
				if dec.Decode(&req) != nil {
					return
				}
				f.mu.Lock()
				f.requests = append(f.requests, req)
				f.mu.Unlock()
				_ = enc.Encode(respond(req))
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return f
}

func savedChats() []protocol.HistoryEntry {
	ended := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	return []protocol.HistoryEntry{
		{Current: true, Title: "what I am doing now", Turns: 4, Unsaved: true},
		{ID: "20260928T100000Z-0000aaaa", Title: "add a --verbose flag", LastPrompt: "now document it",
			Ended: ended, Turns: 12, Incomplete: protocol.IncompleteUserCancelled,
			Spec: "specs/verbose.md", SpecOpen: 2, SpecTotal: 5},
		{ID: "20260927T100000Z-0000bbbb", Title: "fix the parser", Ended: ended, Turns: 6},
	}
}

func lastNote(t *testing.T, m tea.Model) turn {
	t.Helper()
	cm := m.(chatModel)
	if len(cm.turns) == 0 {
		t.Fatal("nothing was added to the transcript")
	}
	return cm.turns[len(cm.turns)-1]
}

func TestHistory_ListShowsSavedChatsAndWhatWasLeftHalfDone(t *testing.T) {
	serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Entries: savedChats()}
	})
	got, _ := newTestModel().handleHistoryCommand("")
	note := lastNote(t, got)
	for _, want := range []string{
		`*    now`, `"what I am doing now"`, `(not saved -- /history save keeps it)`,
		`1    2h ago`, `"add a --verbose flag"`, "\n       ⚠ you stopped it mid-turn · spec specs/verbose.md: 2 of 5 open",
		`last: "now document it"`,
		`2`, `"fix the parser"`,
	} {
		if !strings.Contains(note.text, want) {
			t.Errorf("the list is missing %q:\n%s", want, note.text)
		}
	}
	if strings.Contains(strings.SplitN(note.text, "fix the parser", 2)[1], "⚠") {
		t.Errorf("a finished chat was marked half done:\n%s", note.text)
	}
	// A list of old chats must not be sent to the model with the next prompt.
	if note.role != roleSystem {
		t.Errorf("the list was added as role %v, want roleSystem (buildHistory skips it)", note.role)
	}
	for _, h := range buildHistory(got.(chatModel).turns) {
		if strings.Contains(h.Content, "fix the parser") {
			t.Errorf("the list rode into the model's history: %+v", h)
		}
	}
}

func TestHistory_ResumeUsesTheNumberTheListShowed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "specs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "specs", "verbose.md"), []byte("# v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // saveActiveSpec writes here
	resumed := []protocol.Turn{
		{Role: "user", Content: "add a --verbose flag"}, {Role: "assistant", Content: "added"},
		{Role: "user", Content: "now document it"}, {Role: "assistant", Content: "halfway"},
	}
	f := serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		if req.Action == protocol.HistoryResume {
			e := savedChats()[1]
			e.Current, e.ID = true, ""
			return protocol.HistoryResponse{Entry: &e, Turns: resumed}
		}
		entries := savedChats()
		entries[0].Unsaved, entries[0].SavedAs = false, entries[2].ID // the chat on screen is saved
		return protocol.HistoryResponse{Entries: entries}
	})

	m := newTestModelWithRoot(root)
	m.appendTurn(turn{role: roleUser, text: "what I am doing now"})
	listed, _ := m.handleHistoryCommand("")
	got, _ := listed.(chatModel).handleHistoryCommand("resume 1")
	cm := got.(chatModel)

	reqs := f.seen()
	if last := reqs[len(reqs)-1]; last.Action != protocol.HistoryResume || last.ID != "20260928T100000Z-0000aaaa" || !last.Chats {
		t.Fatalf("resume sent %+v, want saved chat 1's id", last)
	}
	if len(cm.turns) != 5 || cm.turns[0].text != "add a --verbose flag" || cm.turns[3].text != "halfway" {
		t.Fatalf("the transcript is %+v, want the resumed chat then one note", cm.turns)
	}
	note := cm.turns[4].text
	for _, want := range []string{`you left off at: "now document it"`, "you stopped it mid-turn", "active spec: specs/verbose.md", "2 of 5", "/history save updates"} {
		if !strings.Contains(note, want) {
			t.Errorf("the resume note is missing %q:\n%s", want, note)
		}
	}
	if cm.activeSpec != "specs/verbose.md" {
		t.Errorf("activeSpec = %q, want the resumed chat's spec", cm.activeSpec)
	}
	if len(cm.promptHistory) != 2 || cm.promptHistory[1] != "now document it" {
		t.Errorf("↑↓ recalls %q, want the resumed chat's prompts", cm.promptHistory)
	}
	if cm.chatIDs != nil {
		t.Errorf("the list's numbers survived a resume that moved them: %q", cm.chatIDs)
	}
}

// Everything a saved chat carries is text a model or an old session wrote:
// it reaches the screen sanitized.
func TestHistory_TextFromSavedChatsIsSanitized(t *testing.T) {
	hostile := "evil\x1b]52;c;cm0gLXJmIH4=\x07title\x1b[2J"
	serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		e := protocol.HistoryEntry{ID: "20260928T100000Z-0000aaaa", Title: hostile, LastPrompt: hostile, Turns: 2}
		if req.Action == protocol.HistoryShow {
			return protocol.HistoryResponse{Entry: &e, Turns: []protocol.Turn{{Role: "user", Content: hostile}, {Role: "assistant", Content: hostile}}}
		}
		if req.Action == protocol.HistoryResume {
			return protocol.HistoryResponse{Entry: &e, Turns: []protocol.Turn{{Role: "user", Content: hostile}, {Role: "assistant", Content: hostile}}}
		}
		return protocol.HistoryResponse{Entries: []protocol.HistoryEntry{e}}
	})
	m := newTestModel()
	for _, cmd := range []string{"", "1", "resume 1"} {
		got, _ := m.handleHistoryCommand(cmd)
		for _, tn := range got.(chatModel).turns {
			if strings.ContainsAny(tn.text, "\x1b\x07") {
				t.Errorf("/history %s put a control sequence on screen: %q", cmd, tn.text)
			}
		}
	}
}

func TestHistory_ANumberWithNoChatSaysSo(t *testing.T) {
	f := serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Entries: savedChats()}
	})
	for _, cmd := range []string{"resume 9", "9", "delete 9"} {
		got, _ := newTestModel().handleHistoryCommand(cmd)
		if note := lastNote(t, got); !strings.Contains(note.text, "no saved chat 9") {
			t.Errorf("/history %s = %q, want it to say there is no chat 9", cmd, note.text)
		}
	}
	for _, r := range f.seen() {
		if r.Action != protocol.HistoryList {
			t.Errorf("an out-of-range number reached the daemon as %+v", r)
		}
	}
	for _, cmd := range []string{"resume", "resume x", "0", "frobnicate 1 2"} {
		got, _ := newTestModel().handleHistoryCommand(cmd)
		if note := lastNote(t, got); !strings.Contains(note.text, "usage") && !strings.Contains(note.text, "no saved chat") {
			t.Errorf("/history %s = %q, want usage", cmd, note.text)
		}
	}
}

func TestHistory_ResumeWaitsForTheTurnInFlight(t *testing.T) {
	f := serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Entries: savedChats()}
	})
	m := newTestModel()
	m.state = stateStreaming
	got, _ := m.handleHistoryCommand("resume 1")
	if note := lastNote(t, got); !strings.Contains(note.text, "wait for this turn") {
		t.Errorf("resume during a turn = %q", note.text)
	}
	if len(f.seen()) != 0 {
		t.Errorf("resume during a turn reached the daemon: %+v", f.seen())
	}
}

// A daemon started before /history answers a history request as an empty
// prompt. The client must say "restart it", not show that confusion.
func TestHistory_AnOlderDaemonIsToldApart(t *testing.T) {
	f := serveHistoryWith(t, nil, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Error: "prompt is empty"}
	})
	got, _ := newTestModel().handleHistoryCommand("")
	note := lastNote(t, got)
	if !strings.Contains(note.text, "older than /history") || !strings.Contains(note.text, "./run-tui.sh --stop") {
		t.Errorf("/history against an older daemon = %q, want it to say restart it", note.text)
	}
	if len(f.seen()) != 0 {
		t.Errorf("a history request was sent to a daemon that cannot answer it: %+v", f.seen())
	}
}

func TestHistory_DaemonRefusalIsShown(t *testing.T) {
	serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Error: "conversation memory is not available, so there is no history"}
	})
	got, _ := newTestModel().handleHistoryCommand("")
	if note := lastNote(t, got); !strings.Contains(note.text, "memory is not available") {
		t.Errorf("the daemon's refusal was not shown: %q", note.text)
	}
}

// Only what the user saves is kept, so ctrl+n on an UNSAVED chat warns once:
// the first press only arms, any other key disarms, a second press discards.
func TestCtrlN_WarnsOnceBeforeDiscardingAnUnsavedChat(t *testing.T) {
	serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Entries: savedChats()} // current: Unsaved
	})
	m := newTestModel()
	m.appendTurn(turn{role: roleUser, text: "q"})
	m.appendTurn(turn{role: roleAssistant, text: "a"})

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	cm := got.(chatModel)
	if len(cm.turns) != 2 || !cm.newChatArmed {
		t.Fatalf("the first ctrl+n discarded an unsaved chat (turns %d, armed %v)", len(cm.turns), cm.newChatArmed)
	}
	if !strings.Contains(cm.View(), "chat not saved: ctrl+n again discards it") || !strings.Contains(cm.View(), "/history save keeps it") {
		t.Errorf("the footer does not say the chat is unsaved and how to keep it:\n%s", cm.View())
	}

	// Any other key cancels: the next ctrl+n warns again rather than discarding.
	got, _ = cm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	cm = got.(chatModel)
	if cm.newChatArmed {
		t.Fatal("another key did not disarm ctrl+n")
	}
	got, _ = cm.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	cm = got.(chatModel)
	if len(cm.turns) != 2 {
		t.Fatal("a ctrl+n after a cancelled warning discarded the chat")
	}
	got, _ = cm.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if n := len(got.(chatModel).turns); n != 0 {
		t.Errorf("the second consecutive ctrl+n left %d turns, want the chat discarded", n)
	}
}

func TestCtrlN_ASavedChatClearsAtOnce(t *testing.T) {
	serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		e := savedChats()
		e[0].Unsaved, e[0].SavedAs = false, e[1].ID
		return protocol.HistoryResponse{Entries: e}
	})
	m := newTestModel()
	m.appendTurn(turn{role: roleUser, text: "q"})
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if cm := got.(chatModel); len(cm.turns) != 0 || cm.newChatArmed {
		t.Errorf("ctrl+n on a saved chat did not clear at once (turns %d, armed %v)", len(cm.turns), cm.newChatArmed)
	}
}

// With no daemon to ask, a chat with a question in it counts as unsaved; one
// with only command output on screen has nothing to lose and clears at once.
func TestCtrlN_WithoutADaemonGuessesOnTheSafeSide(t *testing.T) {
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	asked := newTestModel()
	asked.appendTurn(turn{role: roleUser, text: "q"})
	if got, _ := asked.Update(tea.KeyMsg{Type: tea.KeyCtrlN}); !got.(chatModel).newChatArmed {
		t.Error("a chat with a question was discarded on one ctrl+n with no daemon to ask")
	}
	local := newTestModel()
	local.appendTurn(turn{role: roleAssistant, text: formatSlashHelp()})
	if got, _ := local.Update(tea.KeyMsg{Type: tea.KeyCtrlN}); got.(chatModel).newChatArmed || len(got.(chatModel).turns) != 0 {
		t.Error("only command output on screen still asked before clearing")
	}
}

func TestHistory_SaveSendsTheNameAndSaysWhatWasSaved(t *testing.T) {
	f := serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Entry: &protocol.HistoryEntry{ID: "20260928T100000Z-0000aaaa", Title: "verbose flag", Turns: 4}, Pruned: 2}
	})
	m := newTestModel()
	m.activeSpec = "specs/verbose.md"
	got, _ := m.handleHistoryCommand("save   verbose flag ")
	note := lastNote(t, got).text
	reqs := f.seen()
	if len(reqs) != 1 || reqs[0].Action != protocol.HistorySave || reqs[0].Name != "verbose flag" || reqs[0].Spec != "specs/verbose.md" {
		t.Fatalf("save sent %+v, want the name and the active spec", reqs)
	}
	if !strings.Contains(note, `saved "verbose flag" (4 turns)`) || !strings.Contains(note, "oldest 2 saved chat") {
		t.Errorf("the save note = %q, want what was saved and what was pruned", note)
	}
	if _, _ = newTestModel().handleHistoryCommand("save"); f.seen()[1].Name != "" {
		t.Errorf("a bare /history save sent a name: %+v", f.seen()[1])
	}
}

// Resuming REPLACES the chat on screen: when that chat is not saved, the first
// resume only says so, and the same command again goes ahead.
func TestHistory_ResumeAsksFirstWhenTheChatOnScreenIsUnsaved(t *testing.T) {
	f := serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		if req.Action == protocol.HistoryResume {
			e := savedChats()[1]
			return protocol.HistoryResponse{Entry: &e, Turns: []protocol.Turn{{Role: "user", Content: "old"}, {Role: "assistant", Content: "chat"}}}
		}
		return protocol.HistoryResponse{Entries: savedChats()} // current: Unsaved
	})
	m := newTestModel()
	m.appendTurn(turn{role: roleUser, text: "unsaved work"})
	first, _ := m.handleHistoryCommand("resume 1")
	if note := lastNote(t, first).text; !strings.Contains(note, "not saved") || !strings.Contains(note, "/history resume 1 again") {
		t.Errorf("the first resume over an unsaved chat = %q, want a warning", note)
	}
	for _, r := range f.seen() {
		if r.Action == protocol.HistoryResume {
			t.Fatal("the first resume replaced an unsaved chat without asking")
		}
	}
	second, _ := first.(chatModel).handleHistoryCommand("resume 1")
	if cm := second.(chatModel); cm.turns[0].text != "old" {
		t.Errorf("the second resume did not go ahead: %+v", cm.turns)
	}
}

// The popup in the screenshot listed /model twice: the catalog gained /model
// and a hand-appended copy was never removed.
func TestSlashPopup_ListsEachCommandOnce(t *testing.T) {
	for _, typed := range []string{"/", "/m", "/mo", "/h", "/hi"} {
		m := newTestModel()
		m.input.SetValue(typed)
		seen := map[string]bool{}
		for _, d := range m.slashMatches() {
			if seen[d.Name] {
				t.Errorf("typing %q lists /%s twice", typed, d.Name)
			}
			seen[d.Name] = true
		}
		if typed == "/hi" && !seen["history"] {
			t.Errorf("typing /hi does not offer /history")
		}
	}
}
