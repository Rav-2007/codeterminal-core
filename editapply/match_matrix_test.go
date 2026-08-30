package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the before/after evidence for Fix 6: SEARCH matching was
// byte-exact, so an edit was hard-refused whenever the model's SEARCH text
// differed from the file in a way that is invisible or semantically irrelevant
// -- CRLF line endings, trailing whitespace, tabs vs spaces, smart quotes, and
// (from the A8 pass) unicode differences where the SEARCH text is VISUALLY
// IDENTICAL to what is in the file. "search text not found" for text the user
// can see is right there is the shape that makes a tool feel broken.
//
// The matrix below is the review's 20 cases. Run against the pre-fix matcher,
// only the exact case and the two must-refuse cases behave correctly; run
// after, every case marked wantMatch matches, at the tier named in wantTier.
//
// The two must-refuse cases are the load-bearing negative controls: tolerance
// is only allowed to help LOCATE text, never to make a genuinely-absent or
// genuinely-ambiguous edit apply.

type matchCase struct {
	name      string
	file      string // file content on disk
	search    string // SEARCH text as the model wrote it
	replace   string
	wantMatch bool
	wantTier  MatchTier
	// wantFile, when set, is the exact expected file content after the edit --
	// used to pin that a non-exact match still writes back byte-correct
	// surroundings (line endings above all), AND that the replacement itself is
	// written in the file's own convention rather than the one it arrived in.
	// The second half is newer than the first: the splice used to insert the
	// replacement's bytes verbatim, which left a CRLF file mixed.
	wantFile string
}

// Written as escapes on purpose: these characters are invisible in an editor
// (and a literal BOM is illegal in Go source). That invisibility is the whole
// point of the A8 cases -- the SEARCH text LOOKS identical to the file.
const (
	zeroWidthSpace = "\u200b"
	byteOrderMark  = "\ufeff"
	nonBreakingSp  = "\u00a0"
	nfcCafe        = "caf\u00e9"  // e-acute as one code point
	nfdCafe        = "cafe\u0301" // e + combining acute
	smartQuoted    = "\u201chi\u201d"
	straightQuoted = `"hi"`
)

