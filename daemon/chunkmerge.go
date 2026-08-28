package main

import (
	"fmt"
	"sort"
	"strings"
)

// mergeAdjacentChunks folds retrieved chunks that come from the SAME file and
// whose line ranges touch or overlap into single contiguous spans, before the
// set is budgeted and rendered into the prompt (see gatherContext /
// buildAugmentedUserMessage in context.go).
//
// Why this exists. The indexer produces overlapping windows on purpose
// (chunkLines=40, overlapLines=10, chunker.go) so a construct straddling a
// window boundary is fully present in at least one of them. That overlap is
// correct at INDEX time and pure waste at PROMPT time: when retrieval returns
// two consecutive windows of one file — the measured case was
// daemon/server.go:661-700 alongside 691-712 — the rendered context repeats
// their ten shared lines verbatim and hands the model two fragments with a
// duplicated seam instead of one whole span. Merging spends the seam once and
// gives the model contiguous code.
//
// What happens to the reclaimed bytes, explicitly: nothing is refetched and k
// is unchanged. Merging runs BEFORE truncateToBudget, so the effect is (a) a
// tighter prompt for the same information in every case, and (b) when the
// budget was actually binding, lower-ranked chunks that used to be dropped now
// fit. It never enlarges the retrieved set on its own.
//
// Ordering is preserved by rank, not by line number: a merged span takes the
// position of its BEST-ranked constituent, so truncateToBudget's "never
// sacrifice the top hit" property still holds. Its Score/RawScore are the best
// (max) of the constituents' for the same reason — a span containing the best
// hit is at least as good as that hit.
//
// It deliberately does not merge:
//   - different files (obviously), or
//   - same-file chunks that are genuinely far apart — only ranges that touch
//     (next.StartLine == cur.EndLine+1) or overlap are joined, so two distinct
//     regions of one file stay two labeled spans rather than becoming one span
//     whose stated range lies about the code in between, or
//   - anything whose content and declared line range disagree, or whose
//     overlapping lines don't actually match (see spliceChunk) — those keep
//     their original separate form rather than being spliced on a guess.
func mergeAdjacentChunks(chunks []Chunk) []Chunk {
	if len(chunks) < 2 {
		return chunks
	}

	// run collects one group of chunks being merged: their positions in the
	// incoming ranked order (rank is the best/lowest of them, which is where
	// the merged span will be emitted) and the accumulating span itself.
	type run struct {
		rank  int
		chunk Chunk
	}

	byFile := make(map[string][]int, len(chunks))
	order := make([]string, 0, len(chunks))
	for i, c := range chunks {
		if _, seen := byFile[c.FilePath]; !seen {
			order = append(order, c.FilePath)
		}
		byFile[c.FilePath] = append(byFile[c.FilePath], i)
	}

	runs := make([]run, 0, len(chunks))
	for _, file := range order {
		idxs := byFile[file]
		// Line order within the file, so a sweep only ever has to consider the
		// chunk immediately after the current span. Ties (same start line) put
		// the longer span first so a contained duplicate is absorbed, not
		// treated as the anchor.
		sort.SliceStable(idxs, func(a, b int) bool {
			ca, cb := chunks[idxs[a]], chunks[idxs[b]]
			if ca.StartLine != cb.StartLine {
				return ca.StartLine < cb.StartLine
			}
			return ca.EndLine > cb.EndLine
		})

		cur := run{rank: idxs[0], chunk: chunks[idxs[0]]}
		for _, i := range idxs[1:] {
			next := chunks[i]
			merged, ok := spliceChunk(cur.chunk, next)
			if !ok {
				runs = append(runs, cur)
				cur = run{rank: i, chunk: next}
				continue
			}
			cur.chunk = merged
			if i < cur.rank {
				cur.rank = i
			}
		}
		runs = append(runs, cur)
	}

	// Back to ranked order: best-ranked span first, exactly as retrieval
	// returned it.
	sort.SliceStable(runs, func(a, b int) bool { return runs[a].rank < runs[b].rank })

	out := make([]Chunk, len(runs))
	for i, r := range runs {
		out[i] = r.chunk
	}
	return out
}

