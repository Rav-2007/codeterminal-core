package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// WHAT IS ASKED ABOUT IS WHAT WAS SHOWN.
//
// THE DEFECT (FOUND 2026-10-12, reproduced by approvalview_test.go before any
// of this existed). The approval panel is the last thing in the transcript, and
// the view followed the transcript to its bottom. A call whose arguments wrapped
// to more lines than the terminal has was therefore asked about with its tool
// name and its first arguments above the top of the screen: 24 rows of the tail
// of some `content`, then "y yes". The panel's own promise -- "nothing is
// truncated: the approval binds to a digest of all of it, so all of it is
// shown" -- was true of the string and false of the screen, and no key scrolled.
//
// Two more ways to the same place. A question that arrived while the user was
// scrolled back was not on screen at all, because the view follows only for
// someone at the bottom. And who decides how long the arguments are is the
// model, which is the party the question exists to check.
//
// THE RULE NOW. A question is shown from its BEGINNING. If it does not fit, the
// arrow and page keys scroll it, the key line says how much is unread, and the
// keys that agree do nothing until every line of it has been on screen, in
// order. Declining is never gated: n and esc answer from the first frame.

// approvalReading is how much of the pending question has been shown.
type approvalReading struct {
	// req is the question being read; nil when none is pending.
	req *protocol.ToolApprovalRequest
	// width and height are the viewport it was laid out for. A resize rewraps
	// the question, so the reading starts again.
	width, height int
	// top is the question's first line in the viewport's content, and lines how
	// many it takes there.
	top, lines int
	// shownTo is the last content line shown WITHOUT A GAP since top; top-1
	// when nothing has been. A jump to the end leaves a gap and moves nothing:
	// the lines skipped were never on screen.
	shownTo int
}

// trackApprovalReading is called whenever the viewport's content was set. A new
// question, or one rewrapped by a resize, is put at the top of the screen (or
// the view at the bottom, when the whole question fits above it); after that
// the view is left where the user put it and only what it shows is recorded.
func (m *chatModel) trackApprovalReading() {
	// Not before the terminal's size is known: there is no screen yet for
	// anything to be off, and a viewport of height zero would call every
	// question unread. The first WindowSizeMsg arrives before any turn can.
	if m.state != stateToolApproval || m.pendingApproval == nil || !m.ready {
		m.approvalRead = approvalReading{}
		return
	}
	panel := wrapToWidth(renderApprovalPanel(*m.pendingApproval), m.viewport.Width)
	lines := strings.Count(panel, "\n") + 1

	r := &m.approvalRead
	if r.req != m.pendingApproval || r.width != m.viewport.Width || r.height != m.viewport.Height || r.lines != lines {
		top := m.viewport.TotalLineCount() - lines
		*r = approvalReading{
			req: m.pendingApproval, width: m.viewport.Width, height: m.viewport.Height,
			top: top, lines: lines, shownTo: top - 1,
		}
		if lines > m.viewport.Height {
			m.viewport.SetYOffset(top)
		} else {
			m.viewport.GotoBottom()
		}
	}
	m.noteApprovalShown()
}

// noteApprovalShown records what the viewport is showing now. Called after
// anything that can move it: a key, the mouse wheel, a repaint.
func (m *chatModel) noteApprovalShown() {
	r := &m.approvalRead
	if r.req == nil {
		return
	}
	first := m.viewport.YOffset
	last := first + m.viewport.Height - 1
	if first <= r.shownTo+1 && last > r.shownTo {
		r.shownTo = last
	}
}

// approvalUnread is how many lines of the pending question have not been on
// screen yet. Zero when there is no question, or all of it has been shown.
func (m chatModel) approvalUnread() int {
	r := m.approvalRead
	if r.req == nil {
		return 0
	}
	if unread := r.top + r.lines - 1 - r.shownTo; unread > 0 {
		return unread
	}
	return 0
}

// approvalScrollKey scrolls the question for the four keys that move through
// text and reports whether it took the key. Only named keys: the viewport's own
// bindings include j, k, f, b, u, d and space, and a letter typed at a security
// question must never turn out to have been a command.
func (m *chatModel) approvalScrollKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyUp:
		m.viewport.ScrollUp(1)
	case tea.KeyDown:
		m.viewport.ScrollDown(1)
	case tea.KeyPgUp:
		m.viewport.PageUp()
	case tea.KeyPgDown:
		m.viewport.PageDown()
	default:
		return false
	}
	m.noteApprovalShown()
	return true
}

// approvalKeysLine is the key line under a pending question: the keys that
// answer it, or -- while part of it is unread -- how to read the rest. The
// keys that agree are not listed until they work.
func (m chatModel) approvalKeysLine(keys string) string {
	unread := m.approvalUnread()
	if unread == 0 {
		return keys
	}
	// SHORT ENOUGH FOR 80 COLUMNS, with the way out at the end still on it: the
	// first wording ran to 84 characters and the prompt box cut "n no · esc
	// stop" off at the edge (seen in the real app, 2026-10-12).
	return fmt.Sprintf("↓ PgDn to read on · %d more line(s) before yes · n no · esc stop", unread)
}
