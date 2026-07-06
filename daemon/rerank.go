package main

import "sort"

// Class weights used to tilt ranking toward code for code questions, without
// hard-banning any class — a doc chunk with high enough raw similarity can
// still win (weighted = raw * weight). Tune these; they're deliberately
// named constants rather than inline literals.
const (
	// codeClassWeight boosts code chunks above comparable doc/config hits —
	// the fix for the measured bug (prose about code outranking the code).
	codeClassWeight float32 = 1.15
	// otherClassWeight is the neutral baseline for unclassified files
	// (e.g. LICENSE) — neither boosted nor penalized.
	otherClassWeight float32 = 1.00
	// configClassWeight mildly down-weights config: rarely the answer to
	// "how does X work", but not prose either.
	configClassWeight float32 = 0.90
	// docClassWeight down-weights prose most strongly — this is the tilt
	// that keeps README/system.txt from beating the code they describe,
	// while still letting a doc win outright if its similarity is high
	// enough (0.75 is a tilt, not a ban).
	docClassWeight float32 = 0.75
)

// rerankOverfetchFactor and rerankOverfetchFloor size the raw candidate pool
// fetched from the vector store BEFORE reweighting. This matters: reweighting
// only the raw top-k could never recover a chunk ranked just outside it (the
// measured failure had editblock.go missing from the raw top 5 entirely) —
// fetching a wider net first gives a good code chunk ranked, say, #8 by raw
// similarity a chance to win after its class boost is applied.
const (
	rerankOverfetchFactor = 6  // fetch this many times k from the raw store...
	rerankOverfetchFloor  = 20 // ...but never fewer than this many candidates
)

// rerankPoolSize returns how many raw candidates to fetch from the vector
// store before reweighting down to k.
func rerankPoolSize(k int) int {
	pool := k * rerankOverfetchFactor
	if pool < rerankOverfetchFloor {
		pool = rerankOverfetchFloor
	}
	return pool
}

// classWeight returns the ranking weight for class, defaulting to the
// neutral weight for an empty/unrecognized class.
func classWeight(class FileClass) float32 {
	switch class {
	case FileClassCode:
		return codeClassWeight
	case FileClassDoc:
		return docClassWeight
	case FileClassConfig:
		return configClassWeight
	default:
		return otherClassWeight
	}
}

// effectiveClass returns c.Class if set, otherwise recomputes it from
// c.FilePath. Classification is a pure function of the path, so this is a
// safe fallback for chunks that never went through the indexer with class
// metadata (e.g. hand-built Chunks in tests, or a chunk read back before a
// stale index was caught) rather than silently treating them as
// unclassified-and-neutral by accident.
func effectiveClass(c Chunk) FileClass {
	if c.Class != "" {
		return c.Class
	}
	return classifyFile(c.FilePath)
}

// rerankChunks reweights candidates by combining each chunk's raw similarity
// score with its file-class weight, re-sorts by that weighted score
// descending, and truncates to k. RawScore is set to the original similarity
// on every returned chunk (for logging/observability); Score becomes the
// effective weighted score that determined the final order.
func rerankChunks(candidates []Chunk, k int) []Chunk {
	weighted := make([]Chunk, len(candidates))
	copy(weighted, candidates)

	for i := range weighted {
		raw := weighted[i].Score
		weighted[i].RawScore = raw
		weighted[i].Class = effectiveClass(weighted[i])
		weighted[i].Score = raw * classWeight(weighted[i].Class)
	}

	sort.SliceStable(weighted, func(i, j int) bool {
		return weighted[i].Score > weighted[j].Score
	})

	if len(weighted) > k {
		weighted = weighted[:k]
	}
	return weighted
}
