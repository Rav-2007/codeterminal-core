package editapply

import (
	"strings"
	"testing"
)

// blockWith wraps a path line and whatever separates it from the SEARCH marker
// around one ordinary edit, so a test only has to state the variation.
func blockWith(lead ...string) string {
	return strings.Join(append(lead,
		searchMarker,
		"foo",
		separatorMarker,
		"bar",
		replaceMarker,
	), "\n")
}

// TestParseEditBlocks_TolerantPathLine is the Fix 14 regression: the path-line
// variants models actually produce all parse to the same edit. Each of these
// previously lost the edit outright — the parser refused the block and the
// user was shown nothing, for a difference in decoration that changes nothing
// about what the model asked to do.
func TestParseEditBlocks_TolerantPathLine(t *testing.T) {
	want := EditBlock{FilePath: "a.go", Search: "foo", Replace: "bar"}

	tests := []struct {
		name     string
		response string
	}{
		{"canonical", blockWith("path: a.go")},
		{"blank line before SEARCH", blockWith("path: a.go", "")},
		{"several blank lines before SEARCH", blockWith("path: a.go", "", "", "")},
		{"markdown fence before SEARCH", blockWith("path: a.go", "```go")},
		{"blank line and fence before SEARCH", blockWith("path: a.go", "", "```")},
		{"tilde fence before SEARCH", blockWith("path: a.go", "~~~")},
		{"File: label", blockWith("File: a.go")},
		{"file: lowercase label", blockWith("file: a.go")},
		{"Path: capitalized", blockWith("Path: a.go")},
		{"backticked path", blockWith("path: `a.go`")},
		{"double-quoted path", blockWith(`path: "a.go"`)},
		{"single-quoted path", blockWith("path: 'a.go'")},
		{"markdown-bold label", blockWith("**path:** a.go")},
		{"bold File label with backticks", blockWith("**File:** `a.go`")},
		{"indented path line", blockWith("   path: a.go")},
		{"everything at once", blockWith("  **File:**  `a.go` ", "", "```go")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, rejected := ParseEditBlocks(tt.response)
			if len(rejected) != 0 {
				t.Fatalf("rejected = %+v, want none", rejected)
			}
			if len(blocks) != 1 || blocks[0] != want {
				t.Fatalf("blocks = %+v, want [%+v]", blocks, want)
			}
		})
	}
}

// TestParseEditBlocks_PathLineToleranceStopsAtMeaningfulLines is the other
// half: tolerance must not become "find a path line anywhere". The backward
// scan stops at the first line that carries meaning of its own, so a block
// genuinely missing its path line is still refused rather than silently
// adopting some unrelated file's path.
func TestParseEditBlocks_PathLineToleranceStopsAtMeaningfulLines(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "prose between path and SEARCH",
			response: blockWith("path: a.go", "Here is the change I suggest:"),
		},
		{
			name: "a previous block's terminator is not crossed",
			response: strings.Join([]string{
				"path: a.go",
				searchMarker, "foo", separatorMarker, "bar", replaceMarker,
				"",
				searchMarker, "baz", separatorMarker, "qux", replaceMarker,
			}, "\n"),
		},
		{
			name:     "no path line at all",
			response: blockWith(),
		},
		{
			name:     "empty path",
			response: blockWith("path:"),
		},
		{
			name:     "empty backticked path",
			response: blockWith("path: ``"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, rejected := ParseEditBlocks(tt.response)
			// The "previous terminator" case has one good block plus one refusal.
			if len(rejected) == 0 {
				t.Fatalf("expected a refusal, got blocks=%+v", blocks)
			}
			for _, b := range blocks {
				if b.Search == "baz" || (b.FilePath == "" && b.Search == "foo") {
					t.Errorf("a block without its own path line was accepted: %+v", b)
				}
			}
		})
	}
}

// TestParsePathLine_ReturnsBarePath pins that tolerance is stripped at the
// parse boundary: whatever decoration the line carried, downstream gates
// (PrepareEdit's confinement, protected-target and secret-name checks) see the
// same bare path they always did.
func TestParsePathLine_ReturnsBarePath(t *testing.T) {
	for _, line := range []string{
		"path: daemon/server.go",
		"File: daemon/server.go",
		"**path:** `daemon/server.go`",
		`  PATH:  "daemon/server.go"  `,
	} {
		got, ok := parsePathLine(line)
		if !ok || got != "daemon/server.go" {
			t.Errorf("parsePathLine(%q) = (%q, %t), want (\"daemon/server.go\", true)", line, got, ok)
		}
	}

	for _, line := range []string{
		"just some prose",
		"// path is a variable name here",
		"pathological: a.go",
	} {
		if got, ok := parsePathLine(line); ok {
			t.Errorf("parsePathLine(%q) = (%q, true), want no match", line, got)
		}
	}
}
