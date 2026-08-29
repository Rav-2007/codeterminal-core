package editapply

// Fuzzing the edit trust boundary (P3.1).
//
// Both targets read text the model produced, not text this code authored.
// ParseEditBlocks consumes raw LLM output; findSearch consumes a SEARCH block
// from that same output and returns byte offsets that a caller then SLICES FILE
// CONTENT WITH. An out-of-range offset there is not a parse bug, it is a corrupt
// write to a user's source file.
//
// Invariants: never panic, always terminate, and fail closed -- a block that
// cannot be understood must be REFUSED (reported as a BlockError), never
// silently turned into an edit with a missing or empty target.

import (
	"reflect"
	"strings"
	"testing"
)

// FuzzParseEditBlocks pins the property that makes the parser safe to trust:
// anything it hands back as an EditBlock is complete enough to act on.
//
// A block with an empty FilePath is the dangerous shape -- it is an edit with no
// target, and whatever a caller resolves that to (the workspace root, the
// current directory) is somewhere the user did not ask to modify.
func FuzzParseEditBlocks(f *testing.F) {
	f.Add("")
	f.Add("no blocks here at all")
	f.Add("path/to/file.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n")
	// Truncated / malformed shapes: each must be refused, not half-accepted.
	f.Add("<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n")
	f.Add("f.go\n<<<<<<< SEARCH\nold\n=======\nnew\n")
	f.Add("f.go\n<<<<<<< SEARCH\n=======\n>>>>>>> REPLACE\n")
	f.Add("f.go\n<<<<<<< SEARCH\nold\n>>>>>>> REPLACE\n")
	f.Add("   \n<<<<<<< SEARCH\na\n=======\nb\n>>>>>>> REPLACE\n")
	// Two blocks, the second malformed.
	f.Add("a.go\n<<<<<<< SEARCH\n1\n=======\n2\n>>>>>>> REPLACE\nb.go\n<<<<<<< SEARCH\n")

	f.Fuzz(func(t *testing.T, response string) {
		blocks, blockErrs := ParseEditBlocks(response)

		for i, b := range blocks {
			if strings.TrimSpace(b.FilePath) == "" {
				t.Fatalf("block %d was ACCEPTED with an empty FilePath: an edit with no "+
					"target resolves to whatever the caller defaults to, which is not a file "+
					"the user asked to change. input=%q", i, response)
			}
			if strings.ContainsAny(b.FilePath, "\x00\n") {
				t.Fatalf("block %d has a FilePath containing a NUL or newline (%q), which no "+
					"path check downstream is written to expect. input=%q", i, b.FilePath, response)
			}
		}

		for i, be := range blockErrs {
			if be.Reason == "" {
				t.Fatalf("blockErr %d has an empty Reason, so a refused block would be "+
					"reported to the user with no explanation. input=%q", i, response)
			}
		}
	})
}

// FuzzFindSearch is the higher-stakes of the two: the returned offsets are used
// to splice replacement text into real file content.
//
// The invariant is bounds. If findSearch reports success, Start and End must
// describe a real slice of `original` -- otherwise the caller either panics
// while writing a user's file, or writes to the wrong place.
func FuzzFindSearch(f *testing.F) {
	f.Add("hello world", "world")
	f.Add("hello world", "")
	f.Add("", "x")
	f.Add("a\r\nb\r\n", "a\nb\n")
	f.Add("  indented\n", "indented")
	f.Add("dup dup", "dup")
	f.Add("café", "café") // unicode normalization tier
	f.Add("trailing   \n", "trailing\n")

	f.Fuzz(func(t *testing.T, original, search string) {
		m, err := findSearch(original, search, "fuzz.txt")
		if err != nil {
			return // a refusal is always an acceptable answer
		}

		if m.Start < 0 || m.End < 0 {
			t.Fatalf("negative offsets Start=%d End=%d for original=%q search=%q",
				m.Start, m.End, original, search)
		}
		if m.Start > m.End {
			t.Fatalf("inverted match Start=%d > End=%d for original=%q search=%q",
				m.Start, m.End, original, search)
		}
		if m.End > len(original) {
			t.Fatalf("End=%d runs past len(original)=%d -- slicing file content with this "+
				"would panic or corrupt the write. original=%q search=%q",
				m.End, len(original), original, search)
		}

		// The offsets must be sliceable. This is the operation the caller performs.
		_ = original[m.Start:m.End]

		// An empty SEARCH is refused up front; a successful match must therefore
		// name a non-empty region, or a "replacement" would be a blind insertion
		// at an arbitrary offset.
		if m.Start == m.End {
			t.Fatalf("matched an empty region at offset %d, which would insert rather than "+
				"replace. original=%q search=%q", m.Start, original, search)
		}
	})
}

