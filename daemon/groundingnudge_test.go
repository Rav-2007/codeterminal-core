package main

import (
	"strings"
	"testing"
)

// The sentence from the live report that started this work. If this case ever
// stops being detected, the product has regressed to the behaviour the whole
// change exists to remove.
const observedHedge = `The Prime Minister of India is **Narendra Modi**, who has held the office since May 2014. ` +
	`He was re-elected for a third term in June 2024.

Note: I don't have real-time access to current events, so this is based on my existing knowledge rather than live data.`

func TestTheObservedHedgeIsDetected(t *testing.T) {
	if !looksLikeStalenessHedge(observedHedge) {
		t.Fatal("the exact answer this change was filed against is not recognised as a hedge")
	}
}

func TestHedgeDetectionOnAnswersThatShouldAndShouldNotTrigger(t *testing.T) {
	hedges := []string{
		"...answer... Note: I don't have real-time access to current events.",
		"That said, my knowledge cutoff means this may be out of date.",
		"As of my last update, the current version was 3.2.",
		"I cannot browse the web, so please verify with a current source.",
		"This information might have changed since my training data was collected.",
	}
	for _, h := range hedges {
		if !looksLikeStalenessHedge(h) {
			t.Errorf("looksLikeStalenessHedge(%q) = false, want true", h)
		}
	}

	// FALSE POSITIVES ARE THE REAL RISK, because the detector runs on ordinary
	// coding answers all day. These are the shapes that would trip a naive
	// substring match, and every one of them is a legitimate answer that must
	// be left alone.
	fine := []string{
		"",
		"Use time.Now() and compare it against the cached value.",
		"func handleStale(cache *Cache) error { return cache.Refresh() }",
		// The words appear, but as the SUBJECT of the answer rather than as a
		// disclaimer about it -- and far from the end, which is why the
		// detector only reads the tail.
		"A model's knowledge cutoff is the date its training data ends. " +
			strings.Repeat("Here is how you would store that date in your config and surface it in the UI. ", 12),
		"The retry loop gives up after three attempts and logs the failure.",
	}
	for _, f := range fine {
		if looksLikeStalenessHedge(f) {
			t.Errorf("looksLikeStalenessHedge(%q) = true; a normal answer was flagged", truncateForClient(f, 80))
		}
	}
}

func TestTheNudgeOnlyAppliesWhenAWebToolWasOfferedAndUnused(t *testing.T) {
	withWeb := []toolSpec{
		{Function: toolSpecFunction{Name: "builtin__read_file"}},
		{Function: toolSpecFunction{Name: "builtin__web_search"}},
	}
	withoutWeb := []toolSpec{
		{Function: toolSpecFunction{Name: "builtin__read_file"}},
		{Function: toolSpecFunction{Name: "builtin__search_code"}},
	}

	if !webToolOffered(withWeb) {
		t.Error("webToolOffered missed an advertised web_search")
	}
	// The important negative: nudging toward a tool that was never advertised
	// buys an iteration that ends in the same hedge.
	if webToolOffered(withoutWeb) {
		t.Error("webToolOffered reported a web tool that was not on the menu")
	}

	if webToolUsed([]string{"builtin__read_file", "builtin__search_code"}) {
		t.Error("webToolUsed reported a call that never happened")
	}
	// A model that DID search and still qualified its answer is being careful,
	// not lazy. Nudging it would tell it to do what it just did.
	if !webToolUsed([]string{"builtin__read_file", "builtin__web_search"}) {
		t.Error("webToolUsed missed a web_search that ran")
	}
	if !webToolUsed([]string{"builtin__web_fetch"}) {
		t.Error("webToolUsed missed a web_fetch that ran")
	}
}

// The nudge text itself has a job beyond existing: it must give the model an
// exit, because a heuristic that cannot be overruled is a heuristic that is
// wrong in public.
func TestTheNudgeTextPermitsTheModelToDecline(t *testing.T) {
	lower := strings.ToLower(groundingNudge)
	if !strings.Contains(lower, "ignore this") {
		t.Error("the nudge does not let the model decline; a false positive would then force a pointless search")
	}
	if !strings.Contains(lower, "web_search") {
		t.Error("the nudge does not name the tool to call")
	}
	if !strings.Contains(lower, "citing") && !strings.Contains(lower, "source") {
		t.Error("the nudge does not ask for a citation, so a looked-up answer stays unverifiable")
	}
}

// The user watched the hedge arrive. Text after it needs an explanation.
func TestTheUserIsToldWhyMoreTextIsComing(t *testing.T) {
	if strings.TrimSpace(nudgeNotice) == "" {
		t.Fatal("the nudge is silent; more text would arrive after a hedge with no explanation")
	}
	if !strings.Contains(strings.ToLower(nudgeNotice), "web") {
		t.Error("the notice does not say what is happening")
	}
}

func TestQualifiedBuiltinNamesMatchWhatTheModelIsShown(t *testing.T) {
	if got := mcpBuiltinQualified("web_search"); got != "builtin__web_search" {
		t.Fatalf("mcpBuiltinQualified = %q; the nudge would match nothing", got)
	}
}

// A BARE TOOL NAME IS A SUCCESSFUL CALL, NOT A FAILED ONE.
//
// turn.toolNames records what the MODEL typed, and models routinely type
// "web_search" rather than "builtin__web_search". The daemon resolves the bare
// form and runs it. The first version of webToolUsed compared only against the
// qualified spelling, so every such call was invisible to the backstop.
//
// MEASURED LIVE: the model searched twice by bare name and the loop still
// logged "answered without a lookup" and burned another model call re-asking
// for the search it had already done.
func TestABareToolNameCountsAsHavingSearched(t *testing.T) {
	if !webToolUsed([]string{"web_search"}) {
		t.Error("a bare web_search call was not recognised; the backstop would re-ask for a search that happened")
	}
	if !webToolUsed([]string{"read_file", "web_fetch"}) {
		t.Error("a bare web_fetch call was not recognised")
	}
	// The qualified form must keep working.
	if !webToolUsed([]string{"builtin__web_search"}) {
		t.Error("the qualified form regressed")
	}
	// And unrelated tools must still not count, in either spelling.
	if webToolUsed([]string{"read_file", "builtin__search_code", "search_code"}) {
		t.Error("a non-web tool was counted as a search")
	}
}

func TestUnqualifiedToolNameHandlesBothSpellings(t *testing.T) {
	cases := map[string]string{
		"builtin__web_search": "web_search",
		"web_search":          "web_search",
		"someserver__do_it":   "do_it",
		"":                    "",
	}
	for in, want := range cases {
		if got := unqualifiedToolName(in); got != want {
			t.Errorf("unqualifiedToolName(%q) = %q, want %q", in, got, want)
		}
	}
}
