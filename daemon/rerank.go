package main

import (
	"regexp"
	"sort"
)

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
	// testClassWeight down-weights _test.go chunks relative to the
	// codeClassWeight boost they'd otherwise get, so a test file's often-
	// prose-like test names/section comments (which echo natural-language
	// query vocabulary more closely than terse implementation) don't
	// routinely bury the implementation for an implementation-seeking
	// query. Only applied when looksTestSeeking(query) is false — see
	// rerankChunks. Measured against the real repo (rerank_eval_test.go):
	// a static, query-blind version of this weight was proven (by algebra
	// on live scores) to be irreconcilable with also finding tests for a
	// genuinely test-seeking query, which is why this is intent-gated
	// rather than a flat constant.
	testClassWeight float32 = 0.85
)

// testSeekingWords matches whole-word mentions of testing vocabulary in a
// query. Word-boundaried deliberately — a bare "\btest\b" substring check
// would still be fine, but this also catches the common inflections
// ("tested", "testing") and "spec"/"specs" without matching unrelated words
// that merely contain "test" as a substring (e.g. "latest", "contest").
var testSeekingWords = regexp.MustCompile(`(?i)\b(tests?|tested|testing|specs?)\b`)

// testFuncPattern matches a Go test-function identifier mentioned in the
// query (e.g. "what does TestHandlerAcceptsValidInput check?") — a strong,
// unambiguous signal the user is asking about a test, independent of
// testSeekingWords.
var testFuncPattern = regexp.MustCompile(`\bTest[A-Z]\w*`)

// looksTestSeeking reports whether query appears to be asking about tests
// themselves (as opposed to asking a question about implementation that
// merely happens to retrieve test chunks). When true, rerankChunks skips
// the testClassWeight down-weight entirely, so a genuinely test-seeking
// query is never penalized for finding test files.
func looksTestSeeking(query string) bool {
	return testSeekingWords.MatchString(query) || testFuncPattern.MatchString(query)
}

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
// neutral weight for an empty/unrecognized class. FileClassTest gets the
// same boost as FileClassCode here — the down-weight is applied
// conditionally in rerankChunks (only when the query isn't itself
// test-seeking), not baked into this class-only mapping.
func classWeight(class FileClass) float32 {
	switch class {
	case FileClassCode, FileClassTest:
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
//
// query is the original user question, used only to gate the test-file
// down-weight (looksTestSeeking): a _test.go chunk is down-weighted for an
// implementation-seeking query, but competes at full codeClassWeight when
// the query itself looks like it's asking about tests.
func rerankChunks(candidates []Chunk, k int, query string) []Chunk {
	weighted := make([]Chunk, len(candidates))
	copy(weighted, candidates)

	testSeeking := looksTestSeeking(query)
	for i := range weighted {
		raw := weighted[i].Score
		weighted[i].RawScore = raw
		weighted[i].Class = effectiveClass(weighted[i])

		w := classWeight(weighted[i].Class)
		if weighted[i].Class == FileClassTest && !testSeeking {
			w = testClassWeight
		}
		weighted[i].Score = raw * w
	}

	sort.SliceStable(weighted, func(i, j int) bool {
		return weighted[i].Score > weighted[j].Score
	})

	if len(weighted) > k {
		weighted = weighted[:k]
	}
	return weighted
}
