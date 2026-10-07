package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// THE PAGES A TURN READ, LISTED BY THE PROGRAM THAT READ THEM.
//
// MEASURED 2026-10-07: eight questions about current facts, one model, the
// prompt as it stood ("say where it came from -- the source's name or URL").
// The model cited eight ways. A list of names with no address. A list with
// addresses. One address in brackets no keyboard has. A pointer nothing can
// open (citemarks.go). An italic line naming a site. An answer a person is
// meant to check should not depend on how the model felt like citing it that
// time -- and the first of those, the one the owner was looking at, could not
// be checked at all.
//
// The daemon fetched those pages itself. It knows which ones the model was
// shown, to the address, whatever the model then writes. So it says so, under
// the answer, every time.
//
// WHAT THE BLOCK CLAIMS, AND WHAT IT DOES NOT. That these pages were read for
// this answer. Not that every one was used: the daemon cannot know which
// sentence came from where, and the heading says "read" for that reason. A
// page that could not be opened is not listed; if none could, the block says
// the answer rests on search results alone and lists those.
//
// ADDRESSES, NOT TITLES. A title is a sentence of the page author's choosing,
// and this block becomes part of the answer: it is saved, and a client sends it
// back as the assistant's own words on the next turn -- the most trusted place
// a stranger's sentence could be put. An address is the author's choice too,
// but the prompt has asked the model to write its sources' addresses into the
// answer since the web tools shipped, so nothing reaches the conversation this
// way that could not already. It is shown in plain printable characters only
// (showableAddress).
//
// AS TEXT, AT THE END OF THE ANSWER, not as a field of its own. It reaches both
// clients without either learning anything new, and it is still there when a
// saved chat is opened next week -- which a field would not be, because a chat
// is stored as a question and an answer and nothing else.

// maxSourcesListed bounds the block. A turn that read twenty pages gets five
// lines and a count, not a second screen.
const maxSourcesListed = 5

// maxSourceAddressChars is the longest address shown whole. A longer one is a
// tracking link or a search page; its site is still named.
const maxSourceAddressChars = 200

const (
	sourcesReadHeading   = "Sources (pages read for this answer):"
	sourcesListedHeading = "Sources (search results only; none of the pages could be opened):"
)

// pagesRead collects, for one turn, the pages whose text the model was given.
// Tools may run side by side, hence the lock.
type pagesRead struct {
	mu     sync.Mutex
	read   []string // addresses whose text reached the model, in the order read
	listed []string // addresses a search returned, kept for a turn that could open none
}

type pagesReadKey struct{}

// withPagesRead starts a turn's record of what it read from the web.
func withPagesRead(ctx context.Context) (context.Context, *pagesRead) {
	p := &pagesRead{}
	return context.WithValue(ctx, pagesReadKey{}, p), p
}

// notePageRead records a page whose text was just handed to the model. A no-op
// outside an agent turn, which is every caller that did not start a record.
func notePageRead(ctx context.Context, raw string) {
	if p, ok := ctx.Value(pagesReadKey{}).(*pagesRead); ok && p != nil {
		p.add(&p.read, raw)
	}
}

// noteResultListed records a search result the model was shown a title and a
// snippet of, for the turn in which no page could be opened at all.
func noteResultListed(ctx context.Context, raw string) {
	if p, ok := ctx.Value(pagesReadKey{}).(*pagesRead); ok && p != nil {
		p.add(&p.listed, raw)
	}
}

func (p *pagesRead) add(list *[]string, raw string) {
	address, ok := showableAddress(raw)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, have := range *list {
		if have == address {
			return
		}
	}
	*list = append(*list, address)
}

// under returns the lines to put under a turn's answer, or "". Only under an
// answer: a turn that said nothing has nothing for a list of sources to stand
// under, and "Sources" alone on the screen would read as the answer.
func (p *pagesRead) under(answer string) string {
	if strings.TrimSpace(answer) == "" {
		return ""
	}
	return p.block()
}

// block renders what the turn read, or "" for a turn that read nothing from
// the web.
func (p *pagesRead) block() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	list, heading := p.read, sourcesReadHeading
	if len(list) == 0 {
		list, heading = p.listed, sourcesListedHeading
	}
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + heading)
	for i, address := range list {
		if i == maxSourcesListed {
			b.WriteString(fmt.Sprintf("\n- and %d more", len(list)-i))
			break
		}
		b.WriteString("\n- " + sourceLine(address))
	}
	return b.String()
}

// sourceLine is one page: the site's name, which is what a reader recognises,
// then the address, which is what they can open.
//
// THE ADDRESS IS IN ANGLE BRACKETS, the plain-text way to say "this is an
// address, exactly, from here to there" (RFC 3986, appendix C) -- and to a
// Markdown renderer an autolink, whose contents are literal. Without them
// /wiki/_Go_/ is the word "Go" in italics with two characters missing, and an
// address shown wrong is worse than none.
func sourceLine(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return "<" + address + ">"
	}
	site := strings.TrimPrefix(u.Hostname(), "www.")
	if len(address) > maxSourceAddressChars {
		return site + " — <" + u.Scheme + "://" + u.Host + "/> (a long address; only the site is shown)"
	}
	return site + " — <" + address + ">"
}

// showableAddress returns an http(s) address as it may be printed under an
// answer: in the one form its spellings share, with no sign-in details, and in
// plain printable ASCII.
//
// The address came from a search engine or a redirect, so it is text a stranger
// chose, and this block is shown in a terminal, drawn as Markdown, saved, and
// sent back to the model as its own words. Two things follow.
//
//   - A character that could act on any of those readers -- a control byte, a
//     space, a bracket, a quote, a backtick, anything outside ASCII -- is
//     written percent-encoded, which is the same address as a browser would
//     request it and inert as text.
//   - A host that is not plain letters, digits, dots and hyphens is not shown at
//     all. An encoded host is not a name anyone can recognise, and a name that
//     only looks like one they recognise is the reason not to try.
func showableAddress(raw string) (string, bool) {
	normalised, ok := normaliseWebURL(raw)
	if !ok {
		return "", false
	}
	u, err := url.Parse(normalised)
	if err != nil {
		return "", false
	}
	// fetchURL strips these before it requests the page; the page that was read
	// is the one without them.
	u.User = nil
	if u.Host == "" {
		return "", false
	}
	for i := 0; i < len(u.Host); i++ {
		c := u.Host[i]
		plain := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte(".-:[]", c) >= 0
		if !plain {
			return "", false
		}
	}
	// The host may hold [ ] (an IPv6 literal); nothing after it may.
	rest := strings.TrimPrefix(u.String(), u.Scheme+"://"+u.Host)
	var b strings.Builder
	b.WriteString(u.Scheme + "://" + u.Host)
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c <= ' ' || c > '~' || strings.IndexByte("<>[]\"`\\", c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String(), true
}
