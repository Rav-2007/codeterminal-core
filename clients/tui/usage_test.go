package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"

	tea "github.com/charmbracelet/bubbletea"
)

func turnBill(calls, in, out, cached int, dollars float64, context, window int) *protocol.TurnUsage {
	return &protocol.TurnUsage{Calls: calls, PromptTokens: in, CompletionTokens: out, CachedTokens: cached,
		CostUSD: dollars, ContextTokens: context, ContextWindow: window, Model: "deepseek/deepseek-v4-pro"}
}

func TestUsage_ReportsWhatTheProviderBilled(t *testing.T) {
	m := newTestModel()
	m.recordUsage(turnBill(3, 1000, 40, 800, 0.002, 1000, 1024000))
	m.recordUsage(turnBill(2, 1500, 60, 0, 0.003, 41200, 1024000))

	// Through the command path: typed, answered locally.
	m = typeText(m, "/usage")
	cm, _ := pressEnter(m)
	if cm.streamCh != nil {
		t.Fatal("/usage started a model turn")
	}
	note := cm.turns[len(cm.turns)-1]
	if note.role != roleSystem {
		t.Errorf("/usage was added as role %v, want roleSystem (never sent to the model)", note.role)
	}
	for _, want := range []string{
		"this chat     2 turns · 5 model calls",
		"in 2.5K (800 cached) · out 100 · $0.0050",
		"this session  2 turns · 5 model calls",
		"context       41.2K of 1.02M tokens (4%) · deepseek/deepseek-v4-pro",
	} {
		if !strings.Contains(note.text, want) {
			t.Errorf("/usage is missing %q:\n%s", want, note.text)
		}
	}
}

// ctrl+n starts a new chat: its totals restart, the session's carry on.
func TestUsage_ANewChatRestartsTheChatTotalsOnly(t *testing.T) {
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	m := newTestModel()
	m.recordUsage(turnBill(1, 500, 10, 0, 0.001, 500, 64000))
	cleared, _ := m.clearConversation()
	report := cleared.(chatModel).usageReport()
	if !strings.Contains(report, "this chat     nothing yet") || !strings.Contains(report, "this session  1 turn · 1 model call") {
		t.Errorf("after ctrl+n:\n%s", report)
	}
}

func TestUsage_SaysSoWhenThereIsNothingOrTheCostIsMissing(t *testing.T) {
	if r := newTestModel().usageReport(); !strings.Contains(r, "no model calls yet") {
		t.Errorf("an empty session reports %q", r)
	}
	m := newTestModel()
	m.recordUsage(&protocol.TurnUsage{Calls: 1, PromptTokens: 10, CostMissing: true, ContextTokens: 10})
	if r := m.usageReport(); !strings.Contains(r, "cost not reported") || !strings.Contains(r, "window unknown") {
		t.Errorf("no cost and no window:\n%s", r)
	}
	m.recordUsage(&protocol.TurnUsage{Calls: 1, PromptTokens: 10, CostUSD: 0.01, CostMissing: true})
	if r := m.usageReport(); !strings.Contains(r, "at least $0.0100") {
		t.Errorf("a partly-costed session:\n%s", r)
	}
	if percent(0.4) != "<1%" || compactTokens(812) != "812" || compactTokens(1024000) != "1.02M" {
		t.Error("the number formats drifted")
	}
}

// fakeDaemonReplying answers one prompt with the given messages.
func fakeDaemonReplying(t *testing.T, replies ...protocol.TokenResponse) {
	t.Helper()
	addr := testAddress(t)
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
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
		_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})
		var req protocol.PromptRequest
		if dec.Decode(&req) != nil {
			return
		}
		for _, r := range replies {
			_ = enc.Encode(r)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
}

// collect runs one streamed prompt and returns every message it produced.
func collect(t *testing.T) []tea.Msg {
	t.Helper()
	ch := make(chan tea.Msg, 16)
	go streamPrompt(context.Background(), "test", "/w", "hi", "", "", "", nil, nil, ch)
	var msgs []tea.Msg
	for {
		select {
		case msg := <-ch:
			msgs = append(msgs, msg)
			switch msg.(type) {
			case streamDoneMsg, streamErrMsg:
				return msgs
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the stream never finished; got %#v", msgs)
		}
	}
}

// The bill rides on the final message, and reaches the model as a usageMsg
// BEFORE the turn ends -- on success, and on a failure after the model was
// called (it was still billed).
func TestStream_TheTurnsBillArrivesBeforeItEnds(t *testing.T) {
	bill := turnBill(2, 900, 30, 0, 0.001, 600, 64000)
	for _, tc := range []struct {
		name  string
		final protocol.TokenResponse
	}{
		{"done", protocol.TokenResponse{Done: true, Usage: bill}},
		{"error after the model was called", protocol.TokenResponse{Done: true, Error: "provider failed", Usage: bill}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDaemonReplying(t, protocol.TokenResponse{Token: "hello"}, tc.final)
			msgs := collect(t)
			if len(msgs) < 2 {
				t.Fatalf("messages = %#v", msgs)
			}
			u, ok := msgs[len(msgs)-2].(usageMsg)
			if !ok || u.usage.Calls != 2 || u.usage.PromptTokens != 900 {
				t.Errorf("the message before the end is %#v, want the turn's bill", msgs[len(msgs)-2])
			}
		})
	}
}

