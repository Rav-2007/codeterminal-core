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
