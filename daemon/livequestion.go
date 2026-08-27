package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Classifying the QUESTION, which is the half the first version of this feature
// got wrong.
//
// A question about who currently holds an office, what a thing costs today, or
// which version is newest has an answer that changes. Whether the model then
// hedges about it, states it confidently, or gets lucky is downstream of the
// only fact that matters: it should have looked.
//
// TWO CONSUMERS, TWO COST PROFILES, ONE CLASSIFIER.
//
//   - PRE-FLIGHT (preflightGroundingDirective) runs before the first model call
//     and costs about forty tokens of system prompt. A false positive there is
//     free: the directive says to ignore it for a codebase question, and the
//     model does.
//   - THE BACKSTOP (the nudge in agentloop.go) costs one extra model call. A
//     false positive there is ~10 seconds.
//
// So the classifier is tuned for RECALL and the suppressors below carry the
// precision. That direction is chosen deliberately: missing a live question
// means a user is told, with confidence, something that stopped being true --
// which is the failure this exists to prevent and the expensive one.

// currencyMarkers are the words that make a question about NOW rather than
// about a fact that stays put.
var currencyMarkers = []string{
	"current", "currently", "latest", "newest", "most recent", "right now",
	"today", "tonight", "this week", "this month", "this year", "nowadays",
	"these days", "at the moment", "as of now", "as of today", "up to date",
	"up-to-date", "so far this", "at present", "present day", "recent",
	"still the", "these days", "this quarter",
}

// worldFrames are question shapes whose answer lives outside this machine, even
// with no currency word in them. "Who is the prime minister of India" needs no
// "current" to be a question about now.
var worldFrames = []*regexp.Regexp{
	// Office holders. The one that started this: "who is the cm of tn".
	regexp.MustCompile(`(?i)\bwho(?:'s| is| are)\s+(?:the\s+)?` +
		`(?:pm|cm|ceo|cto|president|prime\s*minister|chief\s*minister|chief\s*executive|` +
		`governor|mayor|chancellor|premier|monarch|king|queen|pope|head\s+of)\b`),
	// "who is the X of Y" more generally, where Y is a place or org.
	regexp.MustCompile(`(?i)\bwho\s+(?:is|are)\s+the\s+\w+(?:\s+\w+)?\s+of\s+[A-Za-z]`),
	regexp.MustCompile(`(?i)\bwho\s+(?:won|holds|leads|runs|owns|is leading)\b`),
	// Prices, rates, market state.
	regexp.MustCompile(`(?i)\b(?:price|cost|worth|rate|valuation|market cap|exchange rate)\s+of\b`),
	regexp.MustCompile(`(?i)\bhow much (?:is|does|do|are)\b`),
	// Release and version state, which a coding assistant is asked constantly
	// and gets wrong in exactly the same way.
	regexp.MustCompile(`(?i)\b(?:latest|newest|current|stable)\s+(?:version|release)\b`),
	regexp.MustCompile(`(?i)\bhas\s+\w+\s+(?:released|shipped|launched|announced)\b`),
	// Events and status.
	regexp.MustCompile(`(?i)\bwhen\s+is\s+the\s+next\b`),
	regexp.MustCompile(`(?i)\bwhat\s+happened\s+(?:to|with|in)\b`),
	regexp.MustCompile(`(?i)\bis\s+\w+\s+still\b`),
	regexp.MustCompile(`(?i)\bwhat(?:'s| is)\s+(?:the\s+)?(?:news|weather|score|date|time)\b`),
}

// workspaceSignals mark a question about THIS repository rather than about the
// world. They are the precision half.
//
// "What is the current implementation of runAgentLoop" contains a currency
// marker and is not a question the web can answer. Without these, every such
// question would buy a pointless search.
var workspaceSignals = []string{
	"this code", "this repo", "this repository", "this project", "this file",
	"this function", "this package", "this module", "this branch", "this test",
	"this struct", "this method", "this class", "this script", "this config",
	"the codebase", "our code", "in the code", "in this codebase", "the diff",
	"implementation of", "implemented in", "defined in", "declared in",
	"stack trace", "build error", "compile error", "test failure",
	"git log", "git diff", "git status", "the commit", "this commit",
}

// codingIntent matches "write/implement/add ... a function/handler/test", which
// is a request to PRODUCE code rather than a question about the world.
//
// IT IS DELIBERATELY NARROW -- a verb and a code noun within a short span --
// rather than a bare verb list. A test caught the case it exists for: "write a
// function that returns the current time" contains "current" and is not a
// question the web can answer. Suppressing on "write" alone would have been
// the easy fix and would also have eaten "write a summary of the latest news",
// which IS a lookup. The code noun is what separates them.
var codingIntent = regexp.MustCompile(`(?i)\b(?:write|implement|add|create|generate|refactor|rewrite|build|make)\b` +
	`.{0,40}?\b(?:function|func|method|class|struct|interface|script|handler|test|endpoint|` +
	`component|module|package|helper|wrapper|parser|type|field|flag|command|migration)\b`)

// filePathish matches a token that looks like a source file, which is the
// strongest single signal that a question is about the workspace.
var filePathish = regexp.MustCompile(`(?i)[\w./-]+\.(?:go|js|jsx|ts|tsx|py|rs|java|rb|md|json|ya?ml|toml|sh|c|h|hpp|cpp|cs|php|sql|txt|mod|sum)\b`)

