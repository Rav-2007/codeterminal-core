package main

import (
	"encoding/json"
	"fmt"
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
		`*    now`, `"what I am doing now"`, `(not saved -- /save keeps it)`,
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
	for _, want := range []string{`you left off at: "now document it"`, "you stopped it mid-turn", "active spec: specs/verbose.md", "2 of 5", "/save updates"} {
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
	for _, cmd := range []string{"delete", "resume x", "0", "frobnicate 1 2"} {
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
	if !strings.Contains(cm.View(), "chat not saved: ctrl+n again discards it") || !strings.Contains(cm.View(), "/save keeps it") {
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

// /save [name] is the name people reach for (the owner did, 2026-10-05, after
// reading the whole popup and not finding a way to save). It must do exactly
// what /history save does -- typed at the prompt, not through a helper, because
// the failure being guarded is the command going to the MODEL as a question.
func TestSave_TypedAtThePromptSavesTheChatUnderThatName(t *testing.T) {
	f := serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Entry: &protocol.HistoryEntry{ID: "20260928T100000Z-0000aaaa", Title: req.Name, Turns: 4}}
	})
	for i, tc := range []struct{ typed, name string }{
		{"/save nvidia setup", "nvidia setup"},
		{"/save", ""}, // the name is optional: the daemon titles the chat itself
		{"/SAVE   spaced   out ", "spaced   out"},
	} {
		cm, _ := pressEnter(typeText(newTestModel(), tc.typed))
		if cm.streamCh != nil || cm.state != stateIdle {
			t.Fatalf("%q started a model turn", tc.typed)
		}
		reqs := f.seen()
		if len(reqs) != i+1 || reqs[i].Action != protocol.HistorySave || reqs[i].Name != tc.name {
			t.Fatalf("%q sent %+v, want one save named %q", tc.typed, reqs, tc.name)
		}
		last := cm.turns[len(cm.turns)-1]
		if last.role != roleSystem || !strings.HasPrefix(last.text, "saved") {
			t.Errorf("%q answered %q as %v, want a note that it was saved", tc.typed, last.text, last.role)
		}
	}

	// Mid-turn it waits, as /history save does: a save taken while an answer is
	// still arriving would keep half of it.
	busy := newTestModel()
	busy.state = stateStreaming
	got, _ := busy.handleLocalSlash("save", "too early")
	if note := lastNote(t, got).text; !strings.Contains(note, "wait for this turn to finish") {
		t.Errorf("a save during a turn answered %q, want it to wait", note)
	}
	if len(f.seen()) != 3 {
		t.Errorf("a save during a turn reached the daemon: %+v", f.seen())
	}
}

// namedChats is a project's saved chats as the daemon lists them, newest first:
// the chat on screen (saved as the first), two the user named, one called by
// its first prompt, and one whose name is a number.
func namedChats() []protocol.HistoryEntry {
	ended := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	return []protocol.HistoryEntry{
		{Current: true, Title: "test1", Turns: 86, SavedAs: "20261005T163000Z-00000001"},
		{ID: "20261005T163000Z-00000001", Title: "test1", Ended: ended, Turns: 86},
		{ID: "20261004T100000Z-00000002", Title: "NVIDIA setup", Ended: ended, Turns: 8},
		{ID: "20261003T100000Z-00000003", Title: "add a --verbose flag to the indexer", Ended: ended, Turns: 12},
		{ID: "20261002T100000Z-00000004", Title: "2026", Ended: ended, Turns: 2},
	}
}

// serveNamedChats answers every history request over entries, and says what a
// resume, a read or a delete was asked for.
func serveNamedChats(t *testing.T, entries []protocol.HistoryEntry) *fakeHistoryDaemon {
	t.Helper()
	return serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		if req.Action == protocol.HistoryList {
			return protocol.HistoryResponse{Entries: entries}
		}
		for _, e := range entries {
			if e.ID == req.ID {
				e := e
				return protocol.HistoryResponse{Entry: &e, Turns: []protocol.Turn{
					{Role: "user", Content: "first question of " + e.Title}, {Role: "assistant", Content: "an answer"},
				}}
			}
		}
		return protocol.HistoryResponse{Error: "no such chat"}
	})
}

// actionsOf is every request that was not a list: what the command DID.
func actionsOf(f *fakeHistoryDaemon) []protocol.HistoryRequest {
	var out []protocol.HistoryRequest
	for _, r := range f.seen() {
		if r.Action != protocol.HistoryList {
			out = append(out, r)
		}
	}
	return out
}

