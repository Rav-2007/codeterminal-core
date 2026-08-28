package main

import (
	"os"
	"path/filepath"
	"sort"
)

// expandNeighbourTopN was the whole policy until 2026-08-28 and is now just its
// TopN half, kept because the tests and the eval's reporting still name it.
// defaultExpandPolicy is the live setting.
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

// expandPolicy is how far, and by what rule, a top hit is widened.
//
// IT IS A STRUCT SO THE EVAL AND PRODUCTION READ THE SAME KNOBS. The sweep in
// deliverysweep_eval_test.go scores a grid of these through this very function;
// a policy expressed as loose parameters at each call site is one the instrument
// can drift away from, which is the standing failure mode in this package.
type expandPolicy struct {
	// TopN is how many of the highest-ranked hits get widened. 0 disables
	// expansion entirely.
	TopN int
	// ConstructCap is the largest construct, in lines, that a hit may be widened
	// to. 0 means never widen to a construct -- fixed neighbours only, which is
	// the pre-2026-08-28 behaviour.
	//
	// A CAP IS NOT OPTIONAL. Constructs here reach 438 lines, and widening to one
	// costs roughly 8,000 of the character budget: the span crowds every other
	// file out of the prompt, which is precisely why "+-2 on all" measured WORSE
	// than no expansion at all.
	ConstructCap int
}

// defaultExpandPolicy is what production uses.
//
// MEASURED 2026-08-28 by TestDeliveryPolicySweep -- one index build, every
// policy scored through these same functions. DELIVERED out of 49, mean rendered
// chars beside it:
//
//	policy                 b=16000        20000          24000          28000
//	+-1 top 3 (before)     32 (15.3k)     35 (18.6k)     35 (20.2k)     35
//	+-1 top 5              32             34             36 (21.8k)     36
//	construct<=60  top 3   33             36             36             36
//	construct<=160 top 5   28             35             35             36
//	construct<=300 top 5   27             36             37 (22.2k)     38 (24.7k)
//	construct<=300 top 10  27             35             39 (22.2k)     39 (25.8k)
//
// THREE THINGS THAT TABLE SAYS, AND NONE OF THEM WAS OBVIOUS.
//
// Construct widening is NOT free at a fixed budget. At 16,000 the large-cap arms
// score 27-28 against a baseline of 32 -- five queries WORSE -- because one
// widened function displaces whole files from the prompt. It is the same
// mechanism that made "+-2 on all" worse than no expansion at all. Widening does
// not need a small cap; it needs ROOM, and given room the large cap wins.
//
// TOP 10, NOT TOP 3, AND THIS REVERSES THE EARLIER FINDING. expandNeighbourTopN
// is 3 because expanding everything crowded the budget -- true, and measured, of
// +-1 widening at 8,000-16,000 chars. Under construct widening at 24,000 it
// inverts: top 10 delivers 39 against top 5's 37 for the SAME 22.2k of context,
// because the budget is what binds, so offering it more candidates costs
// nothing and lets it choose better ones.
//
// 24,000 AND NOT 28,000. The extra 4,000 chars buys nothing at top 10 (39 both
// ways) and one query at top 5, so the cheaper cell wins.
//
// Against this run's own baseline (+-1 top 3 at 16,000 = 32/49) that is +7
// queries: impl 13 -> 20/24, defuse 6 -> 7/7, cross and doc unchanged, multi
// unchanged, test 2 -> 1/3. The one regression is a single query of three.
//
// This is one decision with defaultContextBudgetChars, not two. Reverting either
// constant alone lands in the WORST cell of the table above rather than a middle
// one.
var defaultExpandPolicy = expandPolicy{TopN: 10, ConstructCap: 300}

// resolvedExpandPolicy returns the policy this Server widens hits with,
// falling back to defaultExpandPolicy when none was set.
//
// Every Server that does not name a policy -- the daemon's own, and every test
// written before the field existed -- keeps getting production's behaviour.
// Only a caller that deliberately sets one gets something else.
func (s *Server) resolvedExpandPolicy() expandPolicy {
	if s.expandPolicy == nil {
		return defaultExpandPolicy
	}
	return *s.expandPolicy
}

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
func expandToNeighbours(hits []Chunk, workspaceRoot string, policy expandPolicy) []Chunk {
	if policy.TopN <= 0 || workspaceRoot == "" || len(hits) == 0 {
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
	regions := make(map[string]fileRegions)

	out := make([]Chunk, 0, len(hits)*3)
	for rank, h := range hits {
		out = append(out, h)
		if rank >= policy.TopN {
			continue
		}
		reg, ok := regions[h.FilePath]
		if !ok {
			reg = regionsOnDisk(realRoot, h.FilePath, ignore)
			regions[h.FilePath] = reg
		}
		sibs := reg.chunks
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

		// Prefer the construct. Where one qualifies, every chunk overlapping it
		// is appended and mergeAdjacentChunks folds them into a single span
		// covering the whole declaration.
		widened := false
		for _, ext := range reg.extents {
			if policy.ConstructCap <= 0 || ext[1]-ext[0]+1 > policy.ConstructCap {
				continue
			}
			if ext[0] > h.EndLine || ext[1] < h.StartLine {
				continue // this construct does not touch the hit
			}
			for j := range sibs {
				if sibs[j].StartLine > ext[1] || ext[0] > sibs[j].EndLine {
					continue
				}
				widened = true
				if seen[sibs[j].ID] {
					continue
				}
				seen[sibs[j].ID] = true
				out = append(out, sibs[j])
			}
		}
		if widened {
			continue
		}

		// FALL BACK TO FIXED NEIGHBOURS, always. No construct was found, or the
		// only one enclosing this hit is over the cap -- and an oversized
		// construct is exactly where a hit is most likely to be one window away
		// from its answer. Widening by nothing there would regress hits the
		// pre-construct policy already caught.
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

// fileRegions is one file's chunking and its construct extents, read together
// because expansion needs both and the file should only be read once per query.
type fileRegions struct {
	chunks  []Chunk
	extents [][2]int
}

// regionsOnDisk re-chunks one workspace file and locates its top-level
// constructs, or returns the zero value if the file is not eligible for
// indexing. The eligibility check is shouldSkipFile -- the indexer's own gate --
// so a file the index refuses to read cannot reach a prompt through this path
// either.
func regionsOnDisk(realRoot, relPath string, ignore *gitignoreMatcher) fileRegions {
	absPath := filepath.Join(realRoot, filepath.FromSlash(relPath))
	if _, skip, err := shouldSkipFile(absPath, relPath, ignore); err != nil || skip {
		return fileRegions{}
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fileRegions{}
	}
	chunks := chunkContent(content, relPath)
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].StartLine < chunks[j].StartLine })
	return fileRegions{chunks: chunks, extents: constructExtents(splitLines(content))}
}

// chunksOnDisk re-chunks one workspace file, in source order, or returns nil if
// the file is not eligible for indexing. The eligibility check is
// shouldSkipFile -- the indexer's own gate -- so a file the index refuses to
// read cannot reach a prompt through this path either.
func chunksOnDisk(realRoot, relPath string, ignore *gitignoreMatcher) []Chunk {
	return regionsOnDisk(realRoot, relPath, ignore).chunks
}
