package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// THE SCENARIO THESE TESTS ARE ABOUT, FOUND 2026-10-08: a terminal and an
// editor open on one workspace, so one daemon and one shared chat. A question
// asked in the editor never reached the terminal -- not its screen, and not the
// history it sent with its next question -- so the model answering in the
// terminal had never heard of it. Each "client" below is just a revision and
// the requests it sends, because that is all a client is to the daemon.

// syncDaemon is a Server with conversation memory and a fake provider that
// answers "answer 1", "answer 2", ... and records every request body.
type syncDaemon struct {
	srv *Server
	mu  sync.Mutex
	// bodies are the provider requests, in order.
	bodies []string
}

func newSyncDaemon(t *testing.T) *syncDaemon {
	t.Helper()
	d := &syncDaemon{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.bodies = append(d.bodies, string(b))
		n := len(d.bodies)
		d.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer %d\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", n)
	}))
	t.Cleanup(upstream.Close)
	mem, _ := openTestMemoryStore(t)
	d.srv = historyServer(t, mem)
	d.srv.apiBase, d.srv.apiKey, d.srv.cfg, d.srv.modelOverride = upstream.URL, "test-key", &Config{}, "test/model"
	return d
}

func (d *syncDaemon) lastBody(t *testing.T) string {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.bodies) == 0 {
		t.Fatal("the provider was never called")
	}
	return d.bodies[len(d.bodies)-1]
}

// connect opens one connection and handshakes, as every client request does.
func (d *syncDaemon) connect(t *testing.T) (*json.Encoder, *json.Decoder, protocol.HandshakeResponse, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		d.srv.serveConn(serverConn)
		_ = serverConn.Close()
		close(done)
	}()
	_ = clientConn.SetDeadline(time.Now().Add(10 * time.Second))
	enc, dec := json.NewEncoder(clientConn), json.NewDecoder(clientConn)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "sync-test"}); err != nil {
		t.Fatal(err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil || !hs.Ok {
		t.Fatalf("handshake = %+v (err %v)", hs, err)
	}
	return enc, dec, hs, func() { _ = clientConn.Close(); <-done }
}

// start is a client opening: what it shows, and the revision that is.
func (d *syncDaemon) start(t *testing.T) ([]protocol.Turn, string) {
	t.Helper()
	_, _, hs, closeConn := d.connect(t)
	closeConn()
	return hs.PersistedHistory, hs.ChatRevision
}

// ask sends one prompt and returns its Done.
func (d *syncDaemon) ask(t *testing.T, req protocol.PromptRequest) protocol.TokenResponse {
	t.Helper()
	enc, dec, _, closeConn := d.connect(t)
	defer closeConn()
	req.ProtocolVersion = protocol.ProtocolVersion
	if err := enc.Encode(req); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the answer: %v", err)
		}
		if tok.Error != "" {
			t.Fatalf("the turn failed: %s", tok.Error)
		}
		if tok.Done {
			return tok
		}
	}
}

func (d *syncDaemon) current(t *testing.T, since string) protocol.HistoryResponse {
	t.Helper()
	resp := askHistory(t, d.srv, protocol.HistoryRequest{Action: protocol.HistoryCurrent, Since: since})
	if resp.Error != "" {
		t.Fatalf("current: %s", resp.Error)
	}
	return resp
}

func turnTexts(turns []protocol.Turn) string {
	var parts []string
	for _, t := range turns {
		parts = append(parts, t.Role+": "+t.Content)
	}
	return strings.Join(parts, " | ")
}

// What one client said reaches the other, as the part it is missing.
//
// Neuter check: make currentChat ignore since (always after = 0) -- the delta
// becomes the whole chat with Append false, and the first check fails.
func TestChatSync_WhatOneClientAddedReachesTheOther(t *testing.T) {
	d := newSyncDaemon(t)
	d.srv.persistTurn("asked in the terminal earlier", "earlier answer", nil)

	_, terminal := d.start(t)
	_, editor := d.start(t)
	if terminal == "" || terminal != editor {
		t.Fatalf("two clients opening on one chat got revisions %q and %q, want one non-empty revision", terminal, editor)
	}

	done := d.ask(t, protocol.PromptRequest{Prompt: "asked in the editor", ChatRevision: editor})
	if done.ChatRevision == "" || done.ChatRevision == editor || done.ChatBehind {
		t.Fatalf("Done = revision %q behind %v, want a new revision and not behind (nobody else wrote)", done.ChatRevision, done.ChatBehind)
	}
	editor = done.ChatRevision

	got := d.current(t, terminal)
	if !got.Append || turnTexts(got.Turns) != "user: asked in the editor | assistant: answer 1" {
		t.Errorf("the terminal catching up got append=%v %q, want just the editor's exchange to append", got.Append, turnTexts(got.Turns))
	}
	if got.ChatRevision != editor {
		t.Errorf("after catching up the terminal is at %q, the editor at %q: they show the same chat", got.ChatRevision, editor)
	}

	again := d.current(t, got.ChatRevision)
	if len(again.Turns) != 0 || again.ChatRevision != got.ChatRevision {
		t.Errorf("a client that is level was sent %q at %q, want nothing", turnTexts(again.Turns), again.ChatRevision)
	}
}

