package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// foldTranscript is a chat as the screen keeps it -- with a system line and a
// local reply in it, which are never sent -- and the question being asked last.
func foldTranscript() []turn {
	return []turn{
		{role: roleUser, text: "q1"}, {role: roleAssistant, text: "a1"},
		{role: roleSystem, text: "a note"},
		{role: roleUser, text: "q2"}, {role: roleAssistant, text: "a2"},
		{role: roleAssistant, text: "/help output", local: true},
		{role: roleUser, text: "q3"}, {role: roleAssistant, text: "a3"},
		{role: roleUser, text: "q4 (asking now)"},
	}
}

func foldPair() []protocol.Turn {
	return []protocol.Turn{{Role: "user", Content: "Continuing: q1"},
		{Role: "assistant", Content: "Summary of the conversation so far (...):\n\nq1 was asked"}}
}

func TestFoldStart_CountsOnlyTheTurnsThatAreSent(t *testing.T) {
	turns := foldTranscript()
	// Kept 4 are a3, q3, a2, q2 -- the local reply in between is not one of them.
	if from, ok := foldStart(turns, 4); !ok || turns[from].text != "q2" {
		t.Errorf("kept 4 starts at %d (%q), want q2", from, turns[from].text)
	}
	if from, ok := foldStart(turns, 0); !ok || turns[from].text != "q4 (asking now)" {
		t.Errorf("kept 0 should start at the question itself, got %d", from)
	}
	if _, ok := foldStart(turns, 7); ok {
		t.Error("more kept turns than the transcript holds must not line up")
	}
	if _, ok := foldStart([]turn{{role: roleSystem, text: "x"}}, 0); ok {
		t.Error("a transcript with no question has no fold point")
	}
}

func foldModel(t *testing.T) chatModel {
	t.Helper()
	m := newTestModel()
	m.turns = foldTranscript()
	m.state = stateSending
	m.chatRev = "old-revision"
	m.streamCh = make(chan tea.Msg, 4) // a fold arrives on a turn's stream
	return m
}

func TestHandleFold_SendsTheSummaryAndShowsEverything(t *testing.T) {
	m := foldModel(t)
	before := len(m.turns)
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 4, Folded: 2, Revision: "folded",
		Usage: &protocol.TurnUsage{Calls: 1, PromptTokens: 900, CompletionTokens: 90, CostUSD: 0.01}}})
	m = next.(chatModel)

	if len(m.turns) != before+1 || m.turns[before].role != roleSystem ||
		!strings.Contains(m.turns[before].text, "2 earlier messages are now a summary") {
		t.Fatalf("a fold must add exactly one line and remove nothing: %+v", m.turns[before:])
	}
	if m.chatRev != "folded" {
		t.Errorf("chatRev = %q: the fold's revision must be adopted, or the next Done reads as another window's change", m.chatRev)
	}
	got := m.historyPreview()
	var texts []string
	for _, h := range got {
		texts = append(texts, h.Content)
	}
	want := []string{"Continuing: q1", foldPair()[1].Content, "q2", "a2", "q3", "a3", "q4 (asking now)"}
	if !slices.Equal(texts, want) {
		t.Errorf("history after the fold =\n%q\nwant\n%q", texts, want)
	}
	if m.sessionUsage.prompt != 900 || m.chatUsage.prompt != 900 || m.chatUsage.turns != 0 {
		t.Errorf("the summary's bill: chat %+v, session %+v -- counted, but not as a turn", m.chatUsage, m.sessionUsage)
	}
	if !strings.Contains(m.historyLabel(), "as a summary") {
		t.Errorf("/context's memory line does not say part of it is a summary: %q", m.historyLabel())
	}
	if got := m.foldSummaryText(); !strings.Contains(got, "q1 was asked") || strings.Contains(got, "Summary of the conversation") {
		t.Errorf("/context should show the summary's text without its lead line: %q", got)
	}
}

func TestHandleFold_AKeptCountThatDoesNotLineUpChangesNothing(t *testing.T) {
	m := foldModel(t)
	before := len(m.turns)
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 40, Folded: 2, Revision: "folded"}})
	m = next.(chatModel)
	if m.fold != nil || m.chatRev != "old-revision" || len(m.turns) != before {
		t.Errorf("an unlined-up fold must leave the client behind, so it catches up: fold=%v rev=%q", m.fold, m.chatRev)
	}
}

func TestHandleFold_PendingShowsTheWaitAndTheNextTurnEndsIt(t *testing.T) {
	m := foldModel(t)
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Pending: true, Folded: 8}})
	m = next.(chatModel)
	line, ok := m.foldIndicator()
	if !ok || !strings.Contains(line, "summarising 8 earlier messages") || m.indicatorLine() != line {
		t.Errorf("waiting for a summary shows %q (ok %v), indicator %q", line, ok, m.indicatorLine())
	}
	if m.fold != nil {
		t.Error("a pending notice is not a fold")
	}
	m.historyToSend()
	if _, ok := m.foldIndicator(); ok {
		t.Error("a new turn must end a wait that never finished")
	}
}

