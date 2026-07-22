package main

import (
	"strings"
	"testing"

	"codeterminal/protocol"
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
// identically to a healthy one.
func TestChat_DegradedNoticeIsActuallyRendered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, Chunks: 3}})
	m = updated.(chatModel)
	healthyHeader := m.renderHeader()

	updated, _ = m.Update(degradedMsg{degradedFixture()})
	m = updated.(chatModel)
	degradedHeader := m.renderHeader()

	if degradedHeader == healthyHeader {
		t.Fatal("header is byte-identical with and without a degradation reported; the signal reaches the client and is not rendered")
	}
	if !strings.Contains(degradedHeader, protocol.DegradedLexicalRetrieval) {
		t.Errorf("renderHeader = %q, want it to name the degraded component", degradedHeader)
	}
	// The rendered header line is truncated to the terminal width on purpose
	// (see noticeLines), so the full detail is asserted on the untruncated
	// label rather than on the header — the daemon's own wording must reach
	// the client verbatim, not be paraphrased here.
	labels := m.degradedLabels()
	if len(labels) != 1 || !strings.Contains(labels[0], "semantic similarity alone") {
		t.Errorf("degradedLabels = %q, want the daemon's detail text rendered verbatim", labels)
	}
}

// Each degradation gets its own guaranteed row, for the reason renderHeader
// documents: a joined line can overflow the terminal, soft-wrap, and desync
// the row count the viewport height is computed from — which would let a
// notice hide itself.
func TestChat_EachDegradationGetsItsOwnHeaderLine(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(degradedMsg{[]protocol.Degradation{
		{Component: protocol.DegradedLexicalRetrieval, Detail: "lexical detail"},
		{Component: protocol.DegradedMemory, Detail: "memory detail"},
		{Component: protocol.DegradedProviderRouting, Detail: "routing detail"},
	}})
	m = updated.(chatModel)

	lines := strings.Split(m.renderHeader(), "\n")
	if len(lines) != 4 {
		t.Fatalf("renderHeader produced %d line(s), want 4 (brand/state + one per degradation): %q", len(lines), m.renderHeader())
	}
	if got := m.headerLineCount(); got != 4 {
		t.Errorf("headerLineCount = %d, want 4 — the viewport height would desync from what is actually rendered", got)
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