// THE OWNER'S FIRST SAVE (2026-10-05): `/save test1`, then
// `/history resume test1` -- answered with the bare usage line, four times
// over. A chat is resumed by the name it was just saved under, typed at the
// prompt exactly as they typed it.
func TestHistory_ResumeTakesTheNameTheChatWasSavedUnder(t *testing.T) {
	for _, tc := range []struct{ typed, id, why string }{
		{"/resume test1", "20261005T163000Z-00000001", "the short command the owner asked for"},
		{"/resume nvidia setup", "20261004T100000Z-00000002", "the short command, a name of two words"},
		{"/resume 2", "20261004T100000Z-00000002", "the short command, by number"},
		{"/RESUME Test1", "20261005T163000Z-00000001", "the short command, however it is cased"},
		{"/history resume test1", "20261005T163000Z-00000001", "the name, as saved"},
		{"/history resume TEST1", "20261005T163000Z-00000001", "case does not count"},
		{`/history resume "test1"`, "20261005T163000Z-00000001", "quoted, as the list shows it"},
		{"/history resume nvidia setup", "20261004T100000Z-00000002", "a name of two words"},
		{"/history resume  nvidia   setup ", "20261004T100000Z-00000002", "spacing does not count"},
		{"/history resume add a --verb", "20261003T100000Z-00000003", "the start of an unnamed chat's first prompt"},
		{"/history resume 2", "20261004T100000Z-00000002", "a number is still the list's row"},
		{"/history resume 2026", "20261002T100000Z-00000004", "a number that is no row, but is a name"},
	} {
		f := serveNamedChats(t, namedChats())
		cm, _ := pressEnter(typeText(newTestModel(), tc.typed))
		did := actionsOf(f)
		if len(did) != 1 || did[0].Action != protocol.HistoryResume || did[0].ID != tc.id {
			t.Errorf("%q (%s) did %+v, want one resume of %s\nnote: %s", tc.typed, tc.why, did, tc.id, cm.turns[len(cm.turns)-1].text)
			continue
		}
		if cm.streamCh != nil {
			t.Errorf("%q started a model turn", tc.typed)
		}
		if len(cm.turns) != 3 || cm.turns[0].role != roleUser || !strings.HasPrefix(cm.turns[2].text, "resumed ") {
			t.Errorf("%q left the transcript as %+v, want the resumed chat and one note", tc.typed, cm.turns)
		}
	}
}

// A name nothing is saved under is SAID to be that -- not answered with a line
// of syntax that leaves the user guessing which part was wrong.
func TestHistory_ANameWithNoChatSaysSo(t *testing.T) {
	for _, tc := range []struct{ typed, want string }{
		{"/history resume nope", `no saved chat named "nope" -- /history lists them`},
		{"/history delete nope", `no saved chat named "nope" -- /history lists them`},
		{"/history nope", `no saved chat named "nope" -- /history lists them`},
		{"/history delete", "usage: /history delete <n>"},
	} {
		f := serveNamedChats(t, namedChats())
		cm, _ := pressEnter(typeText(newTestModel(), tc.typed))
		note := cm.turns[len(cm.turns)-1].text
		if !strings.Contains(note, tc.want) {
			t.Errorf("%q answered %q, want %q", tc.typed, note, tc.want)
		}
		if did := actionsOf(f); len(did) != 0 {
			t.Errorf("%q reached the daemon as %+v", tc.typed, did)
		}
	}

	// A word after /history that is no chat may be a mistyped subcommand, so
	// that one answer carries the usage as well.
	_ = serveNamedChats(t, namedChats())
	cm, _ := pressEnter(typeText(newTestModel(), "/history resum 1"))
	if note := cm.turns[len(cm.turns)-1].text; !strings.Contains(note, `no saved chat named "resum 1"`) || !strings.Contains(note, "usage: /save [name] · /resume <n>") {
		t.Errorf("a mistyped subcommand answered %q, want what was not found and the usage", note)
	}

	// With nothing saved at all, that is the thing to say.
	_ = serveNamedChats(t, namedChats()[:1])
	cm, _ = pressEnter(typeText(newTestModel(), "/history resume test1"))
	if note := cm.turns[len(cm.turns)-1].text; !strings.Contains(note, "no saved chats in this project yet") {
		t.Errorf("resume with nothing saved answered %q", note)
	}
}

