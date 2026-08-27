package main

import (
	"html"
	"strings"
	"unicode"
)

// HTML to readable text, with no dependency.
//
// WHY NOT golang.org/x/net/html. This module's go.mod has fifteen direct
// requirements and every one of them earns its place; x/net is a large surface
// pulled in to do a job whose hard part is not parsing. A real parser builds a
// correct tree of a document nobody is going to walk -- what a language model
// needs from a web page is its sentences, in order, with the navigation
// removed. That is a scan, and a scan is auditable in one screen.
//
// IT IS DELIBERATELY LOSSY AND DELIBERATELY BORING. There is no attempt at
// readability-style main-content detection, because guessing wrong there
// silently deletes the answer the user asked for. Everything outside the
// non-content elements survives; whitespace is collapsed; block boundaries
// become newlines. The model can be trusted to skip a nav bar. It cannot
// recover a paragraph this function decided was chrome.

// dropWholeElements are elements whose CONTENT is discarded along with their
// tags. Script and style are the obvious two; the rest are pure chrome that
// otherwise arrives as a wall of single words and pushes the actual page out
// of the byte cap.
var dropWholeElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "canvas": true, "iframe": true, "object": true,
	"embed": true, "form": true, "select": true, "nav": true, "footer": true,
}

// "head" IS DELIBERATELY ABSENT from that list, and it was there until a test
// caught what it cost: <title> lives inside <head>, so dropping the head whole
// discarded every page title before the title extractor could see one. The head
// needed no entry anyway -- its only text-bearing children are <title>, which
// we want, and <script>/<style>, which are dropped on their own account. The
// rest (<meta>, <link>) carry no text between their tags, so they contribute
// nothing to strip.

// blockElements end a line when they open or close, so sentences from
// different blocks do not run together into one that says something neither of
// them said.
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "td": true, "th": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"section": true, "article": true, "header": true, "blockquote": true,
	"pre": true, "table": true, "ul": true, "ol": true, "dl": true, "dt": true, "dd": true,
	"hr": true, "figcaption": true, "main": true, "aside": true,
}

// extractReadableText reduces a fetched document to a title and body text.
//
// Non-HTML content types are returned with whitespace normalised and nothing
// else done to them: JSON and plain text are already what the model wants, and
// running a tag stripper over JSON would eat every value between angle
// brackets.
func extractReadableText(body, ctype string) (title, text string) {
	switch ctype {
	case "text/html", "application/xhtml+xml", "":
		return extractHTML(body)
	default:
		return "", collapseBlankLines(strings.TrimSpace(body))
	}
}

// extractHTML is the scanner. One pass, no recursion -- a deeply nested
// hostile document costs linear time and constant stack.
func extractHTML(body string) (title, text string) {
	var out strings.Builder
	out.Grow(len(body) / 2)

	// skipUntil is the closing tag being hunted for while inside a dropped
	// element. Tracked as a single name rather than a stack on purpose: nested
	// <script> is not legal, and a stack here would let malformed markup drive
	// unbounded state.
	skipUntil := ""
	inTitle := false
	var titleBuf strings.Builder

	i := 0
	for i < len(body) {
		if body[i] != '<' {
			if skipUntil != "" {
				i++
				continue
			}
			j := strings.IndexByte(body[i:], '<')
			if j < 0 {
				j = len(body) - i
			}
			chunk := body[i : i+j]
			if inTitle {
				titleBuf.WriteString(chunk)
			} else {
				out.WriteString(chunk)
			}
			i += j
			continue
		}

		// Comments carry nothing and may contain a '>' of their own, so a
		// comment is scanned to its real terminator rather than to the first
		// '>'. Checked before the generic tag scan for exactly that reason.
		if strings.HasPrefix(body[i:], "<!--") {
			if k := strings.Index(body[i+4:], "-->"); k >= 0 {
				i = i + 4 + k + 3
			} else {
				i = len(body)
			}
			continue
		}

		// A '<' that starts no tag is literal text, not markup. Without this a
		// page containing "a < b" swallows the rest of itself.
		end := strings.IndexByte(body[i:], '>')
		if end < 0 {
			if skipUntil == "" && !inTitle {
				out.WriteString(body[i:])
			}
			break
		}
		raw := body[i : i+end+1]
		i += end + 1

		if strings.HasPrefix(raw, "<!") || strings.HasPrefix(raw, "<?") {
			continue
		}

		name, closing := tagName(raw)
		if name == "" {
			continue
		}

		if skipUntil != "" {
			if closing && name == skipUntil {
				skipUntil = ""
			}
			continue
		}

		switch {
		case name == "title" && !closing:
			inTitle = true
		case name == "title" && closing:
			inTitle = false
		case dropWholeElements[name] && !closing && !isSelfClosing(raw):
			skipUntil = name
		case blockElements[name]:
			out.WriteByte('\n')
		}
	}

	title = normaliseSpace(html.UnescapeString(titleBuf.String()))
	text = collapseBlankLines(html.UnescapeString(out.String()))
	return title, text
}

// tagName extracts a tag's lowercased name and whether it closes.
func tagName(raw string) (name string, closing bool) {
	s := strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if s[0] == '/' {
		closing = true
		s = strings.TrimSpace(s[1:])
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '/' {
			s = s[:i]
			break
		}
	}
	return strings.ToLower(s), closing
}

func isSelfClosing(raw string) bool {
	return strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(raw, ">")), "/")
}

// normaliseSpace collapses every run of whitespace to one space.
func normaliseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// collapseBlankLines trims each line, drops empty ones, and collapses internal
// whitespace.
//
// THE POINT IS TOKENS, NOT TIDINESS. A typical HTML page reduced by the scanner
// above is roughly half indentation; that indentation is content the user pays
// the provider to read and it says nothing. Collapsing it here, before the
// result reaches the turn's byte cap, means the cap spends its budget on
// sentences.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// Non-breaking space and friends survive html.UnescapeString as real
		// runes and are not caught by strings.Fields, so a page full of &nbsp;
		// collapses to nothing without this mapping.
		line = normaliseSpace(strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return ' '
			}
			return r
		}, line))
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
