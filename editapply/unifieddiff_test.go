package editapply

import (
	"strings"
	"testing"
)

// diffOf assembles a git-style file stanza around the hunk lines given, so a
// test only has to state the variation it cares about.
func diffOf(oldPath, newPath string, body ...string) string {
	head := []string{
		"diff --git a/f.txt b/f.txt",
		"index 0906fba..86ba82a 100644",
		"--- " + oldPath,
		"+++ " + newPath,
	}
	return strings.Join(append(head, body...), "\n") + "\n"
}

func onlyBlock(t *testing.T, p EditPayload) EditBlock {
	t.Helper()
	if p.Format != FormatUnifiedDiff {
		t.Fatalf("Format = %v, want FormatUnifiedDiff (rejections: %v)", p.Format, p.Rejected)
	}
	if len(p.Rejected) != 0 {
		t.Fatalf("unexpected rejections: %v", p.Rejected)
	}
	if len(p.Blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(p.Blocks), p.Blocks)
	}
	return p.Blocks[0]
}

// wantRefused asserts the payload was read AS a diff and that every hunk in it
// was refused for a reason mentioning want. A refusal is only useful if it says
// what to do instead, so the reason text is part of the contract.
func wantRefused(t *testing.T, p EditPayload, want string) {
	t.Helper()
	if len(p.Blocks) != 0 {
		t.Fatalf("expected no applicable blocks, got %d: %+v", len(p.Blocks), p.Blocks)
	}
	if len(p.Rejected) == 0 {
		t.Fatal("expected a named refusal, got none — a construct this engine cannot express must never be silently dropped")
	}
	for _, r := range p.Rejected {
		if strings.Contains(r.Reason, want) {
			return
		}
	}
	t.Fatalf("no refusal mentioned %q; got: %v", want, p.Rejected)
}

// The headline: a real patch becomes ordinary edit blocks.
func TestUnifiedDiffPayloadIsIngested(t *testing.T) {
	p := ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -1,3 +1,3 @@",
		" first",
		"-second",
		"+SECOND",
		" third",
	))
	b := onlyBlock(t, p)

	if b.FilePath != "f.txt" {
		t.Errorf("FilePath = %q, want f.txt", b.FilePath)
	}
	// Context lines belong to BOTH sides: they are what makes SEARCH unique.
	if b.Search != "first\nsecond\nthird" {
		t.Errorf("Search = %q, want context+'-' lines", b.Search)
	}
	if b.Replace != "first\nSECOND\nthird" {
		t.Errorf("Replace = %q, want context+'+' lines", b.Replace)
	}
}

func TestMultiHunkAndMultiFileDiffsBecomeOneBlockEach(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/one.txt b/one.txt",
		"--- a/one.txt",
		"+++ b/one.txt",
		"@@ -1,2 +1,2 @@",
		" keep",
		"-a",
		"+A",
		"@@ -20,2 +20,2 @@",
		" keep2",
		"-b",
		"+B",
		"diff --git a/two.txt b/two.txt",
		"--- a/two.txt",
		"+++ b/two.txt",
		"@@ -1,2 +1,2 @@",
		" keep3",
		"-c",
		"+C",
	}, "\n") + "\n"

	p := ParseEditPayload(diff)
	if p.Format != FormatUnifiedDiff || len(p.Rejected) != 0 {
		t.Fatalf("Format = %v, rejections = %v", p.Format, p.Rejected)
	}
	if len(p.Blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (one per hunk)", len(p.Blocks))
	}
	if p.Blocks[0].FilePath != "one.txt" || p.Blocks[2].FilePath != "two.txt" {
		t.Errorf("paths = %q/%q/%q", p.Blocks[0].FilePath, p.Blocks[1].FilePath, p.Blocks[2].FilePath)
	}
}

func TestGitStylePathsResolveToWorkspacePaths(t *testing.T) {
	tests := []struct{ name, old, new, want string }{
		{"git a/ b/ prefixes", "a/dir/f.txt", "b/dir/f.txt", "dir/f.txt"},
		{"plain diff -u, no prefixes", "dir/f.txt", "dir/f.txt", "dir/f.txt"},
		{"timestamps after a tab", "old.txt\t2026-01-01 10:00", "old.txt\t2026-01-02 10:00", "old.txt"},
		// Only ONE component is stripped: a real path that begins with "a/"
		// must survive, which is why the strip is a spelling convention and
		// not a loop.
		{"path whose own first component is a", "a/a/f.txt", "b/a/f.txt", "a/f.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := onlyBlock(t, ParseEditPayload(diffOf(tt.old, tt.new,
				"@@ -1,2 +1,2 @@", " keep", "-x", "+y")))
			if b.FilePath != tt.want {
				t.Errorf("FilePath = %q, want %q", b.FilePath, tt.want)
			}
		})
	}
}

