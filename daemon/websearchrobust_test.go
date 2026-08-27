package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// THE TWO REAL MARKUPS, COPIED FROM A LIVE PROBE.
//
// They differ in attribute ORDER and QUOTE STYLE, and that is the whole reason
// the parser was rewritten: the original regex required class-before-href in
// double quotes, so it read the first and silently returned nothing for the
// second.
const ddgHTMLMarkup = `<div class="result">` +
	`<a rel="nofollow" class="result__a" href="https://en.wikipedia.org/wiki/Chief_Minister_of_Tamil_Nadu">Chief Minister of Tamil Nadu - Wikipedia</a>` +
	`<a class="result__snippet" href="#">The current Chief Minister is ...</a></div>`

const ddgLiteMarkup = `<table><tr><td>` +
	`<a rel="nofollow" href="https://en.wikipedia.org/wiki/Chief_Minister_of_Tamil_Nadu" class='result-link'>Chief Minister of Tamil Nadu - Wikipedia</a>` +
	`</td></tr></table>`

func TestBothLiveMarkupsParse(t *testing.T) {
	cases := []struct {
		name, body, hint string
	}{
		{"html endpoint: class before href, double quotes", ddgHTMLMarkup, "result__a"},
		{"lite endpoint: href before class, SINGLE quotes", ddgLiteMarkup, "result-link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, fallback := selectResults(extractAnchors(tc.body), tc.hint, "duckduckgo.com", 5)
			if len(got) != 1 {
				t.Fatalf("parsed %d result(s), want 1", len(got))
			}
			if fallback {
				t.Error("the class hint did not match, so the generic path was used for markup it should read directly")
			}
			if !strings.Contains(got[0].URL, "en.wikipedia.org") {
				t.Errorf("URL = %q", got[0].URL)
			}
			if !strings.Contains(got[0].Title, "Chief Minister") {
				t.Errorf("Title = %q", got[0].Title)
			}
		})
	}
}

// THE FAILURE THIS EXISTS TO SURVIVE: the operator renames their CSS class.
// The specific parser matches nothing and the generic one must still find the
// results, because the alternative is a silent fall back to the model's memory.
func TestARenamedResultClassStillYieldsResults(t *testing.T) {
	renamed := strings.ReplaceAll(ddgHTMLMarkup, "result__a", "brand-new-class-name")
	got, fallback := selectResults(extractAnchors(renamed), "result__a", "duckduckgo.com", 5)
	if len(got) == 0 {
		t.Fatal("a class rename returned zero results; the search would silently stop working")
	}
	if !fallback {
		t.Error("the fallback path was not reported, so the degradation would be invisible")
	}
	if !strings.Contains(got[0].URL, "en.wikipedia.org") {
		t.Errorf("generic extraction picked the wrong link: %q", got[0].URL)
	}
}

// The engine's own navigation is not a result.
func TestEngineNavigationIsNotMistakenForResults(t *testing.T) {
	body := `<a href="https://duckduckgo.com/settings">Settings</a>` +
		`<a href="/about">About</a>` +
		`<a href="javascript:void(0)">x</a>` +
		`<a href="https://example.org/real">Real result</a>`
	got, _ := selectResults(extractAnchors(body), "nope", "duckduckgo.com", 5)
	if len(got) != 1 {
		t.Fatalf("got %d result(s), want only the outbound one: %+v", len(got), got)
	}
	if got[0].URL != "https://example.org/real" {
		t.Errorf("URL = %q", got[0].URL)
	}
}

// "WE COULD NOT READ THE PAGE" AND "THERE WERE NO RESULTS" MUST NOT LOOK ALIKE.
//
// They produce the same empty list and only one is a bug on our side. Conflating
// them is exactly how a parser breakage hides: every request succeeds, every
// search returns nothing, and the answers quietly come from memory instead.
func TestAnUnreadablePageIsReportedAsAParseFailureNotAsNoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// A big, real-looking page with nothing this parser can use.
		fmt.Fprintf(w, "<html><body>%s</body></html>", strings.Repeat("<p>content</p>", 400))
	}))
	defer srv.Close()

	be := searchBackend{name: "test", endpoint: srv.URL, classHint: "result__a"}
	_, _, err := searchOnce(context.Background(), unguardedTestClient(srv), be, "q", 5)
	if err == nil {
		t.Fatal("an unparseable page was reported as success")
	}
	if !errors.Is(err, errSearchParse) {
		t.Fatalf("err = %v, want it to wrap errSearchParse so the counter and the fallback can act on it", err)
	}
}

// A genuinely small/empty response is NOT a parse failure.
func TestATinyResponseIsNotCalledAParseFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>no results</body></html>"))
	}))
	defer srv.Close()

	be := searchBackend{name: "test", endpoint: srv.URL, classHint: "result__a"}
	got, _, err := searchOnce(context.Background(), unguardedTestClient(srv), be, "q", 5)
	if err != nil {
		t.Fatalf("a small empty page was reported as a failure: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d results from an empty page", len(got))
	}
}

// A configured endpoint REPLACES the chain. Falling back to DuckDuckGo would
// send the query to a service the user had deliberately configured away from.
func TestAConfiguredEndpointIsNeverSilentlyFallenBackFrom(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, servedBy, _, err := searchWeb(context.Background(), unguardedTestClient(srv),
		MCPWebConfig{Endpoint: srv.URL}, "q", 5)
	if err == nil {
		t.Fatal("a failing configured endpoint was papered over")
	}
	if servedBy != "" {
		t.Errorf("servedBy = %q, want none — nothing served this", servedBy)
	}
	if hits != 1 {
		t.Errorf("the configured endpoint was tried %d time(s), want 1 and no fallback elsewhere", hits)
	}
}

