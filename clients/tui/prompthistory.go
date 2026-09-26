package main

import (
	"strings"

	"mochiii/protocol"
)

// PROMPT HISTORY, LIKE A SHELL. Up recalls the prompt sent before the one on
// screen, down moves forward again, and past the newest one the text that was
// being typed when browsing began comes back. Seeded from the saved
// conversation, so prompts from earlier sessions are there after a restart.

// maxPromptHistory bounds what is kept; older prompts drop off the front.
const maxPromptHistory = 200

// promptsFrom collects the user's prompts from a hydrated conversation.
func promptsFrom(turns []protocol.Turn) []string {
	var out []string
	for _, t := range turns {
		if t.Role == "user" {
			out = appendPrompt(out, sanitizeText(strings.TrimSpace(t.Content)))
		}
	}
	return out
}

// appendPrompt adds p unless it is empty or repeats the newest entry.
func appendPrompt(h []string, p string) []string {
	if p == "" || (len(h) > 0 && h[len(h)-1] == p) {
		return h
	}
	h = append(h, p)
	if len(h) > maxPromptHistory {
		h = h[len(h)-maxPromptHistory:]
	}
	return h
}

// rememberPrompt records a submitted prompt and ends any browsing.
func (m *chatModel) rememberPrompt(p string) {
	m.promptHistory = appendPrompt(m.promptHistory, p)
	m.historyIdx, m.historyDraft = -1, ""
}

func (m *chatModel) showPrompt(p string) {
	m.input.SetValue(p)
	m.input.SetCursor(len(p))
}

// historyUp shows the previous prompt; at the oldest it stays there.
func (m *chatModel) historyUp() {
	if len(m.promptHistory) == 0 {
		return
	}
	switch {
	case m.historyIdx == -1:
		m.historyDraft = m.input.Value()
		m.historyIdx = len(m.promptHistory) - 1
	case m.historyIdx > 0:
		m.historyIdx--
	}
	m.showPrompt(m.promptHistory[m.historyIdx])
}

// historyDown shows the next prompt, and past the newest the saved draft.
func (m *chatModel) historyDown() {
	if m.historyIdx == -1 {
		return
	}
	m.historyIdx++
	if m.historyIdx >= len(m.promptHistory) {
		m.historyIdx = -1
		m.showPrompt(m.historyDraft)
		m.historyDraft = ""
		return
	}
	m.showPrompt(m.promptHistory[m.historyIdx])
}
