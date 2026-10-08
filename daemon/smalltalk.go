package main

import (
	"strings"
	"unicode"
)

// smallTalkWords are the words a greeting, thanks or acknowledgement is made
// of. A prompt made ONLY of these (see isSmallTalk) asks nothing about code.
var smallTalkWords = map[string]bool{
	"hi": true, "hello": true, "hey": true, "heya": true, "hiya": true, "yo": true, "howdy": true,
	"sup": true, "whats": true, "what's": true, "up": true, "how": true, "are": true, "you": true,
	"u": true, "doing": true, "good": true, "morning": true, "afternoon": true, "evening": true,
	"night": true, "gm": true, "gn": true, "thanks": true, "thank": true, "thx": true, "ty": true,
	"ok": true, "okay": true, "k": true, "kk": true, "cool": true, "nice": true, "great": true,
	"awesome": true, "bye": true, "goodbye": true, "cya": true, "later": true, "yes": true,
	"no": true, "yep": true, "nope": true, "sure": true, "there": true, "mochiii": true,
	"so": true, "much": true, "a": true, "lot": true, "again": true,
}

// isSmallTalk reports whether prompt is only a greeting, thanks or
// acknowledgement -- short, and every word from smallTalkWords, with
// stretched letters folded ("hiii" is "hi", "heyyy" is "hey"). Anything
// naming something else ("hi, explain retry.go") is a real question.
func isSmallTalk(prompt string) bool {
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(unicode.IsLetter(r) || r == '\'')
	})
	if len(words) == 0 || len(words) > 6 {
		return false
	}
	for _, w := range words {
		if !smallTalkWords[w] && !smallTalkWords[foldStretched(w)] {
			return false
		}
	}
	return true
}

// foldStretched collapses a run of three or more of the same letter to one:
// "hiii" -> "hi", "heyyy" -> "hey", "thanksss" -> "thanks". Runs of two are
// kept, because English has them ("good", "cool").
func foldStretched(w string) string {
	r := []rune(w)
	var out []rune
	for i := 0; i < len(r); {
		j := i
		for j < len(r) && r[j] == r[i] {
			j++
		}
		n := j - i
		if n >= 3 {
			n = 1
		}
		for k := 0; k < n; k++ {
			out = append(out, r[i])
		}
		i = j
	}
	return string(out)
}

// followUpWords are the words a short follow-up is made of: "continue", "yes
// do it", "ok fix it", "try again", "now add a test for that". None of them
// names anything in a codebase, so a prompt made ONLY of these (see isFollowUp)
// says what to do next with the conversation, not what to look up.
var followUpWords = map[string]bool{
	"continue": true, "continuing": true, "go": true, "on": true, "ahead": true, "proceed": true,
	"keep": true, "going": true, "carry": true, "yeah": true, "please": true, "pls": true,
	"do": true, "it": true, "that": true, "this": true, "those": true, "these": true, "them": true,
	"fix": true, "try": true, "redo": true, "why": true, "what": true, "now": true,
	"add": true, "an": true, "the": true, "test": true, "tests": true, "for": true, "explain": true,
	"more": true, "detail": true, "details": true, "elaborate": true, "expand": true, "show": true,
	"me": true, "next": true, "same": true, "other": true, "one": true, "ones": true, "rest": true,
	"finish": true, "complete": true, "done": true, "and": true, "then": true, "also": true,
	"too": true, "just": true, "still": true, "it's": true, "that's": true, "is": true, "was": true,
	"were": true, "can": true, "could": true, "would": true, "should": true, "we": true, "i": true,
	"let's": true, "lets": true, "right": true, "correct": true, "wrong": true, "works": true,
	"work": true, "working": true, "doesn't": true, "didn't": true, "isn't": true, "not": true,
	"don't": true, "hmm": true, "well": true, "wait": true, "here": true, "apply": true, "use": true,
	"change": true, "update": true, "looks": true, "look": true, "like": true, "with": true,
	"to": true, "of": true, "in": true,
}

// maxFollowUpWords bounds a follow-up. Anything longer is a request in its own
// right, whatever its words.
const maxFollowUpWords = 8

// isFollowUp reports whether prompt only moves the conversation along: short,
// every word a follow-up or small-talk word, and nothing path- or code-shaped.
// Any identifier or file name breaks it ("fix the retry bug" is a question).
//
// A code search keyed on such words finds whatever happens to say "continue"
// or "try" -- MEASURED 2026-09-29 on this repository: each follow-up drew ~30
// KB of unrelated files ("try again" drew retry.go), billed at full price
// because it sits in the newest message.
func isFollowUp(prompt string) bool {
	if strings.ContainsRune(prompt, '`') || filePathish.MatchString(prompt) {
		return false
	}
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(unicode.IsLetter(r) || r == '\'')
	})
	if len(words) == 0 || len(words) > maxFollowUpWords {
		return false
	}
	for _, w := range words {
		folded := foldStretched(w)
		if !followUpWords[w] && !followUpWords[folded] && !smallTalkWords[w] && !smallTalkWords[folded] {
			return false
		}
	}
	return true
}