func matchMatrix() []matchCase {
	return []matchCase{
		// --- tier 1: exact (unchanged behaviour) ---
		{
			name: "01 exact match", file: "alpha\nbeta\ngamma\n",
			search: "beta", replace: "BETA",
			wantMatch: true, wantTier: MatchExact,
			wantFile: "alpha\nBETA\ngamma\n",
		},
		{
			name: "02 exact multi-line", file: "a\nb\nc\n",
			search: "a\nb", replace: "x\ny",
			wantMatch: true, wantTier: MatchExact,
			wantFile: "x\ny\nc\n",
		},

		// --- tier 2: line endings ---
		{
			name: "03 CRLF file, LF search", file: "alpha\r\nbeta\r\ngamma\r\n",
			search: "alpha\nbeta", replace: "ALPHA\nBETA",
			wantMatch: true, wantTier: MatchLineEndings,
			// The file is CRLF and stays CRLF. Outside the replacement, which
			// was always true; and inside it, which is conformReplacementEOL --
			// the LF replacement is re-encoded rather than spliced in as-is.
			wantFile: "ALPHA\r\nBETA\r\ngamma\r\n",
		},
		{
			name: "04 LF file, CRLF search", file: "alpha\nbeta\ngamma\n",
			search: "alpha\r\nbeta", replace: "ALPHA\nBETA",
			wantMatch: true, wantTier: MatchLineEndings,
			wantFile: "ALPHA\nBETA\ngamma\n",
		},
		{
			name: "05 CR-only file", file: "alpha\rbeta\rgamma\r",
			search: "alpha\nbeta", replace: "X",
			wantMatch: true, wantTier: MatchLineEndings,
			wantFile: "X\rgamma\r",
		},

		// --- tier 3: trailing whitespace ---
		{
			name: "06 trailing WS in file", file: "alpha   \nbeta\t\ngamma\n",
			search: "alpha\nbeta", replace: "X\nY",
			wantMatch: true, wantTier: MatchTrailingSpace,
			wantFile: "X\nY\ngamma\n",
		},
		{
			name: "07 trailing WS in search", file: "alpha\nbeta\ngamma\n",
			search: "alpha  \nbeta\t", replace: "X\nY",
			wantMatch: true, wantTier: MatchTrailingSpace,
			wantFile: "X\nY\ngamma\n",
		},
		{
			name: "08 trailing WS both sides, different", file: "alpha \nbeta\n",
			search: "alpha\t\t\nbeta", replace: "X\nY",
			wantMatch: true, wantTier: MatchTrailingSpace,
			wantFile: "X\nY\n",
		},

		// --- tier 4: leading indentation ---
		{
			name: "09 tabs in file, spaces in search", file: "func f() {\n\treturn 1\n}\n",
			search: "func f() {\n    return 1\n}", replace: "func f() {\n\treturn 2\n}",
			wantMatch: true, wantTier: MatchIndentation,
			wantFile: "func f() {\n\treturn 2\n}\n",
		},
		{
			name: "10 spaces in file, tabs in search", file: "func f() {\n    return 1\n}\n",
			search: "func f() {\n\treturn 1\n}", replace: "func f() {\n    return 2\n}",
			wantMatch: true, wantTier: MatchIndentation,
			wantFile: "func f() {\n    return 2\n}\n",
		},
		{
			name: "11 indent width 2 vs 4", file: "if x {\n  do()\n}\n",
			search: "if x {\n    do()\n}", replace: "if x {\n  redo()\n}",
			wantMatch: true, wantTier: MatchIndentation,
			wantFile: "if x {\n  redo()\n}\n",
		},

		// --- tier 5: unicode / invisible differences ---
		{
			name: "12 smart quotes in file", file: "msg := " + smartQuoted + "\nnext\n",
			search: "msg := " + straightQuoted, replace: "msg := ok",
			wantMatch: true, wantTier: MatchUnicode,
			wantFile: "msg := ok\nnext\n",
		},
		{
			name: "13 straight quotes in file", file: "msg := " + straightQuoted + "\nnext\n",
			search: "msg := " + smartQuoted, replace: "msg := ok",
			wantMatch: true, wantTier: MatchUnicode,
			wantFile: "msg := ok\nnext\n",
		},
		{
			name: "14 NFC file, NFD search", file: "x := \"" + nfcCafe + "\"\nnext\n",
			search: "x := \"" + nfdCafe + "\"", replace: "x := \"tea\"",
			wantMatch: true, wantTier: MatchUnicode,
			wantFile: "x := \"tea\"\nnext\n",
		},
		{
			name: "15 NFD file, NFC search", file: "x := \"" + nfdCafe + "\"\nnext\n",
			search: "x := \"" + nfcCafe + "\"", replace: "x := \"tea\"",
			wantMatch: true, wantTier: MatchUnicode,
			wantFile: "x := \"tea\"\nnext\n",
		},
		{
			name: "16 zero-width space in search", file: "alpha\nbeta\n",
			search: "al" + zeroWidthSpace + "pha", replace: "ALPHA",
			wantMatch: true, wantTier: MatchUnicode,
			wantFile: "ALPHA\nbeta\n",
		},
		{
			name: "17 zero-width space in file", file: "al" + zeroWidthSpace + "pha\nbeta\n",
			search: "alpha", replace: "ALPHA",
			wantMatch: true, wantTier: MatchUnicode,
			wantFile: "ALPHA\nbeta\n",
		},
		{
			// Recorded as EXACT on purpose. A leading BOM was on the suspect list
			// but turns out never to have been a matching problem at all: the BOM
			// sits before the searched text, so a plain substring search already
			// finds it. Kept in the matrix as the standing proof of that, and so
			// a future normalization change that starts consuming the BOM (and
			// would therefore delete it from the file on write) shows up here.
			name: "18 BOM at start of file (was never broken)", file: byteOrderMark + "alpha\nbeta\n",
			search: "alpha", replace: "ALPHA",
			wantMatch: true, wantTier: MatchExact,
			wantFile: byteOrderMark + "ALPHA\nbeta\n",
		},
		{
			name: "19 non-breaking space in file", file: "a" + nonBreakingSp + "b\nnext\n",
			search: "a b", replace: "ab",
			wantMatch: true, wantTier: MatchUnicode,
			wantFile: "ab\nnext\n",
		},

		// --- mixed ---
		{
			name: "20 CRLF + trailing WS + tabs together", file: "func f() {\r\n\treturn 1  \r\n}\r\n",
			search: "func f() {\n    return 1\n}", replace: "func f() {\n\treturn 2\n}",
			wantMatch: true, wantTier: MatchIndentation,
			// Three tolerances at once, and the write still lands entirely in
			// the file's CRLF.
			wantFile: "func f() {\r\n\treturn 2\r\n}\r\n",
		},

		// --- negative controls: tolerance must NOT rescue these ---
		{
			name: "N1 genuinely absent text", file: "alpha\nbeta\n",
			search: "delta", replace: "x",
			wantMatch: false,
		},
		{
			name: "N2 genuinely ambiguous text", file: "dup\nother\ndup\n",
			search: "dup", replace: "x",
			wantMatch: false,
		},
	}
}

