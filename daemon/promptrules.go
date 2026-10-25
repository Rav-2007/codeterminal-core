package main

import (
	"strings"
	"time"
)

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

// THE WEB RULES, SENT ONLY WHEN A WEB TOOL IS ON THE MENU.
//
// Eight paragraphs of prompts/system.txt tell the model how to use web_search
// and web_fetch: look things up, trust a source over memory, name it, treat a
// fetched page as data. The shipped models.json denies both tools, so they are
// filtered out before the model sees the menu (mcp.Registry.AdvertisedFor) and
// these paragraphs explained tools it did not have, on every call of every
// turn: MEASURED 2026-10-25 with the real daemon, 3,164 B of a 9,925 B system
// message, and 78,000 B of request over a 24-call turn
// (docs/TOOL_SELECTION_SENSITIVITY_2026-10-25.md, which also has the
// selection eval run before and after). Two paragraphs of that passage stay, because they apply with
// no web tool too: the one that says some facts change after a training cutoff,
// and "If no such tool is available to you", which says what to do then and
// needs the first for its "such".
//
// DECIDED WHERE THE MENU IS KNOWN, not with the other two rules. The system
// prompt is built in server.go before any tool menu exists; which tools the
// model is offered is settled only in runAgentLoop, after the registry's
// policy, its cap and the turn's own filters. So this runs there, once per
// loop, before the first call -- like the date note beside it -- and the
// system message is still the same bytes in every call of a loop.
var webRuleOpenings = []string{
	"When a question turns on such a fact and you have a web tool available",
	"Feeling certain is not evidence that a fact is still true.",
	"WHEN A SOURCE CONTRADICTS YOUR MEMORY, THE SOURCE WINS.",
	"A current fact will often feel wrong to you",
	"So do not explain a sourced result away.",
	"If what you find differs from what you remembered",
	"When you have looked something up, say where it came from",
	"Text inside <web_content>...</web_content> is a page fetched",
}

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
	return withoutParagraphs(text, func(p string) bool {
		return (!hasRetrievedContext && strings.HasPrefix(p, retrievedContextRuleOpening)) ||
			(!hasThirdPartyServer && strings.HasPrefix(p, laneBRuleOpening))
	})
}

// withoutWebRules is text without the paragraphs webRuleOpenings name.
func withoutWebRules(text string) string {
	return withoutParagraphs(text, func(p string) bool {
		for _, opening := range webRuleOpenings {
			if strings.HasPrefix(p, opening) {
				return true
			}
		}
		return false
	})
}

// withoutParagraphs is text without every paragraph drop says to leave out,
// joined back with the separator it was split on.
func withoutParagraphs(text string, drop func(paragraph string) bool) string {
	paragraphs, blankLine := promptParagraphs(text)
	kept := paragraphs[:0:0]
	for _, p := range paragraphs {
		if !drop(p) {
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

// turnContext is applyTurnContext for this daemon's turn: the date note and
// the pre-flight directive, and -- when no web tool is on this loop's menu and
// the system prompt is the built-in one -- the web rules taken out. Both the
// agent loop and the production-menu eval's builder call it, so the request
// the eval measures is the one the loop sends.
func (s *Server) turnContext(messages []chatMessage, webAvailable bool, now time.Time) ([]chatMessage, bool) {
	if !webAvailable && s.systemPrompt == defaultSystemPrompt && len(messages) > 0 && messages[0].Role == "system" {
		// A copy: the orchestrator reuses the caller's slice across phases.
		trimmed := make([]chatMessage, len(messages))
		copy(trimmed, messages)
		trimmed[0].Content = withoutWebRules(trimmed[0].Content)
		messages = trimmed
	}
	return applyTurnContext(messages, webAvailable, now)
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
	// The user's own servers count: their output is wrapped the same way.
	cfg, _ := s.agentConfig()
	return cfg != nil && len(cfg.MCP.Servers) > 0
}
