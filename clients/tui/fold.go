package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// A FOLD CHANGES WHAT THIS CLIENT SENDS, NOT WHAT IT SHOWS (daemon/fold.go).
//
// The daemon caps the conversation it gives the model, and used to drop the
// oldest messages past the cap. A daemon that folds writes them into a summary
// instead and says so (protocol.FoldInfo) on the turn it happened. This client
// sends the history with every question, so from then on it sends the summary
// and the turns after the fold point -- and keeps showing every message: the
// person can still read what they wrote, and in inline mode the scrollback is
// not this program's to take back anyway. Replacing the transcript, the way a
// typed /compact does, would print the whole chat again below a rule
// (inline.go) every four exchanges.

// foldMsg is a FoldInfo from the stream.
type foldMsg struct{ info *protocol.FoldInfo }

// foldPoint is what replaces the transcript's earlier part in what is sent.
type foldPoint struct {
	// summary is the two turns the daemon wrote: they open every history sent.
	summary []protocol.Turn
	// from is the index in turns of the first turn kept after the summary, and
	// fromText that turn's text, to find it again if the transcript moved.
	from     int
	fromText string
	// folded is how many messages the summaries stand for in all.
	folded int
	// skipped is the last reason a fold could not be made, so it is said once,
	// not with every question. A point with only this set has no summary yet:
	// it lives here so that everything that forgets the fold point forgets it.
	skipped string
}

// withFoldCap adds CapFold to the capabilities a question is asked with: the
// daemon folds only for a client that sends the summary afterwards.
func withFoldCap(caps []string) []string {
	return append(append([]string(nil), caps...), protocol.CapFold)
}

// handleFold takes a fold: the notice that one is being written, or the result.
// It arrives on the turn's stream like any of its messages, so every way out
// asks for the stream's next one -- found by the live check, 2026-11-27: a
// handler that did not left the turn's answer unread behind it, and the chat
// said "thinking…" for good.
func (m chatModel) handleFold(msg foldMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	next := waitForNext(m.streamCh)
	info := msg.info
	if info == nil || info.Kept < 0 || info.Folded < 0 || info.Dropped < 0 {
		return m, next // not a notice this client can use
	}
	if info.Pending {
		m.foldPending = info.Folded
		return m, next
	}
	m.foldPending = 0
	m.recordSummaryUsage(info.Usage)
	if info.Skipped != "" {
		return m.foldSkipped(info), next
	}
	from, ok := foldStart(m.turns, info.Kept)
	if !ok || !summaryPairOK(info.Summary) {
		// The kept turns cannot be lined up with this transcript. The revision
		// is left as it was, so the next question finds this client behind and
		// is answered from the stored chat, which the client then fetches.
		return m, next
	}
	total := info.Folded
	if m.fold != nil {
		total += m.fold.folded
	}
	m.fold = &foldPoint{summary: info.Summary, from: from, fromText: m.turns[from].text, folded: total}
	m.chatRev, m.chatQuiet = info.Revision, false
	m.appendTurn(turn{role: roleSystem, text: fmt.Sprintf(
		"● %d earlier message%s %s now a summary for the model — the full text stays above",
		info.Folded, plural(info.Folded), isAre(info.Folded))})
	m.resizeViewport()
	m.refreshViewport()
	return m, next
}

// foldSkipped says, once per reason, that the messages this question would
// have folded were left out instead: the old cut, and why (daemon/fold.go).
func (m chatModel) foldSkipped(info *protocol.FoldInfo) chatModel {
	if m.fold == nil {
		m.fold = &foldPoint{}
	}
	if m.fold.skipped == info.Skipped {
		return m
	}
	m.fold.skipped = info.Skipped
	what := "the earliest messages"
	if info.Dropped > 0 {
		what = fmt.Sprintf("the %d earliest message%s", info.Dropped, plural(info.Dropped))
	}
	m.appendTurn(turn{role: roleSystem, text: fmt.Sprintf(
		"● %s could not be summarised (%s), so the model does not have %s this time — /compact writes a summary now",
		what, info.Skipped, itThem(info.Dropped))})
	m.resizeViewport()
	m.refreshViewport()
	return m
}

// summaryPairOK reports whether a fold's summary is the pair it should be: a
// user turn and an assistant turn, both with something in them.
func summaryPairOK(pair []protocol.Turn) bool {
	return len(pair) == 2 && pair[0].Role == "user" && pair[1].Role == "assistant" &&
		strings.TrimSpace(pair[0].Content) != "" && strings.TrimSpace(pair[1].Content) != ""
}

func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// sends reports whether a turn goes into the history this client sends -- the
// rule buildHistory applies, kept beside it here.
func sends(t turn) bool {
	return t.role == roleUser || (t.role == roleAssistant && !t.local)
}

// foldStart finds where the kept turns begin: walking back from the question
// being asked (the newest user turn), past kept turns that are sent. With kept
// 0 the fold point is the question itself.
func foldStart(turns []turn, kept int) (int, bool) {
	q := len(turns) - 1
	for q >= 0 && turns[q].role != roleUser {
		q--
	}
	if q < 0 {
		return 0, false
	}
	from, seen := q, 0
	for i := q - 1; i >= 0 && seen < kept; i-- {
		if sends(turns[i]) {
			seen++
			from = i
		}
	}
	if seen < kept {
		return 0, false
	}
	return from, true
}