// THE MODEL HALF OF THE DEFECT: a client whose copy is behind is answered from
// the stored chat, so the model has heard what was said in the other window.
//
// Neuter check: make historyForTurn always return req.History -- the provider
// never sees the editor's question and the first check fails.
func TestChatSync_AClientThatIsBehindIsAnsweredFromTheStoredChat(t *testing.T) {
	d := newSyncDaemon(t)
	_, terminal := d.start(t)
	_, editor := d.start(t)
	d.ask(t, protocol.PromptRequest{Prompt: "the release is on Friday", ChatRevision: editor})

	// The terminal has not looked since it opened: its history is empty.
	done := d.ask(t, protocol.PromptRequest{Prompt: "when is the release?", ChatRevision: terminal})
	if body := d.lastBody(t); !strings.Contains(body, "the release is on Friday") {
		t.Errorf("the model answering in the terminal was not shown the editor's turn:\n%s", body)
	}
	if !done.ChatBehind {
		t.Errorf("Done.ChatBehind = false: the terminal would go on showing a chat without the editor's turn")
	}

	// And then the terminal fetches the chat whole, in the order it was said.
	whole := d.current(t, "")
	want := "user: the release is on Friday | assistant: answer 1 | user: when is the release? | assistant: answer 2"
	if whole.Append || turnTexts(whole.Turns) != want {
		t.Errorf("the whole chat = append %v %q, want %q", whole.Append, turnTexts(whole.Turns), want)
	}
	if whole.ChatRevision != done.ChatRevision {
		t.Errorf("fetched at %q, but the turn ended at %q", whole.ChatRevision, done.ChatRevision)
	}
}

// A client that is level keeps sending its own history, which the daemon uses
// as sent -- and so does a client that never sends a revision at all, which is
// every client written before this existed.
func TestChatSync_ALevelOrOlderClientsHistoryIsUsedAsSent(t *testing.T) {
	d := newSyncDaemon(t)
	_, editor := d.start(t)
	d.ask(t, protocol.PromptRequest{Prompt: "stored question", ChatRevision: editor})

	d.ask(t, protocol.PromptRequest{Prompt: "older client", History: []protocol.Turn{
		{Role: "user", Content: "what this client showed"}, {Role: "assistant", Content: "its answer"},
	}})
	body := d.lastBody(t)
	if !strings.Contains(body, "what this client showed") || strings.Contains(body, "stored question") {
		t.Errorf("a client without a revision did not get its own history used:\n%s", body)
	}

	_, level := d.start(t)
	d.ask(t, protocol.PromptRequest{Prompt: "level client", ChatRevision: level, History: []protocol.Turn{
		{Role: "user", Content: "as this client typed it"}, {Role: "assistant", Content: "its answer"},
	}})
	if body := d.lastBody(t); !strings.Contains(body, "as this client typed it") {
		t.Errorf("a level client's own history was replaced:\n%s", body)
	}
}

// ctrl+n, resume and compact REPLACE the chat, and the ids of the old one may be
// handed out again -- so after one, a client is sent the whole chat rather than
// "the turns after" an id that now means something else.
//
// Neuter check: drop the chatReplacedLocked call from resetPersistedHistory --
// SQLite reuses the cleared ids, and the client from before ctrl+n is told
// nothing changed (append, no turns) while the chat is a different one.
func TestChatSync_AReplacedChatIsSentWhole(t *testing.T) {
	d := newSyncDaemon(t)
	d.srv.persistTurn("old question", "old answer", nil)
	_, stale := d.start(t)

	reset := d.ask(t, protocol.PromptRequest{Reset: true})
	if reset.ChatRevision == "" || reset.ChatRevision == stale {
		t.Fatalf("ctrl+n's Done carries revision %q, want a new one", reset.ChatRevision)
	}
	d.srv.persistTurn("new question", "new answer", nil)

	got := d.current(t, stale)
	if got.Append || turnTexts(got.Turns) != "user: new question | assistant: new answer" {
		t.Errorf("a client from before ctrl+n got append=%v %q, want the new chat whole", got.Append, turnTexts(got.Turns))
	}

	// The client that pressed ctrl+n is level with the empty chat, so the next
	// exchange is what it is missing.
	if got := d.current(t, reset.ChatRevision); !got.Append || len(got.Turns) != 2 {
		t.Errorf("the client that cleared the chat got append=%v %q, want the one new exchange", got.Append, turnTexts(got.Turns))
	}

	// Resume replaces it too, and the client that resumed is level with it.
	id := saveNow(t, d.srv, "kept").Entry.ID
	d.srv.persistTurn("one more", "and its answer", nil)
	beforeResume := d.current(t, "").ChatRevision
	resumed := askHistory(t, d.srv, protocol.HistoryRequest{Action: protocol.HistoryResume, ID: id})
	if resumed.ChatRevision == "" || resumed.ChatRevision == beforeResume {
		t.Fatalf("resume answered at revision %q, want a new one", resumed.ChatRevision)
	}
	if got := d.current(t, resumed.ChatRevision); len(got.Turns) != 0 {
		t.Errorf("the client that resumed is not level with what it was given: %q", turnTexts(got.Turns))
	}
	if got := d.current(t, beforeResume); got.Append || len(got.Turns) != 2 {
		t.Errorf("a client from before the resume got append=%v %q, want the resumed chat whole", got.Append, turnTexts(got.Turns))
	}
}

