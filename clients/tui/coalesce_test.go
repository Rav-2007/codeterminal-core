package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// COALESCED REPAINTS, AND THE WAYS THEY COULD LOSE TEXT.
//
// A repaint that is deferred is a repaint that can be forgotten, and the
// symptom is not a crash: it is an answer that stops one tick short of what the
// model actually said and stays that way until the user happens to type
// something. So the tests here are mostly about the ENDINGS -- the last token,
// the stream that finishes between two ticks, the tick that arrives after
// everything is over.

// armTracker counts how many times a repaint was newly scheduled, which is the
// number of ticks the event loop is asked to deliver.
type armTracker struct {
	m     chatModel
	arms  int
	ticks int
}

func (a *armTracker) token(text string) {
	was := a.m.refreshScheduled
	u, _ := a.m.Update(tokenMsg(text))
	a.m = u.(chatModel)
	if !was && a.m.refreshScheduled {
		a.arms++
	}
}

func (a *armTracker) tick() {
	u, _ := a.m.Update(refreshTickMsg{})
	a.m = u.(chatModel)
	a.ticks++
}

func streamingTracker(t *testing.T) *armTracker {
	t.Helper()
	return &armTracker{m: benchTranscript(2, 40)}
}

// 3.3a/3.3b: tokens arriving between two ticks collapse into ONE repaint, not
// one per token. This is the whole saving, and it is counted rather than timed.
func TestManyTokensBetweenTicksProduceOneRepaint(t *testing.T) {
	a := streamingTracker(t)
	a.token("settle ")
	a.tick()

	// THE ASSERTION THAT ACTUALLY CATCHES A REPAINT, which is the drawn view
	// and not a counter. An earlier version of this test counted how many times
	// a tick was ARMED, and a neutered refreshSoon that repainted on every
	// token still armed exactly once -- so the test passed while measuring
	// nothing. What "coalesced" means is that the screen does not change
	// between ticks; that is what is checked.
	quiet := a.m.viewport.View()
	for i := 0; i < 200; i++ {
		a.token("tok ")
	}
	if a.m.viewport.View() != quiet {
		t.Error("the view changed while 200 tokens arrived between two ticks. " +
			"Repaints are not being coalesced -- a token is drawing.")
	}
	if a.arms != 2 {
		t.Errorf("200 tokens armed %d repaints in total, want 2 (one for the settling "+
			"token, one for the burst). Repaint requests are a single bit, not a queue.", a.arms)
	}
	if !a.m.refreshPending {
		t.Error("200 tokens arrived and nothing is marked as needing a repaint")
	}

	a.tick()
	if a.m.viewport.View() == quiet {
		t.Error("the tick did not draw the tokens that had arrived")
	}
	if a.m.refreshPending {
		t.Error("the tick did not clear the pending repaint")
	}
	if a.m.refreshScheduled {
		t.Error("the tick re-armed itself. A quiet stream must stop ticking rather " +
			"than wake the process sixty times a second to do nothing.")
	}
}

// ...and the next token after that tick must arm again, or the stream stops
// being drawn at all after the first repaint.
func TestATokenAfterARepaintArmsTheNextOne(t *testing.T) {
	a := streamingTracker(t)
	a.token("first ")
	a.tick()
	a.token("second ")
	if a.arms != 2 {
		t.Fatalf("a token arriving after a repaint armed %d repaints in total, want 2", a.arms)
	}
	a.tick()
	if got := a.m.turns[a.m.streamAssistant].text; !strings.Contains(got, "second") {
		t.Errorf("the second token never reached the turn: %q", got)
	}
}

// 3.3b, THE CASE THE TASK NAMES: a stream that ends between two ticks. The
// tokens that arrived since the last repaint, plus the sanitizer's held tail,
// are in the model and not on the screen at the moment the stream finishes.
//
// Nothing else is obliged to draw them. The ordinary ending -- streamDoneMsg,
// then checkForEditBlocks -- returns without a refresh when the answer holds no
// edit blocks, which is the common case. That is why endStream repaints.
func TestAStreamEndingBetweenTicksStillShowsItsLastTokens(t *testing.T) {
	m := benchTranscript(2, 40)
	u, _ := m.Update(tokenMsg("visible-early "))
	m = u.(chatModel)
	u, _ = m.Update(refreshTickMsg{})
	m = u.(chatModel)

	// Arrives after the last repaint and before the end of the stream.
	u, _ = m.Update(tokenMsg("TRAILING-TOKEN"))
	m = u.(chatModel)
	if strings.Contains(m.viewport.View(), "TRAILING-TOKEN") {
		t.Fatal("the token drew immediately, so this test is not exercising a deferred repaint")
	}

	u, _ = m.Update(streamDoneMsg{})
	m = u.(chatModel)

	if !strings.Contains(m.viewport.View(), "TRAILING-TOKEN") {
		t.Errorf("the last token of the answer was never drawn. The stream ended "+
			"between two ticks and nothing repainted.\nviewport:\n%s", m.viewport.View())
	}
}

// The same ending through the error door and through an interrupt, since both
// go out via endStream and both leave a partial answer that must be visible.
func TestAnInterruptedStreamStillShowsWhatArrived(t *testing.T) {
	m := benchTranscript(2, 40)
	u, _ := m.Update(tokenMsg("PARTIAL-ANSWER"))
	m = u.(chatModel)

	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = u.(chatModel)

	if !strings.Contains(m.viewport.View(), "PARTIAL-ANSWER") {
		t.Errorf("an interrupted stream lost the text that had arrived.\nviewport:\n%s", m.viewport.View())
	}
}

// 3.3d, THE BACKPRESSURE POLICY, stated as a test rather than only as prose.
//
// Nothing queues. A repaint request is one bit, so an unbounded number of
// tokens between two ticks produces exactly one outstanding tick and no growth
// anywhere. Tokens themselves are never dropped -- they are appended to the
// turn as they arrive, which is not throttled -- so what is discarded is
// intermediate FRAMES and nothing else.
func TestTokensOutpacingTheRendererQueueNothing(t *testing.T) {
	a := streamingTracker(t)
	a.token("settle ")
	a.tick()
	atStart := a.m.viewport.View()

	const flood = 20000
	for i := 0; i < flood; i++ {
		a.token("x")
	}
	if a.arms != 2 {
		t.Errorf("%d tokens armed %d repaints in total, want 2: repaint requests must not accumulate", flood, a.arms)
	}
	if a.m.viewport.View() != atStart {
		t.Error("the view changed during the flood; repaints are not coalesced")
	}
	// And no token was lost on the way.
	text := a.m.turns[a.m.streamAssistant].text
	if got := strings.Count(text, "x"); got != flood {
		t.Errorf("%d tokens arrived but the turn holds %d. Coalescing may drop FRAMES, never text.", flood, got)
	}
}

// A tick that arrives after everything is over must be harmless: the stream is
// gone, the model may have moved on, and a stray tick is exactly what a
// cancelled timer looks like.
func TestAStrayTickAfterTheStreamIsHarmless(t *testing.T) {
	m := benchTranscript(2, 40)
	m = deliverToken(m, "answer ")
	u, _ := m.Update(streamDoneMsg{})
	m = u.(chatModel)

	before := m.viewport.View()
	u, _ = m.Update(refreshTickMsg{})
	m = u.(chatModel)
	if after := m.viewport.View(); after != before {
		t.Errorf("a tick arriving after the stream ended changed the view.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
