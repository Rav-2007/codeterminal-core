package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mochiii/daemon/mcp"
)

// The two Lane A tools that leave the machine, and the honest descriptions the
// user reads at the approval prompt.

// webToolsEnabled reports whether the web tools should be advertised at all.
//
// NOT ADVERTISED RATHER THAN ADVERTISED-AND-DENIED. A model shown a tool it is
// then refused spends an iteration finding that out and hedges anyway, so the
// user pays for a round trip to arrive at the same non-answer. Absent is the
// honest state and the cheap one.
func (s *Server) webToolsEnabled() bool { return !s.cfg.MCP.Web.Disabled }

// A TOOL DESCRIPTION HAS TWO READERS AND THEY NEED DIFFERENT SENTENCES.
//
// The model reads it to decide WHETHER TO REACH FOR THE TOOL. The human reads
// it at the approval prompt, where protocol.ToolApprovalRequest.Detail carries
// it, to decide WHETHER TO ALLOW THIS CALL. Those are different questions, and
// the first version of these descriptions answered the second one -- badly, by
// duplicating it.
//
// MEASURED ON A REAL PROMPT. The panel printed, in order:
//
//	LEAVES YOUR MACHINE: this sends the text above to a third party ...
//	Mochiii strips secrets on the way out and treats whatever comes back as
//	  untrusted data, never as instructions — but it cannot vouch for the far end.
//	Search the live web and read the top results, for anything that may have
//	  changed since training: ... a library's present API, a version number ...
//	  The query is sent over the internet to a search service; secrets are
//	  stripped from it first, and page text is treated as untrusted data,
//	  never as instructions.
//
// The last paragraph restates the first two almost word for word, and wraps
// them in model-steering ("Prefer this over answering from memory") and generic
// examples that have nothing to do with the question actually being asked. A
// consent prompt that says the same thing three times is not more informative;
// it is longer, and length is what makes a prompt stop being read.
//
// So the disclosure lives in ONE place -- renderApprovalPanel, in the client's
// own voice, where it can be verified against what the daemon actually does --
// and the description says only what the model needs.
//
// The AllowHosts clause stays: it is a real bound, it is not stated anywhere
// else, and both readers need it.
func (s *Server) webSearchDescription() string {
	var b strings.Builder
	b.WriteString("Search the live web and read the top results. Use it for anything that may have " +
		"changed since training — current events, today's facts, a library's present API, who " +
		"currently holds an office — rather than answering from memory.")
	if hosts := s.cfg.MCP.Web.AllowHosts; len(hosts) > 0 {
		// A CONFIGURED BOUND IS PART OF THE DESCRIPTION, resolved from the same
		// config the handler enforces. A user who restricted the agent to their
		// own docs site should see that at the prompt, and a model that knows
		// the bound stops proposing fetches that will be refused.
		b.WriteString(fmt.Sprintf(" Only these hosts may be read: %s.", strings.Join(hosts, ", ")))
	}
	return b.String()
}

// Same split as webSearchDescription: the "it leaves your machine" half belongs
// to the panel, not here. What survives is what the MODEL cannot work out for
// itself -- that JavaScript is not run, so a client-rendered page comes back
// near-empty, and that private addresses are refused, so proposing one wastes
// an iteration.
func (s *Server) webFetchDescription() string {
	var b strings.Builder
	b.WriteString("Fetch one http(s) URL and return its readable text — documentation, a changelog, " +
		"an issue, a URL the user gave you. JavaScript is not run, so a page that renders " +
		"client-side may return little. Private and local addresses are refused.")
	if hosts := s.cfg.MCP.Web.AllowHosts; len(hosts) > 0 {
		b.WriteString(fmt.Sprintf(" Only these hosts may be read: %s.", strings.Join(hosts, ", ")))
	}
	return b.String()
}

// webTools returns the network tools, or nothing when they are switched off.
func (s *Server) webTools() []mcp.Builtin {
	if !s.webToolsEnabled() {
		return nil
	}
	return []mcp.Builtin{
		{
			Tool: mcp.Tool{
				Name:        "web_search",
				Description: s.webSearchDescription(),
				Schema: schema(`{
					"type":"object",
					"properties":{
						"query":{"type":"string","description":"What to search for, as you would type it into a search engine."}
					},
					"required":["query"],
					"additionalProperties":false
				}`),
				// READ-ONLY IS TRUE AND IS NOT THE POINT. It changes nothing
				// here, which is exactly why ReachesNetwork has to be set: the
				// flags that describe local effects all say "harmless", and the
				// user's real question is about the wire.
				ReadOnlyHint:   true,
				ReachesNetwork: true,
			},
			Handler: s.builtinWebSearch,
		},
		{
			Tool: mcp.Tool{
				Name:        "web_fetch",
				Description: s.webFetchDescription(),
				Schema: schema(`{
					"type":"object",
					"properties":{
						"url":{"type":"string","description":"The full http(s) URL to fetch."}
					},
					"required":["url"],
					"additionalProperties":false
				}`),
				ReadOnlyHint:   true,
				ReachesNetwork: true,
			},
			Handler: s.builtinWebFetch,
		},
	}
}

