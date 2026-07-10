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

// lexicalOverfetchFactor and lexicalOverfetchFloor size the lexical
// candidate pool the same way rerankOverfetchFactor/Floor size the semantic
// one: FTS5 is cheap enough (milliseconds even over a generous LIMIT) that
// overfetching costs nothing, and fuseRRF benefits from a wider net before
// rerankChunks's class-weight tilt gets the final say.
const (
	lexicalOverfetchFactor = 6
	lexicalOverfetchFloor  = 20
)

// lexicalPoolSize returns how many raw candidates to fetch from the lexical
// store before fusing with the semantic pool.
func lexicalPoolSize(k int) int {
	pool := k * lexicalOverfetchFactor
	if pool < lexicalOverfetchFloor {
		pool = lexicalOverfetchFloor
	}
	return pool
}

// rrfK dampens how much rank #1 dominates rank #2 in each tier's
// contribution to fuseRRF -- a smoothing constant, not a correctness knob.
// Grid-searched over {10, 60, 100} x lexical-pool-sizes {20, 30, 50} against
// this repo's real-repo eval harness (the 9-query set in
// rerank_eval_test.go): under the MAX-based fusion fuseRRF actually uses
// (see its doc comment for why SUM was rejected), chunk-level hit rate was
// IDENTICAL (8/9) at every single point in that 3x3 grid -- once fusion is
// max-based rather than additive, the result is insensitive to k in the
// tested range. With no combination measurably better than another, 60 (the
// conventional RRF default from Cormack et al.) is kept rather than picked
// arbitrarily from an otherwise-flat grid.
const rrfK = 60.0

// fuseRRF merges semantic and lexical candidate lists by taking, per chunk,
// the BEST reciprocal-rank score it earns from either tier --
// fused_score(chunk) = max(1/(k+rank_semantic), 1/(k+rank_lexical)) -- not
// the textbook summed form (1/(k+rank_semantic) + 1/(k+rank_lexical)). This
// is a deliberate, measured departure, not an arbitrary variant:
//
// Summed RRF rewards cross-modal CONSENSUS — a chunk ranked moderately by
// BOTH tiers out-scores one ranked excellently by only one. That is exactly
// backwards for the asymmetric case this feature exists to fix: the
// measured failure (see rerank_eval_test.go query 8, "where is the ZDR
// refusal string matched") has the correct chunk (provider.go:91-130)
// findable ONLY lexically (terse, identifier-heavy code with no semantic
// echo of the natural-language query) while provider_test.go's chunks —
// whose doc comments and assertion messages restate "ZDR", "refusal", and
// "matched" in prose that reads as a near-paraphrase of the query — rank
// respectably on BOTH tiers simultaneously and, under summed RRF, edge out
// the real answer even after class-weighting. Switching to max fixed this
// (and the other motivating query, "what files does SearchRequest touch")
// across every point in the rrfK/pool-size grid, with no regressions on the
// 7 already-passing queries; summed RRF only fixed a minority of grid points
// and regressed one existing query at the current default pool size. Max
// fusion still requires no score-scale calibration between BM25 and cosine
// similarity (still rank-based, still one constant) — it just changes how
// "found by tier A but not B" is credited, which is the actual shape of the
// bug being fixed.
//
// A chunk absent from one list simply never gets a candidate score from it
// rather than being penalized for it. The returned chunks have RawScore and
// Score both set to the fused score. rerankChunks (the caller, always run
// immediately after this) reads that as its input "raw" similarity and
// applies class weighting on top, exactly as it already does for a plain
// semantic candidate list — fusion only changes what pool and base score
// rerankChunks sees, not what it does with them.
func fuseRRF(semantic, lexical []Chunk, k float64) []Chunk {
	type entry struct {
		chunk Chunk
		score float32
	}
	byID := make(map[string]*entry, len(semantic)+len(lexical))
	order := make([]string, 0, len(semantic)+len(lexical))

	consider := func(list []Chunk) {
		for rank, c := range list {
			s := float32(1.0 / (k + float64(rank+1)))
			e, ok := byID[c.ID]
			if !ok {
				e = &entry{chunk: c, score: s}
				byID[c.ID] = e
				order = append(order, c.ID)
			} else if s > e.score {
				e.score = s
			}
		}
	}
	consider(semantic)
	consider(lexical)

	fused := make([]Chunk, 0, len(order))
	for _, id := range order {
		e := byID[id]
		e.chunk.RawScore = e.score
		e.chunk.Score = e.score
		fused = append(fused, e.chunk)
	}
	sort.SliceStable(fused, func(i, j int) bool { return fused[i].Score > fused[j].Score })
	return fused
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
