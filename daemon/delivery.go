package main

import (
	"fmt"
	"slices"
)

// delivery is what one request's retrieval hands the model, and the numbers
// the log line reports about it.
type delivery struct {
	Chunks []Chunk
	// Truncated reports that a retrieved hit or a direct span did not fit. A
	// widening that did not fit is not counted: what it would have added is
	// context around a hit that was delivered, not a result that was lost.
	Truncated   bool
	InputCount  int
	SavedBytes  int
	DirectSpans int
}

// deliverWithinBudget is the whole delivery tail: the direct spans, the
// retrieved hits, and the regions widening adds around them, merged and fitted
// to the character budget.
//
// ONE FUNCTION, THREE CALLERS -- gatherContext, gatherDirectRefs and the locate
// eval -- and the reason is the one this package keeps relearning: an eval that
// re-implements what production does eventually measures something else. The
// delivery sweep scores candidate policies through this same function.
//
// Without policy.TwoPass this is exactly the composition it replaced: widen
// every top hit in place (expandToNeighbours), fuse and merge
// (fuseDirectSpans), keep spans in rank order while they fit
// (truncateToBudget). With it, see packHitsFirst.
func deliverWithinBudget(direct, hits []Chunk, root string, policy expandPolicy, k, budget int, scrubDisabled bool) delivery {
	if !policy.TwoPass {
		fused := fuseDirectSpans(direct, expandToNeighbours(hits, root, policy), k, scrubDisabled)
		kept, truncated := truncateToBudget(fused.Chunks, budget, scrubDisabled)
		return delivery{
			Chunks:      kept,
			Truncated:   truncated,
			InputCount:  fused.InputCount,
			SavedBytes:  fused.SavedBytes,
			DirectSpans: fused.DirectSpans,
		}
	}
	return packHitsFirst(direct, hits, widenHits(hits, root, policy), policy.WidenFirst, k, budget, scrubDisabled)
}

// packHitsFirst spends the budget in a different order from the one-pass
// path: direct spans, then every retrieved hit's own chunk, and only then the
// regions around them, in rank order. The first widenFirst hits are still
// widened as they are placed, so a policy can keep its best hits wide and still
// guarantee the rest a place.
//
// THE OUTPUT IS STILL IN RANK ORDER. A widening always merges into the span of
// the hit it surrounds -- its chunks overlap or touch that hit -- and a merged
// span sits at the rank of its best member, so placing widenings last changes
// what is spent, never the order the model reads.
//
// Each step is accepted only if the merged result still fits; one that does
// not is skipped and the next is tried, as truncateToBudget skips rather than
// stops. The first thing placed is always kept, as there.
func packHitsFirst(direct, hits []Chunk, wid [][]Chunk, widenFirst, k, budget int, scrubDisabled bool) delivery {
	type step struct {
		chunks []Chunk
		direct bool
		places int // the hit this step places, or -1
		widens int // the hit this step widens, or -1
	}
	var steps []step
	for _, d := range directSpansFor(direct, k) {
		steps = append(steps, step{chunks: []Chunk{d}, direct: true, places: -1, widens: -1})
	}
	widening := func(i int) []Chunk {
		if i < len(wid) {
			return wid[i]
		}
		return nil
	}
	for i, h := range hits {
		steps = append(steps, step{chunks: []Chunk{h}, places: i, widens: -1})
		if w := widening(i); i < widenFirst && len(w) > 0 {
			steps = append(steps, step{chunks: w, places: -1, widens: i})
		}
	}
	for i := max(widenFirst, 0); i < len(hits); i++ {
		if w := widening(i); len(w) > 0 {
			steps = append(steps, step{chunks: w, places: -1, widens: i})
		}
	}

	// Rendered sizes are cached by span, because every step re-measures the
	// whole merged set and renderChunk scrubs the text it sizes. A span's text
	// is fixed by its file and range, and the "[n] " label is the only part
	// that depends on its position.
	//
	// The merge here is fuseDirectSpans' without its savings report, which
	// renders every chunk again: the direct spans are already folded and
	// capped, so merging them once more changes nothing, and the result is the
	// same spans fuseDirectSpans returns below.
	sizes := map[string]int{}
	fits := func(d, s []Chunk) bool {
		merged := mergeAdjacentChunks(append(slices.Clone(d), s...))
		if k > 0 && len(merged) > k {
			merged = merged[:k]
		}
		total := 0
		for i, c := range merged {
			key := fmt.Sprintf("%s:%d-%d:%d", c.FilePath, c.StartLine, c.EndLine, len(c.Content))
			n, ok := sizes[key]
			if !ok {
				n = len(renderChunk(1, c, scrubDisabled))
				sizes[key] = n
			}
			total += n + len(fmt.Sprint(i+1)) - 1
			if total > budget {
				return false
			}
		}
		return true
	}

	var selDirect, selSimilar []Chunk
	placed := make(map[int]bool, len(hits))
	truncated := false
	for _, st := range steps {
		if st.widens >= 0 && !placed[st.widens] {
			continue // never widen a hit that is not there
		}
		nd, ns := selDirect, selSimilar
		if st.direct {
			nd = append(slices.Clone(selDirect), st.chunks...)
		} else {
			ns = append(slices.Clone(selSimilar), st.chunks...)
		}
		if len(selDirect)+len(selSimilar) > 0 && !fits(nd, ns) {
			if st.widens < 0 {
				truncated = true
			}
			continue
		}
		selDirect, selSimilar = nd, ns
		if st.places >= 0 {
			placed[st.places] = true
		}
	}

	fused := fuseDirectSpans(selDirect, selSimilar, k, scrubDisabled)
	return delivery{
		Chunks:      fused.Chunks,
		Truncated:   truncated,
		InputCount:  fused.InputCount,
		SavedBytes:  fused.SavedBytes,
		DirectSpans: fused.DirectSpans,
	}
}
