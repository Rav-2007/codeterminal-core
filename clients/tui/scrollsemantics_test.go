package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// SCROLLING BACK DURING A STREAM.
//
// refreshViewport ends in GotoBottom, and refreshViewport runs on every token.
// So reading back through a long answer while it is still arriving is not
// merely awkward, it is impossible: the view snaps to the bottom within
// milliseconds, every time, and the text the user was reading is gone.
//
// The two halves are separate tests on purpose. They pull in opposite
// directions -- a fix that never scrolls to the bottom breaks following a
// stream, and a fix that always does is the defect -- so a single test could be
// satisfied by either mistake.

// streamingModelWithBacklog returns a model mid-stream whose transcript is far
// taller than the viewport, which is the only situation where scrolling means
// anything.
func streamingModelWithBacklog(t *testing.T) chatModel {
	t.Helper()
	m := benchTranscript(60, 400)
	if m.viewport.TotalLineCount() <= m.viewport.Height {
		t.Fatalf("the transcript fits on screen (%d lines in %d rows), so there is nothing to scroll",
			m.viewport.TotalLineCount(), m.viewport.Height)
	}
	return m
}

func sendToken(m chatModel, text string) chatModel {
	u, _ := m.Update(tokenMsg(text))
	return u.(chatModel)
}

// A user who has scrolled back must not be yanked to the bottom by the next
// token to arrive.
func TestAStreamDoesNotYankAScrolledUserToTheBottom(t *testing.T) {
	m := streamingModelWithBacklog(t)

	u, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = u.(chatModel)
	for i := 0; i < 8; i++ {
		u, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		m = u.(chatModel)
	}
	scrolled := m.viewport.YOffset
	if m.viewport.AtBottom() {
		t.Fatalf("the wheel did not scroll: still at the bottom, YOffset %d", scrolled)
	}

	m = sendToken(m, "one more token ")

	if m.viewport.AtBottom() {
		t.Errorf("a streamed token yanked the view from YOffset %d back to the bottom. "+
			"Reading back through an answer while it arrives is impossible while this is true.", scrolled)
	}
	if m.viewport.YOffset != scrolled {
		t.Errorf("a streamed token moved the view from YOffset %d to %d", scrolled, m.viewport.YOffset)
	}
}

// And the ordinary case, which is the whole reason GotoBottom is there: someone
// watching an answer arrive must keep seeing the newest text.
func TestAStreamKeepsFollowingForAUserAtTheBottom(t *testing.T) {
	m := streamingModelWithBacklog(t)
	if !m.viewport.AtBottom() {
		t.Fatal("the fixture did not start pinned to the bottom")
	}

	for i := 0; i < 20; i++ {
		m = sendToken(m, strings.Repeat("more text ", 12))
	}

	if !m.viewport.AtBottom() {
		t.Errorf("the view stopped following the stream: YOffset %d of %d lines, height %d",
			m.viewport.YOffset, m.viewport.TotalLineCount(), m.viewport.Height)
	}
}

// Scrolling back and then returning to the bottom must re-arm following. A fix
// that latches "the user scrolled once" and never follows again would pass both
// tests above and still be wrong.
func TestReturningToTheBottomResumesFollowing(t *testing.T) {
	m := streamingModelWithBacklog(t)

	for i := 0; i < 8; i++ {
		u, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		m = u.(chatModel)
	}
	if m.viewport.AtBottom() {
		t.Fatal("the wheel did not scroll")
	}
	m.viewport.GotoBottom()

	for i := 0; i < 10; i++ {
		m = sendToken(m, strings.Repeat("more text ", 12))
	}
	if !m.viewport.AtBottom() {
		t.Errorf("following did not resume after the user scrolled back to the bottom: "+
			"YOffset %d of %d lines", m.viewport.YOffset, m.viewport.TotalLineCount())
	}
}
