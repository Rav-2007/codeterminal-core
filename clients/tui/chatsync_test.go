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

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// FOUND 2026-10-08: with the VS Code panel and this client open on one
// workspace, a question asked in the panel never reached this screen, nor the
// history this client sent next -- the chat is shared, and this client read it
// once at startup. These tests play the daemon's half of keeping up
// (daemon/chatsync.go) and check what reaches the screen and the wire.

// sharedChatDaemon answers HistoryCurrent with current and a prompt with done,
// recording both. One request per connection, like the real daemon.
type sharedChatDaemon struct {
	mu       sync.Mutex
	catchUps []protocol.HistoryRequest
	prompts  []protocol.PromptRequest
}

func (d *sharedChatDaemon) seenCatchUps() []protocol.HistoryRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]protocol.HistoryRequest(nil), d.catchUps...)
}

func (d *sharedChatDaemon) seenPrompts() []protocol.PromptRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]protocol.PromptRequest(nil), d.prompts...)
}

func serveSharedChat(t *testing.T, current func(since string) protocol.HistoryResponse, done protocol.TokenResponse) *sharedChatDaemon {
	t.Helper()
	d := &sharedChatDaemon{}
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
				var raw json.RawMessage
				if dec.Decode(&raw) != nil {
					return
				}
				var probe struct {
					Chats bool `json:"chats"`
				}
				_ = json.Unmarshal(raw, &probe)
				if probe.Chats {
					var req protocol.HistoryRequest
					_ = json.Unmarshal(raw, &req)
					d.mu.Lock()
					d.catchUps = append(d.catchUps, req)
					d.mu.Unlock()
					resp := current(req.Since)
					resp.ProtocolVersion = protocol.ProtocolVersion
					_ = enc.Encode(resp)
					return
				}
				var req protocol.PromptRequest
				_ = json.Unmarshal(raw, &req)
				d.mu.Lock()
				d.prompts = append(d.prompts, req)
				d.mu.Unlock()
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: "ok"})
				done.ProtocolVersion, done.Done = protocol.ProtocolVersion, true
				_ = enc.Encode(done)
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return d
}

// editorAdded is the daemon's answer when the VS Code panel asked one question
// since revision r1: the exchange it is missing, to append.
func editorAdded(since string) protocol.HistoryResponse {
	if since != "r1" {
		return protocol.HistoryResponse{ChatRevision: "r9", Turns: []protocol.Turn{
			{Role: "user", Content: "the whole chat"}, {Role: "assistant", Content: "as stored"}}}
	}
	return protocol.HistoryResponse{ChatRevision: "r2", Append: true, Turns: []protocol.Turn{
		{Role: "user", Content: "asked in the editor"}, {Role: "assistant", Content: "answered there"}}}
}

// syncingModel is a client that opened at revision r1 with one exchange.
func syncingModel() chatModel {
	m := newTestModel()
	m.chatSync, m.chatRev = true, "r1"
	m.turns = []turn{{role: roleUser, text: "asked here"}, {role: roleAssistant, text: "answered here"}}
	return m
}

func transcriptOf(m chatModel) string {
	var parts []string
	for _, t := range m.turns {
		parts = append(parts, t.text)
	}
	return strings.Join(parts, " | ")
}

// focus delivers a focus report and runs the catch-up it starts.
func focus(t *testing.T, m chatModel) chatModel {
	t.Helper()
	updated, cmd := m.Update(tea.FocusMsg{})
	m = updated.(chatModel)
	if cmd == nil {
		return m
	}
	updated, _ = m.Update(cmd())
	return updated.(chatModel)
}

// Coming back to this terminal shows what the editor added, below what was
// here, and it goes out as history with the next question.
//
// Neuter check: delete the tea.FocusMsg case from Update -- nothing is fetched
// and the transcript check fails.
func TestChatSync_FocusShowsWhatTheOtherWindowAdded(t *testing.T) {
	d := serveSharedChat(t, editorAdded, protocol.TokenResponse{})
	m := focus(t, syncingModel())

	want := "asked here | answered here | ↓ 2 messages from another Mochiii window on this workspace | asked in the editor | answered there"
	if got := transcriptOf(m); got != want {
		t.Errorf("transcript = %q\nwant         %q", got, want)
	}
	if m.chatRev != "r2" {
		t.Errorf("chatRev = %q, want r2", m.chatRev)
	}
	if reqs := d.seenCatchUps(); len(reqs) != 1 || reqs[0].Action != protocol.HistoryCurrent || reqs[0].Since != "r1" {
		t.Errorf("catch-up requests = %+v, want one current since r1", reqs)
	}
	history := buildHistory(m.turns)
	if len(history) != 4 || history[2].Content != "asked in the editor" {
		t.Errorf("the next question's history = %+v, want the editor's exchange in it", history)
	}
}

