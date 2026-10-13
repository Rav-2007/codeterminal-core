package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// longChat is a chat of n exchanges, as the transcript holds it.
func longChat(m *chatModel, n int) {
	for i := 1; i <= n; i++ {
		m.appendTurn(turn{role: roleUser, text: fmt.Sprintf("question %d", i)})
		m.appendTurn(turn{role: roleAssistant, text: fmt.Sprintf("answer %d", i)})
	}
}

// compactedChat is what the daemon sends back: its summary, then the last
// exchange word for word.
func compactedChat(n int) []protocol.Turn {
	return []protocol.Turn{
		{Role: "user", Content: "Continuing: question 1"},
		{Role: "assistant", Content: "Summary of the conversation so far: the user asked several questions."},
		{Role: "user", Content: fmt.Sprintf("question %d", n)},
		{Role: "assistant", Content: fmt.Sprintf("answer %d", n)},
	}
}

// runCompact types /compact and delivers the daemon's answer, returning the
// model at both moments: while it waits, and after.
func runCompact(t *testing.T, m chatModel) (waiting, done chatModel) {
	t.Helper()
	next, cmd := m.handleLocalSlash("compact", "")
	waiting = next.(chatModel)
	if cmd == nil {
		return waiting, waiting
	}
	after, _ := waiting.Update(cmd())
	return waiting, after.(chatModel)
}

// /compact ASKS THE DAEMON FOR A SUMMARY, with the model the chat is using, and
// shows the chat the daemon made of it.
//
// THE DEFECT: it kept the last eight turns of its own transcript and asked the
// daemon for nothing, so the earlier context was dropped with no summary in its
// place and the daemon's copy of the chat was not compacted at all.
//
// Neuter check: make startCompact call compactLocally instead of sendHistory.
func TestCompactAsksTheDaemonForASummary(t *testing.T) {
	f := serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Turns: compactedChat(10), Compacted: 18, ChatRevision: "rev-after"}
	})
	m := newTestModel()
	m.preferredTier = "kimi"
	longChat(&m, 10)

	waiting, done := runCompact(t, m)

	reqs := f.seen()
	if len(reqs) != 1 || reqs[0].Action != protocol.HistoryCompact {
		t.Fatalf("the daemon was sent %+v, want one compact request", reqs)
	}
	if reqs[0].Tier != "kimi" {
		t.Errorf("the summary was asked of tier %q, want the model the chat is using", reqs[0].Tier)
	}
	if !waiting.compacting {
		t.Errorf("the keyboard was not held while the daemon was asked")
	}
	if done.compacting {
		t.Errorf("the keyboard is still held after the answer")
	}
	text := transcriptText(done)
	for _, want := range []string{"Summary of the conversation so far", "question 10", "answer 10", "18 earlier messages summarised", "/history"} {
		if !strings.Contains(text, want) {
			t.Errorf("after /compact the screen does not show %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "question 3") {
		t.Errorf("a turn the daemon folded into the summary is still on screen:\n%s", text)
	}
	if done.chatRev != "rev-after" {
		t.Errorf("chat revision %q, want the one the daemon answered with", done.chatRev)
	}
	// What the next question carries is the compacted chat, not the old one.
	if h := buildHistory(done.turns); len(h) != 4 {
		t.Errorf("the next question would carry %d turns, want the 4 of the compacted chat", len(h))
	}
}

// WHILE THE DAEMON IS WRITING THE SUMMARY, NOTHING ELSE STARTS: an exchange
// begun now would be appended to a chat that is about to be replaced.
func TestTheKeyboardIsHeldWhileCompacting(t *testing.T) {
	serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Turns: compactedChat(10), Compacted: 18}
	})
	m := newTestModel()
	m.chatSync = true // a daemon that shares the chat between windows
	longChat(&m, 10)
	if !m.canCatchUp() {
		t.Fatalf("the control is wrong: an idle window on a sharing daemon should be able to fetch the chat")
	}
	next, _ := m.handleLocalSlash("compact", "")
	waiting := next.(chatModel)

	typed, _ := waiting.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	if got := typed.(chatModel).input.Value(); got != "" {
		t.Errorf("typing reached the prompt while compacting: %q", got)
	}
	cleared, _ := waiting.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if len(cleared.(chatModel).turns) < len(waiting.turns) {
		t.Errorf("ctrl+n cleared the chat while it was being compacted")
	}
	if waiting.canCatchUp() {
		t.Errorf("the chat can be fetched from the daemon while this window's own compact is in flight")
	}
}