// locate is where the fold point is in turns, checked against its turn's text.
//
// IT IS NEVER LOOKED FOR BY TEXT. It used to be, from the newest end, when its
// index no longer held its turn -- and a reply typed twice ("continue", "yes")
// then matched the later copy, and the history sent silently skipped every turn
// between the two (found by QA, 2026-11-28). The index is kept exact instead:
// what drops turns from the front moves it (followFoldPoint). A check that
// fails means the screen changed in a way the point did not follow, and the
// whole transcript is sent, as before folding existed.
func (f *foldPoint) locate(turns []turn) (int, bool) {
	if f.from < len(turns) && turns[f.from].text == f.fromText {
		return f.from, true
	}
	return 0, false
}

// followFoldPoint moves the fold point with a change that dropped the turns
// before index first and put added turns in their place (the eviction marker,
// transcriptbound.go): turn j >= first is now at j-first+added. A point that
// was itself dropped moves to the first turn left -- every turn after it was
// kept, so what is sent is the summary and all of them.
func (m *chatModel) followFoldPoint(first, added int) {
	f := m.fold
	if f == nil || len(f.summary) != 2 {
		return
	}
	if f.from >= first {
		f.from += added - first
		return
	}
	if added < len(m.turns) {
		f.from, f.fromText = added, m.turns[added].text
	}
}

// historyPreview is the history the next question would send: the summary and
// what follows the fold point, or the whole transcript when nothing was folded.
func (m chatModel) historyPreview() []protocol.Turn {
	if m.fold == nil || len(m.fold.summary) != 2 {
		return buildHistory(m.turns)
	}
	from, ok := m.fold.locate(m.turns)
	if !ok {
		return buildHistory(m.turns)
	}
	return append(append([]protocol.Turn(nil), m.fold.summary...), buildHistory(m.turns[from:])...)
}

// historyToSend is historyPreview for a question being asked now. A new turn
// starts here, so a "summarising…" left from a fold that did not finish ends.
func (m *chatModel) historyToSend() []protocol.Turn {
	m.foldPending = 0
	return m.historyPreview()
}

// foldLabel says, after the memory count in /context, that part of it is a
// summary.
func (m chatModel) foldLabel() string {
	if m.fold == nil || m.fold.folded == 0 {
		return ""
	}
	return fmt.Sprintf(", the earlier %d message%s as a summary", m.fold.folded, plural(m.fold.folded))
}

// foldSummaryText is what /context shows after the memory line once the chat
// was folded: the summary the model has in place of the earlier messages, so
// the person can read what is remembered for them. Sanitised like everything
// the model wrote.
func (m chatModel) foldSummaryText() string {
	if m.fold == nil || len(m.fold.summary) != 2 {
		return ""
	}
	return "\n\nwhat the model has in place of the earlier messages:\n" + sanitizeText(summaryBodyOf(m.fold.summary[1].Content))
}

// foldedForSave is what a save sends along once the chat has folded: the
// messages before the fold point, as this screen still has them -- the ones
// the daemon summarised and no longer keeps. The daemon uses them only for a
// chat that was never saved (daemon/fold.go, saveFoldedChatLocked), so the
// saved copy holds what the person can read, not the summary (the owner's
// decision, 2026-11-28).
func (m chatModel) foldedForSave() protocol.HistoryRequestExtras {
	if m.fold == nil || len(m.fold.summary) != 2 {
		return protocol.HistoryRequestExtras{}
	}
	from, ok := m.fold.locate(m.turns)
	if !ok {
		return protocol.HistoryRequestExtras{}
	}
	return protocol.HistoryRequestExtras{Folded: buildHistory(m.turns[:from])}
}

// savedFromScreenNote is what a save's reply adds when the saved copy was made
// from this screen and the screen had already dropped its oldest turns to bound
// memory (transcriptbound.go): those are not in it. "" otherwise.
func (m chatModel) savedFromScreenNote(resp protocol.HistoryResponse) string {
	if !resp.FoldedSaved || m.evictedTurns == 0 {
		return ""
	}
	return fmt.Sprintf("the %d earliest turn%s had already left this screen to bound memory, so the saved copy begins after them",
		m.evictedTurns, plural(m.evictedTurns))
}

// summaryBodyOf is a summary turn without its lead line.
func summaryBodyOf(text string) string {
	if _, body, ok := strings.Cut(text, "\n\n"); ok {
		return strings.TrimSpace(body)
	}
	return text
}

// foldIndicator is drawn under the chat while a question waits for its summary.
func (m chatModel) foldIndicator() (string, bool) {
	if m.foldPending <= 0 || !m.turnInFlight() {
		return "", false
	}
	return helpStyle.Render(fmt.Sprintf("⏳ summarising %d earlier message%s so %s stay%s in the conversation…",
		m.foldPending, plural(m.foldPending), itThey(m.foldPending), sIfOne(m.foldPending))), true
}

func itThey(n int) string {
	if n == 1 {
		return "it"
	}
	return "they"
}

func sIfOne(n int) string {
	if n == 1 {
		return "s"
	}
	return ""
}
