package main

import (
	"strings"
	"testing"
	"time"
)

// THE QUESTION THAT DEFEATED THE FIRST VERSION.
//
// "who is the current cm of tn" was answered from memory, confidently, with no
// hedge and no tool call -- so a hedge detector had nothing to match and the
// user was handed a three-month-stale name in the voice of a fact.
func TestTheQuestionThatWasAnsweredStaleIsClassifiedLive(t *testing.T) {
	for _, q := range []string{
		"who is the current cm of tn",
		"who is the cm of tamil nadu",
		"who is the pm for india",
		"who's the CEO of Anthropic",
	} {
		if !looksLikeLiveWorldQuestion(q) {
			t.Errorf("looksLikeLiveWorldQuestion(%q) = false; this is the exact shape that was answered stale", q)
		}
	}
}

func TestLiveWorldQuestionsAreRecognised(t *testing.T) {
	live := []string{
		"what is the latest stable version of Go",
		"what's the current price of bitcoin",
		"who won the election",
		"when is the next Go release",
		"is Redis still open source",
		"what happened to the Twitter API",
		"how much does an EC2 m5.large cost",
		"has OpenAI released a new model",
		"what is the news today",
		"most recent kubernetes release",
		"who leads the project now",
	}
	for _, q := range live {
		if !looksLikeLiveWorldQuestion(q) {
			t.Errorf("looksLikeLiveWorldQuestion(%q) = false, want true", q)
		}
	}
}

// PRECISION IS THE OTHER HALF. A coding assistant is asked about "the current
// implementation" all day long, and buying a web search for each one is a tax
// on every turn.
func TestCodebaseQuestionsAreNotClassifiedLive(t *testing.T) {
	notLive := []string{
		"",
		"what is the current implementation of runAgentLoop",
		"show me the latest changes in this file",
		"why does daemon/agentloop.go fail to compile",
		"explain this function",
		"what does the codebase do when a tool is denied",
		"fix the current test failure in webfetch_test.go",
		"add a field to this struct",
		"what is the newest commit on this branch",
		"refactor the current implementation of the scrubber",
		"why is git status showing these files",
		"write a function that returns the current time",
	}
	for _, q := range notLive {
		if looksLikeLiveWorldQuestion(q) {
			t.Errorf("looksLikeLiveWorldQuestion(%q) = true; a codebase question would buy a pointless search", q)
		}
	}
}

// A long paste is not a lookup. Without the length bound, any stack trace or
// spec containing the word "current" would trip this.
func TestALongPasteIsNotALookup(t *testing.T) {
	paste := "here is the current output, please fix it:\n" + strings.Repeat("goroutine 1 [running]:\n", 60)
	if looksLikeLiveWorldQuestion(paste) {
		t.Error("a long paste was classified as a live-world question")
	}
}

// THE CLASSIFIER MUST SEE THE USER'S WORDS, NOT THEIR SOURCE CODE.
//
// With retrieval on -- the shipped default -- the "user" message is several
// kilobytes of chunks wrapped around one line of question. Classifying the
// whole message would mean filePathish vetoes every question ever asked,
// silently disabling the entire feature on exactly the configuration users run.
func TestTheUsersQuestionIsExtractedFromTheGroundedMessage(t *testing.T) {
	grounded := "<retrieved_context>\n" +
		"[1] daemon/agentloop.go:1-40\nfunc runAgentLoop() {}\n" +
		"[2] daemon/webfetch.go:1-20\nfunc fetchURL() {}\n" +
		"</retrieved_context>\n" +
		"<user_request>\nwho is the current cm of tn\n</user_request>"

	got := lastUserQuestion([]chatMessage{
		{Role: "system", Content: "you are..."},
		{Role: "user", Content: grounded},
	})
	if got != "who is the current cm of tn" {
		t.Fatalf("lastUserQuestion = %q, want the user's own words", got)
	}
	if !looksLikeLiveWorldQuestion(got) {
		t.Error("the extracted question was not classified live; retrieval would disable the whole feature")
	}
	// The proof that extraction is load-bearing: the raw message is full of .go
	// paths and would be vetoed outright.
	if looksLikeLiveWorldQuestion(grounded) {
		t.Error("fixture is wrong: the raw grounded message should be vetoed by filePathish")
	}
}

func TestLastUserQuestionHandlesTheUngroundedAndEmptyCases(t *testing.T) {
	if got := lastUserQuestion([]chatMessage{{Role: "user", Content: "  plain question  "}}); got != "plain question" {
		t.Errorf("ungrounded = %q, want the trimmed message", got)
	}
	if got := lastUserQuestion(nil); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	// The LAST user message, not the first: a follow-up is what is being asked.
	msgs := []chatMessage{
		{Role: "user", Content: "explain this function"},
		{Role: "assistant", Content: "..."},
		{Role: "user", Content: "who is the current cm of tn"},
	}
	if got := lastUserQuestion(msgs); got != "who is the current cm of tn" {
		t.Errorf("lastUserQuestion = %q, want the follow-up", got)
	}
}