// Two chats can carry one name. Neither is picked: both are shown with their
// numbers, and the number then means the row that was shown.
func TestHistory_ANameTwoChatsShareAsksForTheNumber(t *testing.T) {
	entries := namedChats()
	entries[3].Title = "Test1" // a second chat given the first one's name
	f := serveNamedChats(t, entries)

	cm, _ := pressEnter(typeText(newTestModel(), "/history resume test1"))
	note := cm.turns[len(cm.turns)-1].text
	if did := actionsOf(f); len(did) != 0 {
		t.Fatalf("an ambiguous name resumed something: %+v", did)
	}
	for _, want := range []string{`2 saved chats match "test1" -- use the number`, "\n  1 ", "\n  3 ", `"Test1"`} {
		if !strings.Contains(note, want) {
			t.Errorf("the answer is missing %q:\n%s", want, note)
		}
	}
	cm, _ = pressEnter(typeText(cm, "/history resume 3"))
	if did := actionsOf(f); len(did) != 1 || did[0].ID != "20261003T100000Z-00000003" {
		t.Errorf("the number shown beside the second chat resumed %+v", did)
	}

	// The start of a name that fits several chats is the same question; with
	// enough of it to fit one, it is answered.
	entries = namedChats()
	entries[3].Title = "test2 on the parser"
	f = serveNamedChats(t, entries)
	cm, _ = pressEnter(typeText(newTestModel(), "/history resume tes"))
	if did := actionsOf(f); len(did) != 0 {
		t.Fatalf(`"tes" starts two names, yet resumed %+v`, did)
	}
	if note := cm.turns[len(cm.turns)-1].text; !strings.Contains(note, `2 saved chats match "tes"`) {
		t.Errorf("a shared start answered %q", note)
	}
	// "test1" is the WHOLE of one name and only the start of nothing else that
	// matters: a whole name is never made ambiguous by a longer one.
	entries[3].Title = "test10"
	f = serveNamedChats(t, entries)
	if _, _ = pressEnter(typeText(newTestModel(), "/history resume test1")); len(actionsOf(f)) != 1 || actionsOf(f)[0].ID != "20261005T163000Z-00000001" {
		t.Errorf(`"test1" beside "test10" did %+v, want the chat named exactly that`, actionsOf(f))
	}
}

// Reading takes a name as resuming does. Deleting does too -- but only the
// WHOLE name: it is not undone, so it is never done on part of one.
func TestHistory_ReadAndDeleteTakeANameAndDeleteWantsAllOfIt(t *testing.T) {
	f := serveNamedChats(t, namedChats())
	cm, _ := pressEnter(typeText(newTestModel(), "/history nvidia setup"))
	if did := actionsOf(f); len(did) != 1 || did[0].Action != protocol.HistoryShow || did[0].ID != "20261004T100000Z-00000002" {
		t.Fatalf("reading by name did %+v", did)
	}
	if note := cm.turns[len(cm.turns)-1].text; !strings.Contains(note, `saved chat "nvidia setup" -- "NVIDIA setup", 8 turns`) ||
		!strings.Contains(note, "(/resume nvidia setup to continue it)") {
		t.Errorf("the chat read by name is headed %q", note)
	}

	f = serveNamedChats(t, namedChats())
	cm, _ = pressEnter(typeText(newTestModel(), "/history delete add a"))
	if did := actionsOf(f); len(did) != 0 {
		t.Fatalf("part of a name deleted a chat: %+v", did)
	}
	if note := cm.turns[len(cm.turns)-1].text; !strings.Contains(note, "delete takes the whole name, or the number") ||
		!strings.Contains(note, `did you mean "add a --verbose flag to the indexer"?`) {
		t.Errorf("a partial name for delete answered %q", note)
	}
	cm, _ = pressEnter(typeText(cm, "/history delete NVIDIA setup"))
	if did := actionsOf(f); len(did) != 1 || did[0].Action != protocol.HistoryDelete || did[0].ID != "20261004T100000Z-00000002" {
		t.Fatalf("the whole name did %+v, want that chat deleted", did)
	}
	if note := cm.turns[len(cm.turns)-1].text; note != `deleted saved chat "NVIDIA setup"` {
		t.Errorf("the delete note = %q", note)
	}
}

// A list shows twenty rows of up to fifty chats. A number cannot reach the
// rest; a name can, because every saved chat is searched for it.
func TestHistory_ANameReachesAChatOlderThanTheListShows(t *testing.T) {
	entries := []protocol.HistoryEntry{{Current: true, Title: "now", Turns: 2, Unsaved: false}}
	for i := 1; i <= 30; i++ {
		entries = append(entries, protocol.HistoryEntry{ID: fmt.Sprintf("20260901T100000Z-%08d", i), Title: fmt.Sprintf("chat number %d", i), Turns: 2})
	}
	f := serveNamedChats(t, entries)
	if _, _ = pressEnter(typeText(newTestModel(), "/history resume chat number 27")); len(actionsOf(f)) != 1 || actionsOf(f)[0].ID != "20260901T100000Z-00000027" {
		t.Errorf("row 27 by name did %+v", actionsOf(f))
	}
	cm, _ := pressEnter(typeText(newTestModel(), "/history resume 27"))
	if note := cm.turns[len(cm.turns)-1].text; !strings.Contains(note, "there is no saved chat 27") {
		t.Errorf("row 27 by number answered %q", note)
	}
}