// spliceChunk joins next onto cur when the two are the same file and their
// line ranges touch or overlap, returning the contiguous span. ok is false
// whenever the join would be a guess rather than a fact, in which case the
// caller keeps both chunks as they were.
//
// It requires cur.StartLine <= next.StartLine (the caller sorts by start
// line) and refuses in four cases:
//
//   - different files;
//   - a gap between them (next.StartLine > cur.EndLine+1) — the "far apart"
//     case, where merging would produce a span whose stated range covers lines
//     that are not in its content;
//   - a chunk whose content line count contradicts its declared range, which
//     means the two can't be spliced by line arithmetic at all (a stale or
//     hand-built chunk); and
//   - overlapping regions whose text differs, which can only happen if the two
//     chunks were indexed from different versions of the file. Splicing those
//     would silently invent a version of the file that never existed.
//
// The returned span's StartLine/EndLine/ID describe exactly the lines its
// Content holds — the line-number attribution the model is shown stays true.
func spliceChunk(cur, next Chunk) (Chunk, bool) {
	if cur.FilePath != next.FilePath {
		return Chunk{}, false
	}
	if next.StartLine < cur.StartLine {
		// Precondition violated (the caller sorts by start line); refuse rather
		// than splice backwards.
		return Chunk{}, false
	}
	if next.StartLine > cur.EndLine+1 {
		return Chunk{}, false
	}

	curLines, ok := chunkLinesOf(cur)
	if !ok {
		return Chunk{}, false
	}
	nextLines, ok := chunkLinesOf(next)
	if !ok {
		return Chunk{}, false
	}

	// Lines the two ranges share, compared where they actually sit in cur —
	// NOT at cur's tail, which is only the same thing when next extends past
	// cur. A next fully CONTAINED in cur (which Fix 12's fusion produces
	// routinely: a directly-resolved span inside a bigger similarity chunk)
	// shares an interior slice, and comparing cur's tail against it would
	// spuriously report a mismatch and refuse a perfectly valid absorb.
	offset := next.StartLine - cur.StartLine
	sharedEnd := next.EndLine
	if cur.EndLine < sharedEnd {
		sharedEnd = cur.EndLine
	}
	overlap := sharedEnd - next.StartLine + 1
	if overlap < 0 {
		overlap = 0 // ranges merely touch
	}
	if overlap > 0 {
		if offset+overlap > len(curLines) || overlap > len(nextLines) {
			return Chunk{}, false
		}
		if !equalLines(curLines[offset:offset+overlap], nextLines[:overlap]) {
			return Chunk{}, false
		}
	}

	merged := cur
	merged.Content = strings.Join(append(curLines, nextLines[overlap:]...), "\n")
	if next.EndLine > merged.EndLine {
		merged.EndLine = next.EndLine
	}
	merged.ID = fmt.Sprintf("%s:%d-%d", merged.FilePath, merged.StartLine, merged.EndLine)
	// The span inherits the better of the two scores: it contains everything
	// the better-scoring chunk contained.
	if next.Score > merged.Score {
		merged.Score = next.Score
	}
	if next.RawScore > merged.RawScore {
		merged.RawScore = next.RawScore
	}
	if merged.Class == "" {
		merged.Class = next.Class
	}
	// A merged span is no longer a stored document; nothing downstream reads
	// Vector off a retrieved chunk, and a stale one would be a lie about the
	// new content.
	//
	// EmbedText goes for exactly the same reason, and it is the newer half of
	// this pair. `merged := cur` above copies cur's embedded text onto a span
	// whose Content is now two chunks joined, so it describes a chunk that no
	// longer exists. Nothing re-embeds a merged span today -- embedTextOf is
	// only reached from the index paths -- so this is latent rather than live,
	// which is precisely the state Vector was in when it was given this comment.
	merged.Vector = nil
	merged.EmbedText = ""
	return merged, true
}

// chunkLinesOf returns c's content split into lines, and whether that line
// count actually matches the range c claims to cover. Every chunk the indexer
// produces satisfies this by construction (chunkContent joins exactly
// EndLine-StartLine+1 lines); one that doesn't is stale or hand-built, and is
// not safe to splice by line arithmetic.
func chunkLinesOf(c Chunk) ([]string, bool) {
	if c.StartLine < 1 || c.EndLine < c.StartLine {
		return nil, false
	}
	lines := strings.Split(c.Content, "\n")
	if len(lines) != c.EndLine-c.StartLine+1 {
		return nil, false
	}
	return lines, true
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mergeSavings reports how many rendered bytes merging reclaimed, for the
// per-request retrieval log line — the measurement, not an estimate: both
// sides are sized through renderChunk, the same function that produces the
// bytes actually sent.
func mergeSavings(before, after []Chunk, scrubDisabled bool) int {
	return renderedSize(before, scrubDisabled) - renderedSize(after, scrubDisabled)
}

func renderedSize(chunks []Chunk, scrubDisabled bool) int {
	total := 0
	for i, c := range chunks {
		total += len(renderChunk(i+1, c, scrubDisabled))
	}
	return total
}
