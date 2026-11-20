package main

import (
	"path/filepath"
	"slices"
	"sort"
)

// GAP FILL: two delivered spans of one file, close together, are joined.
//
// A LEVER UNDER MEASUREMENT, OFF BY DEFAULT (expandPolicy.GapFill = 0). It was
// pre-registered in docs/RETRIEVAL_EVAL_TREND.md on 2026-11-20, before it was
// measured, and ships only if TestExternalLeverSweep's GAP arms meet the rule
// written there.
//
// WHY IT MIGHT HELP. When retrieval puts several chunks of one file in the top
// ten, the file is the answer's neighbourhood, but the chunk that answers can
// sit between two delivered spans and be the one piece the model never sees.
// Locate question 40 ("how does the daemon count tokens it has spent in a
// turn") is that case since 2026-10-26: daemon/usage.go's lines 1-40 and
// 121-280 are delivered, and usageTally.add, the answer, is in between. A
// short gap costs little budget and reads as one contiguous passage.
//
// WHERE IT SITS. Last in two-pass delivery (packHitsFirst), after every hit is
// placed and widened, and only in the room left: a gap never displaces a hit
// or its widening. One step per gap, nearest-ranked file first, each accepted
// only if the merged result still fits the budget and the span count.

// gapFiller returns the gap-fill step for spans placed so far: for each pair of
// adjacent spans of one file at most maxGap lines apart, the chunks on disk
// that cover the lines between them. It returns nothing when maxGap is 0.
func gapFiller(root string, maxGap int) func(placed []Chunk) [][]Chunk {
	return func(placed []Chunk) [][]Chunk {
		if maxGap <= 0 || root == "" || len(placed) < 2 {
			return nil
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil
		}
		ignore := newGitignoreMatcher(realRoot)

		byFile := map[string][]Chunk{}
		var order []string
		for _, sp := range mergeAdjacentChunks(slices.Clone(placed)) {
			if _, ok := byFile[sp.FilePath]; !ok {
				order = append(order, sp.FilePath)
			}
			byFile[sp.FilePath] = append(byFile[sp.FilePath], sp)
		}
		var out [][]Chunk
		for _, file := range order {
			spans := byFile[file]
			if len(spans) < 2 {
				continue
			}
			sort.Slice(spans, func(i, j int) bool { return spans[i].StartLine < spans[j].StartLine })
			var disk []Chunk
			for i := 1; i < len(spans); i++ {
				from, to := spans[i-1].EndLine+1, spans[i].StartLine-1
				if to < from || to-from+1 > maxGap {
					continue
				}
				if disk == nil {
					disk = chunksOnDisk(realRoot, file, ignore)
				}
				var g []Chunk
				for _, c := range disk {
					if c.EndLine >= from && c.StartLine <= to {
						g = append(g, c)
					}
				}
				if len(g) > 0 {
					out = append(out, g)
				}
			}
		}
		return out
	}
}
