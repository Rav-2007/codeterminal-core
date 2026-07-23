package main

import (
	"strings"
	"testing"

	"codeterminal/protocol"
)

// Per-turn provider identity (E2): the daemon already observed and logged which
// upstream provider served a turn; these tests exercise the TUI's own read and
// render path so the Fix-13 trap (a wire field no client reads) fails here
// rather than being discovered by a user. The provider notice is a plain
// "served by X" fact, so — unlike the degraded notices — it is rendered in
// neutral, not warning, styling, and its ABSENCE is a normal state that must
// render as nothing.

func TestChat_ProviderMsgSetsStateAndKeepsWaiting(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, cmd := m.Update(providerMsg{"DeepInfra"})
	m = updated.(chatModel)

	if m.lastProvider != "DeepInfra" {
		t.Errorf("lastProvider = %q, want %q", m.lastProvider, "DeepInfra")
	}
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after a providerMsg (it's not terminal)")
	}
	// providerMsg can arrive at or before the first token; either way it must
	// not itself move the stream out of a sending/streaming state.
	if m.state != stateSending {
		t.Errorf("state = %v, want unchanged by a providerMsg", m.state)
	}
}

// The Fix-13 regression: a turn served by a named provider must not render
// identically to one with none reported.
func TestChat_ProviderNoticeIsActuallyRendered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	noProviderHeader := m.renderHeader()

	updated, _ := m.Update(providerMsg{"DeepInfra"})
	m = updated.(chatModel)
	providerHeader := m.renderHeader()

	if providerHeader == noProviderHeader {
		t.Fatal("header is byte-identical with and without a provider reported; the signal reaches the client and is not rendered")
	}
	if label := m.providerLabel(); !strings.Contains(label, "served by: DeepInfra") {
		t.Errorf("providerLabel = %q, want it to name the serving provider", label)
	}
	// It is a fact, not a warning: no ⚠ marker, and nothing implying fallback.
	if label := m.providerLabel(); strings.ContainsAny(label, "⚠") || strings.Contains(strings.ToLower(label), "fallback") {
		t.Errorf("providerLabel = %q, want a plain fact with no warning marker or fallback claim", label)
	}
}

// Absence is normal (OpenRouter does not guarantee the field): a turn with no
// provider reported must show no provider line at all, not a placeholder or an
// error.
func TestChat_NoProviderNoticeWhenNoneReported(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, Chunks: 3}})
	m = updated.(chatModel)

	if label := m.providerLabel(); label != "" {
		t.Errorf("providerLabel = %q with no provider reported, want empty", label)
	}
	if strings.Contains(m.renderHeader(), "served by") {
		t.Errorf("renderHeader = %q, want no provider notice when none was reported", m.renderHeader())
	}
}

func TestChat_ProviderClearsOnNewTurn(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	updated, _ := m.Update(providerMsg{"DeepInfra"})
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	m = typeText(m, "second")
	m, _ = pressEnter(m)

	if m.lastProvider != "" {
		t.Errorf("lastProvider = %q after starting a new turn, want cleared — a previous turn's provider must not be shown against the in-flight one", m.lastProvider)
	}
}

// ctrl+n (clearConversation) must also drop a stale provider, same as it drops
// grounding/redactions/degraded.
func TestChat_ProviderClearsOnConversationReset(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(providerMsg{"DeepInfra"})
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	cleared, _ := m.clearConversation()
	m = cleared.(chatModel)

	if m.lastProvider != "" {
		t.Errorf("lastProvider = %q after ctrl+n, want cleared", m.lastProvider)
	}
}
