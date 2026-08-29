package editapply

import (
	"strings"
	"testing"
)

// realGitDiff is `git diff` output in the shape git actually emits: a
// `diff --git` line, the index line, the ---/+++ pair, and a hunk header with
// a section heading. Embedded verbatim rather than shelled out for, so the test
// does not depend on a git binary or on this checkout having history.
const realGitDiff = `diff --git a/editapply/apply.go b/editapply/apply.go
index 3f9352f..07e97d1 100644
--- a/editapply/apply.go
+++ b/editapply/apply.go
@@ -44,7 +44,7 @@ func refuseIfUnparseable(relPath, content string) error {
 	if !strings.EqualFold(filepath.Ext(relPath), ".go") {
 		return nil
 	}
-	if _, err := parser.ParseFile(token.NewFileSet(), relPath, content, parser.AllErrors); err != nil {
+	if _, err := parseFor(relPath, content); err != nil {
 		return fmt.Errorf("edit would make %s unparseable as Go: %w", relPath, err)
 	}
 	return nil
`

// TestLooksLikeEditPayload_RecognisesEditShapedInput is the headline
// regression. Every input here previously produced (nil, nil) from
// ParseEditBlocks, which callers read as "a plain-text answer with no edit in
// it" -- so `edits apply` printed "no edit blocks found in input" and exited 0,
// and the TUI said nothing at all. The user lost the edit and was told
// everything was fine.
func TestLooksLikeEditPayload_RecognisesEditShapedInput(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantKind string
	}{
		{"real git diff", realGitDiff, PayloadUnifiedDiff},
		{"diff --git line alone", "diff --git a/x.go b/x.go", PayloadUnifiedDiff},
		{"bare hunk header", "@@ -1,4 +1,6 @@", PayloadUnifiedDiff},
		{"hunk header without counts", "@@ -12 +12 @@", PayloadUnifiedDiff},
		{"hunk header with section heading", "@@ -3,2 +3,4 @@ func run() {", PayloadUnifiedDiff},
		{"file header pair", "--- a/x.go\n+++ b/x.go", PayloadUnifiedDiff},
		{"file header pair for a new file", "--- /dev/null\n+++ b/new.go", PayloadUnifiedDiff},
		{"plain diff -u output", "--- old.txt\t2026-01-01\n+++ new.txt\t2026-01-02\n@@ -1 +1 @@", PayloadUnifiedDiff},
		{"prose then a diff", "Here is the change you asked for:\n\ndiff --git a/x.go b/x.go", PayloadUnifiedDiff},
		{"diff inside a fenced block", "```diff\n@@ -1,2 +1,3 @@\n-a\n+b\n```", PayloadUnifiedDiff},

		{"SEARCH marker with no space", "path: a.go\n<<<<<<<SEARCH\nfoo\n=======\nbar\n>>>>>>>REPLACE", PayloadMalformedMarkers},
		{"SEARCH marker with trailing colon", "path: a.go\n<<<<<<< SEARCH:\nfoo", PayloadMalformedMarkers},
		{"short angle-bracket run", "path: a.go\n<<<< SEARCH\nfoo", PayloadMalformedMarkers},
		{"lowercase marker", "path: a.go\n<<<<<<< search\nfoo", PayloadMalformedMarkers},
	}

	if len(tests) < 10 {
		t.Fatalf("positive corpus has only %d cases; it must cover the real shapes or it proves nothing", len(tests))
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Precondition: the batch parser must genuinely be silent on this
			// input. If it ever learns to read one of these, the case is no
			// longer testing what it claims to.
			blocks, rejected := ParseEditBlocks(tt.response)
			if len(blocks) != 0 || len(rejected) != 0 {
				t.Fatalf("precondition failed: ParseEditBlocks read %d block(s) and %d rejection(s) from this input; the sniffer is only consulted when it reads neither",
					len(blocks), len(rejected))
			}

			hint, ok := LooksLikeEditPayload(tt.response)
			if !ok {
				t.Fatalf("LooksLikeEditPayload did not recognise this as an edit payload:\n%s", tt.response)
			}
			if hint.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", hint.Kind, tt.wantKind)
			}
			if hint.Line < 1 {
				t.Errorf("Line = %d, want a 1-indexed line number", hint.Line)
			}
			if strings.TrimSpace(hint.Advice) == "" {
				t.Error("Advice is empty; the hint is shown to a user verbatim and must say what to do")
			}
		})
	}
}