// A bare "/word" that is not a command is answered here -- it used to go to
// the model as a question and come back as a screen of guesswork.
func TestUnknownCommand_IsAnsweredLocallyNotSentToTheModel(t *testing.T) {
	for _, tc := range []struct{ typed, want string }{
		{"/frobnicate", "unknown command /frobnicate -- /help lists the commands"},
		{"/histroy", "did you mean /history?"},
		{"/useage", "did you mean /usage?"}, // (not "/usag": Enter on an open popup completes a prefix)
	} {
		cm, _ := pressEnter(typeText(newTestModel(), tc.typed))
		if cm.streamCh != nil || cm.state != stateIdle {
			t.Errorf("%s started a model turn", tc.typed)
		}
		if last := cm.turns[len(cm.turns)-1]; last.role != roleSystem || !strings.Contains(last.text, tc.want) {
			t.Errorf("%s -> %q, want %q", tc.typed, last.text, tc.want)
		}
	}
	// Questions that merely START with a slash still go to the model.
	for _, q := range []string{"/etc/hosts is broken", "/tmp is full, why?", "/model", "/team", "/usage"} {
		if note := unknownCommandNote(q); note != "" {
			t.Errorf("%q was caught as an unknown command: %s", q, note)
		}
	}
}

// A STOPPED TURN WAS BILLED TOO. It sends no final message, so the daemon keeps
// its bill and sends it on the next turn's; /usage counts it there, in the chat
// it was stopped in.
//
// Neuter check: drop the stopsInChat++ in interruptTurn and the chat total
// misses the stopped turn; drop recordStoppedUsage's call in the usageMsg case
// and both totals do.
func TestUsage_AStoppedTurnIsCountedWhenItsBillArrives(t *testing.T) {
	m, _ := startedTurn(t)
	updated, _ := m.Update(tokenMsg("partial"))
	updated, _ = updated.(chatModel).Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	// The next turn's final message, carrying the stopped turn's bill.
	m.streamCh = make(chan tea.Msg)
	stopped := []protocol.TurnUsage{{Calls: 2, PromptTokens: 1000, CompletionTokens: 20, CostUSD: 0.001, CostMissing: true}}
	updated, _ = m.Update(usageMsg{usage: turnBill(1, 500, 10, 0, 0.0005, 500, 64000), stopped: stopped})
	m = updated.(chatModel)

	report := m.usageReport()
	for _, want := range []string{
		"this chat     2 turns (1 stopped) · 3 model calls",
		"this session  2 turns (1 stopped) · 3 model calls",
		"at least $0.0015",
		"context       500 of 64.0K tokens", // the newest call's, not the stopped one's
	} {
		if !strings.Contains(report, want) {
			t.Errorf("/usage is missing %q:\n%s", want, report)
		}
	}
}

// A bill for a turn stopped in a chat the user has since left belongs to the
// session, not to the chat they are in now.
func TestUsage_AStoppedTurnFromAnEarlierChatCountsForTheSessionOnly(t *testing.T) {
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	m, _ := startedTurn(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated, _ = updated.(chatModel).clearConversation() // ctrl+n: a new chat
	m = updated.(chatModel)

	m.recordStoppedUsage([]protocol.TurnUsage{{Calls: 1, CostMissing: true}})
	report := m.usageReport()
	if !strings.Contains(report, "this chat     nothing yet") ||
		!strings.Contains(report, "this session  1 turn (1 stopped) · 1 model call") {
		t.Errorf("a stopped turn from the previous chat:\n%s", report)
	}
}

// The stream reader passes stopped turns' bills on even when the final message
// has no bill of its own (a turn answered before any model call).
func TestStream_StoppedTurnsBillsArriveOnTheNextFinalMessage(t *testing.T) {
	stopped := []protocol.TurnUsage{{Calls: 3, CostUSD: 0.002}}
	fakeDaemonReplying(t, protocol.TokenResponse{Token: "hi"}, protocol.TokenResponse{Done: true, StoppedUsage: stopped})
	msgs := collect(t)
	if len(msgs) < 2 {
		t.Fatalf("messages = %#v", msgs)
	}
	u, ok := msgs[len(msgs)-2].(usageMsg)
	if !ok || u.usage != nil || len(u.stopped) != 1 || u.stopped[0].Calls != 3 {
		t.Errorf("the message before the end is %#v, want the stopped turn's bill", msgs[len(msgs)-2])
	}
}
