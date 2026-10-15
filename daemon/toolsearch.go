package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// A TOOL LEFT OFF THE MENU MUST NOT BE A TOOL THE MODEL CAN NEVER HAVE.
//
// The advertised cap (mcp.budget.max_advertised_tools) is a quality limit -- a
// wider menu measurably made the model choose worse -- and it stays. What was
// wrong is what happened to the tools past it: they were gone for the turn, and
// the user was handed a notice naming a config key. MEASURED 2026-10-15: with
// Supabase's 29 tools connected, "which project is active in supabase" ran
// without supabase__list_projects on the menu, and the model said there was no
// active project.
//
// Two halves fix it. The registry now keeps the tools that best match the
// request (mcp/relevance.go), and this file gives the model find_tools: when
// anything was left out, it can describe what it needs and the matching tools
// join its menu for the rest of the turn.
//
// WHAT find_tools CANNOT DO, by construction:
//
//   - Reach a tool the configuration denies. It searches Registry.DroppedTools,
//     which only ever holds tools that passed the deny filter.
//   - Reach a tool this step's role or this turn's mode withholds. The
//     candidates are filtered here exactly as the menu is, and dispatch checks
//     both again (resolveExecutable) whatever the menu said.
//   - Run anything. It reads names and descriptions the registry already holds;
//     a tool it finds asks for approval like any other when it is CALLED.
//
// It is answered by the loop, not registered as a built-in: it has no handler
// worth auditing, no policy a user should have to configure, and what it
// changes -- this turn's menu -- is the loop's own state.

const findToolsName = "find_tools"

// findToolsQualified is the name the model is offered. Under "builtin__" like
// every confined tool: the namespace no configured server may take
// (mcp.ValidateServerName), so no server can shadow it.
var findToolsQualified = mcp.BuiltinServerName + mcp.QualifiedNameSeparator + findToolsName

const (
	// maxFoundPerCall bounds one lookup's answer, and so how far one call can
	// widen the menu.
	maxFoundPerCall = 5
	// maxFoundPerTurn bounds how far the menu can grow over a whole turn, so
	// repeated lookups cannot rebuild the wide menu the cap exists to avoid.
	maxFoundPerTurn = 15
)

// toolSearch is one turn's find_tools state.
type toolSearch struct {
	candidates []mcp.Tool
	added      map[string]bool
	queries    map[string]bool
}