// ordinaryProse is the false-positive corpus. A sniffer that fires on any of
// these would attach a warning about an edit nobody proposed to ordinary chat
// answers -- which is worse than the silence it replaces, because it would
// train users to ignore the message that matters.
//
// Every entry is a shape that genuinely occurs in model answers and that
// contains at least one character sequence a lazier recogniser would trip on.
var ordinaryProse = []struct {
	name string
	text string
}{
	{"plain answer", "The retrieval budget is 24000 characters, set in RETRIEVAL_BUDGET_DESIGN.md."},
	{"markdown horizontal rule", "First point.\n\n---\n\nSecond point."},
	{"setext heading underline", "Overview\n--------\n\nThe daemon streams tokens."},
	{"yaml front matter", "---\ntitle: Notes\n---\n\nBody text here."},
	{"yaml document separator", "a: 1\n---\nb: 2\n---\nc: 3"},
	{"markdown table", "| col | col |\n|-----|-----|\n| a   | b   |"},
	{"changelog divider", "## 1.2.0\n\n=======\n\nFixed a bug."},
	{"equals underline", "Title\n=======\n\nText."},
	{"python repl transcript", ">>> import os\n>>> os.getcwd()\n'/tmp'"},
	{"doctest block", "Example:\n\n    >>> add(1, 2)\n    3"},
	{"shift operators in code", "x := 1 << 3\ny := x >> 2"},
	{"generic type nesting", "var m map[string]List<List<int>>"},
	{"email quoting", ">> original message\n> reply\nmy answer"},
	{"go doc comment", "// Package editapply applies SEARCH/REPLACE edits.\n// It performs no I/O beyond reading the target."},
	{"prose mentioning diffs", "You could compute a unified diff here, but the engine reads edit blocks instead."},
	{"prose mentioning git", "Run git diff to see what changed, then re-read the file."},
	{"arrow in prose", "The flow is: parse -> confine -> verify -> write."},
	{"comparison in prose", "If len(a) < len(b) then a is shorter."},
	{"ascii art box", "+-----+\n| box |\n+-----+"},
	{"decorative plus rule", "+++\nsection\n+++"},
	{"minus bullet list", "- first\n- second\n- third"},
	{"plus bullet list", "+ first\n+ second"},
	{"at signs in prose", "Reach me @here or @there, and see @@ for the annotation syntax."},
	{"incomplete hunk shape", "@@ -a,b +c,d @@"},
	{"empty response", ""},
	{"whitespace only", "   \n\t\n  "},
}

// TestLooksLikeEditPayload_DoesNotFireOnProse is the anti-vacuity half. The
// count assertion is not decoration: a corpus that silently shrank to nothing
// would pass this test by comparing two empty sets, which is exactly how a
// scanner in this repo has been wrong before.
func TestLooksLikeEditPayload_DoesNotFireOnProse(t *testing.T) {
	if len(ordinaryProse) < 20 {
		t.Fatalf("false-positive corpus has only %d entries; it must be broad enough to be evidence (want >= 20)", len(ordinaryProse))
	}

	for _, tt := range ordinaryProse {
		t.Run(tt.name, func(t *testing.T) {
			if hint, ok := LooksLikeEditPayload(tt.text); ok {
				t.Errorf("fired %q on line %d of ordinary prose:\n%s", hint.Kind, hint.Line, tt.text)
			}
		})
	}
}