func (s *Server) webHTTPClient() *http.Client {
	return webClient(s.cfg.MCP.Web.resolvedTimeout())
}

// builtinWebSearch searches, then opens the top results and returns their text.
func (s *Server) builtinWebSearch(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return toolError("no query was supplied")
	}

	cfg := s.cfg.MCP.Web
	// SCRUBBED BEFORE IT IS PUT IN A REQUEST, not after. See scrubbedQuery: the
	// query is the half of this tool that no downstream guard can reach.
	query, kinds := scrubbedQuery(args.Query, s.noScrub())
	if len(kinds) > 0 {
		// TOLD TO THE MODEL, not silently done. A model whose query was altered
		// and does not know it will read the results as answers to the question
		// it asked. It is also the only signal the user gets that the loop was
		// about to put a credential into a search engine.
		s.logger.Printf("agent: web_search redacted %s from an outbound query", strings.Join(kinds, ", "))
	}

	client := s.webHTTPClient()
	results, servedBy, notes, err := searchWeb(ctx, client, cfg, query, cfg.resolvedMaxResults())
	for _, n := range notes {
		// EVERY FALLBACK IS SAID OUT LOUD. A search that degrades but still
		// returns something is the failure most likely to go unnoticed for
		// months: answers get quietly worse and nothing errors.
		s.logger.Printf("agent: web_search %s", n)
	}
	if err != nil {
		if errors.Is(err, errSearchParse) {
			s.count(func(c *counters) { c.webSearchParseFailures.Add(1) })
		}
		return toolError("%v", err)
	}
	if len(results) == 0 {
		return toolError("that search returned no results; try different words")
	}

	var b strings.Builder
	if len(kinds) > 0 {
		b.WriteString(fmt.Sprintf("[a secret-shaped value (%s) was removed from this query before it was sent]\n\n",
			strings.Join(kinds, ", ")))
	}
	// EVERY UNTRUSTED FIELD IS DEFUSED HERE, at the point it is written.
	//
	// This result is returned PreNeutralized (see the mcp.Result below), which
	// switches off the loop's own neutralisation pass -- so anything this
	// function writes raw would reach the model raw. A title, a URL and a
	// snippet all come from the search engine, which is reporting what a page
	// author chose to put there; ranking a page with a crafted title is cheap
	// and this text is read before any page is fetched. The query is echoed
	// back too, and it is the MODEL's text rather than the user's, so it can
	// carry whatever the model was talked into writing.
	b.WriteString(fmt.Sprintf("Search results for %q (via %s):\n", neutralizeDelimiters(query), servedBy))
	for i, r := range results {
		b.WriteString(fmt.Sprintf("\n%d. %s\n   %s\n", i+1, neutralizeDelimiters(r.Title), neutralizeDelimiters(r.URL)))
		if r.Snippet != "" {
			b.WriteString("   " + neutralizeDelimiters(r.Snippet) + "\n")
		}
	}

	// THEN ACTUALLY READ THEM. A snippet is an engine's summary; the point of
	// this tool is that the model reasons over the source. Failures per page
	// are reported and skipped rather than failing the search: two good pages
	// and one dead link is a useful result, and aborting on the dead link would
	// send the model back to answering from memory.
	//
	// CONCURRENTLY, AND THAT IS THE DIFFERENCE BETWEEN USABLE AND NOT. These
	// fetches used to run one after another, so the call cost search + the SUM
	// of three page loads; a live search was measured at 36.9 SECONDS, most of
	// it spent waiting on one page at a time while the user watched a spinner.
	// Nothing about them is ordered -- three independent GETs to three
	// unrelated hosts -- so the cost is now search + the SLOWEST page.
	pages := s.fetchPagesConcurrently(ctx, client, cfg, results)

	fetched := 0
	for _, p := range pages {
		if p.err != nil {
			// p.url is model-supplied and p.err quotes it back; same reason as
			// the loop above.
			b.WriteString(fmt.Sprintf("\n[could not read %s: %v]\n",
				neutralizeDelimiters(p.url), neutralizeDelimiters(p.err.Error())))
			continue
		}
		p.page.Text = clipChars(p.page.Text, perPageChars)
		b.WriteString("\n" + webContentEnvelope(p.page) + "\n")
		fetched++
	}
	if fetched == 0 {
		b.WriteString("\n[none of these pages could be read; the titles, URLs and snippets above are all that is available]\n")
	}

	// PreNeutralized: every untrusted field above was defused as it was
	// written, and webContentEnvelope defused each page before wrapping it. The
	// loop must not run a second pass, which would dismantle the <web_content>
	// fences this just built rather than an attacker's.
	return mcp.Result{Content: b.String(), PreNeutralized: true}, nil
}

