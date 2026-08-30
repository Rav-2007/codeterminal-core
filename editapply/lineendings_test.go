package editapply

import (
	"path/filepath"
	"strings"
	"testing"
)

// eolOf is the assertion the whole file is really about: it reports the one
// convention s uses, and fails the test if s is MIXED -- which is the defect
// conformReplacementEOL exists to prevent and the thing a human eye cannot see
// in test output.
func eolOf(t *testing.T, what, s string) eolStyle {
	t.Helper()
	got := dominantEOL(s)
	if got == eolMixed {
		t.Errorf("%s came back MIXED: %q (%d LF, %d CRLF, %d lone CR)",
			what, s, strings.Count(s, "\n"), strings.Count(s, "\r\n"),
			strings.Count(s, "\r")-strings.Count(s, "\r\n"))
	}
	return got
}

func TestDominantEOL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want eolStyle
	}{
		{"empty", "", eolNone},
		{"no line break at all", "alpha", eolNone},
		{"single LF", "a\n", eolLF},
		{"several LF", "a\nb\nc\n", eolLF},
		{"single CRLF", "a\r\n", eolCRLF},
		{"several CRLF", "a\r\nb\r\nc\r\n", eolCRLF},
		{"lone CR", "a\rb\rc\r", eolCR},
		{"LF then CRLF", "a\nb\r\n", eolMixed},
		{"CRLF then LF", "a\r\nb\n", eolMixed},
		{"CR then LF", "a\rb\n", eolMixed},
		{"CRLF then lone CR", "a\r\nb\r", eolMixed},
		// A carriage return that is part of a CRLF must not also be counted as
		// a lone CR, or every CRLF text would read as mixed.
		{"CRLF is one break, not two", "a\r\nb\r\n", eolCRLF},
		{"trailing CR at end of text", "a\r\nb\r\n\r", eolMixed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dominantEOL(c.in); got != c.want {
				t.Errorf("dominantEOL(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestReencodeEOL(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		style eolStyle
		want  string
	}{
		{"LF to CRLF", "a\nb\nc", eolCRLF, "a\r\nb\r\nc"},
		{"CRLF to LF", "a\r\nb\r\nc", eolLF, "a\nb\nc"},
		{"CR to CRLF", "a\rb\rc", eolCRLF, "a\r\nb\r\nc"},
		{"CRLF to CR", "a\r\nb\r\nc", eolCR, "a\rb\rc"},
		{"already LF, to LF", "a\nb", eolLF, "a\nb"},
		{"mixed input is made uniform", "a\r\nb\nc\rd", eolCRLF, "a\r\nb\r\nc\r\nd"},
		{"no line break", "alpha", eolCRLF, "alpha"},
		{"trailing break is re-encoded too", "a\n", eolCRLF, "a\r\n"},
		// The two styles that name no convention impose nothing.
		{"eolNone imposes nothing", "a\r\nb", eolNone, "a\r\nb"},
		{"eolMixed imposes nothing", "a\r\nb", eolMixed, "a\r\nb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reencodeEOL(c.in, c.style); got != c.want {
				t.Errorf("reencodeEOL(%q, %v) = %q, want %q", c.in, c.style, got, c.want)
			}
		})
	}
}

// TestAnEditNeverChangesTheFilesLineEndingConvention is the four-cell matrix and
// the statement of the invariant: whatever convention the replacement arrives
// in, the file keeps its own.
func TestAnEditNeverChangesTheFilesLineEndingConvention(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		search  string
		replace string
		want    string
	}{
		{
			name: "LF file, LF replacement -- the ordinary case, unchanged",
			file: "one\ntwo\nthree\nfour\n", search: "two\nthree", replace: "TWO\nTHREE",
			want: "one\nTWO\nTHREE\nfour\n",
		},
		{
			name: "LF file, CRLF replacement",
			file: "one\ntwo\nthree\nfour\n", search: "two\nthree", replace: "TWO\r\nTHREE",
			want: "one\nTWO\nTHREE\nfour\n",
		},
		{
			name: "CRLF file, LF replacement -- the Windows `git diff` case",
			file: "one\r\ntwo\r\nthree\r\nfour\r\n", search: "two\nthree", replace: "TWO\nTHREE",
			want: "one\r\nTWO\r\nTHREE\r\nfour\r\n",
		},
		{
			name: "CRLF file, CRLF replacement",
			file: "one\r\ntwo\r\nthree\r\nfour\r\n", search: "two\r\nthree", replace: "TWO\r\nTHREE",
			want: "one\r\nTWO\r\nTHREE\r\nfour\r\n",
		},
		{
			name: "CR-only file keeps CR rather than being promoted to LF",
			file: "one\rtwo\rthree\rfour\r", search: "two\nthree", replace: "TWO\nTHREE",
			want: "one\rTWO\rTHREE\rfour\r",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := realTempDir(t)
			writeTempFile(t, root, "f.txt", c.file)

			prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: c.search, Replace: c.replace})
			if err != nil {
				t.Fatalf("PrepareEdit: %v", err)
			}
			if prepared.NewContent != c.want {
				t.Errorf("NewContent = %q, want %q", prepared.NewContent, c.want)
			}
			if got, want := eolOf(t, "the edited file", prepared.NewContent), dominantEOL(c.file); got != want {
				t.Errorf("file convention changed: was %v, now %v", want, got)
			}
		})
	}
}