// looksLikeLiveWorldQuestion reports whether a question's answer plausibly
// changed since the model was trained.
func looksLikeLiveWorldQuestion(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	// A very long message is a paste, a stack trace or a spec, not a lookup.
	// Bounding it keeps a 4 KB code dump that happens to contain the word
	// "current" from buying a search.
	if len(q) > 600 {
		return false
	}
	lower := strings.ToLower(q)

	// PRECISION FIRST: a workspace question is never a live-world question,
	// however many currency words it contains.
	if filePathish.MatchString(q) || codingIntent.MatchString(q) {
		return false
	}
	for _, sig := range workspaceSignals {
		if strings.Contains(lower, sig) {
			return false
		}
	}

	for _, re := range worldFrames {
		if re.MatchString(q) {
			return true
		}
	}
	for _, m := range currencyMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// lastUserQuestion returns the user's own words from the message list.
//
// IT MUST UNWRAP <user_request>. When retrieval is on -- which is the shipped
// default -- the user's question is not the message; it is a fragment inside a
// message that also carries several kilobytes of their source code. Classifying
// the whole thing would mean every grounded turn gets judged on the contents of
// four random chunks, and filePathish alone would then veto every question ever
// asked.
func lastUserQuestion(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		content := messages[i].Content
		if open := strings.LastIndex(content, userRequestOpenTag); open >= 0 {
			rest := content[open+len(userRequestOpenTag):]
			if close := strings.Index(rest, userRequestCloseTag); close >= 0 {
				return strings.TrimSpace(rest[:close])
			}
			return strings.TrimSpace(rest)
		}
		return strings.TrimSpace(content)
	}
	return ""
}

// preflightGroundingDirective is added to the SYSTEM message for a turn whose
// question looks like it depends on current facts.
//
// STEERING BEATS RETRYING, and that is why this exists alongside the backstop
// rather than instead of it. The backstop can only act after a whole answer has
// been generated, streamed to the user, and judged -- it costs a model call and
// it leaves a wrong answer visible on screen above the right one. This costs
// nothing and prevents the same failure, so it runs first and the backstop
// catches what it misses.
//
// IT GOES IN THE SYSTEM MESSAGE, not appended to the user's. The system prompt
// already states the policy; this says "this particular question is one of
// those", which is the same voice. Appending it to the user's message would
// either forge words the user did not write, or land OUTSIDE the
// <user_request> fence that the system prompt tells the model is the only
// trustworthy region -- so it would be correctly ignored.
const preflightGroundingDirective = "\n\nFOR THIS TURN: the user's question looks like it may depend on " +
	"facts that change over time — who currently holds a position, what something costs now, which " +
	"version is newest, what has recently happened. Your memory of such facts has a cutoff and may be " +
	"wrong even when you feel certain, and stating a stale fact confidently is worse than saying you " +
	"checked. Call web_search FIRST and answer from what it returns, citing the source. If the question " +
	"is actually about this codebase or about something that does not change, ignore this paragraph. " +
	"If what the search returns disagrees with what you remember, the search is what is current — " +
	"report it, do not explain it away as a confusion or a mistake in the source."

// todayNote tells the model what day it is.
//
// THIS WAS MISSING ENTIRELY, and its absence is what turned a working search
// into a wrong answer. Live, on the question this feature exists for, the model
// searched, FOUND the correct current officeholder, and then wrote:
//
//	Some search results mention "C. Joseph Vijay" — that appears to be either
//	an actor-related confusion or a speculative/fictional entry. The factual
//	and widely reported incumbent AS OF 2025 remains M. K. Stalin.
//
// It was August 2026. The model believed it was 2025 because nothing had ever
// told it otherwise, so a 2026 election result was not merely surprising to it
// -- it was in the future, and therefore fiction. Every other guard in this
// change worked perfectly and the answer was still wrong, because a model with
// no clock cannot tell "this happened after my training" from "this did not
// happen".
//
// PER TURN, NOT AT STARTUP. resolveSystemPrompt runs once; a daemon left
// running for a week would otherwise insist it is still the day it booted,
// which is the same bug with a longer fuse.
//
// Deliberately not conditional on the question looking live: it is about
// fifteen tokens, and "what day is it" is context every turn benefits from.
func todayNote(now time.Time) string {
	return fmt.Sprintf("\n\nToday's date is %s. Your training data ends before this. "+
		"Anything you \"know\" about who holds an office, what version is current, or what has "+
		"recently happened may have changed since — and events after your cutoff will feel "+
		"unfamiliar or implausible to you precisely BECAUSE they are after your cutoff. "+
		"Unfamiliarity is not evidence that something is false.", now.Format("Monday, 2 January 2006"))
}

// applyTurnContext adds the date note, and the pre-flight directive when the
// question warrants one, to the system message.
//
// ONE COPY FOR BOTH, so a turn never duplicates the system message twice, and
// so the caller's slice is left untouched -- the orchestrator reuses it across
// phases and per-turn text that leaked into it would accumulate.
func applyTurnContext(messages []chatMessage, webAvailable bool, now time.Time) (out []chatMessage, steered bool) {
	if len(messages) == 0 || messages[0].Role != "system" {
		return messages, false
	}
	addition := todayNote(now)
	steered = webAvailable && looksLikeLiveWorldQuestion(lastUserQuestion(messages))
	if steered {
		addition += preflightGroundingDirective
	}

	out = make([]chatMessage, len(messages))
	copy(out, messages)
	out[0].Content += addition
	return out, steered
}
