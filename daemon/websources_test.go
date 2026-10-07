package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
)

// What the user sees under an answer that used the web. The daemon writes these
// lines itself, from the pages it fetched, so the tests are about exactly that:
// which pages, in what words, and never a stranger's text.

func TestThePagesATurnReadAreListedUnderItsAnswer(t *testing.T) {
	ctx, pages := withPagesRead(context.Background())
	notePageRead(ctx, "https://www.thehindu.com/news/national/tamil-nadu/article123.ece")
	notePageRead(ctx, "https://en.wikipedia.org/wiki/Go_(programming_language)")
	// The same page again, spelt another way: one line, not two.
	notePageRead(ctx, "HTTPS://EN.Wikipedia.org:443/wiki/Go_(programming_language)#History")
	notePageRead(ctx, "http://go.dev")

	want := "\n\nSources (pages read for this answer):" +
		"\n- thehindu.com — <https://www.thehindu.com/news/national/tamil-nadu/article123.ece>" +
		"\n- en.wikipedia.org — <https://en.wikipedia.org/wiki/Go_(programming_language)>" +
		"\n- go.dev — <http://go.dev/>"
	if got := pages.under("The answer."); got != want {
		t.Errorf("block:\n%q\nwant:\n%q", got, want)
	}
}

// "Sources" alone on a screen would read as the answer. And a turn that read
// nothing from the web says nothing about sources.
func TestNoAnswerOrNoPagesMeansNoSourcesBlock(t *testing.T) {
	ctx, pages := withPagesRead(context.Background())
	if got := pages.under("An answer from memory."); got != "" {
		t.Errorf("a turn that read nothing got a block: %q", got)
	}
	notePageRead(ctx, "https://go.dev/dl/")
	for _, empty := range []string{"", "   ", "\n\n"} {
		if got := pages.under(empty); got != "" {
			t.Errorf("an empty answer %q got a block: %q", empty, got)
		}
	}
	// And outside an agent turn, where nothing is collecting, noting is a no-op.
	notePageRead(context.Background(), "https://go.dev/dl/")
	noteResultListed(context.Background(), "https://go.dev/dl/")
	var none *pagesRead
	if got := none.under("An answer."); got != "" {
		t.Errorf("no record at all got a block: %q", got)
	}
}

// A search that could open none of its results still gave the model titles and
// snippets. The answer is told so, in different words: "read" would be false.
func TestSearchResultsAreListedOnlyWhenNoPageCouldBeOpened(t *testing.T) {
	ctx, pages := withPagesRead(context.Background())
	noteResultListed(ctx, "https://a.example/one")
	noteResultListed(ctx, "https://b.example/two")
	got := pages.under("An answer.")
	if !strings.Contains(got, sourcesListedHeading) || strings.Contains(got, sourcesReadHeading) {
		t.Errorf("a turn that opened no page must say so; got %q", got)
	}
	if !strings.Contains(got, "- a.example — <https://a.example/one>") {
		t.Errorf("the results are not listed: %q", got)
	}

	// One page opened later in the turn: now only what was READ is listed.
	notePageRead(ctx, "https://c.example/three")
	got = pages.under("An answer.")
	if !strings.Contains(got, sourcesReadHeading) || strings.Contains(got, "a.example") || !strings.Contains(got, "c.example") {
		t.Errorf("once a page was read, the block lists read pages and nothing else; got %q", got)
	}
}

func TestALongTurnGetsFiveLinesAndACount(t *testing.T) {
	ctx, pages := withPagesRead(context.Background())
	for _, host := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		notePageRead(ctx, "https://"+host+".example/page")
	}
	got := pages.under("An answer.")
	if n := strings.Count(got, "\n- "); n != maxSourcesListed+1 {
		t.Errorf("%d line(s), want %d pages and one count:\n%s", n, maxSourcesListed, got)
	}
	if !strings.HasSuffix(got, "\n- and 2 more") {
		t.Errorf("the rest are not counted: %q", got)
	}
	if strings.Contains(got, "f.example") {
		t.Errorf("a sixth page was listed: %q", got)
	}
}