// TestMatchMatrix runs the review's matrix through the real PrepareEdit path
// and prints a per-case table, so "which cases pass now" is answerable at a
// glance before and after the fix.
func TestMatchMatrix(t *testing.T) {
	var matched, refused int
	for _, tc := range matchMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
			writeTempFile(t, root, "f.txt", tc.file)

			prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: tc.search, Replace: tc.replace})
			if !tc.wantMatch {
				if err == nil {
					t.Errorf("REGRESSION: matched %q, want a refusal", tc.name)
				} else {
					refused++
					t.Logf("REFUSED (correctly): %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("NO MATCH: %v", err)
				return
			}
			matched++
			if prepared.Tier != tc.wantTier {
				t.Errorf("matched at tier %v, want %v", prepared.Tier, tc.wantTier)
			}
			if tc.wantFile != "" && prepared.NewContent != tc.wantFile {
				t.Errorf("NewContent = %q, want %q", prepared.NewContent, tc.wantFile)
			}
			t.Logf("MATCHED at tier %v", prepared.Tier)
		})
	}
	t.Logf("matrix summary: %d matched, %d correctly refused", matched, refused)
}

// TestMatchTier_LineEndingsPreservedOnWrite is the specific trap the review
// called out: a matcher that normalizes CRLF in order to FIND the text, and
// then writes the normalized form back, silently rewrites every line ending in
// the file. This drives the real write path end to end and compares bytes.
func TestMatchTier_LineEndingsPreservedOnWrite(t *testing.T) {
	root := realTempDir(t)
	original := "alpha\r\nbeta\r\ngamma\r\ndelta\r\n"
	writeTempFile(t, root, "crlf.txt", original)

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "crlf.txt", Search: "beta", Replace: "BETA"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.Tier != MatchExact {
		t.Logf("matched at tier %v", prepared.Tier)
	}

	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filepath.Join(root, "crlf.txt"))
	want := "alpha\r\nBETA\r\ngamma\r\ndelta\r\n"
	if got != want {
		t.Errorf("LINE ENDINGS CORRUPTED: %q, want %q", got, want)
	}
	if strings.Count(got, "\r\n") != 4 {
		t.Errorf("got %d CRLF line endings, want 4", strings.Count(got, "\r\n"))
	}
}

// TestMatchTier_MultiLineCRLFMatchPreservesUnmatchedLineEndings is the harder
// version: the match itself succeeds at the line-ending tier, spanning several
// CRLF lines. Everything OUTSIDE the replaced span must keep its exact bytes --
// and, since conformReplacementEOL, the span itself comes back CRLF too, so the
// file has one convention rather than two.
func TestMatchTier_MultiLineCRLFMatchPreservesUnmatchedLineEndings(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "crlf.txt", "one\r\ntwo\r\nthree\r\nfour\r\n")

	prepared, err := PrepareEdit(root, EditBlock{
		FilePath: "crlf.txt",
		Search:   "two\nthree", // LF search against a CRLF file
		Replace:  "TWO\nTHREE",
	})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.Tier != MatchLineEndings {
		t.Errorf("Tier = %v, want %v", prepared.Tier, MatchLineEndings)
	}

	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filepath.Join(root, "crlf.txt"))
	want := "one\r\nTWO\r\nTHREE\r\nfour\r\n"
	if got != want {
		t.Errorf("got %q, want %q -- lines outside the match must keep their CRLF, and the replacement must be written in it", got, want)
	}
	if strings.Contains(got, "\n") && strings.Count(got, "\r\n") != strings.Count(got, "\n") {
		t.Errorf("file came back MIXED: %q has %d LF but only %d CRLF", got, strings.Count(got, "\n"), strings.Count(got, "\r\n"))
	}
}

// TestMatchTier_NormalizationInducedAmbiguityRefuses is the requirement that a
// looser tier must not silently pick one of several now-equal candidates.
// These two lines are DISTINCT byte-for-byte (tab vs spaces) and become
// identical once indentation is normalized; that must refuse, not guess.
func TestMatchTier_NormalizationInducedAmbiguityRefuses(t *testing.T) {
	root := realTempDir(t)
	// The two "do()" lines are distinct byte-for-byte (one tab vs two) and stay
	// distinct at every tier ABOVE indentation; the four-space SEARCH matches
	// neither of them as a substring until indentation is ignored, at which
	// point it matches both. A ".txt" target keeps the Go syntax gate out of the
	// way so the refusal under test is unambiguously the matcher's.
	writeTempFile(t, root, "f.txt", "if a {\n\tdo()\n}\nif b {\n\t\tdo()\n}\n")

	_, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "    do()", Replace: "redo()"})
	if err == nil {
		t.Fatal("SILENT PICK: expected an ambiguity refusal once indentation is normalized")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %v, want it to name the ambiguity", err)
	}
	t.Logf("refused with: %v", err)
}