func TestFold_ClearingTheChatForgetsTheFoldPoint(t *testing.T) {
	m := foldModel(t)
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 4, Folded: 2, Revision: "folded"}})
	m = next.(chatModel)
	m.state = stateIdle
	next, _ = m.clearConversation()
	if next.(chatModel).fold != nil {
		t.Error("ctrl+n kept the old chat's fold point")
	}
}

// The transcript bound drops turns from the front; the fold point follows its
// turn instead of pointing at whatever took its index.
func TestFoldPoint_FollowsItsTurnWhenTheFrontIsDropped(t *testing.T) {
	m := foldModel(t)
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 4, Folded: 2, Revision: "folded"}})
	m = next.(chatModel)
	m.limits = transcriptLimits{turns: len(m.turns) - 2, bytes: 1 << 30} // q1, a1 and the note go
	m.enforceTranscriptBound()
	got := m.historyPreview()
	if len(got) < 3 || got[2].Content != "q2" {
		t.Errorf("after the front was dropped the history should still continue at q2: %+v", got)
	}
}

func TestWithFoldCap_AddsTheCapabilityAndLeavesItsInputAlone(t *testing.T) {
	in := []string{protocol.CapToolApproval}
	out := withFoldCap(in)
	if !slices.Contains(out, protocol.CapFold) || len(in) != 1 {
		t.Errorf("withFoldCap(%v) = %v", in, out)
	}
	if !slices.Contains(withFoldCap(nil), protocol.CapFold) {
		t.Error("a client with no other capability still takes folds")
	}
}

// Inline mode prints the one line and nothing else: no rule, no second copy of
// the chat -- which is what replacing the transcript would have done.
func TestFold_InlineModePrintsOneLineAndNoRule(t *testing.T) {
	m := inlineModel(t)
	m.turns = foldTranscript()
	m, _ = settlePrints(t, m)
	m.state = stateSending
	m.streamCh = make(chan tea.Msg, 4) // the turn's stream the Fold arrives on
	next, cmd := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 4, Folded: 2, Revision: "folded"}})
	m = next.(chatModel)
	out := printOf(t, false, m, cmd)
	m, rest := settlePrints(t, m)
	all := out + rest
	if !strings.Contains(all, "now a summary for the model") {
		t.Errorf("the fold line was not printed: %q", all)
	}
	if strings.Contains(all, "──") || strings.Contains(all, "q1") {
		t.Errorf("a fold printed a rule or the chat again: %q", all)
	}
}

// The real stream: the handshake says this client takes folds, and a Fold from
// the daemon reaches the chat as a foldMsg.
func TestStreamPrompt_DeclaresCapFoldAndDeliversAFold(t *testing.T) {
	dir := t.TempDir()
	addr := testAddress(t)
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	lockPath := filepath.Join(dir, "daemon.lock")
	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	defer setLockPathForTest(t, lockPath)()
	caps := make(chan []string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
		var hs protocol.HandshakeRequest
		if dec.Decode(&hs) != nil {
			return
		}
		caps <- hs.Capabilities
		_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})
		var req protocol.PromptRequest
		if dec.Decode(&req) != nil {
			return
		}
		_ = enc.Encode(protocol.TokenResponse{TokenExtras: protocol.TokenExtras{Fold: &protocol.FoldInfo{Kept: 4, Folded: 6}}})
		_ = enc.Encode(protocol.TokenResponse{Token: "ok"})
		_ = enc.Encode(protocol.TokenResponse{Done: true})
	}()
	ch := make(chan tea.Msg, 16)
	streamPrompt(context.Background(), "test-client", "", "hi", "", "", "", nil, nil, ch)
	gotFold := false
	for done := false; !done; {
		select {
		case msg := <-ch:
			switch msg := msg.(type) {
			case foldMsg:
				gotFold = msg.info != nil && msg.info.Folded == 6
			case streamErrMsg:
				t.Fatalf("stream error: %v", msg.err)
			case streamDoneMsg:
				done = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the stream never finished")
		}
	}
	if got := <-caps; !slices.Contains(got, protocol.CapFold) {
		t.Errorf("the handshake declared %v, without %s", got, protocol.CapFold)
	}
	if !gotFold {
		t.Error("the daemon's Fold never reached the chat")
	}
}

// foldedModel is a chat that was folded on its last question and has finished
// that turn: ready for the next one.
func foldedModel(t *testing.T) chatModel {
	t.Helper()
	m := foldModel(t)
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 4, Folded: 2, Revision: "folded"}})
	m = next.(chatModel)
	m.appendTurn(turn{role: roleAssistant, text: "a4"})
	m.state = stateIdle
	if m.fold == nil {
		t.Fatal("the fold did not set a fold point")
	}
	return m
}

