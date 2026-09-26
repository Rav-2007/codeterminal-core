package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// The Tier 2.5 / Fix 13 lesson applied to this cluster: a degraded-state
// field that reaches the wire and is dropped by every shipped client is a
// wire change, not a fix. These tests exercise the TUI's own read and render
// path, so a regression that stopped dispatching or stopped rendering fails
// here rather than being discovered by an operator.

func degradedFixture() []protocol.Degradation {
	return []protocol.Degradation{{
		Component: protocol.DegradedLexicalRetrieval,
		Detail:    "keyword (lexical) search is unavailable, so answers are grounded by semantic similarity alone; exact identifier matches may be missed",
	}}
}

func TestChat_DegradedMsgSetsStateAndKeepsWaiting(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, cmd := m.Update(degradedMsg{degradedFixture()})
	m = updated.(chatModel)

	if len(m.lastDegraded) != 1 || m.lastDegraded[0].Component != protocol.DegradedLexicalRetrieval {
		t.Errorf("lastDegraded = %v, want one lexical_retrieval entry", m.lastDegraded)
	}
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after a degradedMsg (it's not terminal)")
	}
	if m.state != stateSending {
		t.Errorf("state = %v, want unchanged (degradedMsg arrives before any token)", m.state)
	}
}

// The regression that matters most: a half-working daemon must not render
// identically to a healthy one. The header no longer carries notices (see
// renderHeader), so the place it must reach is /context -- the component and
// the daemon's own detail, verbatim.
func TestChat_DegradedNoticeIsActuallyRendered(t *testing.T) {
	m := finishTurnWith(t, degradedMsg{degradedFixture()})

	ctx := contextReply(t, m)
	if !strings.Contains(ctx, protocol.DegradedLexicalRetrieval) {
		t.Errorf("/context = %q, want it to name the degraded component", ctx)
	}
	if !strings.Contains(ctx, "semantic similarity alone") {
		t.Errorf("/context = %q, want the daemon's detail text verbatim", ctx)
	}
}

// Every degradation reaches /context, not just the first.
func TestChat_EachDegradationReachesContext(t *testing.T) {
	m := finishTurnWith(t, degradedMsg{[]protocol.Degradation{
		{Component: protocol.DegradedLexicalRetrieval, Detail: "lexical detail"},
		{Component: protocol.DegradedMemory, Detail: "memory detail"},
		{Component: protocol.DegradedProviderRouting, Detail: "routing detail"},
	}})

	ctx := contextReply(t, m)
	for _, want := range []string{"lexical detail", "memory detail", "routing detail"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("/context = %q, missing %q", ctx, want)
		}
	}
}

func TestChat_DegradedClearsOnNewTurn(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	updated, _ := m.Update(degradedMsg{degradedFixture()})
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	m = typeText(m, "second")
	m, _ = pressEnter(m)

	if len(m.lastDegraded) != 0 {
		t.Errorf("lastDegraded = %v after starting a new turn, want cleared — a stale claim must not be shown as if it described the in-flight turn", m.lastDegraded)
	}
}

// A healthy daemon sends no degradations, and the header must show no
// persistent indicator for one.
func TestChat_NoDegradedNoticeWhenHealthy(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, Chunks: 3}})
	m = updated.(chatModel)

	if lines := m.degradedLabels(); len(lines) != 0 {
		t.Errorf("degradedLabels = %v on a healthy daemon, want none", lines)
	}
	if strings.Contains(m.renderHeader(), "degraded") {
		t.Errorf("renderHeader = %q, want no degraded notice when nothing is degraded", m.renderHeader())
	}
}
