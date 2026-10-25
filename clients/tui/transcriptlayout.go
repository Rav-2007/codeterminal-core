package tui

import "strings"

// HOW TURNS SIT AGAINST EACH OTHER. renderTranscript and the render cache both
// lay the transcript out through these, so the two cannot disagree -- which the
// equivalence gate asserts over randomised transcripts.

// turnShown reports whether a turn draws anything. An answer that is only
// whitespace does not: a reasoning model streams a newline or two before its
// first tool call, and that drew "Mochiii:" over nothing, left behind when the
// turn stopped -- the owner's "something still visible" (2026-10-25).
func turnShown(t turn) bool {
	return t.role != roleAssistant || strings.TrimSpace(t.text) != ""
}

// renderShownBlock is renderTurnBlock for a turn that draws something, and ""
// for one that does not; separatorBefore leaves no gap for it either.
func renderShownBlock(t turn, width int) string {
	if !turnShown(t) {
		return ""
	}
	return renderTurnBlock(t, width)
}

// separatorBefore is what goes in front of turn i: a blank line after the turn
// shown before it, except inside a run of tool calls, which are stacked
// (activityline.go) -- and nothing at all for a turn that is not shown, or for
// the first one that is.
func separatorBefore(turns []turn, i int) string {
	if !turnShown(turns[i]) {
		return ""
	}
	prev := i - 1
	for prev >= 0 && !turnShown(turns[prev]) {
		prev--
	}
	switch {
	case prev < 0:
		return ""
	case turns[prev].role == roleActivity && turns[i].role == roleActivity:
		return "\n"
	}
	return turnSeparator
}

// appendAnswer adds a streamed chunk to this turn's answer (handleToken).
func (m *chatModel) appendAnswer(chunk string) {
	m.turns[m.answerTurn(chunk)].text += chunk
}

// answerTurn is ensureAssistantTurn for a chunk about to be added to the answer.
//
// AN ANSWER THAT IS SO FAR ONLY WHITESPACE MOVES BELOW THE CALLS MADE SINCE IT
// OPENED (2026-10-25). A reasoning model streams "\n\n" before its first tool
// call, which opened the answer's turn above every call of the run -- so the
// answer that came at the end, "Done: …", was drawn above the work it reported.
// Whitespace draws nothing (turnShown), so moving it costs nothing; an answer
// that has already said something stays where it said it.
func (m *chatModel) answerTurn(chunk string) int {
	i := m.ensureAssistantTurn()
	last := len(m.turns) - 1
	if i == last || strings.TrimSpace(m.turns[i].text) != "" || strings.TrimSpace(chunk) == "" {
		return i
	}
	// A move, not an append: copy the turns after it down one and put it last,
	// so the indices that point past it (activityTurns) shift by one.
	moved := m.turns[i]
	copy(m.turns[i:], m.turns[i+1:])
	m.turns[last] = moved
	for id, at := range m.activityTurns {
		if at > i {
			m.activityTurns[id] = at - 1
		}
	}
	m.streamAssistant = last
	return last
}