// SOURCE DIVERSITY. Three pages from one site cannot corroborate each other,
// and cross-checking is the entire reason several are opened.
func TestFetchTargetsPreferDistinctSites(t *testing.T) {
	results := []searchResult{
		{URL: "https://en.wikipedia.org/wiki/A"},
		{URL: "https://en.wikipedia.org/wiki/B"},
		{URL: "https://www.wikipedia.org/wiki/C"},
		{URL: "https://example.org/x"},
		{URL: "https://news.site.test/y"},
	}
	chosen := chooseDiverseResults(MCPWebConfig{}, results, 3)
	if len(chosen) != 3 {
		t.Fatalf("chose %d, want 3", len(chosen))
	}
	sites := map[string]bool{}
	for _, c := range chosen {
		site := registrableDomain(c.URL)
		if sites[site] {
			t.Errorf("chose two pages from %s: %+v", site, chosen)
		}
		sites[site] = true
	}
	// Rank order is kept among the distinct sites.
	if chosen[0].URL != "https://en.wikipedia.org/wiki/A" {
		t.Errorf("top-ranked result was not chosen first: %q", chosen[0].URL)
	}
}

// When every result IS from one site, a second page from it beats fetching
// nothing.
func TestDiversityDoesNotStarveASingleSourceSearch(t *testing.T) {
	results := []searchResult{
		{URL: "https://en.wikipedia.org/wiki/A"},
		{URL: "https://en.wikipedia.org/wiki/B"},
		{URL: "https://en.wikipedia.org/wiki/C"},
	}
	if got := chooseDiverseResults(MCPWebConfig{}, results, 3); len(got) != 3 {
		t.Fatalf("chose %d of 3 same-site results, want all 3 rather than starving", len(got))
	}
}

// THE SPEED FIX, MEASURED. Sequential fetching cost search + the SUM of the
// page loads; a live search was 36.9s. Three independent GETs have no ordering
// between them, so the cost must be the SLOWEST page, not their total.
func TestPagesAreFetchedConcurrently(t *testing.T) {
	const delay = 600 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>a page with some readable text</p></body></html>"))
	}))
	defer srv.Close()

	// Distinct hostnames so diversity selection keeps all three; they all
	// resolve to the same test server.
	results := []searchResult{
		{URL: srv.URL + "/one"}, {URL: srv.URL + "/two"}, {URL: srv.URL + "/three"},
	}
	// chooseDiverseResults would collapse these to one site, so exercise the
	// fetch path directly with the selection already made.
	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{Enabled: true}}

	start := time.Now()
	var pages []fetchedOrNot
	done := make(chan struct{})
	go func() {
		pages = s.fetchPagesConcurrently(context.Background(), unguardedTestClient(srv),
			MCPWebConfig{FetchTop: 3}, results)
		close(done)
	}()
	<-done
	elapsed := time.Since(start)

	if len(pages) == 0 {
		t.Fatal("no pages fetched")
	}
	// Sequential would be >= 3*delay. Generous ceiling so this is not a flaky
	// timing test -- it only fails if the fetches genuinely serialise.
	if ceiling := 2 * delay; elapsed > ceiling {
		t.Errorf("fetched %d page(s) in %v; concurrent work should finish well under %v (sequential would be ~%v)",
			len(pages), elapsed, ceiling, 3*delay)
	}
}

// Order must follow the engine's ranking, not whichever host answered first.
func TestConcurrentFetchesKeepRankOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first-ranked page is the SLOWEST, so any order-by-arrival bug
		// puts it last.
		if strings.HasSuffix(r.URL.Path, "/one") {
			time.Sleep(400 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body><p>page %s here</p></body></html>", r.URL.Path)
	}))
	defer srv.Close()

	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{Enabled: true}}
	results := []searchResult{
		{URL: srv.URL + "/one"}, {URL: srv.URL + "/two"}, {URL: srv.URL + "/three"},
	}
	pages := s.fetchPagesConcurrently(context.Background(), unguardedTestClient(srv),
		MCPWebConfig{FetchTop: 3}, results)

	if len(pages) != 3 {
		t.Fatalf("got %d pages, want 3", len(pages))
	}
	for i, want := range []string{"/one", "/two", "/three"} {
		if !strings.HasSuffix(pages[i].url, want) {
			t.Errorf("position %d = %q, want %s — results were reordered by response speed", i, pages[i].url, want)
		}
	}
}

// SNIPPETS ARE PART OF THE PARSE AND WERE NOT ASSERTED, so a broken snippet
// regex passed every test and shipped: the live class is "result__snippet" with
// a DOUBLE underscore, and the pattern allowed exactly one separator. Snippets
// vanished from every listing and nothing failed.
func TestSnippetsAreParsedFromTheRealClassName(t *testing.T) {
	results, _ := selectResults(extractAnchors(ddgHTMLMarkup), "result__a", "duckduckgo.com", 5)
	if len(results) != 1 {
		t.Fatalf("parsed %d result(s), want 1", len(results))
	}
	snips := snippetRe.FindAllStringSubmatch(ddgHTMLMarkup, 5)
	if len(snips) == 0 {
		t.Fatal("no snippet matched result__snippet; the listing loses its summaries silently")
	}
	if got := stripTags(snips[0][1]); !strings.Contains(got, "current Chief Minister") {
		t.Errorf("snippet = %q", got)
	}
}
