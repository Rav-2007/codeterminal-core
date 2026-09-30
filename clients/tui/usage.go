package main

import (
	"fmt"
	"regexp"
	"strings"

	"mochiii/protocol"
)

// /usage: TOKENS AND DOLLARS THIS SESSION HAS SPENT, and how full the context
// is -- from the provider's own bill, never an estimate. The daemon sums every
// model call a turn made (agent loop, /team phases, retries) and sends the
// total on the turn's final message (protocol.TurnUsage); this file adds those
// up and draws them. It answers locally: /usage costs no model call.

// usageTotals adds up turns' bills.
type usageTotals struct {
	turns, calls                        int
	stopped                             int // turns the user stopped, counted in turns
	prompt, completion, cached, reasons int
	cost                                float64
	costMissing                         bool
}

func (t *usageTotals) add(u protocol.TurnUsage) {
	t.turns++
	t.calls += u.Calls
	t.prompt += u.PromptTokens
	t.completion += u.CompletionTokens
	t.cached += u.CachedTokens
	t.reasons += u.ReasoningTokens
	t.cost += u.CostUSD
	t.costMissing = t.costMissing || u.CostMissing
}

// recordUsage adds a finished turn's bill to this chat's and this session's
// totals, and keeps it as the latest word on the context.
func (m *chatModel) recordUsage(u *protocol.TurnUsage) {
	if u == nil {
		return
	}
	m.chatUsage.add(*u)
	m.sessionUsage.add(*u)
	last := *u
	m.lastUsage = &last
}

// recordStoppedUsage adds the bills of turns the user stopped. A stopped turn
// sends no final message, so the daemon keeps its bill and sends it on the next
// turn's. They are in order, so the newest stopsInChat of them were stopped in
// this chat; any older ones belong to a chat since left, and count for the
// session only. The context line is left alone: it describes the newest call.
func (m *chatModel) recordStoppedUsage(stopped []protocol.TurnUsage) {
	here := min(m.stopsInChat, len(stopped))
	for i, u := range stopped {
		m.sessionUsage.addStopped(u)
		if i >= len(stopped)-here {
			m.chatUsage.addStopped(u)
		}
	}
	if len(stopped) > 0 {
		m.stopsInChat = 0
	}
}

func (t *usageTotals) addStopped(u protocol.TurnUsage) {
	t.add(u)
	t.stopped++
}

// usageReport renders /usage.
func (m chatModel) usageReport() string {
	if m.sessionUsage.turns == 0 {
		return "usage: no model calls yet in this session -- /usage counts from the first answer"
	}
	var b strings.Builder
	b.WriteString("usage (as billed by the provider; context is the last call's)")
	if m.chatUsage.turns == 0 {
		b.WriteString("\n  this chat     nothing yet")
	} else {
		writeUsageRows(&b, "this chat", m.chatUsage)
	}
	writeUsageRows(&b, "this session", m.sessionUsage)
	if u := m.lastUsage; u != nil && u.ContextTokens > 0 {
		model := u.Model
		if model == "" {
			model = "the model"
		}
		if u.ContextWindow > 0 {
			pct := 100 * float64(u.ContextTokens) / float64(u.ContextWindow)
			fmt.Fprintf(&b, "\n  context       %s of %s tokens (%s) · %s",
				compactTokens(u.ContextTokens), compactTokens(u.ContextWindow), percent(pct), model)
		} else {
			fmt.Fprintf(&b, "\n  context       %s tokens · %s (window unknown)",
				compactTokens(u.ContextTokens), model)
		}
	}
	return b.String()
}

func writeUsageRows(b *strings.Builder, label string, t usageTotals) {
	stopped := ""
	if t.stopped > 0 {
		stopped = fmt.Sprintf(" (%d stopped)", t.stopped)
	}
	fmt.Fprintf(b, "\n  %-13s %d turn%s%s · %d model call%s", label, t.turns, plural(t.turns), stopped, t.calls, plural(t.calls))
	in := "in " + compactTokens(t.prompt)
	if t.cached > 0 {
		in += " (" + compactTokens(t.cached) + " cached)"
	}
	out := "out " + compactTokens(t.completion)
	if t.reasons > 0 {
		out += " (" + compactTokens(t.reasons) + " reasoning)"
	}
	// The cost on a row of its own: on the in/out row it made the line 82
	// columns, and an 80-column terminal wrapped "at least" away from its
	// amount (seen live, 2026-09-30).
	fmt.Fprintf(b, "\n  %-13s %s · %s", "", in, out)
	fmt.Fprintf(b, "\n  %-13s cost %s", "", dollars(t))
}

// dollars is the cost as billed: a lower bound when some call reported none.
func dollars(t usageTotals) string {
	switch {
	case t.costMissing && t.cost == 0:
		return "not reported"
	case t.costMissing:
		return fmt.Sprintf("at least $%.4f", t.cost)
	}
	return fmt.Sprintf("$%.4f", t.cost)
}

// compactTokens renders 812, 41.2K, 1.02M.
func compactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// percent shows a small share as "<1%" rather than a misleading "0%".
func percent(p float64) string {
	if p > 0 && p < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", p)
}

// A BARE "/word" THAT IS NOT A COMMAND is answered here, not sent to the
// model. Typed "/usage" before it existed went out as a question, and the model
// answered a whole screen about a command that did not exist -- a model call
// spent on a typo. Only the bare form is caught: "/etc/hosts is broken" or
// "/tmp is full" carry more than one word (or another slash) and still go to
// the model as the questions they are.
var bareCommandWord = regexp.MustCompile(`^/[a-z][a-z0-9-]*$`)

// unknownCommandNote returns what to say about raw, or "" when it is not a
// bare unknown command.
func unknownCommandNote(raw string) string {
	if !bareCommandWord.MatchString(raw) {
		return ""
	}
	name := raw[1:]
	if lookupSlash(name) != nil || name == "model" {
		return ""
	}
	note := "unknown command " + raw + " -- /help lists the commands"
	if near := nearestCommand(name); near != "" {
		note = "unknown command " + raw + " -- did you mean /" + near + "? (/help lists them all)"
	}
	return note
}

// nearestCommand is the catalog command within two edits of name, if any.
func nearestCommand(name string) string {
	best, bestDist := "", 3
	for _, d := range slashCatalog {
		if dist := editDistance(name, d.Name); dist < bestDist {
			best, bestDist = d.Name, dist
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
