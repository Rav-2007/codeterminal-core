package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A LIVE TEST, OFF BY DEFAULT, and both halves of that are deliberate.
//
// Every other test in this change is hermetic, and hermetic tests cannot answer
// the one question that matters here: does the search endpoint still return
// what the parser expects? searchDuckDuckGo reads markup nobody in this
// repository controls, and the day that markup changes, the tool starts
// returning "no results" and the model goes back to answering from memory --
// silently, because "no results" is a perfectly ordinary outcome.
//
// So this exists to be RUN, on purpose, when that suspicion arises:
//
//	MOCHIII_LIVE_WEB=1 go test -run TestLiveWeb ./daemon/
//
// It is skipped in CI because a test that depends on the open internet and the
// stability of somebody's HTML is a test that will eventually fail for reasons
// unrelated to this repository, and a suite that cries wolf gets ignored.
func liveOrSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("MOCHIII_LIVE_WEB") == "" {
		t.Skip("set MOCHIII_LIVE_WEB=1 to run the live web checks")
	}
}

// The exact question from the report that started this work.
func TestLiveWebSearchAnswersTheQuestionThatWasHedged(t *testing.T) {
	liveOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	client := webClient(20 * time.Second)
	results, servedBy, notes, err := searchWeb(ctx, client, MCPWebConfig{}, "who is the prime minister of india", 5)
	if err != nil {
		t.Fatalf("live search failed: %v", err)
	}
	t.Logf("served by %s", servedBy)
	// EVERY NOTE IS A DEGRADATION THAT STILL RETURNED SOMETHING -- the exact
	// state that otherwise goes unnoticed for months. Logged, not failed: a
	// fallback that worked is not a broken build, it is a warning that the
	// primary parser needs attention.
	for _, n := range notes {
		t.Logf("NOTE: %s", n)
	}
	if len(results) == 0 {
		t.Fatal("live search returned zero results — the endpoint's markup has probably changed " +
			"and ddgResultLink no longer matches it. This is the failure mode that looks like " +
			"nothing being wrong.")
	}
	for i, r := range results {
		t.Logf("%d. %s\n   %s\n   %s", i+1, r.Title, r.URL, r.Snippet)
		if r.URL == "" || !strings.HasPrefix(r.URL, "http") {
			t.Errorf("result %d has no usable URL: %q", i+1, r.URL)
		}
		if strings.Contains(r.URL, "duckduckgo.com/l/") {
			t.Errorf("result %d is still a redirector wrapper: %q", i+1, r.URL)
		}
	}
}

func TestLiveWebFetchReturnsReadableText(t *testing.T) {
	liveOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	page, err := fetchURL(ctx, webClient(20*time.Second), "https://example.com/", maxWebFetchBytes)
	if err != nil {
		t.Fatalf("live fetch failed: %v", err)
	}
	t.Logf("title=%q\ntext=%s", page.Title, page.Text)
	if !strings.Contains(strings.ToLower(page.Text), "example domain") {
		t.Errorf("extraction lost the page's own text: %q", page.Text)
	}
}
