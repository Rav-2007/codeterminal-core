package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// linkSpan is one stretch of a line drawn as a hyperlink: where it opens, and
// the characters on screen.
type linkSpan struct{ target, text string }

// hyperlinkSpans reads line's OSC 8 sequences. why is "" for a line whose
// sequences are all well formed and closed.
func hyperlinkSpans(line string) (spans []linkSpan, why string) {
	const open = "\x1b]8;;"
	inside, target, from := false, "", 0
	for {
		i := strings.Index(line[from:], open)
		if i < 0 {
			break
		}
		i += from
		end := strings.Index(line[i+len(open):], "\x1b\\")
		if end < 0 {
			return spans, "a hyperlink sequence is not terminated"
		}
		t := line[i+len(open) : i+len(open)+end]
		next := i + len(open) + end + 2
		switch {
		case t != "" && inside:
			return spans, "a hyperlink opens inside another"
		case t != "":
			inside, target = true, t
		case !inside:
			return spans, "a hyperlink closes that was never opened"
		default:
			spans = append(spans, linkSpan{target, ansi.Strip(line[from:i])})
			inside = false
		}
		from = next
	}
	if inside {
		return spans, "a hyperlink is left open at the end of the line"
	}
	return spans, ""
}

// badHyperlinks is why line's hyperlinks break a rule the rest of the client
// relies on, or "": every link closes on its own line (Bubble Tea redraws only
// the lines that changed, so one left open would carry on into whatever is
// drawn next), every target is one hyperlinkTarget allows, and every linked
// character is part of the address it opens -- a link hides nothing.
func badHyperlinks(line string) string {
	spans, why := hyperlinkSpans(line)
	if why != "" {
		return why
	}
	for _, s := range spans {
		if hyperlinkTarget(s.target) != s.target {
			return "a hyperlink to " + strings.ToValidUTF8(s.target, "?") + ", which is not an allowed target"
		}
		if s.text == "" || !strings.Contains(s.target, s.text) {
			return "the text " + s.text + " links to " + s.target + ", which it is not a part of"
		}
	}
	return ""
}

// The owner's screenshot, 2026-11-21: the Sources the daemon writes under an
// answer (daemon/websources.go). At 80 columns the third address does not fit
// on its line and is broken in two. Measured in VTE 0.76 before this change:
// ctrl+click on its first piece opened ".../most-visited-websites-in-the-world-202",
// and its second piece ("4/") opened nothing.
func TestAWrappedAddressOpensWhole(t *testing.T) {
	addresses := []string{
		"https://www.visualcapitalist.com/ranked-the-most-visited-websites-in-2024/",
		"https://en.wikipedia.org/wiki/List_of_most-visited_websites",
		"https://infographicsite.com/infographic/most-visited-websites-in-the-world-2024/",
	}
	answer := "Google.com is the most visited website in the world.\n\n" +
		"Sources (pages read for this answer):\n" +
		"- visualcapitalist.com — <" + addresses[0] + ">\n" +
		"- en.wikipedia.org — <" + addresses[1] + ">\n" +
		"- infographicsite.com — <" + addresses[2] + ">"

	for _, width := range []int{80, 40, 200} {
		var out string
		withColorProfile(t, termenv.ANSI256, func() { out = renderAnswer(answer, width) })
		pieces := map[string][]string{}
		for _, line := range strings.Split(out, "\n") {
			spans, why := hyperlinkSpans(line)
			if why != "" {
				t.Fatalf("width %d: %s: %q", width, why, line)
			}
			for _, s := range spans {
				pieces[s.target] = append(pieces[s.target], s.text)
			}
		}
		for _, a := range addresses {
			if got := strings.Join(pieces[a], ""); got != a {
				t.Errorf("width %d: the pieces linked to %s spell %q; every piece of the address must open all of it\n%s",
					width, a, got, out)
			}
		}
		if width == 80 && len(pieces[addresses[2]]) < 2 {
			t.Fatalf("at 80 columns the third address was meant to wrap (the case this test is for); it did not:\n%s", out)
		}
		if len(pieces) != len(addresses) {
			t.Errorf("width %d: links to %d targets, want exactly the %d addresses: %v", width, len(pieces), len(addresses), pieces)
		}
	}
}

// A label is never a link: "[click here](https://elsewhere)" would put a
// target under words that do not show it. The address the renderer prints
// after the label is the link, so what opens is what is on screen.
func TestALabelIsNeverALink(t *testing.T) {
	answer := "see [click here](https://evil.example/x) or [https://a.dev/y](https://a.dev/y), " +
		"[home](#top), <mailto:a@b.c> and <https://b.dev/z>"
	var out string
	withColorProfile(t, termenv.ANSI256, func() { out = renderAnswer(answer, 200) })
	spans, why := hyperlinkSpans(out)
	if why != "" {
		t.Fatalf("%s: %q", why, out)
	}
	got := map[string]string{}
	for _, s := range spans {
		got[s.text] = s.target
	}
	want := map[string]string{
		"https://evil.example/x": "https://evil.example/x",
		"https://a.dev/y":        "https://a.dev/y",
		"https://b.dev/z":        "https://b.dev/z",
	}
	if len(got) != len(want) {
		t.Errorf("linked %v, want exactly %v\n%q", got, want, out)
	}
	for text, target := range want {
		if got[text] != target {
			t.Errorf("%q links to %q, want %q\n%q", text, got[text], target, out)
		}
	}
	if why := badHyperlinks(out); why != "" {
		t.Errorf("%s: %q", why, out)
	}
}