// aboutAssistantWords are the words a question to Mochiii ABOUT ITSELF is made
// of: "what is your name", "who are you", "what can you do", "who made you",
// "what model are you". None names anything in a codebase.
var aboutAssistantWords = map[string]bool{
	"what": true, "what's": true, "whats": true, "who": true, "who's": true, "which": true,
	"how": true, "is": true, "are": true, "am": true, "do": true, "does": true, "can": true,
	"could": true, "will": true, "your": true, "you": true, "yourself": true, "u": true, "ur": true,
	"r": true, "name": true, "named": true, "called": true, "made": true, "make": true,
	"built": true, "created": true, "wrote": true, "trained": true, "model": true, "version": true,
	"tell": true, "me": true, "about": true, "introduce": true, "help": true, "with": true,
	"an": true, "a": true, "the": true, "ai": true, "llm": true, "bot": true, "assistant": true,
	"work": true, "real": true, "by": true, "please": true, "exactly": true, "mochiii": true,
}

// secondPerson marks a question as addressed to the assistant rather than
// about something else made of the same words ("what is the model").
var secondPerson = map[string]bool{"you": true, "your": true, "yourself": true, "u": true, "ur": true, "mochiii": true}

// isAboutAssistant reports whether prompt only asks Mochiii about itself:
// short, addressed to it, every word from aboutAssistantWords or small talk,
// and nothing path- or code-shaped ("what can you tell me about retry.go" is a
// question about the code).
func isAboutAssistant(prompt string) bool {
	if strings.ContainsRune(prompt, '`') || filePathish.MatchString(prompt) {
		return false
	}
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(unicode.IsLetter(r) || r == '\'')
	})
	if len(words) == 0 || len(words) > maxFollowUpWords {
		return false
	}
	addressed := false
	for _, w := range words {
		folded := foldStretched(w)
		if !aboutAssistantWords[w] && !aboutAssistantWords[folded] && !smallTalkWords[w] && !smallTalkWords[folded] {
			return false
		}
		addressed = addressed || secondPerson[w] || secondPerson[folded]
	}
	return addressed
}

// A GREETING IS NOT A TASK, AND IS NOT BILLED AS ONE.
//
// MEASURED 2026-10-08, on the build this was written against: "hi" in agent
// mode went to the model as 17.5 KB -- the whole system prompt, the reach
// section and eleven tool definitions, about 4,400 tokens -- plus every earlier
// message of the conversation, to be answered "Hello!". "thanks" at the end of
// a long session cost the whole session again to say "You're welcome."
//
// isSmallTalk above already keeps code from being attached to such a message.
// This is the narrower question of whether it needs the AGENT at all, and it is
// narrower on purpose, because the cost of being wrong is different. A message
// wrongly spared retrieval loses some context; a message wrongly spared the
// tools cannot be acted on. So:
//
//   - "yes", "ok", "sure", "no", "again" are small talk to isSmallTalk and are
//     NOT greetings here. They are answers to something the assistant asked --
//     "shall I apply it?" -- and the turn that follows may have to edit a file.
//   - A bare "how" or "you" is a follow-up, not a greeting. A message must hold
//     a word that makes it one (hi, thanks, bye, ...), or be one of a few whole
//     phrases, and nothing else but filler.
//
// What qualifies is answered by a one-line system message with no tools and no
// history: nothing a greeting's reply depends on is in either.

// greetingAnchors are the words that make a message a greeting, a thank-you or
// a goodbye.
var greetingAnchors = map[string]bool{
	"hi": true, "hello": true, "hey": true, "heya": true, "hiya": true, "yo": true, "howdy": true,
	"sup": true, "gm": true, "gn": true, "morning": true, "afternoon": true, "evening": true,
	"thanks": true, "thank": true, "thx": true, "ty": true,
	"bye": true, "goodbye": true, "cya": true,
}

// greetingFillers may stand beside an anchor and are nothing on their own.
var greetingFillers = map[string]bool{
	"there": true, "mochiii": true, "good": true, "you": true, "u": true, "so": true, "much": true,
	"a": true, "lot": true, "very": true, "again": true, "night": true, "all": true,
}

// greetingPhrases are greetings with no anchor word in them.
var greetingPhrases = map[string]bool{
	"how are you": true, "how are u": true, "how are you doing": true,
	"whats up": true, "what's up": true, "good night": true, "see you": true, "see you later": true,
}

// isGreetingOnly reports a message that is a greeting, a thank-you or a goodbye
// and nothing else -- one that can be answered without tools or history.
func isGreetingOnly(prompt string) bool {
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(unicode.IsLetter(r) || r == '\'')
	})
	if len(words) == 0 || len(words) > 6 {
		return false
	}
	// A word as written, or with a stretched letter folded ("heyyy"). Both,
	// because the product's own name is spelt with three of one letter.
	folded := make([]string, len(words))
	for i, w := range words {
		folded[i] = foldStretched(w)
	}
	if greetingPhrases[strings.Join(words, " ")] || greetingPhrases[strings.Join(folded, " ")] {
		return true
	}
	anchored := false
	for i, w := range words {
		switch {
		case greetingAnchors[w] || greetingAnchors[folded[i]]:
			anchored = true
		case !greetingFillers[w] && !greetingFillers[folded[i]]:
			return false
		}
	}
	return anchored
}

// greetingSystemPrompt is the whole system message of a greeting's turn.
// maxEmptyGreetingReplies is how many times a greeting is sent before an empty
// reply is reported: the agent loop's own count for the same fault.
const maxEmptyGreetingReplies = 3

const greetingSystemPrompt = "You are Mochiii, an AI coding assistant working on the user's machine. " +
	"The user has sent a greeting, a thank-you or a goodbye, not a task. Reply in one short, friendly " +
	"sentence. Do not list what you can do unless you are asked."