// A question catches up FIRST: the editor's exchange lands above it on screen,
// and the request carries it and the revision it was built from.
//
// Neuter check: delete m.catchUpNow() from startTurn -- the editor's exchange is
// missing from the history sent and the first check fails.
func TestChatSync_AQuestionIsSentAfterWhatTheOtherWindowAdded(t *testing.T) {
	d := serveSharedChat(t, editorAdded, protocol.TokenResponse{ChatRevision: "r3"})
	m := syncingModel()
	m.input.SetValue("and now?")
	m, cmd := pressEnter(m)
	go runCmdTree(cmd)

	var sent protocol.PromptRequest
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p := d.seenPrompts(); len(p) == 1 {
			sent = p[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no prompt reached the daemon")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var said []string
	for _, h := range sent.History {
		said = append(said, h.Content)
	}
	if strings.Join(said, " | ") != "asked here | answered here | asked in the editor | answered there" {
		t.Errorf("history sent = %q, want the editor's exchange after this client's", said)
	}
	if sent.ChatRevision != "r2" {
		t.Errorf("ChatRevision sent = %q, want r2 -- the revision the history was built from", sent.ChatRevision)
	}
	if got := transcriptOf(m); !strings.HasSuffix(got, "asked in the editor | answered there | and now?") {
		t.Errorf("transcript = %q, want the question below what the editor added", got)
	}
}

// A chat replaced in the other window -- a new chat, a resume, a compact -- is
// shown whole, and says so.
func TestChatSync_AReplacedChatIsShownWhole(t *testing.T) {
	serveSharedChat(t, func(string) protocol.HistoryResponse {
		return protocol.HistoryResponse{ChatRevision: "r5", Turns: []protocol.Turn{
			{Role: "user", Content: "resumed question"}, {Role: "assistant", Content: "resumed answer"}}}
	}, protocol.TokenResponse{})
	m := focus(t, syncingModel())
	want := "resumed question | resumed answer | ↻ this chat was changed in another Mochiii window -- showing it as it is now"
	if got := transcriptOf(m); got != want {
		t.Errorf("transcript = %q\nwant         %q", got, want)
	}

	serveSharedChat(t, func(string) protocol.HistoryResponse {
		return protocol.HistoryResponse{ChatRevision: "r6"}
	}, protocol.TokenResponse{})
	m = focus(t, syncingModel())
	if got := transcriptOf(m); got != "↻ this chat was cleared in another Mochiii window" {
		t.Errorf("after the other window's ctrl+n the transcript = %q", got)
	}
}

// Nothing is fetched, or applied, under a turn in flight -- the stream holds an
// index into the transcript -- and an answer to a question the client has since
// moved past is dropped.
func TestChatSync_NothingChangesUnderATurnInFlight(t *testing.T) {
	d := serveSharedChat(t, editorAdded, protocol.TokenResponse{})
	m := syncingModel()
	m.state = stateStreaming
	if _, cmd := m.Update(tea.FocusMsg{}); cmd != nil {
		t.Error("a focus report during a stream started a catch-up")
	}
	before := transcriptOf(m)
	updated, _ := m.Update(chatCaughtUpMsg{since: "r1", resp: editorAdded("r1")})
	if got := transcriptOf(updated.(chatModel)); got != before {
		t.Errorf("a catch-up landed mid-stream: %q", got)
	}

	m.state = stateIdle
	m.chatRev = "r2" // moved on since the catch-up below was asked
	updated, _ = m.Update(chatCaughtUpMsg{since: "r1", resp: editorAdded("r1")})
	if got := transcriptOf(updated.(chatModel)); got != before {
		t.Errorf("a stale catch-up was applied: %q", got)
	}
	if len(d.seenCatchUps()) != 0 {
		t.Errorf("requests = %+v, want none", d.seenCatchUps())
	}
}

// A daemon without revisions (no memory, or older than this) is never asked,
// and a question goes out exactly as it did before.
func TestChatSync_ADaemonWithoutRevisionsIsNeverAsked(t *testing.T) {
	d := serveSharedChat(t, editorAdded, protocol.TokenResponse{})
	m := syncingModel()
	m.chatSync, m.chatRev = false, ""
	m = focus(t, m)
	m.input.SetValue("plain question")
	_, cmd := pressEnter(m)
	go runCmdTree(cmd)
	deadline := time.Now().Add(5 * time.Second)
	for len(d.seenPrompts()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no prompt reached the daemon")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reqs := d.seenCatchUps(); len(reqs) != 0 {
		t.Errorf("an older daemon was sent %+v", reqs)
	}
	if p := d.seenPrompts()[0]; p.ChatRevision != "" {
		t.Errorf("ChatRevision = %q to a daemon that never gave one", p.ChatRevision)
	}
}

// A turn the daemon says was not level -- the other window wrote while it ran
// -- ends with the chat fetched whole; a level one just moves the revision on.
//
// Neuter check: make handleChatRev always take msg.rev -- the transcript keeps
// missing the other window's turn and the fetch check fails.
func TestChatSync_ATurnThatWasNotLevelFetchesTheChatWhole(t *testing.T) {
	d := serveSharedChat(t, editorAdded, protocol.TokenResponse{})
	streaming := func(m chatModel) chatModel {
		m.state, m.streamCh, m.turnChatRev = stateStreaming, make(chan tea.Msg), "r1"
		return m
	}

	m := streaming(syncingModel())
	updated, _ := m.Update(chatRevMsg{rev: "r3", behind: false})
	updated, cmd := updated.(chatModel).Update(streamDoneMsg{})
	if got := updated.(chatModel).chatRev; got != "r3" {
		t.Errorf("after a level turn chatRev = %q, want r3", got)
	}
	runCmdTree(cmd)
	time.Sleep(200 * time.Millisecond) // runCmdTree fans a batch out to goroutines
	if len(d.seenCatchUps()) != 0 {
		t.Errorf("a level turn fetched the chat: %+v", d.seenCatchUps())
	}

	m = streaming(syncingModel())
	updated, _ = m.Update(chatRevMsg{rev: "r4", behind: true})
	updated, cmd = updated.(chatModel).Update(streamDoneMsg{})
	m = updated.(chatModel)
	caught := firstMsgOf[chatCaughtUpMsg](t, cmd)
	if caught.since != "" {
		t.Fatalf("the catch-up after a turn that was not level asked since %q, want the whole chat", caught.since)
	}
	updated, _ = m.Update(caught)
	if got := transcriptOf(updated.(chatModel)); !strings.HasPrefix(got, "the whole chat | as stored") {
		t.Errorf("transcript = %q, want the chat as stored", got)
	}
}

// An interrupted turn's Done never arrives, but the daemon saves the exchange
// anyway: the next catch-up fetches the chat whole, and says nothing about
// another window, because the change it finds is this one's.
func TestChatSync_AnInterruptedTurnIsFetchedBackQuietly(t *testing.T) {
	serveSharedChat(t, editorAdded, protocol.TokenResponse{})
	m := syncingModel()
	m.state, m.streamCh, m.streamCancel = stateStreaming, make(chan tea.Msg), func() {}
	updated, _ := m.interruptTurn()
	m = updated.(chatModel)
	if m.chatRev != "" || !m.chatQuiet {
		t.Fatalf("after an interrupt chatRev=%q quiet=%v, want unknown and quiet", m.chatRev, m.chatQuiet)
	}
	m = focus(t, m)
	if got := transcriptOf(m); got != "the whole chat | as stored" {
		t.Errorf("transcript = %q, want the stored chat with no note about another window", got)
	}
	if m.chatQuiet || m.chatRev != "r9" {
		t.Errorf("after catching up quiet=%v rev=%q", m.chatQuiet, m.chatRev)
	}
}

// After ctrl+n nothing is fetched until the daemon has cleared the chat too --
// fetching first would bring the old chat back over the cleared screen.
func TestChatSync_CtrlNWaitsForTheDaemon(t *testing.T) {
	d := serveSharedChat(t, editorAdded, protocol.TokenResponse{})
	m := syncingModel()
	updated, _ := m.clearConversation()
	m = updated.(chatModel)
	if _, cmd := m.Update(tea.FocusMsg{}); cmd != nil {
		t.Error("a catch-up started while ctrl+n was still on its way")
	}
	updated, _ = m.Update(resetOkMsg{rev: "r7"})
	m = updated.(chatModel)
	if m.resetPending || m.chatRev != "r7" || m.chatQuiet {
		t.Errorf("after the reset answered: pending=%v rev=%q quiet=%v", m.resetPending, m.chatRev, m.chatQuiet)
	}
	if len(d.seenCatchUps()) != 0 {
		t.Errorf("requests = %+v, want none", d.seenCatchUps())
	}
}

// firstMsgOf runs cmd's tree and returns the first message of type T it yields.
func firstMsgOf[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	found := make(chan T, 1)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, sub := range msg {
				go run(sub)
			}
		case T:
			select {
			case found <- msg:
			default:
			}
		}
	}
	go run(cmd)
	select {
	case msg := <-found:
		return msg
	case <-time.After(5 * time.Second):
		var zero T
		t.Fatalf("no %T was produced", zero)
		return zero
	}
}