// newToolSearch prepares the lookup over the tools the cap left out, keeping
// only those allowed reports true for. Nil when nothing is left to find, so the
// turn offers no lookup that could only ever answer "nothing".
func newToolSearch(dropped []mcp.Tool, allowed func(mcp.Tool) bool) *toolSearch {
	var candidates []mcp.Tool
	for _, t := range dropped {
		if allowed(t) {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return &toolSearch{candidates: candidates, added: map[string]bool{}, queries: map[string]bool{}}
}

// isFindToolsCall reports whether the model is calling the lookup, by its
// qualified name or the bare one models often use.
func isFindToolsCall(name string) bool {
	return name == findToolsQualified || name == findToolsName
}

// spec is find_tools as offered to the model. Its description says which
// servers have tools waiting, so the model knows when a lookup is worth it.
func (ts *toolSearch) spec() toolSpec {
	return toolSpec{
		Type: "function",
		Function: toolSpecFunction{
			Name: findToolsQualified,
			Description: fmt.Sprintf("Find more tools. To keep your tool list short, %d connected tool(s) are not "+
				"on it yet (%s). If the task needs a capability you do not see, describe it in a few words -- for "+
				"example \"list supabase projects\" -- and the matching tools are added to your list so you can "+
				"call them next. Finding a tool runs nothing.",
				len(ts.candidates), ts.serverSummary()),
			Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string",` +
				`"description":"What the tool should do, in a few words."}},"required":["query"]}`),
		},
	}
}

// serverSummary is "supabase: 18, github: 3", largest first.
func (ts *toolSearch) serverSummary() string {
	counts := map[string]int{}
	for _, t := range ts.candidates {
		counts[t.Server]++
	}
	servers := make([]string, 0, len(counts))
	for s := range counts {
		servers = append(servers, s)
	}
	sort.Slice(servers, func(i, j int) bool {
		if counts[servers[i]] != counts[servers[j]] {
			return counts[servers[i]] > counts[servers[j]]
		}
		return servers[i] < servers[j]
	})
	parts := make([]string, len(servers))
	for i, s := range servers {
		parts[i] = fmt.Sprintf("%s: %d", s, counts[s])
	}
	return strings.Join(parts, ", ")
}

// answer runs one lookup. It returns the text the model is sent, the tools to
// add to the menu, and whether this exact query was already asked this turn
// (the caller counts that as a repeat, as for any identical call).
func (ts *toolSearch) answer(arguments string) (content string, found []mcp.Tool, repeat bool) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil || strings.TrimSpace(args.Query) == "" {
		return `find_tools needs a "query": what the tool should do, in a few words.`, nil, false
	}
	query := strings.TrimSpace(args.Query)
	key := strings.ToLower(query)
	repeat = ts.queries[key]
	ts.queries[key] = true

	room := maxFoundPerTurn - len(ts.added)
	if room <= 0 {
		return fmt.Sprintf("No more tools can be added this turn: %d already were, which is the limit. "+
			"Work with the tools you have, or answer from what you know.", maxFoundPerTurn), nil, repeat
	}
	var pool []mcp.Tool
	for _, t := range ts.candidates {
		if !ts.added[t.QualifiedName()] {
			pool = append(pool, t)
		}
	}
	limit := maxFoundPerCall
	if room < limit {
		limit = room
	}
	found = mcp.SearchTools(pool, query, limit)
	if len(found) == 0 {
		return fmt.Sprintf("No tool that is not already on your list matches %q. The tools not on your list "+
			"come from: %s. Try other words, or work with the tools you have.", query, ts.serverSummary()), nil, repeat
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Added %d tool(s) to your list; call them by these names:\n", len(found))
	for _, t := range found {
		ts.added[t.QualifiedName()] = true
		desc := strings.Join(strings.Fields(t.Description), " ")
		if r := []rune(desc); len(r) > 200 {
			desc = string(r[:200]) + "…"
		}
		fmt.Fprintf(&b, "- %s: %s\n", t.QualifiedName(), desc)
	}
	return b.String(), found, repeat
}

// toolSpecFor is a registry tool as the model is offered it -- the same shape
// advertisedToolSpecs builds for the menu.
func toolSpecFor(t mcp.Tool) toolSpec {
	return toolSpec{
		Type: "function",
		Function: toolSpecFunction{
			Name:        t.QualifiedName(),
			Description: t.Description,
			Parameters:  t.Schema,
		},
	}
}

// findToolsActivity reports a lookup on the activity stream the way a
// permitted built-in call is reported -- requested, running, done -- so the
// work block shows a "find_tools" row like any other step.
func findToolsActivity(onActivity func(protocol.ToolActivity), callID string, found int) {
	if onActivity == nil {
		return
	}
	base := protocol.ToolActivity{CallID: callID, Server: mcp.BuiltinServerName, Tool: findToolsName}
	for _, phase := range []string{protocol.ToolPhaseRequested, protocol.ToolPhaseRunning} {
		a := base
		a.Phase = phase
		onActivity(a)
	}
	done := base
	done.Phase = protocol.ToolPhaseSucceeded
	done.Detail = fmt.Sprintf("found %d tool(s)", found)
	onActivity(done)
}

// menuQuery is the text the menu is ranked against: the latest question and
// the one before it, so a short follow-up ("yes, list them") still carries the
// words of what it follows up.
func menuQuery(messages []chatMessage) string {
	var parts []string
	for i := len(messages) - 1; i >= 0 && len(parts) < 2; i-- {
		if messages[i].Role == "user" {
			parts = append(parts, lastUserQuestion(messages[i:i+1]))
		}
	}
	return strings.Join(parts, "\n")
}