// A /dev/null old side is the ONE case where an empty SEARCH is correct: it is
// the create instruction the engine already has (see IsEmptySearch), not a
// second creation mechanism.
func TestDevNullOldSideBecomesACreateBlock(t *testing.T) {
	b := onlyBlock(t, ParseEditPayload(diffOf("/dev/null", "b/new.txt",
		"@@ -0,0 +1,2 @@",
		"+hello",
		"+world",
	)))
	if b.FilePath != "new.txt" {
		t.Errorf("FilePath = %q, want new.txt", b.FilePath)
	}
	if !IsEmptySearch(b.Search) {
		t.Errorf("Search = %q, want empty so the existing create path handles it", b.Search)
	}
	if b.Replace != "hello\nworld" {
		t.Errorf("Replace = %q", b.Replace)
	}
}

// The inverse, and the reason it cannot be waved through: an "add only" hunk
// against an EXISTING file has no text to locate. Letting the empty SEARCH
// through would turn "add three lines" into "this file's whole content is
// these three lines".
func TestPureInsertionIntoAnExistingFileIsRefused(t *testing.T) {
	wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -0,0 +1,2 @@",
		"+hello",
		"+world",
	)), "no before-text to locate")
}

func TestDeleteDiffIsRefusedNotEmptied(t *testing.T) {
	// Emptying the file would be a DIFFERENT change, and `edits undo` records
	// before/after content — so it could not tell an emptied file from an
	// ordinary edit afterwards.
	wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "/dev/null",
		"@@ -1,2 +0,0 @@",
		"-gone",
		"-also gone",
	)), "no delete step")
}

func TestRenameDiffIsRefused(t *testing.T) {
	p := ParseEditPayload(diffOf("a/old.txt", "b/new.txt",
		"@@ -1,2 +1,2 @@", " keep", "-x", "+y"))
	wantRefused(t, p, "never its name")
	// The message must name BOTH paths: "a rename was refused" is not
	// actionable without knowing which rename.
	joined := ""
	for _, r := range p.Rejected {
		joined += r.Reason
	}
	if !strings.Contains(joined, "old.txt") || !strings.Contains(joined, "new.txt") {
		t.Errorf("refusal does not name both paths: %q", joined)
	}
}

func TestExecutableModeDiffIsRefused(t *testing.T) {
	for _, mode := range []string{"new file mode 100755", "new mode 100755", "old mode 100755"} {
		t.Run(mode, func(t *testing.T) {
			diff := strings.Join([]string{
				"diff --git a/f.txt b/f.txt",
				mode,
				"--- a/f.txt",
				"+++ b/f.txt",
				"@@ -1,2 +1,2 @@", " keep", "-x", "+y",
			}, "\n") + "\n"
			wantRefused(t, ParseEditPayload(diff), "0644 on purpose")
		})
	}
}

// The ordinary 100644 mode names the default this engine already writes, so it
// must be ignored rather than refused — otherwise every `git diff` of a new
// file would be rejected.
func TestOrdinaryFileModeIsIgnored(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/new.txt b/new.txt",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1,1 @@",
		"+hello",
	}, "\n") + "\n"
	if b := onlyBlock(t, ParseEditPayload(diff)); b.FilePath != "new.txt" {
		t.Errorf("FilePath = %q", b.FilePath)
	}
}

// The final newline lives OUTSIDE the text a hunk matches, so a change to it
// cannot be spliced. Applying it as an ordinary edit silently adds or drops
// one byte at end of file.
func TestFinalNewlineChangeIsRefused(t *testing.T) {
	wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -1 +1 @@",
		"-a",
		"\\ No newline at end of file",
		"+a",
	)), "ends in a newline")
}

