package main

import (
	"strings"

	"codeterminal/daemon/mcp"
)

// Two mechanisms that stop the assistant answering a changeable fact out of a
// frozen memory: one that steers BEFORE the model speaks, and one that catches
// it AFTER.
//
// THE SECOND ONE WAS BUILT FIRST AND ON ITS OWN IT WAS NOT ENOUGH. The reported
// defect was a hedge -- "I don't have real-time access to current events" -- so
// the first version of this file detected hedges. The very next live question
// showed why that is the wrong target:
//
//	You: who is the current cm of tn
//	Mochiii: The current Chief Minister of Tamil Nadu is M. K. Stalin. He has
//	         been in office since 7 May 2021, leading the DMK government.
//
// No tool call. No hedge. No caveat. Just a confident answer that was three
// months out of date, delivered in the same voice as a correct one -- while
// web_search sat unused on the menu, in the same session where the previous
// question HAD been searched.
//
// THAT IS THE WORSE FAILURE AND IT WAS THE ONE NOT COVERED. A hedge at least
// warns the user that the answer may be stale; an unhedged wrong answer spends
// the product's credibility to deliver it. Detecting hedges catches the polite
// failure and misses the dangerous one.
//
// So the question, not the answer, is what gets classified. A question about
// who currently holds an office is a question whose answer changes, whatever
// the model then chooses to say about it.
//
// WHY THE SYSTEM PROMPT IS NOT ENOUGH. prompts/system.txt now tells the model
// to look a changeable fact up rather than hedge about it, and most of the time
// that works. It is still ADVICE, competing with a very strong trained habit:
// "I don't have real-time access to current events" is a sentence models have
// been rewarded for producing for years, and a prompt line does not reliably
// out-argue that. The observed failure -- a live model, with retrieval
// grounding on, answering a "who currently holds this office" question from
// memory and appending a note about its own limitations -- is exactly the case
// where the habit wins.
//
// So the loop checks. When a turn ends in a staleness hedge, and a web tool was
// on the menu, and the model never called it, the loop says so once and lets
// the model try again. This converts a prompt from advice into a property of
// the product.
//
// THREE PROPERTIES KEEP IT FROM BEING A HACK:
//
//  1. ONCE PER TURN. It cannot loop, and it costs at most one extra iteration,
//     charged to the same budget as every other iteration.
//  2. IT ONLY EVER SUGGESTS. The injected message explicitly permits the model
//     to answer as before if the question does not actually turn on a live
//     fact, which is what makes a false positive cheap rather than a
//     correctness risk -- the model, not this heuristic, has the last word.
//  3. IT IS ANNOUNCED. The user has already WATCHED the hedge stream to their
//     terminal, so more text arriving afterwards with no explanation would look
//     like a glitch. The loop prints one line saying what it is doing, in the
//     same idiom truncation and redaction are announced in.

// stalenessHedges are the phrasings that mean "I am answering from a frozen
// memory and telling you so instead of checking".
//
// MATCHED CASE-INSENSITIVELY ON THE TAIL OF THE ANSWER, not on the whole of it.
// The distinction matters: a model explaining what a knowledge cutoff IS, or
// writing code that handles stale caches, will use these words in the middle of
// a perfectly good answer. The hedge that this exists to catch is the one in
// the closing note, where it functions as a substitute for the answer.
var stalenessHedges = []string{
	"real-time access",
	"real time access",
	"realtime access",
	"access to real-time",
	"access to current",
	"access to live",
	"knowledge cutoff",
	"training cutoff",
	"training data",
	"as of my last update",
	"as of my knowledge",
	"my last update",
	"i cannot browse",
	"i can't browse",
	"cannot browse the web",
	"unable to browse",
	"no browsing",
	"may have changed since",
	"might have changed since",
	"could have changed since",
	"please verify with a current",
	"check a current source",
	"based on my existing knowledge rather than live",
}

// hedgeTailChars is how much of the end of an answer is examined. A hedge is a
// closing disclaimer; text further back is the answer itself.
const hedgeTailChars = 700

// looksLikeStalenessHedge reports whether an answer ends by disclaiming its own
// currency.
func looksLikeStalenessHedge(answer string) bool {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return false
	}
	tail := trimmed
	if len(tail) > hedgeTailChars {
		tail = tail[len(tail)-hedgeTailChars:]
	}
	tail = strings.ToLower(tail)
	for _, h := range stalenessHedges {
		if strings.Contains(tail, h) {
			return true
		}
	}
	return false
}

// webToolNames are the built-ins this check is about, qualified as the model
// sees them.
var webToolNames = []string{
	mcpBuiltinQualified("web_search"),
	mcpBuiltinQualified("web_fetch"),
}

