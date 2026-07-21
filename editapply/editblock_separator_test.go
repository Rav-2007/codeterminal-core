package editapply

import (
	"strings"
	"testing"
)

// This file is the before/after evidence for Fix 4: ParseEditBlocks split each
// block at the FIRST "=======" line it saw, without regard to where that
// block's own ">>>>>>> REPLACE" was. A SEARCH section that itself contained a
// separator line was therefore cut at the wrong place, and the parser handed
// back a block that looked entirely well-formed while carrying the wrong SEARCH
// and the wrong REPLACE -- which then applied cleanly and silently corrupted
// the file. That is the worst failure shape this codebase has: not a refusal, a
// wrong answer presented as a right one.
//
// Scope is the parser alone (confirmed by the A8 pass: PrepareEdit handles
// delimiters inside content correctly). Nothing on the apply path is touched.
//
// Run against the pre-fix parser, the "NotSilentlySplit" cases FAIL by
// returning a block instead of an error. Every "StillParses" case passes before
// and after.

// mustParseOne parses a response expected to yield exactly one block.
func mustParseOne(t *testing.T, response string) EditBlock {
	t.Helper()
	blocks, err := ParseEditBlocks(response)
	if err != nil {
		t.Fatalf("ParseEditBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(blocks), blocks)
	}
	return blocks[0]
}

// TestParseEditBlocks_SearchContainingSeparatorIsNotSilentlySplit is the Fix 4
// repro. With two "=======" lines inside one block there is no way for a parser
// to know which is the divider and which is content -- the file it would apply
// to is not available here. The one outcome that must never happen is the old
// one: confidently picking the first and returning a wrong block as if it were
// right.
func TestParseEditBlocks_SearchContainingSeparatorIsNotSilentlySplit(t *testing.T) {
	response := strings.Join([]string{
		"path: notes.md",
		searchMarker,
		"alpha",
		separatorMarker,
		"beta",
		separatorMarker,
		"gamma",
		replaceMarker,
	}, "\n")

	blocks, err := ParseEditBlocks(response)
	if err == nil {
		t.Fatalf("SILENT CORRUPTION: parser returned %+v for an ambiguous block; want a refusal", blocks)
	}
	if len(blocks) != 0 {
		t.Errorf("got %d blocks alongside the error, want none", len(blocks))
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %v, want it to name the ambiguity", err)
	}
	// The message has to be actionable: the model or user needs to know which
	// lines collided so they can re-issue a SEARCH that avoids them.
	for _, want := range []string{"line 2", "4", "6"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %s", err, want)
		}
	}
}

// TestParseEditBlocks_ConflictMarkersInContentNotSilentlySplit is the same
// defect in the shape most likely to hit a real user: editing a file that has
// an unresolved git conflict in it, where "=======" is genuinely part of the
// content being searched for.
func TestParseEditBlocks_ConflictMarkersInContentNotSilentlySplit(t *testing.T) {
	response := strings.Join([]string{
		"path: app.go",
		searchMarker,
		"ours := 1",
		separatorMarker,
		"theirs := 2",
		separatorMarker,
		"resolved := 3",
		replaceMarker,
	}, "\n")

	if blocks, err := ParseEditBlocks(response); err == nil {
		t.Fatalf("SILENT CORRUPTION: got %+v, want a refusal for the ambiguous separator", blocks)
	}
}

// TestParseEditBlocks_SeparatorBoundedByOwnReplaceMarker pins the structural
// half of the fix: the separator search is now bounded by THIS block's
// ">>>>>>> REPLACE", so a separator belonging to a later block can never be
// mistaken for this one's divider.
func TestParseEditBlocks_SeparatorBoundedByOwnReplaceMarker(t *testing.T) {
	response := strings.Join([]string{
		"Here are two edits.",
		"",
		"path: a.go",
		searchMarker,
		"old a",
		separatorMarker,
		"new a",
		replaceMarker,
		"",
		"Some prose between blocks with a stray " + separatorMarker + " divider:",
		separatorMarker,
		"",
		"path: b.go",
		searchMarker,
		"old b",
		separatorMarker,
		"new b",
		replaceMarker,
	}, "\n")

	blocks, err := ParseEditBlocks(response)
	if err != nil {
		t.Fatalf("ParseEditBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	for i, want := range []EditBlock{
		{FilePath: "a.go", Search: "old a", Replace: "new a"},
		{FilePath: "b.go", Search: "old b", Replace: "new b"},
	} {
		if blocks[i] != want {
			t.Errorf("block %d = %+v, want %+v", i, blocks[i], want)
		}
	}
}

// TestParseEditBlocks_MissingSeparatorRefused covers the other end: a block that
// reaches its REPLACE marker having never had a divider is malformed, and must
// say so rather than silently treating the whole body as SEARCH.
func TestParseEditBlocks_MissingSeparatorRefused(t *testing.T) {
	response := strings.Join([]string{
		"path: a.go",
		searchMarker,
		"old a",
		replaceMarker,
	}, "\n")

	if blocks, err := ParseEditBlocks(response); err == nil {
		t.Fatalf("got %+v, want a refusal for a block with no separator", blocks)
	}
}

// TestParseEditBlocks_RealDividerLinesStillParse is the capability control the
// review asked for: the separator is matched EXACTLY (a trimmed line of seven
// "="), so the divider lines that actually occur in documentation -- setext
// underlines, rule lines, changelog rules of any other length -- are ordinary
// content and must keep parsing cleanly.
func TestParseEditBlocks_RealDividerLinesStillParse(t *testing.T) {
	for name, divider := range map[string]string{
		"rst setext underline":  "========",
		"long markdown rule":    "====================",
		"short run":             "======",
		"dashes":                "--------",
		"trailing text after =": "======= end of section",
	} {
		t.Run(name, func(t *testing.T) {
			response := strings.Join([]string{
				"path: CHANGELOG.md",
				searchMarker,
				"Release 1.0",
				divider,
				"- old entry",
				separatorMarker,
				"Release 1.1",
				divider,
				"- new entry",
				replaceMarker,
			}, "\n")

			block := mustParseOne(t, response)
			wantSearch := "Release 1.0\n" + divider + "\n- old entry"
			wantReplace := "Release 1.1\n" + divider + "\n- new entry"
			if block.Search != wantSearch {
				t.Errorf("Search = %q, want %q", block.Search, wantSearch)
			}
			if block.Replace != wantReplace {
				t.Errorf("Replace = %q, want %q", block.Replace, wantReplace)
			}
		})
	}
}

// TestParseEditBlocks_OrdinaryBlockUnchanged is the baseline control: the
// common case must be byte-for-byte what it always was.
func TestParseEditBlocks_OrdinaryBlockUnchanged(t *testing.T) {
	response := strings.Join([]string{
		"Sure, here is the change:",
		"",
		"path: main.go",
		searchMarker,
		"func old() {}",
		separatorMarker,
		"func new_() {}",
		replaceMarker,
		"",
		"That should do it.",
	}, "\n")

	block := mustParseOne(t, response)
	want := EditBlock{FilePath: "main.go", Search: "func old() {}", Replace: "func new_() {}"}
	if block != want {
		t.Errorf("block = %+v, want %+v", block, want)
	}
}
