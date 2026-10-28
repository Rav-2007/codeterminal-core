package mcp

import (
	"sort"
	"strings"
	"unicode"
)

// WHICH TOOLS MAKE THE MENU WHEN NOT ALL OF THEM CAN.
//
// The advertised cap (Registry.maxAdvertised) used to keep the first N tools in
// alphabetical order. MEASURED 2026-10-15 in a live window: Supabase offers 29
// tools, the cap is 24 after the built-ins, and "which project is active in
// supabase" reached a model whose menu had create_project and get_project but
// not list_projects -- which sorts after the cut. The model answered "there is
// no active project", wrongly, and the only trace was a notice telling the user
// to edit a config file.
//
// So when the cap bites, the third-party tools are ordered by how well they
// match what the user asked, and the alphabet only breaks ties. This is a
// lexical score, not a model: it is cheap, deterministic, and every decision it
// makes can be read back from the words involved. What it cannot find, the
// find_tools lookup (daemon/toolsearch.go) still can, on the model's own words.

// relevanceStopwords are words too common in a request or a tool description
// to say anything about which tool is wanted.
var relevanceStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"can": true, "do": true, "does": true, "for": true, "from": true, "how": true, "i": true, "in": true,
	"is": true, "it": true, "its": true, "me": true, "my": true, "of": true, "on": true, "or": true,
	"please": true, "show": true, "that": true, "the": true, "this": true, "to": true, "use": true,
	"what": true, "which": true, "with": true, "you": true, "your": true, "we": true, "our": true,
	"tool": true, "tools": true, "there": true, "get": true, "all": true, "any": true, "one": true,
}