// A SUMMARY THAT COULD NOT BE WRITTEN REMOVES NOTHING. The model being rate
// limited is a reason to try again, not to discard what was to be summarised.
func TestAFailedSummaryLeavesTheChatAsItWas(t *testing.T) {
	for name, problem := range map[string]string{
		"the model refused":  "the model could not write the summary: rate limit reached for this model",
		"nothing to compact": "nothing to compact yet: this chat has 4 messages, and /compact summarises once there are more than 5",
	} {
		t.Run(name, func(t *testing.T) {
			serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
				return protocol.HistoryResponse{Error: problem}
			})
			m := newTestModel()
			longChat(&m, 10)
			before := len(buildHistory(m.turns))

			_, done := runCompact(t, m)
			if got := len(buildHistory(done.turns)); got != before {
				t.Errorf("the chat went from %d turns to %d after a compact that did not happen", before, got)
			}
			text := transcriptText(done)
			if !strings.Contains(text, problem) || !strings.Contains(text, "nothing was removed") {
				t.Errorf("the screen does not say why, and that nothing was removed:\n%s", text)
			}
			if done.compacting {
				t.Errorf("the keyboard is still held after a failure")
			}
		})
	}
}

// A DAEMON TOO OLD TO SUMMARISE gets what /compact used to do, and is told so
// in words -- it must not read as a summary having been written.
func TestAnOlderDaemonGetsTheOldCompactAndIsToldSo(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"from before /history": func(t *testing.T) {
			serveHistoryWith(t, nil, func(protocol.HistoryRequest) protocol.HistoryResponse {
				return protocol.HistoryResponse{}
			})
		},
		"has /history, not this action": func(t *testing.T) {
			serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
				return protocol.HistoryResponse{Error: "unknown history action; use list, save, show, resume or delete"}
			})
		},
	}
	for name, serve := range cases {
		t.Run(name, func(t *testing.T) {
			serve(t)
			m := newTestModel()
			longChat(&m, 10)

			_, done := runCompact(t, m)
			if got := len(buildHistory(done.turns)); got != compactKeepLocal {
				t.Errorf("%d turns kept, want the last %d", got, compactKeepLocal)
			}
			text := transcriptText(done)
			if !strings.Contains(text, "summarised nothing") || !strings.Contains(text, "older than the summarising /compact") {
				t.Errorf("the screen does not say that nothing was summarised, and why:\n%s", text)
			}
		})
	}
}

// NOT DURING A TURN, AND NOT ON AN EMPTY CHAT: neither asks the daemon anything.
func TestCompactIsNotStartedWhenThereIsNothingToDoItTo(t *testing.T) {
	f := serveHistory(t, func(protocol.HistoryRequest) protocol.HistoryResponse {
		return protocol.HistoryResponse{Turns: compactedChat(10)}
	})

	busy := newTestModel()
	longChat(&busy, 10)
	busy.state = stateStreaming
	next, cmd := busy.handleLocalSlash("compact", "")
	if cmd != nil || next.(chatModel).compacting {
		t.Errorf("/compact started while a turn was in flight")
	}
	if text := transcriptText(next.(chatModel)); !strings.Contains(text, "wait for this turn to finish") {
		t.Errorf("no reason given:\n%s", text)
	}

	empty := newTestModel()
	next, cmd = empty.handleLocalSlash("compact", "")
	if cmd != nil || next.(chatModel).compacting {
		t.Errorf("/compact started on an empty chat")
	}
	if text := transcriptText(next.(chatModel)); !strings.Contains(text, "this chat is empty") {
		t.Errorf("no reason given:\n%s", text)
	}

	if reqs := f.seen(); len(reqs) != 0 {
		t.Errorf("the daemon was asked %d time(s) for a compact that could not start", len(reqs))
	}
}