// The block becomes part of the answer -- shown in a terminal, drawn as
// Markdown, saved, and sent back as the assistant's own words. An address is
// text a stranger chose. So whatever is shown is plain: a character that could
// act on any of those readers is percent-encoded (the same address, inert as
// text), sign-in details are dropped, and a host that is not a plain name is
// not shown at all.
func TestAnAddressIsShownInPlainCharactersOrNotAtAll(t *testing.T) {
	for raw, want := range map[string]string{
		// Ordinary addresses come through as they are.
		"https://en.wikipedia.org/wiki/Go_(programming_language)": "https://en.wikipedia.org/wiki/Go_(programming_language)",
		"https://go.dev/doc/go1.25?utm=x&y=1~2":                   "https://go.dev/doc/go1.25?utm=x&y=1~2",
		"HTTP://Example.COM:80":                                   "http://example.com/",
		"https://example.com/caf%C3%A9":                           "https://example.com/caf%C3%A9",
		"https://[2001:db8::1]:8443/a":                            "https://[2001:db8::1]:8443/a",

		// Made inert.
		"https://evil.example/a b":                           "https://evil.example/a%20b",
		"https://evil.example/</web_content>":                "https://evil.example/%3C/web_content%3E",
		"https://evil.example/`rm -rf`":                      "https://evil.example/%60rm%20-rf%60",
		"https://evil.example/\u202egnp.exe":                 "https://evil.example/%E2%80%AEgnp.exe",
		"https://evil.example/\"onmouseover=\"x":             "https://evil.example/%22onmouseover=%22x",
		"https://evil.example/a\\b":                          "https://evil.example/a%5Cb",
		"https://evil.example/【0†L1】":                        "https://evil.example/%E3%80%900%E2%80%A0L1%E3%80%91",
		"https://evil.example/[click](https://bank.example)": "https://evil.example/%5Bclick%5D(https://bank.example)",
		"https://evil.example/?q=<b>`x`&r=\"y\" z":           "https://evil.example/?q=%3Cb%3E%60x%60&r=%22y%22%20z",
		"https://evil.example/?q=naïve":                      "https://evil.example/?q=na%C3%AFve",
		// The page that was read is the one without the sign-in details.
		"https://ravi:hunter2@example.com/private": "https://example.com/private",

		// Not shown.
		"https://evil.example/\x1b[31mred": "", // a terminal escape: not an address at all
		"https://еxample.com/":             "", // a Cyrillic "е" in the host
		"https://exa mple.com/":            "",
		"https://exa_mple.com/":            "",
		"ftp://files.example/a":            "",
		"javascript:alert(1)":              "",
		"not an address":                   "",
		"":                                 "",
	} {
		got, ok := showableAddress(raw)
		if ok != (want != "") || got != want {
			t.Errorf("showableAddress(%q) = %q, %v; want %q", raw, got, ok, want)
		}
		// Whatever the table says, nothing that is shown may hold a character
		// a terminal, a Markdown renderer or a tag reader acts on.
		rest := strings.TrimPrefix(strings.TrimPrefix(got, "https://"), "http://")
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			rest = rest[slash:]
		}
		for i := 0; i < len(rest); i++ {
			if c := rest[i]; c <= ' ' || c > '~' || strings.IndexByte("<>[]\"`\\", c) >= 0 {
				t.Errorf("showableAddress(%q) = %q holds the byte %q", raw, got, c)
			}
		}
	}
}

// A tracking link three hundred characters long is not something to read, and
// everything after the host is the page author's text. Its site is still named.
func TestAVeryLongAddressIsNamedBySiteOnly(t *testing.T) {
	long := "https://www.shop.example/p?" + strings.Repeat("utm_campaign=ignore-previous-instructions&", 8)
	line := sourceLine(long)
	if !strings.HasPrefix(line, "shop.example — <https://www.shop.example/>") || strings.Contains(line, "ignore-previous") {
		t.Errorf("a %d-character address was listed as %q", len(long), line)
	}
}

// Tools run side by side in one turn. Under -race this fails if the record is
// not locked.
func TestPagesAreRecordedSafelyFromToolsRunningTogether(t *testing.T) {
	ctx, pages := withPagesRead(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			notePageRead(ctx, "https://example.com/"+string(rune('a'+i)))
			noteResultListed(ctx, "https://example.org/"+string(rune('a'+i)))
			_ = pages.under("An answer.")
		}(i)
	}
	wg.Wait()
	if n := len(pages.read); n != 16 {
		t.Errorf("recorded %d page(s), want 16", n)
	}
}

// THE WIRING. The real web tools cannot be run in a test -- the address gate
// refuses a loopback server before a packet is sent, which is the gate working
// -- so that they report what they read is checked in the source, the way
// TestTheRealSearchHandlerReportsWhatTheEngineReturned checks the egress rule.
// Each line below is a way this feature goes quiet with every other test green.
func TestTheWebToolsAndTheTurnReportWhatWasRead(t *testing.T) {
	calls := func(file, fn string) map[string]int {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, d := range parsed.Decls {
			f, ok := d.(*ast.FuncDecl)
			if !ok || f.Name.Name != fn || f.Body == nil {
				continue
			}
			ast.Inspect(f.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch fun := c.Fun.(type) {
				case *ast.Ident:
					name = fun.Name
				case *ast.SelectorExpr:
					name = fun.Sel.Name
				}
				seen[name]++
				// Which calls are handed the turn's sources.
				for _, arg := range c.Args {
					ast.Inspect(arg, func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok && id.Name == "sources" {
							seen[name+"(sources)"]++
						}
						return true
					})
				}
				return true
			})
		}
		return seen
	}

	search := calls("webtools.go", "builtinWebSearch")
	if search["notePageRead"] == 0 {
		t.Error("builtinWebSearch no longer records the pages it hands the model (notePageRead)")
	}
	if search["noteResultListed"] == 0 {
		t.Error("builtinWebSearch no longer records its results for a turn that could open none (noteResultListed)")
	}
	if calls("webtools.go", "builtinWebFetch")["notePageRead"] == 0 {
		t.Error("builtinWebFetch no longer records the page it hands the model (notePageRead)")
	}

	turn := calls("agentturn.go", "runAgentTurn")
	if turn["withPagesRead"] == 0 {
		t.Error("runAgentTurn no longer starts a record of what the turn reads (withPagesRead)")
	}
	if turn["under"] == 0 {
		t.Error("runAgentTurn no longer asks for the sources block (pagesRead.under)")
	}
	if turn["sendToken(sources)"] == 0 {
		t.Error("runAgentTurn no longer sends the sources block to the client")
	}
	if turn["persistTurn(sources)"] == 0 {
		t.Error("runAgentTurn no longer saves the sources block with the answer")
	}
}