// Pre-flight steering: the cheap half.
func TestPreflightSteersTheSystemMessageAndOnlyWhenItShould(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	base := []chatMessage{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "who is the current cm of tn"},
	}

	steered, fired := applyTurnContext(base, true, now)
	if !fired {
		t.Fatal("pre-flight did not fire on a live-world question")
	}
	if !strings.Contains(steered[0].Content, "web_search") {
		t.Error("the directive does not name the tool")
	}
	if !strings.HasPrefix(steered[0].Content, "SYSTEM") {
		t.Error("the original system prompt was replaced rather than extended")
	}
	// THE CALLER'S SLICE MUST NOT BE MUTATED: the orchestrator reuses it for
	// later phases, and per-turn text that leaked would accumulate.
	if base[0].Content != "SYSTEM" {
		t.Errorf("the caller's system message was mutated in place: %q", base[0].Content)
	}

	// No web tool means no directive: steering toward a tool that does not
	// exist just wastes tokens and confuses the model.
	if _, fired := applyTurnContext(base, false, now); fired {
		t.Error("pre-flight fired with no web tool available")
	}

	// A codebase question is left alone.
	code := []chatMessage{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "what is the current implementation of runAgentLoop"},
	}
	if _, fired := applyTurnContext(code, true, now); fired {
		t.Error("pre-flight fired on a codebase question")
	}
}

// THE MODEL MUST BE TOLD WHAT DAY IT IS.
//
// Its absence is what turned a working search into a wrong answer: the model
// found the correct 2026 officeholder, decided it was 2025, and dismissed a
// post-cutoff fact as "speculative/fictional". Every other guard worked and the
// answer was still wrong, because a model with no clock cannot distinguish
// "this happened after my training" from "this did not happen".
func TestTheModelIsAlwaysToldTheDate(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)

	// Even for a plain codebase question, and even with no web tool: knowing
	// the date is cheap and universally useful.
	out, steered := applyTurnContext([]chatMessage{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "explain this function"},
	}, false, now)
	if steered {
		t.Error("a codebase question was steered")
	}
	if !strings.Contains(out[0].Content, "27 August 2026") {
		t.Fatalf("the system message carries no date:\n%s", out[0].Content)
	}
	// The reasoning the model needs, not just the number. Without this it can
	// know the date and still treat an unfamiliar fact as false.
	if !strings.Contains(out[0].Content, "Unfamiliarity is not evidence") {
		t.Error("the date note does not tell the model that post-cutoff facts feel implausible by construction")
	}

	// PER TURN, NOT PER PROCESS. A daemon left running for a week must not
	// insist it is still the day it booted.
	later, _ := applyTurnContext([]chatMessage{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "hello"},
	}, false, now.AddDate(0, 0, 7))
	if strings.Contains(later[0].Content, "27 August 2026") {
		t.Error("the date is fixed at startup rather than resolved per turn")
	}
	if !strings.Contains(later[0].Content, "3 September 2026") {
		t.Errorf("the later turn has the wrong date:\n%s", later[0].Content)
	}

	// The system message must be augmented exactly once.
	if n := strings.Count(out[0].Content, "Today's date is"); n != 1 {
		t.Errorf("the date appears %d times, want 1", n)
	}
}

// The prompt must forbid the specific move the model made: explaining a sourced
// result away because it disagreed with training.
func TestTheSystemPromptForbidsDismissingASourcedResult(t *testing.T) {
	prompt, err := resolveSystemPrompt("")
	if err != nil {
		t.Fatalf("resolveSystemPrompt: %v", err)
	}
	for _, want := range []string{
		"THE SOURCE WINS",
		"fictional",
		"That feeling is what",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the system prompt is missing %q; the model may dismiss current facts as mistakes", want)
		}
	}
}

// The two nudges must say different things, because the models they are
// talking to believe different things.
func TestTheUncheckedNudgeAddressesConfidenceRatherThanHedging(t *testing.T) {
	if nudgeTextFor(true) != groundingNudge {
		t.Error("a hedged answer got the wrong nudge")
	}
	unchecked := nudgeTextFor(false)
	if unchecked == groundingNudge {
		t.Fatal("an unhedged answer got the hedge nudge; it would be told it said something it did not say")
	}
	lower := strings.ToLower(unchecked)
	// The claim the confident model has no basis for, named explicitly.
	if !strings.Contains(lower, "confidence is not evidence") {
		t.Error("the unchecked nudge does not challenge the model's certainty, which is the whole reason it did not search")
	}
	if !strings.Contains(lower, "correct yourself") {
		t.Error("the unchecked nudge does not ask for a visible correction; the model would silently reconcile the two answers")
	}
	if !strings.Contains(lower, "ignore this") {
		t.Error("the unchecked nudge gives the model no way to decline a false positive")
	}
}