// Having just named a chat, the user is told the command that brings it back
// -- in words that work, which is what the test above them proves.
func TestSave_ANamedSaveSaysHowToComeBack(t *testing.T) {
	_ = serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Entry: &protocol.HistoryEntry{ID: "20261005T163000Z-00000001", Title: req.Name, Turns: 86}}
	})
	cm, _ := pressEnter(typeText(newTestModel(), "/save test1"))
	if note := cm.turns[len(cm.turns)-1].text; !strings.Contains(note, `saved "test1" (86 turns) -- /resume test1 continues it later`) {
		t.Errorf("the save note = %q", note)
	}
}

// /resume is /history resume and nothing else: it must not replace an unsaved
// chat without asking, must wait for a turn in flight, and on its own has one
// useful thing to say -- what there is to resume.
func TestResume_IsHistoryResumeUnderTheShortName(t *testing.T) {
	// Bare: the list, and nothing done.
	f := serveNamedChats(t, namedChats())
	cm, _ := pressEnter(typeText(newTestModel(), "/resume"))
	note := cm.turns[len(cm.turns)-1].text
	if cm.streamCh != nil || len(actionsOf(f)) != 0 {
		t.Fatalf("a bare /resume started a turn or did %+v", actionsOf(f))
	}
	for _, want := range []string{"saved chats -- /resume <n> continues one", `"NVIDIA setup"`, "\n  2 "} {
		if !strings.Contains(note, want) {
			t.Errorf("a bare /resume is missing %q:\n%s", want, note)
		}
	}
	// ...and the number it showed is the number it then takes.
	if _, _ = pressEnter(typeText(cm, "/resume 2")); len(actionsOf(f)) != 1 || actionsOf(f)[0].ID != "20261004T100000Z-00000002" {
		t.Errorf("/resume 2 after that list did %+v", actionsOf(f))
	}

	// An unsaved chat on screen is not replaced on the first asking.
	entries := namedChats()
	entries[0].Unsaved, entries[0].SavedAs = true, ""
	f = serveNamedChats(t, entries)
	m := newTestModel()
	m.appendTurn(turn{role: roleUser, text: "work I have not saved"})
	cm, _ = pressEnter(typeText(m, "/resume nvidia setup"))
	if note := cm.turns[len(cm.turns)-1].text; len(actionsOf(f)) != 0 || !strings.Contains(note, "/resume nvidia setup again replaces it") {
		t.Fatalf("an unsaved chat was replaced at once (%+v), or the note does not say how to go on: %q", actionsOf(f), note)
	}
	if cm, _ = pressEnter(typeText(cm, "/resume nvidia setup")); len(actionsOf(f)) != 1 || cm.turns[0].text != "first question of NVIDIA setup" {
		t.Errorf("the second /resume did %+v and left %+v", actionsOf(f), cm.turns)
	}

	// Mid-turn it waits.
	f = serveNamedChats(t, namedChats())
	busy := newTestModel()
	busy.state = stateStreaming
	got, _ := busy.handleLocalSlash("resume", "test1")
	if note := lastNote(t, got).text; !strings.Contains(note, "wait for this turn to finish") || len(f.seen()) != 0 {
		t.Errorf("a resume during a turn answered %q and sent %+v", note, f.seen())
	}

	// The popup offers it, and /help lists it.
	offered := false
	for _, d := range typeText(newTestModel(), "/resu").slashMatches() {
		offered = offered || d.Name == "resume"
	}
	if !offered || !strings.Contains(formatSlashHelp(), "/resume") {
		t.Errorf("/resume is offered by the popup: %v; listed in /help: %v", offered, strings.Contains(formatSlashHelp(), "/resume"))
	}
}

// The popup is where the owner looked. "/s" must offer it, with a summary that
// still says what it does when the popup clips it to its own width.
func TestSave_IsOfferedByThePopupAndListedInHelp(t *testing.T) {
	m := typeText(newTestModel(), "/sa")
	var offered *slashDef
	for _, d := range m.slashMatches() {
		if d.Name == "save" {
			offered = &d
		}
	}
	if offered == nil {
		t.Fatalf("typing /sa does not offer /save: %v", m.slashMatches())
	}
	if !strings.HasPrefix(offered.Summary, "save the current chat") {
		t.Errorf("the summary %q does not lead with what the command does", offered.Summary)
	}
	if !strings.Contains(formatSlashHelp(), "/save") {
		t.Error("/help does not list /save")
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
	if note := lastNote(t, first).text; !strings.Contains(note, "not saved") || !strings.Contains(note, "/resume 1 again") {
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