// webToolOffered reports whether a web tool was on the advertised menu.
//
// CHECKED RATHER THAN ASSUMED, because every reason the menu might not contain
// one is a reason NOT to nudge: the user disabled mcp.web, a specialist role
// excluded it, or max_advertised_tools dropped it. Nudging a model toward a
// tool it was never offered would produce a turn that spends an iteration
// discovering the tool does not exist and then hedges anyway -- strictly worse
// than the hedge alone.
func webToolOffered(tools []toolSpec) bool {
	for _, t := range tools {
		for _, name := range webToolNames {
			if t.Function.Name == name {
				return true
			}
		}
	}
	return false
}

// webToolUsed reports whether this turn already called one.
//
// A model that SEARCHED and still says it cannot be certain is being honest,
// not lazy, and nudging it again would be telling it to do the thing it just
// did.
//
// IT MUST MATCH THE BARE NAME AS WELL AS THE QUALIFIED ONE, and the first
// version did not. turn.toolNames records call.Function.Name -- what the MODEL
// typed -- and models routinely type "web_search" rather than
// "builtin__web_search". The daemon resolves that (see the "resolving
// unqualified" path in dispatchToolCall) and runs the tool perfectly happily,
// so a bare-name call is a normal success, not a failure.
//
// MEASURED, on the first live run of the pre-flight change: the model searched
// TWICE, both times by bare name, and the backstop still logged "a question
// about current facts was answered without a lookup" and spent another model
// call re-asking for a search that had already happened. The comparison, not
// the loop, was wrong.
func webToolUsed(called []string) bool {
	for _, c := range called {
		if isWebToolName(unqualifiedToolName(c)) {
			return true
		}
	}
	return false
}

// unqualifiedToolName strips a "server__" prefix if there is one.
//
// Uses the same separator mcp.SplitQualifiedName does, via that function, so a
// change to the spelling cannot leave this silently matching nothing. A name
// with no separator is already bare and is returned unchanged -- which is the
// case this exists for.
func unqualifiedToolName(name string) string {
	if _, tool, err := mcp.SplitQualifiedName(name); err == nil {
		return tool
	}
	return name
}

// groundingNudge is the message appended to the conversation.
//
// ROLE "user", NOT "system". Providers differ on whether a system message may
// appear mid-conversation, and several silently reorder or drop one that does;
// a dropped nudge would leave the loop spending an extra iteration on a
// conversation that looks, to the model, exactly like the one it just finished
// -- so it would produce the same hedge again, and the user would pay for two
// identical non-answers. A user-role turn is honoured by every provider this
// daemon talks to.
const groundingNudge = "Stop. You just answered from memory and told the user you have no live " +
	"access, when you have a web tool available. Call web_search now and answer from what it " +
	"returns, citing the source. If the question genuinely does not depend on anything that " +
	"changes over time, ignore this and say so briefly instead."

// uncheckedNudge is for the answer that did NOT hedge, and it has to say
// something the other one does not.
//
// A model that hedged already knows it might be wrong; it just needs telling
// that it can check. A model that answered confidently believes it was right,
// so the message has to make the claim it is actually making visible to it --
// that the fact has not changed since training -- because that is the claim it
// has no basis for. Telling it merely to "search anyway" invites it to search,
// find agreement with what it already said, and repeat itself.
const uncheckedNudge = "Stop. That question was about something that changes over time, and you " +
	"answered it from memory without checking. You cannot know from memory whether it is still " +
	"true — elections, releases, prices and appointments all move after a training cutoff, and " +
	"your confidence is not evidence. Call web_search now, and if what it returns differs from " +
	"what you just said, correct yourself plainly and cite the source. If the question was really " +
	"about this codebase or about something fixed, ignore this and say so briefly."

// nudgeTextFor picks the message that fits what actually went wrong.
func nudgeTextFor(hedged bool) string {
	if hedged {
		return groundingNudge
	}
	return uncheckedNudge
}

// nudgeNotice is what the USER sees, because they have already watched the
// hedge arrive and are owed an explanation for the text that follows it.
const nudgeNotice = "\n\n[checking the web rather than answering from memory…]\n\n"

// mcpBuiltinQualified spells a built-in the way the model is shown it. Derived
// from mcp.Tool.QualifiedName rather than written as a literal, so a change to
// the separator cannot leave this file silently matching nothing.
func mcpBuiltinQualified(tool string) string {
	return mcp.Tool{Server: mcp.BuiltinServerName, Name: tool}.QualifiedName()
}
