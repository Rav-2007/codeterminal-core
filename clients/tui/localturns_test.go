package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// WHAT THIS CLIENT SAYS ABOUT ITSELF IS NOT PART OF THE CONVERSATION.
//
// Measured on a real request 2026-10-06: after /help, /model, /git and
// /context, the next question carried four "assistant" messages the model never
// wrote -- 3,543 bytes re-sent with every later prompt, including the
// workspace's absolute path, and after /connect the tail of the API key.
// Each is still drawn exactly as it was; none is sent.
func TestLocalCommandOutputIsShownAndNeverSentToTheModel(t *testing.T) {
	m := newTestModel()
	m.appendTurn(turn{role: roleUser, text: "a real question"})
	m.appendTurn(turn{role: roleAssistant, text: "a real answer"})

	for _, typed := range []string{"/help", "/context", "/init", "/compact", "/search", "/connect a-key-typed-here"} {
		before := len(m.turns)
		m, _ = pressEnter(typeText(m, typed))
		if len(m.turns) == before {
			t.Fatalf("%s put nothing on screen; the test would prove nothing", typed)
		}
		for _, added := range m.turns[before:] {
			if added.role == roleAssistant && !added.local {
				t.Errorf("%s added a reply that will be sent to the model: %q", typed, clipForTest(added.text))
			}
		}
	}
	// The connect dialogue, built the way connect.go builds it.
	m.appendTurn(turn{role: roleAssistant, local: true, text: formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, MaskedKey: "...hBdC (70 characters)", InUse: true,
		APIBase: "https://integrate.api.nvidia.com/v1", Model: "nvidia/some-model", ModelTested: true,
	}})})

	history := buildHistory(m.turns)
	if len(history) != 2 || history[0].Content != "a real question" || history[1].Content != "a real answer" {
		t.Fatalf("the model would be sent %d turns, want only the real exchange:\n%+v", len(history), history)
	}
	for _, h := range history {
		for _, chrome := range []string{"slash commands:", "workspace:", "hBdC", "usage: /"} {
			if strings.Contains(h.Content, chrome) {
				t.Errorf("client chrome %q is in the history sent to the model: %q", chrome, clipForTest(h.Content))
			}
		}
	}

	// ANTI-VACUITY: the chrome really is on screen, drawn as a reply.
	shown := 0
	for _, tn := range m.turns {
		if tn.role == roleAssistant && tn.local {
			shown++
		}
	}
	if shown < 5 {
		t.Fatalf("only %d local replies are on screen; the commands above did not run", shown)
	}

	// /git FETCHES something to talk about, so its output stays in the
	// conversation ("write a commit message for this").
	before := len(m.turns)
	m, _ = pressEnter(typeText(m, "/git"))
	if len(m.turns) != before+1 || m.turns[before].local {
		t.Errorf("/git's output is not part of the conversation: %+v", m.turns[before:])
	}
}

func clipForTest(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