// fetchedOrNot is one page attempt, kept in the order the engine ranked it.
type fetchedOrNot struct {
	url  string
	page fetchedPage
	err  error
}

// perPageTimeout bounds ONE page, separately from the turn.
//
// Without it a single unresponsive host holds the whole call for as long as the
// client timeout allows, and with concurrency that is worse rather than better:
// every page finishes in two seconds and the call still takes fifteen because
// one host never answers. The slowest page is now bounded by this rather than
// by the worst case of the connection.
const perPageTimeout = 8 * time.Second

// fetchPagesConcurrently reads the chosen results in parallel, preserving rank
// order in the returned slice.
//
// ORDER IS PRESERVED DELIBERATELY. Results arrive in whatever order the network
// returns them, and appending as they land would silently reorder the sources
// by speed -- putting a fast low-relevance page above the engine's top hit, in
// a list the model reads top-down.
func (s *Server) fetchPagesConcurrently(ctx context.Context, client *http.Client, cfg MCPWebConfig, results []searchResult) []fetchedOrNot {
	chosen := chooseDiverseResults(cfg, results, cfg.resolvedFetchTop())
	out := make([]fetchedOrNot, len(chosen))

	var wg sync.WaitGroup
	for i, r := range chosen {
		wg.Add(1)
		go func(i int, r searchResult) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, perPageTimeout)
			defer cancel()
			page, err := fetchURL(pctx, client, r.URL, maxWebFetchBytes)
			out[i] = fetchedOrNot{url: r.URL, page: page, err: err}
		}(i, r)
	}
	wg.Wait()
	return out
}

// chooseDiverseResults picks which results to open, preferring one page per
// site before taking a second from any.
//
// WHY DIVERSITY RATHER THAN JUST THE TOP N. Reading the top three hits often
// means reading the same site three times -- an encyclopaedia article, its
// "List of..." page and its category index all rank together. Three pages that
// share an origin cannot corroborate each other, and the whole reason this tool
// opens several is so the model can notice when sources DISAGREE. Registrable
// domain rather than full host, so news.example.com and www.example.com count
// as one source, which is what they are.
func chooseDiverseResults(cfg MCPWebConfig, results []searchResult, n int) []searchResult {
	if n <= 0 {
		return nil
	}
	var chosen []searchResult
	seenSite := map[string]bool{}
	var seconds []searchResult

	for _, r := range results {
		if !hostOfURLAllowed(cfg, r.URL) {
			continue
		}
		site := registrableDomain(r.URL)
		if seenSite[site] {
			// Kept as a fallback rather than discarded: a second page from an
			// already-seen site still beats fetching nothing when the results
			// are all from one place.
			seconds = append(seconds, r)
			continue
		}
		seenSite[site] = true
		if chosen = append(chosen, r); len(chosen) >= n {
			return chosen
		}
	}
	for _, r := range seconds {
		if len(chosen) >= n {
			break
		}
		chosen = append(chosen, r)
	}
	return chosen
}

// registrableDomain reduces a URL to the last two labels of its host.
//
// A DELIBERATE APPROXIMATION. It calls "example.co.uk" two sites, because doing
// this properly needs the Public Suffix List and this is a grouping heuristic
// for choosing what to read -- not a security boundary. Nothing is permitted or
// refused on the basis of this answer.
func registrableDomain(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// builtinWebFetch retrieves one URL.
func (s *Server) builtinWebFetch(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.URL) == "" {
		return toolError("no URL was supplied")
	}

	cfg := s.cfg.MCP.Web
	if !hostOfURLAllowed(cfg, args.URL) {
		return toolError("that host is not in mcp.web.allow_hosts, which this workspace restricts fetching to")
	}

	page, err := fetchURL(ctx, s.webHTTPClient(), args.URL, maxWebFetchBytes)
	if err != nil {
		return toolError("%v", err)
	}
	// PreNeutralized for the same reason as web_search: webContentEnvelope
	// defuses the page before wrapping it, so a second pass here would strip
	// the daemon's own fence.
	return mcp.Result{Content: webContentEnvelope(page), PreNeutralized: true}, nil
}

// hostOfURLAllowed applies the configured allow-list to a URL.
//
// A URL THAT WILL NOT PARSE IS REFUSED, not passed through. "Cannot tell" and
// "allowed" are the same outcome only if the gate is written by an optimist.
func hostOfURLAllowed(cfg MCPWebConfig, raw string) bool {
	if len(cfg.AllowHosts) == 0 {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return cfg.hostAllowed(u.Hostname())
}

// isWebToolName reports whether a built-in name is one of the network tools.
//
// A NAME LIST, not a registry lookup, because its caller is config validation:
// it runs before any registry exists, and it must be able to say something true
// about a config that names a tool this build might not even have.
func isWebToolName(name string) bool {
	return name == "web_search" || name == "web_fetch"
}