// The symmetric case must NOT be refused: both sides lacking a trailing newline
// says nothing about the change, and the natural join is already correct.
func TestSymmetricNoNewlineMarkerIsIgnored(t *testing.T) {
	b := onlyBlock(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -1 +1 @@",
		"-a",
		"\\ No newline at end of file",
		"+b",
		"\\ No newline at end of file",
	)))
	if b.Search != "a" || b.Replace != "b" {
		t.Errorf("Search/Replace = %q/%q, want a/b", b.Search, b.Replace)
	}
}

func TestTruncatedHunkIsRefused(t *testing.T) {
	t.Run("body ends early", func(t *testing.T) {
		wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
			"@@ -1,5 +1,5 @@",
			" one",
			"-two",
		)), "truncated")
	})
	t.Run("prose interrupts the body", func(t *testing.T) {
		wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
			"@@ -1,5 +1,5 @@",
			" one",
			"-two",
			"Sorry, I ran out of room.",
		)), "truncated or malformed")
	})
}

// git emits " " for an empty context line; mailers, chat clients and copy-paste
// strip that trailing space. Reading a zero-length line as an empty context
// line is what makes a pasted patch work.
func TestHunkWithBlankContextLinesRoundTrips(t *testing.T) {
	b := onlyBlock(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -1,3 +1,3 @@",
		" first",
		"",
		"-third",
		"+THIRD",
	)))
	if b.Search != "first\n\nthird" {
		t.Errorf("Search = %q, want the blank line preserved as context", b.Search)
	}
	if b.Replace != "first\n\nTHIRD" {
		t.Errorf("Replace = %q", b.Replace)
	}
}

func TestOverlappingHunksAreRefused(t *testing.T) {
	diff := diffOf("a/f.txt", "b/f.txt",
		"@@ -1,3 +1,3 @@", " one", "-two", "+TWO", " three",
		"@@ -2,3 +2,3 @@", " two", "-three", "+THREE", " four",
	)
	p := ParseEditPayload(diff)
	if len(p.Blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (the first; the overlapping second is refused)", len(p.Blocks))
	}
	wantOneRefusal(t, p, "overlaps")
}

// Adjacent-but-not-overlapping hunks are ordinary and must be kept: they share
// no line, so applying both in order is safe.
func TestAdjacentHunksAreBothKept(t *testing.T) {
	p := ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -1,2 +1,2 @@", " one", "-two", "+TWO",
		"@@ -3,2 +3,2 @@", " three", "-four", "+FOUR",
	))
	if len(p.Rejected) != 0 {
		t.Fatalf("unexpected refusals: %v", p.Rejected)
	}
	if len(p.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(p.Blocks))
	}
}

func TestCombinedMergeDiffIsRefused(t *testing.T) {
	diff := "diff --cc f.txt\n--- a/f.txt\n+++ b/f.txt\n@@@ -1,2 -1,2 +1,2 @@@\n  one\n--two\n++TWO\n"
	wantRefused(t, ParseEditPayload(diff), "combined/merge diff")
}

func TestBinaryPatchIsRefused(t *testing.T) {
	diff := "diff --git a/img.png b/img.png\nindex 111..222 100644\nGIT binary patch\ndelta 42\n@@ -1,1 +1,1 @@\n-x\n+y\n"
	wantRefused(t, ParseEditPayload(diff), "binary patch")
}

// A hunk with no file header above it has nothing to apply to. Guessing the
// path is exactly the kind of inference this reader must not make.
func TestHunkWithNoFileHeaderIsRefused(t *testing.T) {
	wantRefused(t, ParseEditPayload("@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"), "no file to apply to")
}

func TestNoOpHunkIsRefused(t *testing.T) {
	wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -1,2 +1,2 @@",
		" one",
		"-two",
		"+two",
	)), "changes nothing")
}

func wantOneRefusal(t *testing.T, p EditPayload, want string) {
	t.Helper()
	for _, r := range p.Rejected {
		if strings.Contains(r.Reason, want) {
			return
		}
	}
	t.Fatalf("no refusal mentioned %q; got: %v", want, p.Rejected)
}

// TestOverDeclaredHunkCountIsRefused exists because of what the count check
// nearly missed. strings.Split leaves a trailing "" for any text ending in a
// newline, and every real patch ends in one; that phantom is indistinguishable
// from an empty context line, so a hunk claiming one line too many absorbed it
// and parsed "successfully" with a spurious blank line in its SEARCH -- which
// then failed to match, reported as "not found" rather than as the malformed
// header it was.
func TestOverDeclaredHunkCountIsRefused(t *testing.T) {
	wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
		"@@ -1,4 +1,4 @@",
		" first",
		"",
		"-third",
		"+THIRD",
	)), "truncated")
}