// TestIsHunkHeader_MatchesTheShapeNotTheSubstring pins the recogniser that does
// the most work, including the near-misses. "@@ " appearing in text is not a
// hunk; the full span-pair-span shape is.
func TestIsHunkHeader_MatchesTheShapeNotTheSubstring(t *testing.T) {
	yes := []string{
		"@@ -1 +1 @@",
		"@@ -1,2 +3,4 @@",
		"@@ -0,0 +1,120 @@",
		"@@ -12,7 +12,7 @@ func refuseIfUnparseable(relPath, content string) error {",
	}
	no := []string{
		"@@",
		"@@ ",
		"@@ -1 +1",           // no closing @@
		"@@ -1,2 +3,4",       // no closing @@
		"@@ 1 +1 @@",         // missing the minus sign
		"@@ -1 1 @@",         // missing the plus sign
		"@@ -, +, @@",        // no digits
		"@@ -1, +2, @@",      // empty count after the comma
		"@@ -a +b @@",        // not digits
		"@@-1 +1@@",          // no separating spaces
		"see @@ in the docs", // prose
		"",
	}

	for _, s := range yes {
		if !isHunkHeader(s) {
			t.Errorf("isHunkHeader(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isHunkHeader(s) {
			t.Errorf("isHunkHeader(%q) = true, want false", s)
		}
	}
}

// TestLooksLikeEditPayload_ExactMarkersAreNotMalformed guards the one overlap
// between the sniffer and the parser. A response carrying the real markers is
// ParseEditBlocks' business -- it produces a block or a line-numbered
// rejection, both of which the user already sees. The sniffer must not also
// claim it, or a well-formed edit would arrive with a "malformed" warning
// stapled to it.
func TestLooksLikeEditPayload_ExactMarkersAreNotMalformed(t *testing.T) {
	for _, marker := range []string{searchMarker, replaceMarker} {
		if isNearMarker(marker) {
			t.Errorf("isNearMarker(%q) = true; the exact markers belong to ParseEditBlocks", marker)
		}
	}

	wellFormed := strings.Join([]string{
		"path: a.go", searchMarker, "foo", separatorMarker, "bar", replaceMarker,
	}, "\n")
	blocks, rejected := ParseEditBlocks(wellFormed)
	if len(blocks) != 1 || len(rejected) != 0 {
		t.Fatalf("precondition: want 1 block and 0 rejections, got %d and %d", len(blocks), len(rejected))
	}
	if hint, ok := LooksLikeEditPayload(wellFormed); ok {
		t.Errorf("sniffer claimed %q on a well-formed edit block", hint.Kind)
	}
}

// TestLooksLikeEditPayload_PrefersDiffOverStrayMarker records the precedence
// decision. A response carrying both a diff and a stray angle-bracket run is
// overwhelmingly a diff, and naming it as one is the advice that helps.
func TestLooksLikeEditPayload_PrefersDiffOverStrayMarker(t *testing.T) {
	// Each case puts the stray marker FIRST, so a single line-ordered pass
	// would report it and stop. Each also carries exactly ONE diff signal, so
	// no case can be satisfied by a different recogniser standing in for the
	// one it is meant to exercise -- which is what made an earlier version of
	// this test survive having `diff --git` detection removed.
	tests := []struct {
		name  string
		mixed string
	}{
		{"marker then diff --git", "<<<< not a marker\ndiff --git a/x.go b/x.go"},
		{"marker then hunk header", "<<<< not a marker\n@@ -1 +1 @@"},
		{"marker then file-header pair", "<<<< not a marker\n--- a/x.go\n+++ b/x.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint, ok := LooksLikeEditPayload(tt.mixed)
			if !ok {
				t.Fatal("did not recognise a response containing a real diff")
			}
			if hint.Kind != PayloadUnifiedDiff {
				t.Errorf("Kind = %q, want %q: a diff anywhere in the response outranks a stray marker line, whatever order they appear in", hint.Kind, PayloadUnifiedDiff)
			}
		})
	}
}