// From a keystroke to the bytes on the socket, through both places a question
// is sent from: the summary goes instead of the folded turns.
func TestFold_TheNextQuestionSendsTheSummaryNotTheFoldedTurns(t *testing.T) {
	for _, typed := range []string{"a plain question", "/explain the steered path"} {
		_, req := typedRequest(t, foldedModel(t), typed)
		if len(req.History) < 2 || req.History[0].Content != "Continuing: q1" ||
			!strings.HasPrefix(req.History[1].Content, "Summary of the conversation so far") {
			t.Errorf("%q sent a history that does not open with the summary: %+v", typed, req.History)
			continue
		}
		for _, h := range req.History {
			if h.Content == "q1" || h.Content == "a1" {
				t.Errorf("%q sent a folded turn again: %+v", typed, req.History)
			}
		}
		if last := req.History[len(req.History)-1]; last.Content != "a4" {
			t.Errorf("%q: the history should end with the last answer, got %q", typed, last.Content)
		}
	}
}

// Every way the chat is replaced forgets the fold point: it belonged to the
// transcript that was there.
func TestFold_EveryReplaceOfTheChatForgetsTheFoldPoint(t *testing.T) {
	compacted, _ := foldedModel(t).handleCompactDone(compactDoneMsg{resp: protocol.HistoryResponse{
		Turns: foldPair(), Compacted: 3, ChatRevision: "c",
		HistoryExtras: protocol.HistoryExtras{Usage: &protocol.TurnUsage{Calls: 1, PromptTokens: 700}}}})
	if compacted.(chatModel).fold != nil {
		t.Error("/compact kept the fold point")
	}
	if got := compacted.(chatModel).sessionUsage.prompt; got != 700 {
		t.Errorf("/compact's summary cost %d prompt tokens in /usage, want 700", got)
	}
	adopted := foldedModel(t)
	adopted.adoptChat(protocol.HistoryResponse{Turns: foldPair(), ChatRevision: "another"})
	if adopted.fold != nil {
		t.Error("adopting a chat replaced in another window kept the fold point")
	}

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		if req.Action == protocol.HistoryResume {
			e := savedChats()[1]
			e.Current, e.ID = true, ""
			return protocol.HistoryResponse{Entry: &e, Turns: foldPair()}
		}
		entries := savedChats()
		entries[0].Unsaved, entries[0].SavedAs = false, entries[2].ID // the chat on screen is saved
		return protocol.HistoryResponse{Entries: entries}
	})
	m := foldedModel(t)
	listed, _ := m.handleHistoryCommand("")
	resumed, _ := listed.(chatModel).handleHistoryCommand("resume 1")
	if rm := resumed.(chatModel); rm.fold != nil || len(rm.turns) == 0 || rm.turns[0].text != "Continuing: q1" {
		t.Errorf("resuming kept the fold point (%v) or did not resume: %+v", rm.fold, rm.turns)
	}
}

// THE BUG THE LIVE CHECK FOUND (2026-11-27): a Fold is one of the turn's stream
// messages, and a handler that does not ask for the next one leaves the answer
// unread -- the chat said "thinking…" for good after the fold line. Every kind
// of Fold must hand back the stream's next message.
func TestHandleFold_KeepsReadingTheTurnsStream(t *testing.T) {
	for name, info := range map[string]*protocol.FoldInfo{
		"pending":        {Pending: true, Folded: 8},
		"result":         {Summary: foldPair(), Kept: 4, Folded: 2, Revision: "folded"},
		"not lined up":   {Summary: foldPair(), Kept: 40, Folded: 2, Revision: "folded"},
		"empty envelope": nil,
	} {
		m := foldModel(t)
		m.streamCh <- tokenMsg("the answer")
		next, cmd := m.Update(foldMsg{info})
		if cmd == nil {
			t.Errorf("%s: no command -- the stream is never read again", name)
			continue
		}
		got := make(chan tea.Msg, 1)
		go func() { got <- cmd() }()
		select {
		case msg := <-got:
			if tok, ok := msg.(tokenMsg); !ok || string(tok) != "the answer" {
				t.Errorf("%s: the command gave %T %v, want the stream's next message", name, msg, msg)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: the command did not read the stream", name)
		}
		_ = next
	}
	// A Fold from a stream already given up on changes nothing and reads nothing.
	m := foldModel(t)
	m.streamCh = nil
	next, cmd := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 4, Folded: 2, Revision: "folded"}})
	if cmd != nil || next.(chatModel).fold != nil {
		t.Error("a stray Fold after its stream was abandoned was taken")
	}
}

// /context shows what the model has in place of the folded messages.
func TestFold_ContextShowsTheSummary(t *testing.T) {
	reply := contextReply(t, foldedModel(t))
	for _, want := range []string{"the earlier 2 messages as a summary", "what the model has in place of the earlier messages", "q1 was asked"} {
		if !strings.Contains(reply, want) {
			t.Errorf("/context is missing %q:\n%s", want, reply)
		}
	}
}