// TestSearchReplaceWinsOverDiffSniff pins the dispatch ORDER. SEARCH/REPLACE is
// what the system prompt asks for, so it is the likelier reading, and running
// the diff reader first would let a response containing both be read as the
// format nobody asked for.
func TestSearchReplaceWinsOverDiffSniff(t *testing.T) {
	mixed := strings.Join([]string{
		"Here is a patch for reference:",
		"",
		"diff --git a/other.txt b/other.txt",
		"--- a/other.txt",
		"+++ b/other.txt",
		"@@ -1 +1 @@",
		"-x",
		"+y",
		"",
		"But the actual edit is:",
		"",
		"path: a.txt",
		searchMarker,
		"old",
		separatorMarker,
		"new",
		replaceMarker,
	}, "\n")

	p := ParseEditPayload(mixed)
	if p.Format != FormatSearchReplace {
		t.Fatalf("Format = %v, want FormatSearchReplace", p.Format)
	}
	if len(p.Blocks) != 1 || p.Blocks[0].FilePath != "a.txt" {
		t.Fatalf("Blocks = %+v, want the single SEARCH/REPLACE block", p.Blocks)
	}
}

// A parser REJECTION also counts as "this was SEARCH/REPLACE". A recognised
// block that failed must keep its own line-numbered reason rather than be
// re-diagnosed as a different format.
func TestARejectedBlockIsNotReinterpretedAsADiff(t *testing.T) {
	mixed := "path: a.txt\n" + searchMarker + "\nunterminated\n\ndiff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	p := ParseEditPayload(mixed)
	if p.Format != FormatSearchReplace {
		t.Fatalf("Format = %v, want FormatSearchReplace", p.Format)
	}
	if len(p.Rejected) == 0 {
		t.Fatal("the malformed block's own reason was lost")
	}
}

// TestHunkBodyOverrunIsRefused is the FuzzParseUnifiedDiff regression. The count
// check was one-sided: it caught a body with too few lines but let one push a
// side past its declared count, so "@@ -0,0 +0 @@" followed by a context line
// produced a block whose SEARCH and REPLACE were both empty -- which against a
// /dev/null stanza means "create this file with no content".
func TestHunkBodyOverrunIsRefused(t *testing.T) {
	t.Run("context line in a pure-insertion hunk", func(t *testing.T) {
		wantRefused(t, ParseEditPayload("--- /dev/null\n+++ b/n.txt\n@@ -0,0 +0 @@\n \n"), "more lines than its header declares")
	})
	t.Run("extra minus line", func(t *testing.T) {
		wantRefused(t, ParseEditPayload(diffOf("a/f.txt", "b/f.txt",
			"@@ -1,1 +1,1 @@", "-one", "-two", "+ONE")), "more lines than its header declares")
	})
}

// TestPathsWithControlCharactersAreRefused is the other FuzzParseUnifiedDiff
// regression, and it applies to BOTH parsers. A NUL byte reached an EditBlock's
// FilePath and travelled to clients as a proposal; nothing wrote it (the
// confinement gates refuse control characters) but a block with an unprintable
// target is malformed, not merely out of bounds. The shared predicate is
// RejectUnprintablePath.
func TestPathsWithControlCharactersAreRefused(t *testing.T) {
	t.Run("diff reader", func(t *testing.T) {
		p := ParseEditPayload("--- /dev/null\n+++ \x00\n@@ -0,0 +1 @@\n+hi\n")
		for _, b := range p.Blocks {
			if strings.ContainsAny(b.FilePath, "\x00\n") {
				t.Fatalf("emitted a block with an unprintable FilePath: %q", b.FilePath)
			}
		}
	})
	t.Run("block parser", func(t *testing.T) {
		blocks, rejected := ParseEditBlocks("path: \x00\n" + searchMarker + "\nfoo\n" + separatorMarker + "\nbar\n" + replaceMarker)
		for _, b := range blocks {
			if strings.ContainsAny(b.FilePath, "\x00\n") {
				t.Fatalf("emitted a block with an unprintable FilePath: %q", b.FilePath)
			}
		}
		if len(rejected) == 0 {
			t.Error("the malformed path was dropped silently rather than refused by line number")
		}
	})
}