// FuzzParseUnifiedDiff holds the diff reader to the same contract as the block
// parser -- never panic, always terminate, fail closed -- plus two invariants
// that belong only to this grammar.
func FuzzParseUnifiedDiff(f *testing.F) {
	f.Add("")
	f.Add("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")
	f.Add("--- /dev/null\n+++ b/n\n@@ -0,0 +1 @@\n+hi\n")
	f.Add("@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n")
	f.Add("--- a/x\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n")
	f.Add("diff --git a/x b/y\n--- a/x\n+++ b/y\n@@ -1 +1 @@\n-a\n+b\n")
	f.Add("@@@ -1,1 -1,1 +1,1 @@@\n  x\n")
	f.Add("--- a/x\n+++ b/x\n@@ -9999999999999999999,1 +1,1 @@\n-a\n+b\n")
	f.Add("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n")

	f.Fuzz(func(t *testing.T, response string) {
		blocks, rejected := ParseUnifiedDiff(response)

		for _, b := range blocks {
			if strings.TrimSpace(b.FilePath) == "" {
				t.Fatalf("block with an empty FilePath: %+v", b)
			}
			if strings.ContainsAny(b.FilePath, "\x00\n") {
				t.Fatalf("FilePath carries a NUL or newline: %q", b.FilePath)
			}
			// An empty SEARCH is the CREATE instruction (see IsEmptySearch).
			// Reaching it from a diff that never mentioned /dev/null would mean
			// an ordinary hunk had been turned into "this file's whole content
			// is now these lines" -- a silent whole-file overwrite.
			if IsEmptySearch(b.Search) && !strings.Contains(response, devNull) {
				t.Fatalf("empty SEARCH from a diff with no /dev/null side: %+v", b)
			}
			// A block that changes nothing would walk a user through a
			// confirmation prompt for a no-op.
			if b.Search == b.Replace {
				t.Fatalf("block whose SEARCH and REPLACE are identical: %+v", b)
			}
		}
		for _, be := range rejected {
			if be.Reason == "" {
				t.Fatal("a refusal with no reason is a silent refusal")
			}
			if be.Line < 1 {
				t.Fatalf("refusal with a non-positive line number: %+v", be)
			}
		}
	})
}

// FuzzParseEditPayload is the proof that adding a second format changed nothing
// about the first. Whenever the dispatcher reports FormatSearchReplace, its
// results must be exactly what ParseEditBlocks alone would have returned --
// which is what three call sites and the existing corpus depend on.
func FuzzParseEditPayload(f *testing.F) {
	f.Add("")
	f.Add("just prose")
	f.Add("path: a.go\n<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>> REPLACE\n")
	f.Add("path: a.go\n<<<<<<< SEARCH\nfoo\n")
	f.Add("---\n\na markdown rule, not a diff\n")
	f.Add("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")

	f.Fuzz(func(t *testing.T, response string) {
		payload := ParseEditPayload(response)
		blocks, rejected := ParseEditBlocks(response)

		if len(blocks) > 0 || len(rejected) > 0 {
			if payload.Format != FormatSearchReplace {
				t.Fatalf("ParseEditBlocks read %d block(s)/%d rejection(s) but the dispatcher reported %v", len(blocks), len(rejected), payload.Format)
			}
			if !reflect.DeepEqual(payload.Blocks, blocks) {
				t.Fatalf("dispatcher altered the blocks:\n got %+v\nwant %+v", payload.Blocks, blocks)
			}
			if !reflect.DeepEqual(payload.Rejected, rejected) {
				t.Fatalf("dispatcher altered the rejections:\n got %+v\nwant %+v", payload.Rejected, rejected)
			}
			return
		}

		// With no SEARCH/REPLACE content, the only format that may carry blocks
		// is a diff, and FormatNone must never carry anything at all.
		switch payload.Format {
		case FormatNone:
			if len(payload.Blocks) > 0 || len(payload.Rejected) > 0 {
				t.Fatalf("FormatNone carried %d block(s) and %d rejection(s)", len(payload.Blocks), len(payload.Rejected))
			}
		case FormatUnrecognised:
			if payload.Hint.Advice == "" {
				t.Fatal("FormatUnrecognised with no advice is the silence this type exists to prevent")
			}
		}
	})
}