// When another client writes WHILE a turn runs, the Done says so: the client's
// screen holds its own exchange but not the other one, which landed first.
func TestChatSync_DoneSaysBehindWhenAnotherClientWroteDuringTheTurn(t *testing.T) {
	d := newSyncDaemon(t)
	_, rev := d.start(t)
	// Written after this client's revision was read and before its turn is
	// saved: the same position as an answer finishing in the other window.
	d.srv.persistTurn("from the other window", "its answer", nil)
	done := d.ask(t, protocol.PromptRequest{Prompt: "mine", ChatRevision: rev})
	if !done.ChatBehind {
		t.Error("Done.ChatBehind = false although another client's exchange landed first")
	}
}

// The turn is in the stored chat BEFORE its Done is sent, so a client acting on
// the Done at once -- the other window checking, a /save -- finds it there. It
// used to be saved after the Done, which a fast client could beat.
//
// Neuter check: move persistTurn back below sendDone in server.go -- the stored
// chat is empty when the Done arrives (the provider call has finished, the save
// has not) often enough to fail within a few runs, and the revision check fails
// every time.
func TestChatSync_TheTurnIsSavedBeforeItsDone(t *testing.T) {
	d := newSyncDaemon(t)
	_, rev := d.start(t)
	done := d.ask(t, protocol.PromptRequest{Prompt: "saved first", ChatRevision: rev})
	turns, err := d.srv.memory.LoadAllTurns(context.Background(), d.srv.workspace)
	if err != nil || len(turns) != 2 {
		t.Fatalf("when the Done arrived the chat held %d turns (err %v), want the exchange", len(turns), err)
	}
	if now := d.current(t, "").ChatRevision; now != done.ChatRevision {
		t.Errorf("the Done says %q and the chat is at %q", done.ChatRevision, now)
	}
}

// With no conversation memory there is no shared chat, and nothing claims one.
func TestChatSync_NoMemoryMeansNoRevision(t *testing.T) {
	d := newSyncDaemon(t)
	d.srv.memory = nil
	if _, rev := d.start(t); rev != "" {
		t.Errorf("handshake revision = %q with no memory", rev)
	}
	if done := d.ask(t, protocol.PromptRequest{Prompt: "hello", ChatRevision: "x.0.0"}); done.ChatRevision != "" || done.ChatBehind {
		t.Errorf("Done = %q behind %v with no memory", done.ChatRevision, done.ChatBehind)
	}
}

// A revision is only ever this daemon's own: one from another daemon process,
// or anything malformed, means "has seen nothing" and gets the whole chat.
func TestChatSync_AForeignOrMalformedRevisionGetsTheWholeChat(t *testing.T) {
	d := newSyncDaemon(t)
	d.srv.persistTurn("q", "a", nil)
	_, rev := d.start(t)
	r, ok := parseChatRevision(rev)
	if !ok {
		t.Fatalf("the daemon's own revision %q does not parse", rev)
	}
	foreign := chatRevision{epoch: "another-daemon", gen: r.gen, last: r.last}.String()
	for _, since := range []string{foreign, "", "garbage", "a.b.c", "e.1", "e.1.-5", rev + ".9"} {
		if got := d.current(t, since); got.Append || len(got.Turns) != 2 {
			t.Errorf("since %q: append=%v %q, want the whole chat", since, got.Append, turnTexts(got.Turns))
		}
	}
}

// The revision a client is given says exactly what it was given: last is the
// newest of the turns it holds, from the same query.
func TestChatSync_TheHandshakeRevisionMatchesItsTurns(t *testing.T) {
	d := newSyncDaemon(t)
	for i := 1; i <= 8; i++ { // more than maxHistoryTurns, so the handshake is a tail
		d.srv.persistTurn(fmt.Sprintf("q%d", i), fmt.Sprintf("a%d", i), nil)
	}
	turns, rev := d.start(t)
	if len(turns) == 0 || turns[len(turns)-1].Content != "a8" {
		t.Fatalf("handshake turns end %q", turnTexts(turns))
	}
	if got := d.current(t, rev); len(got.Turns) != 0 {
		t.Errorf("the handshake's revision understates its turns: catching up from it re-sent %q", turnTexts(got.Turns))
	}
}
