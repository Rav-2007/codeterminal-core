package main

import (
	"strings"
	"testing"
)

// scanRepoChunks returns the chunks the indexer would REALLY produce for this
// repository.
//
// IT CALLS ScanWorkspace RATHER THAN WALKING, and the difference is not
// cosmetic. A hand-rolled walk that only prunes isPrunedDir sees 122,386 chunks
// here; the indexer's own gate -- gitignore layers, the secret-name refusal, the
// size cap, the binary sniff -- sees a tenth of that. Measured while writing
// these tests, the hand-rolled version put the prefix rate at 88.9% of a corpus
// that is not indexed, against the ~4,600 chunks that really are. An invariant
// graded on files the product never embeds is an invariant about nothing.
func scanRepoChunks(t *testing.T) []Chunk {
	t.Helper()
	scan, err := ScanWorkspace("..")
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Chunks) < 2000 {
		t.Fatalf("only %d chunks; these invariants are not being exercised against the real corpus", len(scan.Chunks))
	}
	return scan.Chunks
}

// THE PREFIX MAY ONLY EVER PREFIX. This is the invariant the whole design rests
// on, and it is worth an assertion rather than a comment because everything
// downstream assumes it silently.
//
// Content is what is stored, merged, budgeted, scrubbed and shown to the model,
// and chunkLinesOf (chunkmerge.go) refuses to splice any chunk whose content
// line count contradicts its declared range. A prefix written INTO Content would
// therefore not fail loudly -- it would make mergeAdjacentChunks and
// expandToNeighbours quietly refuse every chunk, and retrieval would get worse
// for a reason no test names.
func TestTheEmbeddedTextOnlyEverPrefixesTheChunk(t *testing.T) {
	for _, c := range scanRepoChunks(t) {
		if c.EmbedText == "" {
			t.Fatalf("chunk %s has empty EmbedText; embedTextOf would fall back and embed Content, silently", c.ID)
		}
		if !strings.HasSuffix(c.EmbedText, c.Content) {
			t.Fatalf("chunk %s: embedded text does not END with its Content, so the prefix modified the "+
				"chunk rather than preceding it.\nprefix region: %q", c.ID, truncateForTest(c.EmbedText))
		}
	}
}

// THE PREFIX IS THE FILE PATH, EXACTLY, AND EVERY CHUNK CARRIES IT.
//
// Both halves are load-bearing and both were measured. Six arms that prefixed
// program structure instead of the path scored 27-31 against a 30/49 baseline;
// the path alone scores 33/49. And a prefix only SOME chunks carry is what those
// arms did -- the asymmetry is part of why they lost -- so "every chunk" is a
// property to pin, not an implementation detail.
func TestEveryChunkIsPrefixedWithItsOwnPathAndNothingElse(t *testing.T) {
	chunks := scanRepoChunks(t)
	var prefixed int
	for _, c := range chunks {
		prefix := strings.TrimSuffix(c.EmbedText, c.Content)
		if activeEmbedPrefix == prefixNone {
			if prefix != "" {
				t.Fatalf("policy is %q but chunk %s carries prefix %q", activeEmbedPrefix, c.ID, prefix)
			}
			continue
		}
		if prefix != c.FilePath+"\n" {
			t.Fatalf("chunk %s carries prefix %q, want its own path %q. A prefix that is not the "+
				"chunk's path is either a stale policy or a path resolved against the wrong root.",
				c.ID, truncateForTest(prefix), c.FilePath+"\n")
		}
		prefixed++
	}
	if activeEmbedPrefix != prefixNone && prefixed != len(chunks) {
		t.Fatalf("only %d of %d chunks carry a prefix; under %q every chunk must",
			prefixed, len(chunks), activeEmbedPrefix)
	}
	t.Logf("policy %q: %d/%d chunks prefixed", activeEmbedPrefix, prefixed, len(chunks))
}

// BOUNDARIES DID NOT MOVE, and that is the whole claim distinguishing this
// change from the six reverted attempts at structure-aware chunking. Line ranges
// are pure arithmetic on the file's length here, so any drift means the
// representation layer reached into the cut.
func TestTheEmbedPrefixMovedNoBoundary(t *testing.T) {
	stride := chunkLines - overlapLines
	for _, c := range scanRepoChunks(t) {
		if (c.StartLine-1)%stride != 0 {
			t.Fatalf("chunk %s starts at line %d, which is not on the %d-line stride: a boundary moved",
				c.ID, c.StartLine, stride)
		}
		if got := c.EndLine - c.StartLine + 1; got > chunkLines {
			t.Fatalf("chunk %s covers %d lines, past the %d-line window: a boundary moved", c.ID, got, chunkLines)
		}
	}
}
