package tui

import (
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// AUTO-APPROVE (owner, 2026-11-21, showing Cline's "Auto-approve all": "when a
// user clicks ctrl+space then the automation turn-on").
//
// ctrl+space -- or /auto, where the terminal or the system takes that key (a
// Mac switches keyboard language with it) -- turns it on and off. While it is
// on, a tool call that would stop and ask is answered yes, as pressing y would
// answer it, without the question being drawn; an edit offered for review is
// applied, as y would apply it, backups and all. What it decides is recorded:
// the call's own line in the chat says it ran (no "✓ auto-approved …" line
// besides -- the owner's word, 2026-11-21), the edits' summary lists every file,
// and the daemon's audit log says auto_approve, not user_approve
// (protocol.ToolApprovalResponse.Auto).
//
// IT ANSWERS ONLY WHAT WOULD BE ASKED. A call the daemon refuses -- a read of
// this machine's keys, a tool the configuration denies -- is refused whatever
// this says; nothing here reaches past the question.
//
// "ALMOST EVERYTHING" (the owner's word for its reach), so two questions are
// still asked (autoSkips): a read OUTSIDE this project, and a command that
// would run in the project folder ITSELF because no private copy could be
// made. Web search no longer asks at all (2026-11-21), and these two are what
// an instruction hidden in a web page would most want answered for it: a file
// from elsewhere on the machine, or a write to the real project -- its
// .git/hooks included -- that nobody reviews.
//
// OFF AT EVERY START (the owner's choice): it is never saved, so a session that
// starts asks until it is told not to. The rows under the prompt say which it
// is, and so does the line above the prompt, which every window has room for.

const (
	// autoApproveKey is what ctrl+space arrives as: the terminal sends NUL for
	// it, and Bubble Tea names NUL ctrl+@.
	autoApproveKey = "ctrl+@"
	// autoApproveDecision is what auto-approve puts on the answer channel: y's
	// decision, marked, so the stream says Auto on the answer (stream.go).
	autoDecisionPrefix  = "auto:"
	autoApproveDecision = autoDecisionPrefix + protocol.ApprovalApprove
	autoMarkOn          = "▸▸"
	autoMarkOff         = "▹▹"
)

var (
	autoOnStyle  = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
	autoOffStyle = lipgloss.NewStyle().Foreground(colorDim)
)

// autoSkips is why auto-approve leaves this question to the person, "" when it
// answers it.
func autoSkips(req protocol.ToolApprovalRequest) string {
	switch {
	case req.OutsidePath != "":
		return "it reads outside this project"
	case req.InPlace:
		return "it runs in the project itself, not a copy"
	}
	return ""
}

// toggleAutoApprove is ctrl+space. Turned on while a question it covers is
// waiting, it answers that question at once, as it would have been answered
// had it been on when the question came.
func (m chatModel) toggleAutoApprove() (tea.Model, tea.Cmd) {
	return m.setAutoApprove(!m.autoApprove)
}

// An edit waiting for review is applied by autoApplyEdits, which Update runs
// after every message.
func (m chatModel) setAutoApprove(on bool) (tea.Model, tea.Cmd) {
	m.autoApprove = on
	if on && m.state == stateToolApproval && m.pendingApproval != nil && autoSkips(*m.pendingApproval) == "" {
		return m.answerAuto()
	}
	return m, nil
}

// HOW IT ANSWERS. Update (inline.go) hands a question auto-approve covers to
// autoAnswer instead of the chat's own handler, so it is answered before it is
// drawn; and after every message it lets autoApplyEdits apply what is waiting
// for review. Neither touches chat.go: lines added there re-window the
// retrieval eval's view of the file (scripts/eval-scope.sh).

// autoCovers reports whether auto-approve answers this question now.
func (m chatModel) autoCovers(req protocol.ToolApprovalRequest) bool {
	return m.autoApprove && m.streamCh != nil && autoSkips(req) == ""
}

// autoAnswer answers a question auto-approve covers, before it is drawn, and
// keeps the stream drained, as the chat's own handler does after a key.
func (m chatModel) autoAnswer(msg toolApprovalMsg) (tea.Model, tea.Cmd) {
	msg.reply <- autoApproveDecision // buffered, one send: cannot block (answerApproval)
	return m, waitForNext(m.streamCh)
}

// answerAuto is answerApproval for auto-approve's yes, to a question already
// on screen: the same answer, and no "✓ approved" line (owner, 2026-11-21:
// "remove the background stuffs like this ✓ auto-approved builtin__…") -- the
// call's own line says it ran, and the audit log says auto-approve answered.
func (m chatModel) answerAuto() (tea.Model, tea.Cmd) {
	if m.approvalReply == nil {
		return m, nil
	}
	m.approvalReply <- autoApproveDecision
	m.pendingApproval, m.approvalReply = nil, nil
	m.state = stateStreaming
	m.refreshViewport()
	return m, nil
}

// autoApplyEdits applies, while auto-approve is on, every edit waiting for
// review, as y would apply each -- backups and all -- until the review is
// done. It reports whether it applied any (Update hands back a model it did
// not change as it was, inline.go).
func (m *chatModel) autoApplyEdits(cmd *tea.Cmd) bool {
	applied := false
	for m.autoApprove && m.state == stateEditReview && m.reviewPrepared != nil {
		next, c := m.applyCurrentReviewEdit()
		*m, *cmd, applied = next.(chatModel), tea.Batch(*cmd, c), true
	}
	return applied
}

// autoCommand is /auto: on, off, or on its own the other one.
func (m chatModel) autoCommand(args string) (tea.Model, tea.Cmd) {
	on := !m.autoApprove
	switch strings.ToLower(args) {
	case "":
	case "on":
		on = true
	case "off":
		on = false
	default:
		return m.sayLocally("usage: /auto [on|off] — or ctrl+space")
	}
	next, cmd := m.setAutoApprove(on)
	nm := next.(chatModel)
	if on {
		next, _ = nm.sayLocally(autoMarkOn + " auto-approve is on: tool calls run without asking, and edits are applied, except a read outside " +
			"this project and a command that would run in the project folder itself. ctrl+space or /auto turns it off.")
	} else {
		next, _ = nm.sayLocally("auto-approve is off: every tool call and edit asks again.")
	}
	return next, cmd
}

// autoAppliedLines lists, for the edits' summary, what auto-approve applied:
// no one read those diffs, so the files are named.
func (m chatModel) autoAppliedLines() []string {
	if !m.autoApprove {
		return nil
	}
	lines := make([]string, 0, len(m.reviewAppliedPaths))
	for _, p := range m.reviewAppliedPaths {
		lines = append(lines, "  "+autoMarkOn+" auto-applied: "+sanitizeText(p))
	}
	return lines
}

// autoHeaderBadge is what the line above the prompt carries while it is on.
func (m chatModel) autoHeaderBadge() string {
	if !m.autoApprove {
		return ""
	}
	return "  " + autoOnStyle.Render(autoMarkOn+" auto")
}

// autoRow is the auto-approve row under the prompt: on or off, and the key.
func (m chatModel) autoRow() string {
	var row string
	switch {
	case !m.autoApprove:
		row = autoOffStyle.Render(autoMarkOff + " auto-approve off (ctrl+space)")
	case m.state == stateToolApproval && m.pendingApproval != nil && autoSkips(*m.pendingApproval) != "":
		row = autoOnStyle.Render(autoMarkOn+" auto-approve on") + autoOffStyle.Render(" · this one asks: "+autoSkips(*m.pendingApproval))
	default:
		row = autoOnStyle.Render(autoMarkOn+" auto-approve on") + autoOffStyle.Render(" · tools run without asking (ctrl+space)")
	}
	return ansi.Truncate(row, max(m.width, 1), "…")
}

// repoLabel is the project for the bottom right corner: its folder's name, and
// nothing else -- no git branch (owner, 2026-11-21: "it should not show any
// github branches in the tui"). Made one clean line before it is drawn: a
// folder's name is whatever its creator chose.
func (m chatModel) repoLabel() string {
	return repoNameStyle.Render(oneCleanLine(filepath.Base(m.workspaceRoot)))
}

var repoNameStyle = lipgloss.NewStyle().Foreground(colorGray)

// contextGauge is how full the model's context was at the last call: a bar
// and the count against the model's limit, amber from three quarters and red
// from nine tenths -- or the count alone when the limit is not known. "" before
// any turn has said.
func (m chatModel) contextGauge() string {
	u := m.lastUsage
	if u == nil || u.ContextTokens <= 0 {
		return ""
	}
	if u.ContextWindow <= 0 {
		return statusLineStyle.Render(compactTokens(u.ContextTokens) + " context")
	}
	const cells = 8
	frac := float64(u.ContextTokens) / float64(u.ContextWindow)
	filled := min(cells, int(frac*cells+0.5))
	if filled == 0 {
		filled = 1 // something is in it
	}
	return gaugeFill(frac).Render(strings.Repeat("█", filled)) + gaugeTrackStyle.Render(strings.Repeat("█", cells-filled)) +
		statusLineStyle.Render(" "+compactTokens(u.ContextTokens)+"/"+compactTokens(u.ContextWindow))
}

// gaugeFill is the bar's colour for a context this full.
func gaugeFill(frac float64) lipgloss.Style {
	switch {
	case frac >= 0.9:
		return gaugeFullStyle
	case frac >= 0.75:
		return gaugeHighStyle
	}
	return gaugeStyle
}

var (
	gaugeStyle      = lipgloss.NewStyle().Foreground(colorGray)
	gaugeHighStyle  = lipgloss.NewStyle().Foreground(colorAmber)
	gaugeFullStyle  = lipgloss.NewStyle().Foreground(colorRed)
	gaugeTrackStyle = lipgloss.NewStyle().Foreground(colorChip)
)
