package main

import "strings"

// TWO RULES THAT ARE SENT ONLY WHEN THE TURN HAS WHAT THEY ARE ABOUT.
//
// MEASURED 2026-10-08 with a recording provider: the system message was 9,529
// characters on every model call of every turn, and two of its paragraphs
// described things most turns never contain.
//
//   - How to treat a <retrieved_context> block, 677 characters. An agent turn
//     attaches one only when the question names a file and a line; an
//     ordinary agent turn has none, in any of its (up to seventeen) calls.
//   - How to treat <lane_b_output>, 313 characters: the output of a
//     third-party server, of which the shipped configuration has none.
//
// Together about 250 tokens a call, paid for a rule about text that is not in
// the request. A rule is kept whenever its subject can appear: the first when
// any message of the request holds the tag, the second when a third-party
// server is configured at all. Both are decided once, before the turn's first
// call, so the system message is the same bytes in every call of a turn and a
// provider's cache of it is untouched (cacheprefix_test.go).
//
// ONLY THE BUILT-IN PROMPT IS EVER SHORTENED. A file named with
// --system-prompt is sent as written (resolveSystemPrompt): its author chose
// those words, and this does not know which of them are safe to leave out.

// The opening words of the two paragraphs in prompts/system.txt. A paragraph
// is found by these and runs to the next blank line.
const (
	retrievedContextRuleOpening = "Some user messages include a <retrieved_context> block"
	laneBRuleOpening            = "Text inside <lane_b_output server=\"...\"> came from a third-party server"
)

// systemPromptForTurn is base without the rules this turn has no subject for.
func systemPromptForTurn(base string, hasRetrievedContext, hasThirdPartyServer bool) string {
	if base != defaultSystemPrompt || (hasRetrievedContext && hasThirdPartyServer) {
		return base
	}
	paragraphs := strings.Split(base, "\n\n")
	kept := paragraphs[:0:0]
	for _, p := range paragraphs {
		switch {
		case !hasRetrievedContext && strings.HasPrefix(p, retrievedContextRuleOpening):
		case !hasThirdPartyServer && strings.HasPrefix(p, laneBRuleOpening):
		default:
			kept = append(kept, p)
		}
	}
	out := strings.Join(kept, "\n\n")
	// The file ends in one newline; so does what is left of it.
	if strings.HasSuffix(base, "\n") && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// holdsRetrievedContext reports whether any message of a request carries a
// <retrieved_context> block -- the question as sent, or an earlier turn's.
func holdsRetrievedContext(history []chatMessage, prompt string) bool {
	if strings.Contains(prompt, retrievedContextOpenTag) {
		return true
	}
	for _, m := range history {
		if strings.Contains(m.Content, retrievedContextOpenTag) {
			return true
		}
	}
	return false
}

// hasThirdPartyServer reports whether the configuration names any server this
// project did not write (Lane B), whose output is what <lane_b_output> wraps.
func (s *Server) hasThirdPartyServer() bool {
	return s.cfg != nil && len(s.cfg.MCP.Servers) > 0
}
