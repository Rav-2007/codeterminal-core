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
	if base != defaultSystemPrompt {
		return base
	}
	return withoutUnusedRules(base, hasRetrievedContext, hasThirdPartyServer)
}

// promptParagraphs splits a prompt into its paragraphs and returns what
// separated them, so the pieces can be joined back exactly as they were.
//
// THE SEPARATOR IS READ FROM THE TEXT, NOT ASSUMED. The built-in prompt is a
// file embedded at build time, and git for Windows checks a text file out with
// CRLF line endings: there a blank line is "\r\n\r\n", a split on "\n\n" finds
// one paragraph, and no rule would ever be left out -- with nothing to say so.
// (The same checkout is what broke a test of the extension's source,
// 2026-10-07.)
func promptParagraphs(text string) (paragraphs []string, blankLine string) {
	blankLine = "\n\n"
	if strings.Contains(text, "\r\n") {
		blankLine = "\r\n\r\n"
	}
	return strings.Split(text, blankLine), blankLine
}

// withoutUnusedRules is text without the paragraph about retrieved context
// and the one about a third-party server's output, each unless wanted.
func withoutUnusedRules(text string, hasRetrievedContext, hasThirdPartyServer bool) string {
	if hasRetrievedContext && hasThirdPartyServer {
		return text
	}
	paragraphs, blankLine := promptParagraphs(text)
	kept := paragraphs[:0:0]
	for _, p := range paragraphs {
		switch {
		case !hasRetrievedContext && strings.HasPrefix(p, retrievedContextRuleOpening):
		case !hasThirdPartyServer && strings.HasPrefix(p, laneBRuleOpening):
		default:
			kept = append(kept, p)
		}
	}
	out := strings.Join(kept, blankLine)
	// The file ends in one line ending; so does what is left of it.
	lineEnd := blankLine[:len(blankLine)/2]
	if strings.HasSuffix(text, lineEnd) && !strings.HasSuffix(out, lineEnd) {
		out += lineEnd
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
