package editapply

import (
	"fmt"
	"strings"
)

// Stable slugs for what LooksLikeEditPayload recognised. They are part of the
// CLI's and the daemon log's vocabulary, so they do not change once shipped.
const (
	// PayloadUnifiedDiff: a git/unified diff. Produced by `git diff`, `git
	// format-patch`, `diff -u`, every code-review UI, and by models that reach
	// for the format they saw most in training instead of the one the system
	// prompt asked for.
	PayloadUnifiedDiff = "unified_diff"

	// PayloadMalformedMarkers: conflict-style edit markers are present but not
	// in the exact grammar ParseEditBlocks reads -- "<<<<<<<SEARCH" with no
	// space, "<<<<<<< SEARCH:" with a trailing colon, a five-angle-bracket
	// run. ParseEditBlocks anchors on the exact trimmed marker line, so none of
	// these produces a block OR a rejection: they vanish.
	PayloadMalformedMarkers = "malformed_edit_markers"
)

// PayloadHint describes edit-shaped text this engine cannot read. It carries a
// ready-to-show sentence rather than only a slug, matching BlockError's shape
// (see editblock.go) so both kinds of "you asked for this and are not getting
// it" reach the user through the same rendering path.
type PayloadHint struct {
	Kind   string // one of the Payload* constants
	Line   int    // 1-indexed line where the shape was recognised
	Advice string // human-readable, meant to be shown verbatim
}

// LooksLikeEditPayload reports whether response contains something its author
// plainly meant as a file edit, in a shape ParseEditBlocks does not parse.
//
// It exists because of an asymmetry that silently cost users their work.
// ParseEditBlocks returns (nil, nil) for two completely different inputs: a
// plain-text answer that legitimately proposes no edit, and a response full of
// unified-diff hunks that it simply cannot read. Its own contract defines that
// return as "no blocks, and that is fine", so every caller treated the second
// case as the first -- `edits apply` printed "no edit blocks found in input"
// and exited 0, and the TUI returned to an idle prompt with no message at all.
// A user who pasted a real patch was told nothing had gone wrong.
//
// This is deliberately a RECOGNISER, not a parser. It answers "did the author
// mean to edit a file?" and nothing else; it never decides what the edit is.
// Callers use it only when ParseEditBlocks produced no blocks and no
// rejections, which is the one case where silence was previously the whole
// response.
//
// It must not fire on ordinary prose. A false positive turns every chat reply
// into a warning about an edit nobody proposed, which is worse than the silence
// it replaces -- so each recogniser below matches a full structural shape, never
// a suggestive substring. In particular a bare "---" line is NOT a signal: it is
// a markdown rule, a YAML document separator and a setext underline far more
// often than it is half of a diff header, so it counts only when the matching
// "+++" line follows it immediately.
func LooksLikeEditPayload(response string) (PayloadHint, bool) {
	lines := strings.Split(response, "\n")

	// TWO PASSES, and the order is the point. A response carrying a real diff
	// AND a stray angle-bracket run is overwhelmingly a diff, and naming it as
	// one is the advice that helps. A single pass in line order would report
	// whichever shape happened to appear first, so the same response would be
	// diagnosed differently depending on where the model put its prose.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// `diff --git a/x b/x` -- unambiguous, and the first line of anything
		// git produced.
		if strings.HasPrefix(trimmed, "diff --git ") {
			return PayloadHint{Kind: PayloadUnifiedDiff, Line: i + 1, Advice: unifiedDiffAdvice}, true
		}

		// A hunk header. Specific enough to stand alone: the full
		// "@@ -l,c +l,c @@" shape does not occur in prose.
		if isHunkHeader(trimmed) {
			return PayloadHint{Kind: PayloadUnifiedDiff, Line: i + 1, Advice: unifiedDiffAdvice}, true
		}

		// The classic file-header pair. Both halves are required, adjacent,
		// for the reason in the doc comment above.
		if strings.HasPrefix(trimmed, "--- ") && i+1 < len(lines) &&
			strings.HasPrefix(strings.TrimSpace(lines[i+1]), "+++ ") {
			return PayloadHint{Kind: PayloadUnifiedDiff, Line: i + 1, Advice: unifiedDiffAdvice}, true
		}
	}

	for i, line := range lines {
		if isNearMarker(strings.TrimSpace(line)) {
			return PayloadHint{
				Kind:   PayloadMalformedMarkers,
				Line:   i + 1,
				Advice: fmt.Sprintf(malformedMarkerAdvice, searchMarker, separatorMarker, replaceMarker),
			}, true
		}
	}

	return PayloadHint{}, false
}

const unifiedDiffAdvice = "this looks like a unified diff (git/patch format), which is not the edit format this engine reads. " +
	"Re-send the change as SEARCH/REPLACE edit blocks, or apply the patch with `git apply`."

const malformedMarkerAdvice = "this carries conflict-style edit markers that do not match the expected grammar, so no block could be read from it. " +
	"Each block must use exactly %q, %q and %q, each alone on its own line."

// isHunkHeader reports whether line is a unified-diff hunk header:
// "@@ -12 +12 @@", "@@ -12,4 +18,6 @@", optionally followed by a section
// heading. Hand-rolled rather than a regexp so the accepted shape is readable
// as a sequence of requirements and each one is independently testable.
func isHunkHeader(line string) bool {
	rest, ok := strings.CutPrefix(line, "@@ ")
	if !ok {
		return false
	}
	if rest, ok = cutLineSpan(rest, '-'); !ok {
		return false
	}
	if rest, ok = strings.CutPrefix(rest, " "); !ok {
		return false
	}
	if rest, ok = cutLineSpan(rest, '+'); !ok {
		return false
	}
	// " @@" must follow; anything after it is a section heading git adds for
	// context and is not part of the shape.
	return strings.HasPrefix(rest, " @@")
}

// cutLineSpan consumes one "<sign><digits>[,<digits>]" span from the front of
// s, returning what is left. Both counts must be non-empty digit runs -- "@@ -,
// +, @@" is not a hunk header.
func cutLineSpan(s string, sign byte) (string, bool) {
	if len(s) == 0 || s[0] != sign {
		return s, false
	}
	s = s[1:]

	s, ok := cutDigits(s)
	if !ok {
		return s, false
	}
	if rest, found := strings.CutPrefix(s, ","); found {
		return cutDigits(rest)
	}
	return s, true
}

// cutDigits consumes one or more ASCII digits from the front of s.
func cutDigits(s string) (string, bool) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 {
		return s, false
	}
	return s[n:], true
}

// isNearMarker reports whether line is a run of '<' or '>' long enough to be a
// deliberate conflict marker rather than a comparison operator or a quoted
// block, but not one ParseEditBlocks accepted.
//
// The threshold is four. Three or fewer occur naturally -- ">>> " is a Python
// REPL prompt, "<<" is a shift, ">>>" appears in doctests -- while four in a row
// is something a model typed on purpose. A bare "=======" is deliberately NOT
// a signal here: it is a setext underline and a changelog divider in real
// documentation, and on its own it says nothing about an edit.
func isNearMarker(line string) bool {
	for _, run := range []byte{'<', '>'} {
		n := 0
		for n < len(line) && line[n] == run {
			n++
		}
		if n >= 4 {
			// The exact markers are read by ParseEditBlocks, so reaching
			// here with one of them means the caller found no blocks and no
			// rejections -- which the parser cannot do with a real marker
			// present. Excluding them keeps this a strictly "malformed" test.
			return line != searchMarker && line != replaceMarker
		}
	}
	return false
}
