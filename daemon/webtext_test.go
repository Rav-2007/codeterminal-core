package main

import (
	"strings"
	"testing"
)

func TestExtractHTMLDropsChromeAndKeepsSentences(t *testing.T) {
	const page = `<!DOCTYPE html>
<html><head><title>  Example   Page </title>
<script>var secret = "do not leak";</script>
<style>body{margin:0}</style>
</head>
<body>
<nav><a href="/">Home</a><a href="/about">About</a></nav>
<h1>The Heading</h1>
<p>First paragraph.</p><p>Second paragraph.</p>
<ul><li>alpha</li><li>beta</li></ul>
<footer>&copy; 2026 Someone</footer>
<noscript>Enable JavaScript</noscript>
</body></html>`

	title, text := extractReadableText(page, "text/html")
	if title != "Example Page" {
		t.Errorf("title = %q, want whitespace-normalised %q", title, "Example Page")
	}
	for _, want := range []string{"The Heading", "First paragraph.", "Second paragraph.", "alpha", "beta"} {
		if !strings.Contains(text, want) {
			t.Errorf("text is missing %q; content must never be dropped as chrome", want)
		}
	}
	for _, unwanted := range []string{"do not leak", "margin:0", "Home", "Enable JavaScript", "2026 Someone"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("text contains %q, which is chrome or script and should not reach the model", unwanted)
		}
	}
	// Adjacent blocks must not weld into a sentence neither of them said.
	if strings.Contains(text, "First paragraph.Second") {
		t.Error("adjacent paragraphs ran together; block boundaries are not producing newlines")
	}
}

func TestExtractHTMLSurvivesMarkupThatIsNotMarkup(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			// The classic: a lone '<' used as less-than. Scanning to the next
			// '>' would eat the rest of the document.
			name: "a bare less-than is text",
			in:   "<p>if a < b then done</p><p>after</p>",
			want: "after",
		},
		{
			// A comment containing '>' must be scanned to "-->" and not to the
			// first '>', or the tail of the comment leaks in as text.
			name: "a comment containing a bracket",
			in:   "<p>before</p><!-- a > b, and <p>fake</p> --><p>after</p>",
			want: "after",
		},
		{
			name: "an unterminated tag at EOF does not panic",
			in:   "<p>visible</p><div class=\"x",
			want: "visible",
		},
		{
			name: "an unclosed script swallows the rest rather than leaking it",
			in:   "<p>visible</p><script>var x = 1;",
			want: "visible",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, text := extractReadableText(tc.in, "text/html")
			if !strings.Contains(text, tc.want) {
				t.Errorf("extract(%q) = %q, want it to contain %q", tc.in, text, tc.want)
			}
		})
	}

	// The comment case, asserted from the other side.
	_, text := extractReadableText("<p>before</p><!-- a > b, and <p>fake</p> --><p>after</p>", "text/html")
	if strings.Contains(text, "fake") {
		t.Errorf("comment contents leaked into the text: %q", text)
	}
}

func TestExtractHTMLDecodesEntitiesAndCollapsesNonBreakingSpace(t *testing.T) {
	_, text := extractReadableText("<p>Tom &amp; Jerry&nbsp;&nbsp;&mdash;&nbsp;5 &lt; 6</p>", "text/html")
	if !strings.Contains(text, "Tom & Jerry") {
		t.Errorf("entities were not decoded: %q", text)
	}
	if !strings.Contains(text, "5 < 6") {
		t.Errorf("escaped comparison was not decoded: %q", text)
	}
	if strings.Contains(text, " ") {
		t.Errorf("a non-breaking space survived collapsing: %q", text)
	}
	if strings.Contains(text, "  ") {
		t.Errorf("runs of whitespace survived: %q", text)
	}
}

// Non-HTML is passed through. Stripping tags from JSON would eat every value
// that happens to sit between angle brackets.
func TestExtractLeavesNonHTMLContentAlone(t *testing.T) {
	const body = `{"office":"Prime Minister","holder":"<redacted>","since":"2014"}`
	_, text := extractReadableText(body, "application/json")
	if !strings.Contains(text, `"holder":"<redacted>"`) {
		t.Errorf("JSON was mangled by the HTML path: %q", text)
	}
}

// Depth must cost linear time and constant stack: a hostile page is free to be
// a million divs deep.
func TestExtractHTMLHandlesPathologicalNestingWithoutRecursion(t *testing.T) {
	deep := strings.Repeat("<div>", 200000) + "needle" + strings.Repeat("</div>", 200000)
	_, text := extractReadableText(deep, "text/html")
	if !strings.Contains(text, "needle") {
		t.Error("content inside deeply nested markup was lost")
	}
}