// TestACRLFReplacementIntoAnLFFileIsReencodedAtTheExactTier is the case that
// decides the SCOPE of the rule, and the reason it is not gated on match.Tier.
//
// The SEARCH text here is byte-exact, so the matcher never has to forgive
// anything and the tier stays MatchExact. A tier-gated re-encode would do
// nothing at all and the file would come back mixed. The invariant is about
// what gets WRITTEN, not about what the matcher had to forgive to find it.
func TestACRLFReplacementIntoAnLFFileIsReencodedAtTheExactTier(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "alpha\nbeta\ngamma\n")

	prepared, err := PrepareEdit(root, EditBlock{
		FilePath: "f.txt",
		Search:   "alpha\nbeta",   // byte-exact against the file
		Replace:  "ALPHA\r\nBETA", // ...but the replacement is CRLF
	})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.Tier != MatchExact {
		t.Fatalf("Tier = %v, want MatchExact -- this test is only evidence while the match is exact", prepared.Tier)
	}
	if want := "ALPHA\nBETA\ngamma\n"; prepared.NewContent != want {
		t.Errorf("NewContent = %q, want %q", prepared.NewContent, want)
	}
	if got := eolOf(t, "the edited file", prepared.NewContent); got != eolLF {
		t.Errorf("file convention = %v, want LF", got)
	}
}

// TestTheReplacedRegionOutranksTheFile pins the precedence. The file as a whole
// is mixed and therefore has no opinion; the region being replaced is CRLF and
// does. Deciding from the file alone would leave the replacement's LF in place.
func TestTheReplacedRegionOutranksTheFile(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "a\nb\nX\r\nY\r\nc\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "X\r\nY", Replace: "P\nQ"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if want := "a\nb\nP\r\nQ\r\nc\n"; prepared.NewContent != want {
		t.Errorf("NewContent = %q, want %q -- the region's CRLF must win over the file's absence of a convention", prepared.NewContent, want)
	}
}

// TestAMixedFileIsLeftAlone is the deliberate no-op. There is no convention to
// preserve, so imposing one would rewrite bytes the user never asked about --
// a bigger change than the bug being fixed.
func TestAMixedFileIsLeftAlone(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "a\r\nb\nc\r\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "b", Replace: "X\nY"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if want := "a\r\nX\nY\nc\r\n"; prepared.NewContent != want {
		t.Errorf("NewContent = %q, want %q -- a mixed file must be spliced verbatim", prepared.NewContent, want)
	}
	if prepared.MatchNote != "" {
		t.Errorf("MatchNote = %q, want empty -- nothing was re-encoded, so nothing may claim it was", prepared.MatchNote)
	}
}

// TestASameLineEditIsSplicedVerbatim is the property that keeps every ordinary
// edit byte-identical to what the caller sent, and the reason
// TestMatchTier_ExactMatchNoteIsQuiet still passes: a replacement with no line
// break has nothing to re-encode.
func TestASameLineEditIsSplicedVerbatim(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "alpha\r\nbeta\r\ngamma\r\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "beta", Replace: "BETA"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if want := "alpha\r\nBETA\r\ngamma\r\n"; prepared.NewContent != want {
		t.Errorf("NewContent = %q, want %q", prepared.NewContent, want)
	}
	if strings.Contains(prepared.MatchNote, "re-encoded") {
		t.Errorf("MatchNote = %q, want no re-encode claim for a replacement with no line break", prepared.MatchNote)
	}
}

// TestTheReencodeIsDisclosed pins that the write is never silent. MatchNote is
// rendered to the MODEL by propose_edit and propose_ast_edit as well as to the
// CLI and the TUI, so this is the channel by which an agent learns its bytes
// were adjusted -- and it must carry BOTH facts, because the tier and the
// re-encode are separate things that happened.
func TestTheReencodeIsDisclosed(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "f.txt", "one\r\ntwo\r\nthree\r\nfour\r\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "two\nthree", Replace: "TWO\nTHREE"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	note := strings.ToLower(prepared.MatchNote)
	if !strings.Contains(note, "line ending") {
		t.Errorf("MatchNote = %q, want it to still name the tier normalization", prepared.MatchNote)
	}
	if !strings.Contains(note, "re-encoded to crlf") {
		t.Errorf("MatchNote = %q, want it to name the re-encode and the convention", prepared.MatchNote)
	}
	t.Logf("MatchNote = %q", prepared.MatchNote)
}

// TestCreateWritesReplaceVerbatim pins the ESCAPE HATCH, so nobody closes it by
// making the create path "consistent" with the edit path.
//
// An empty SEARCH section means "this file's content is, or should be, this",
// and there is no prior convention to preserve -- so REPLACE is written exactly
// as sent. That is what a user who genuinely wants to CONVERT a file's line
// endings uses, and it is the only way to do it once the edit path conforms.
func TestCreateWritesReplaceVerbatim(t *testing.T) {
	root := realTempDir(t)
	content := "a\r\nb\nc\r\n" // deliberately mixed, and deliberately preserved

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "new.txt", Search: "", Replace: content})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.NewContent != content {
		t.Errorf("NewContent = %q, want %q -- the create path must write REPLACE verbatim", prepared.NewContent, content)
	}

	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "new.txt")); got != content {
		t.Errorf("on disk = %q, want %q", got, content)
	}
}

// TestAnOverwritingCreateAlsoWritesVerbatim is the other half of the hatch: an
// EXISTING but empty file takes the create path too, and must not acquire a
// convention from the zero bytes it had.
func TestAnOverwritingCreateAlsoWritesVerbatim(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "empty.txt", "")
	content := "x\r\ny\r\n"

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "empty.txt", Search: "", Replace: content})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.NewContent != content {
		t.Errorf("NewContent = %q, want %q", prepared.NewContent, content)
	}
}