// TestMatchTier_ExactWinsOverLooseTier pins the ladder's ordering: when the
// text matches exactly somewhere, that is the match, even if a looser tier
// would also match somewhere else. Tolerance is a fallback, never a preference.
func TestMatchTier_ExactWinsOverLooseTier(t *testing.T) {
	root := realTempDir(t)
	// "\tvalue" matches line 2 exactly; line 4 "    value" would only match
	// after indentation normalization.
	writeTempFile(t, root, "f.txt", "a\n\tvalue\nb\n    value\nc\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "\tvalue", Replace: "\tVALUE"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.Tier != MatchExact {
		t.Errorf("Tier = %v, want %v", prepared.Tier, MatchExact)
	}
	want := "a\n\tVALUE\nb\n    value\nc\n"
	if prepared.NewContent != want {
		t.Errorf("NewContent = %q, want %q (the exact match, not the loose one)", prepared.NewContent, want)
	}
}

// TestMatchTier_ReportsWhichTierMatched pins the legibility requirement: a
// non-exact match must say so, so the behaviour is debuggable and a user can
// see that a normalization happened.
func TestMatchTier_ReportsWhichTierMatched(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "alpha\r\nbeta\r\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "alpha\nbeta", Replace: "X"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.MatchNote == "" {
		t.Fatal("MatchNote is empty; a non-exact match must be surfaced")
	}
	if !strings.Contains(strings.ToLower(prepared.MatchNote), "line ending") {
		t.Errorf("MatchNote = %q, want it to name the line-ending normalization", prepared.MatchNote)
	}
	t.Logf("MatchNote = %q", prepared.MatchNote)
}

// TestMatchTier_ExactMatchNoteIsQuiet is the other half: the ordinary case must
// not start announcing a normalization that did not happen.
func TestMatchTier_ExactMatchNoteIsQuiet(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "alpha\nbeta\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "alpha", Replace: "X"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.MatchNote != "" {
		t.Errorf("MatchNote = %q, want empty for an exact match", prepared.MatchNote)
	}
}

// --- the mandatory Fix 6 evidence: tolerance must not reach change detection ---

// TestChangeDetection_IsByteExactAndUnaffectedByMatchingTolerance is the
// guarantee Ground Rule 3 protects. The SEARCH ladder may normalize in order to
// LOCATE text in the file as it is NOW; the check for "did this file change
// since we prepared the edit" is a separate, byte-exact comparison, and must
// refuse on exactly the differences the matcher is tolerant of. If tolerance
// ever leaks into that check, this test fails.
func TestChangeDetection_IsByteExactAndUnaffectedByMatchingTolerance(t *testing.T) {
	// Each mutation is invisible to the matching ladder by design -- these are
	// precisely the dimensions Fix 6 made tolerant. Change detection must still
	// refuse every one of them.
	mutations := map[string]func(string) string{
		"line endings changed":     func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") },
		"trailing space added":     func(s string) string { return strings.ReplaceAll(s, "beta", "beta   ") },
		"indentation retabbed":     func(s string) string { return strings.ReplaceAll(s, "\t", "    ") },
		"unicode renormalized":     func(s string) string { return strings.ReplaceAll(s, nfcCafe, nfdCafe) },
		"zero-width char injected": func(s string) string { return strings.ReplaceAll(s, "beta", "be"+zeroWidthSpace+"ta") },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := realTempDir(t)
			original := "alpha\n\tbeta " + nfcCafe + "\ngamma\n"
			path := writeTempFile(t, root, "f.txt", original)

			prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "gamma", Replace: "GAMMA"})
			if err != nil {
				t.Fatalf("PrepareEdit: %v", err)
			}

			// The file changes underneath the prepared edit, in a way the
			// matching ladder considers equivalent.
			mutated := mutate(original)
			if mutated == original {
				t.Fatalf("test bug: mutation %q did not change the content", name)
			}
			if err := os.WriteFile(path, []byte(mutated), 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			backupDir, _ := NewBackupSessionDir(root)
			err = Apply(root, prepared, backupDir)
			if err == nil {
				t.Fatalf("STALE EDIT APPLIED: change detection accepted a file that changed (%s)", name)
			}
			if !strings.Contains(err.Error(), "changed") {
				t.Errorf("error = %v, want it to name the change", err)
			}
			if got := readFile(t, path); got != mutated {
				t.Errorf("file = %q, want the mutated content untouched (%q)", got, mutated)
			}
		})
	}
}

// TestChangeDetection_UnchangedFileStillApplies is the negative control for the
// check above: it must refuse only real changes, never the ordinary path.
func TestChangeDetection_UnchangedFileStillApplies(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "alpha\nbeta\ngamma\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "beta", Replace: "BETA"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("unchanged file must still apply, got: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "f.txt")); got != "alpha\nBETA\ngamma\n" {
		t.Errorf("file = %q, want the edit applied", got)
	}
}
