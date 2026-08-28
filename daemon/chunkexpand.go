package main

import (
	"os"
	"path/filepath"
	"sort"
)

// expandNeighbourTopN is how many of the highest-ranked retrieved chunks get
// widened into the region around them. 0 disables expansion entirely.
//
// WHY EXPANSION EXISTS. Chunks are fixed 40-line windows on a 30-line stride,
// and retrieval is routinely off by one window: it returns chunk i of the right
// file while the answer sits in chunk i-1. Measured 2026-08-28 over the 49-query
// locate eval, of 19 misses where the right FILE was retrieved but the wrong
// chunk, **10 had a retrieved chunk within one position of the answer**. The
// ranker was not wrong about the file or even the region -- it was wrong about
// which forty lines.
//
// Merging is not this. mergeAdjacentChunks folds chunks that are BOTH retrieved;
// here only one of the pair is, so there is nothing for it to fold.
//
// WHY 3 AND NOT ALL. Expanding everything crowds the character budget and
// collapses the span count, dropping whole files to widen ones that were
// already wrong. MEASURED through the full production path at k=10, delivered
// recall out of 49 with the mean rendered size beside it:
//
//	policy            budget=16000     budget=20000     budget=24000
//	none (before)     25 (14,620ch)    26               26
//	+-1 on all        26 (13,174ch)    31               31
//	+-1 on top 5      28 (14,277ch)    30               30
//	+-1 on top 3      30 (14,924ch)    31               32
//	+-2 on all        21 (12,845ch)    24               30
//
// Top 3 at the existing 16,000-char budget is +5 queries (51.0% -> 61.2%,
// +10.2pp) for +2% context. Widening the budget to 24,000 buys two more
// queries for 36% more context, which is a far worse trade and was not taken.
// Expanding by two neighbours is actively worse than not expanding at all.
//
// BOTH SIDES, NOT JUST FORWARD. Of the off-by-one answers, 9 lay BEFORE the
// retrieved chunk and 4 after -- retrieval tends to match slightly below the
// code that answers the question. A forward-only policy is therefore the wrong
// half, and measured worse (27 vs 31 at budget 20,000).
const expandNeighbourTopN = 3

// expandToNeighbours widens the top-ranked retrieved chunks into the region
// around them by appending their file-adjacent siblings, which
// mergeAdjacentChunks then folds into one contiguous span (the siblings share
// the indexer's 10-line window overlap, so they always merge). The result is
// FEWER, WIDER regions rather than more of them.
//
// Siblings come from re-chunking the file on disk with chunkContent -- the same
// function the indexer used -- rather than from the store. That keeps the
// eligibility gate identical to the indexer's (shouldSkipFile, via the same
// gitignore matcher the direct-reference path builds) instead of introducing a
// second, weaker one, and it needs no new store method. A file edited since the
// last index yields current text, which is what the direct-reference path in
// fileref.go already does deliberately.
//
// Ranking order is preserved: expansion appends, and truncateToBudget still
// drops from the tail, so a widened top hit never costs the budget a
// higher-ranked span.
func expandToNeighbours(hits []Chunk, workspaceRoot string, topN int) []Chunk {
	if topN <= 0 || workspaceRoot == "" || len(hits) == 0 {
		return hits
	}
	realRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return hits
	}
	ignore := newGitignoreMatcher(realRoot)

	seen := make(map[string]bool, len(hits)*3)
	for _, h := range hits {
		seen[h.ID] = true
	}
	siblings := make(map[string][]Chunk)

	out := make([]Chunk, 0, len(hits)*3)
	for rank, h := range hits {
		out = append(out, h)
		if rank >= topN {
			continue
		}
		sibs, ok := siblings[h.FilePath]
		if !ok {
			sibs = chunksOnDisk(realRoot, h.FilePath, ignore)
			siblings[h.FilePath] = sibs
		}
		i := -1
		for j := range sibs {
			if sibs[j].ID == h.ID {
				i = j
				break
			}
		}
		if i < 0 {
			// The file changed since indexing, so this chunk's line range is no
			// longer one the chunker produces. Widening around a position that
			// no longer exists would be guessing; leave the hit as it is.
			continue
		}
		for _, j := range []int{i - 1, i + 1} {
			if j < 0 || j >= len(sibs) || seen[sibs[j].ID] {
				continue
			}
			seen[sibs[j].ID] = true
			out = append(out, sibs[j])
		}
	}
	return out
}

// chunksOnDisk re-chunks one workspace file, in source order, or returns nil if
// the file is not eligible for indexing. The eligibility check is
// shouldSkipFile -- the indexer's own gate -- so a file the index refuses to
// read cannot reach a prompt through this path either.
func chunksOnDisk(realRoot, relPath string, ignore *gitignoreMatcher) []Chunk {
	absPath := filepath.Join(realRoot, filepath.FromSlash(relPath))
	if _, skip, err := shouldSkipFile(absPath, relPath, ignore); err != nil || skip {
		return nil
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	chunks := chunkContent(content, relPath)
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].StartLine < chunks[j].StartLine })
	return chunks
}