// relevanceTokens splits text into lower-case word stems: on anything that is
// not a letter or digit, and on a lower-to-upper case change, so
// "list_projects", "listProjects" and "list projects" all yield the same words.
func relevanceTokens(text string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && unicode.IsLower(prev) {
				flush()
			}
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
		prev = r
	}
	flush()

	out := words[:0]
	for _, w := range words {
		if len(w) < 2 || relevanceStopwords[w] {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

// stem folds the endings that most often separate a request's word from a
// tool's: "projects"/"project", "branches"/"branch", "deployed"/"deploy".
// Deliberately crude -- it only has to make the same word meet itself.
func stem(w string) string {
	for _, suffix := range []string{"ing", "ies", "es", "ed", "s"} {
		if len(w) > len(suffix)+2 && strings.HasSuffix(w, suffix) {
			base := strings.TrimSuffix(w, suffix)
			if suffix == "ies" {
				return base + "y"
			}
			return base
		}
	}
	return w
}

// relevanceScore is how well tool matches the request's words. A word in the
// tool's NAME counts three times one in its description: names are short and
// chosen, descriptions are long and repeat the server's vocabulary in every
// tool. Naming the server is worth a little to each of its tools, so "in
// supabase" lifts Supabase's tools over another server's on a tie.
func relevanceScore(tool Tool, query map[string]bool) int {
	if len(query) == 0 {
		return 0
	}
	score := 0
	seen := map[string]bool{}
	for _, w := range relevanceTokens(tool.Name) {
		if query[w] && !seen[w] {
			score += 3
			seen[w] = true
		}
	}
	for _, w := range relevanceTokens(tool.Description) {
		if query[w] && !seen[w] {
			score++
			seen[w] = true
		}
	}
	for _, w := range relevanceTokens(tool.Server) {
		if query[w] {
			score++
			break
		}
	}
	return score
}

// queryWords is a request's distinct word stems.
func queryWords(query string) map[string]bool {
	words := map[string]bool{}
	for _, w := range relevanceTokens(query) {
		words[w] = true
	}
	return words
}

// RankByRelevance orders tools best match first for query, keeping the
// existing (alphabetical) order among equals, so an empty query or one that
// matches nothing leaves the order exactly as it was. It sorts in place.
func RankByRelevance(tools []Tool, query string) {
	words := queryWords(query)
	if len(words) == 0 {
		return
	}
	scores := make(map[string]int, len(tools))
	for _, t := range tools {
		scores[t.QualifiedName()] = relevanceScore(t, words)
	}
	sort.SliceStable(tools, func(i, j int) bool {
		return scores[tools[i].QualifiedName()] > scores[tools[j].QualifiedName()]
	})
}

// SearchTools returns at most limit tools from candidates that match query at
// all, best first. Unlike RankByRelevance it drops the tools that match nothing:
// it answers a lookup, and a list padded with irrelevant tools would put them on
// the menu for no reason.
func SearchTools(candidates []Tool, query string, limit int) []Tool {
	words := queryWords(query)
	if len(words) == 0 || limit <= 0 {
		return nil
	}
	type scored struct {
		tool  Tool
		score int
	}
	var hits []scored
	for _, t := range candidates {
		if s := relevanceScore(t, words); s > 0 {
			hits = append(hits, scored{t, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Tool, len(hits))
	for i, h := range hits {
		out[i] = h.tool
	}
	return out
}

// A SERVER THE USER NAMES GETS A FEW PLACES ON THE MENU.
//
// Built-ins come first when the cap bites (Registry.Advertised), and they take
// 16-18 of the 24 places on an ordinary turn, more in a long task, so the
// connected servers share about 6-8, and a second server the request does not
// name may get none (measured 2026-10-28, docs/MENU_NOTE_FOR_RAV_2026-10-28.md).
// The cap was kept at 24 on 2026-10-28, because the eval in that note found no
// benefit from a larger menu.
//
// SEEN 2026-10-15: Supabase (29 tools) and Firecrawl (17) were connected,
// "have you connected to the supabase via mcp?" was asked, and
// the model was shown supabase__query_logs and supabase__search_docs and
// nothing else from either -- so it told the user those were the only
// Supabase tools it had.
//
// So when the request names a connected server, up to namedServerReserve of
// that server's best-matching tools are kept even if it costs the least
// relevant built-ins their place. A displaced built-in is not lost: it is in
// DroppedTools like any other, and find_tools can bring it back.
const namedServerReserve = 6

// withNamedServerReserve builds the ranked menu-plus-overflow: built-ins first,
// except that a server named in query keeps up to namedServerReserve places.
// The caller cuts the result at maxAdvertised. external must already be ranked.
func withNamedServerReserve(builtins, external []Tool, query string, servers []string, maxAdvertised int) []Tool {
	named := serversNamedIn(query, servers)
	var namedTools, others []Tool
	for _, t := range external {
		if named[t.Server] {
			namedTools = append(namedTools, t)
		} else {
			others = append(others, t)
		}
	}
	reserve := namedServerReserve
	if reserve > maxAdvertised/4 {
		reserve = maxAdvertised / 4
	}
	if reserve > len(namedTools) {
		reserve = len(namedTools)
	}
	// Room the built-ins already leave counts toward the reserve.
	if free := maxAdvertised - len(builtins); free >= reserve {
		return append(append(append([]Tool(nil), builtins...), namedTools...), others...)
	}
	keep := maxAdvertised - reserve
	ranked := append([]Tool(nil), builtins...)
	RankByRelevance(ranked, query)
	out := make([]Tool, 0, len(builtins)+len(external))
	out = append(out, ranked[:keep]...)
	out = append(out, namedTools[:reserve]...)
	out = append(out, ranked[keep:]...)
	out = append(out, namedTools[reserve:]...)
	return append(out, others...)
}

// serversNamedIn is the set of servers whose whole name appears in query, as
// words: "supabase" in "is supabase connected", "brave-search" only when both
// "brave" and "search" do.
func serversNamedIn(query string, servers []string) map[string]bool {
	words := queryWords(query)
	named := map[string]bool{}
	for _, s := range servers {
		parts := relevanceTokens(s)
		if len(parts) == 0 {
			continue
		}
		all := true
		for _, p := range parts {
			if !words[p] {
				all = false
				break
			}
		}
		if all {
			named[s] = true
		}
	}
	return named
}