// What can be a target. The address is text a model or a web page chose, and
// it goes inside an escape sequence: one ESC, BEL or other control byte in it
// could end the sequence early and have the terminal run the rest.
func TestOnlyAPrintableWebAddressIsATarget(t *testing.T) {
	long := "https://x.dev/" + strings.Repeat("a", maxHyperlinkTarget)
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"https://example.com/a?b=1&c=2#d", true},
		{"http://example.com", true},
		{"HTTPS://EXAMPLE.COM/x", true},
		{"https://example.com/a;b\\c", true}, // ';' and a lone backslash cannot end it
		{"https://", false},
		{"https:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"file:///etc/passwd", false},
		{"mailto:a@b.c", false},
		{"ftp://example.com/", false},
		{"https://example.com/\x1b]0;owned\x07", false},
		{"https://example.com/\x1b\\x", false},
		{"https://example.com/\x07", false},
		{"https://example.com/a b", false},
		{"https://example.com/\x7f", false},
		{"https://ex\u0430mple.com/", false},  // a Cyrillic a (U+0430): shown, not linked
		{"https://example.com/\u202e", false}, // right-to-left override
		{long, false},
	} {
		got := hyperlinkTarget(c.in)
		if c.ok && got != c.in || !c.ok && got != "" {
			t.Errorf("hyperlinkTarget(%q) = %q, want ok=%v", c.in, got, c.ok)
		}
	}

	// And through the renderer, without the sanitizer in front of it: an
	// address that carries a control byte is drawn as text and never linked.
	var out string
	withColorProfile(t, termenv.ANSI256, func() {
		out = renderAnswer("<https://x.dev/\x1b]0;owned\x07> [a](https://y.dev/\x07z)", 200)
	})
	if strings.Contains(out, "\x1b]8;;https://x.dev") || strings.Contains(out, "\x1b]8;;https://y.dev") {
		t.Errorf("an address with a control byte in it was linked: %q", out)
	}
}

// The Linux console ends an OSC it does not know after its first character
// and prints the rest ("8;;https://..."), and "dumb" prints everything. They
// get no hyperlinks; nor does NO_COLOR, whose promise is no escape at all.
func TestNoHyperlinksWhereTheTerminalWouldPrintThem(t *testing.T) {
	for term, want := range map[string]bool{
		"xterm-256color": true, "tmux-256color": true, "screen": true, "vt100": true,
		"":      true, // Windows sets no TERM, and its terminals read OSC 8
		"linux": false, "linux-16color": false, "dumb": false,
	} {
		if got := hyperlinksFor(term); got != want {
			t.Errorf("hyperlinksFor(%q) = %v, want %v", term, got, want)
		}
	}

	const answer = "Sources (pages read for this answer):\n- a.dev — <https://a.dev/x>"
	var out string
	withColorProfile(t, termenv.ANSI256, func() { out = renderAnswer(answer, 80) })
	if !strings.Contains(out, "\x1b]8;;https://a.dev/x\x1b\\") {
		t.Fatalf("a terminal that reads hyperlinks got none: %q", out)
	}

	was := hyperlinksOn.Load()
	t.Cleanup(func() { hyperlinksOn.Store(was) })
	hyperlinksOn.Store(false)
	withColorProfile(t, termenv.ANSI256, func() { out = renderAnswer(answer, 80) })
	if strings.Contains(out, "\x1b]8") {
		t.Errorf("hyperlinks were sent to a terminal that prints them: %q", out)
	}
	if !strings.Contains(ansi.Strip(out), "https://a.dev/x") {
		t.Errorf("the address itself went missing: %q", out)
	}

	hyperlinksOn.Store(true)
	withColorProfile(t, termenv.Ascii, func() { out = renderAnswer(answer, 80) })
	if strings.Contains(out, "\x1b") {
		t.Errorf("NO_COLOR got an escape sequence: %q", out)
	}
}

// Main decides from TERM before anything draws, next to the colour profile. A
// test renders with hyperlinks on whatever TERM it runs under, so nothing else
// would notice the call going missing -- and without it the Linux console
// prints every link's sequence as text.
func TestMainDecidesHyperlinksBeforeAnythingDraws(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Main" {
			continue
		}
		for i, st := range fn.Body.List {
			if i > 2 {
				break
			}
			if es, ok := st.(*ast.ExprStmt); ok {
				if call, ok := es.X.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "pinHyperlinks" {
						return
					}
				}
			}
		}
		t.Fatal("Main does not call pinHyperlinks among its first statements")
	}
	t.Fatal("no func Main in main.go")
}

// /help says how a link opens. The owner clicked one and nothing happened:
// a plain click selects, and the terminal opens a link on ctrl+click.
func TestHelpSaysHowALinkOpens(t *testing.T) {
	if got := linkKeyFor("linux"); got != "ctrl+click" {
		t.Errorf("linkKeyFor(linux) = %q, want ctrl+click", got)
	}
	if got := linkKeyFor("windows"); got != "ctrl+click" {
		t.Errorf("linkKeyFor(windows) = %q, want ctrl+click", got)
	}
	if got := linkKeyFor("darwin"); got != "cmd+click" {
		t.Errorf("linkKeyFor(darwin) = %q, want cmd+click", got)
	}
	help := formatSlashHelp()
	var line string
	for _, l := range strings.Split(help, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), linkKey()+" ") {
			line = l
		}
	}
	if !strings.Contains(line, "open a link") {
		t.Errorf("/help has no %s line saying it opens a link:\n%s", linkKey(), help)
	}
}
