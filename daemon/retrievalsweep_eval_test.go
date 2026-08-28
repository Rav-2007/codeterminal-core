//go:build eval

// THE RETRIEVAL-POLICY SWEEP.
//
// Same argument as TestDeliveryPolicySweep, one stage earlier in the pipeline.
// The six-minute cost of this eval is the index build and the query embeddings;
// everything after them -- pooling, fusion, class weighting, top-k selection --
// is microseconds of pure computation over candidates already in hand. So one
// index build can score every ranking policy instead of one.
//
// WHAT IT WAS BUILT TO ANSWER. TestDeepPoolProbe established that all four
// file-level misses are RANKING failures, not representation failures: every
// one of the four ground-truth files sits inside a 300-deep pool, between rank
// 7 and rank 87, and production simply cuts at 10. It also showed the shape of
// the problem -- in all four cases the FUSED rank is WORSE than the better of
// the two tier ranks that produced it, because max-based RRF orders by best
// single-tier rank and therefore gives each tier only about half the final
// slots.
//
// The other measured fact this sweeps against: the ranker had no diversity
// control of any kind, and the top-10 is routinely 5-of-10 and up to 9-of-10
// chunks from a single file. A large file earns slots for being large.
//
// THE GUARD ROW IS PRODUCTION'S OWN CONFIGURATION and must reproduce the gated
// eval. An instrument that reimplements what it measures eventually measures
// something else; this package has that failure on record three times.
//
//	go test -tags eval -count=1 -timeout 40m -v -run TestRetrievalPolicySweep ./
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// sweepPoolDepth is how deep the pools are pulled ONCE, per query. Every arm
// below is a prefix of these, so no arm may name a pool larger than this.
const sweepPoolDepth = 300

// ---------------------------------------------------------------------------
// CANDIDATE POLICIES -- deliberately implemented HERE and not in production.
//
// The guard-row rule in this package is "drive production's own functions, or
// the instrument drifts off the product". That rule is about measuring what
// production DOES. These measure what production does NOT do, which can only be
// a local implementation: shipping a knob to production so an eval can sweep it
// leaves dead configuration behind, and a knob measured to be harmful at every
// setting is a trap for the next reader. Anything that WINS here gets
// implemented in production and re-verified through the gated eval.
// ---------------------------------------------------------------------------

// selectTopKCapped is the per-file diversity cap: at most perFileCap chunks
// from any one file, with displaced chunks refilling leftover slots so the cap
// reorders rather than shrinks.
func selectTopKCapped(weighted []Chunk, k, perFileCap int) []Chunk {
	if k > len(weighted) {
		k = len(weighted)
	}
	if perFileCap <= 0 {
		return weighted[:k]
	}
	out := make([]Chunk, 0, k)
	seen := make(map[string]int, k)
	var deferred []Chunk
	for _, c := range weighted {
		if len(out) == k {
			break
		}
		if seen[c.FilePath] >= perFileCap {
			deferred = append(deferred, c)
			continue
		}
		seen[c.FilePath]++
		out = append(out, c)
	}
	for _, c := range deferred {
		if len(out) >= k {
			break
		}
		out = append(out, c)
	}
	return out
}

// fuseSum is textbook RRF -- score = SUM of 1/(k+rank) over the tiers a chunk
// appears in -- against production's MAX.
//
// Production chose max deliberately and measured it, but on the NINE-query set
// that this eval replaced. The rationale was that consensus scoring buries an
// answer only one tier can see. Worth re-asking at 49 queries because the
// semantic tier has since improved a lot (24 -> 31 on the path-prefix change),
// and fusion's net contribution has collapsed from +3 to 0 as a result.
func fuseSum(semantic, lexical []Chunk, k float64) []Chunk {
	score := map[string]float32{}
	chunk := map[string]Chunk{}
	var order []string
	add := func(list []Chunk) {
		for rank, c := range list {
			if _, ok := chunk[c.ID]; !ok {
				chunk[c.ID] = c
				order = append(order, c.ID)
			}
			score[c.ID] += float32(1.0 / (k + float64(rank+1)))
		}
	}
	add(semantic)
	add(lexical)
	out := make([]Chunk, 0, len(order))
	for _, id := range order {
		c := chunk[id]
		c.RawScore, c.Score = score[id], score[id]
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// fuseInterleave alternates strictly between the two tier rankings.
//
// THIS IS THE ONE THE PROBE POINTS AT. TestDeepPoolProbe found that for all four
// file-level misses the FUSED rank is worse than the better of the two tier
// ranks that produced it -- lexical rank 7 became fused rank 13, lexical 49
// became 87. Max-based RRF orders by best single-tier rank, so the two tiers
// compete for the same ten slots and each effectively gets five. Interleaving
// guarantees each tier half the slots outright, which is the direct remedy for
// that specific mechanism rather than a reweighting that hopes to approximate it.
func fuseInterleave(semantic, lexical []Chunk, k float64) []Chunk {
	seen := map[string]bool{}
	var out []Chunk
	take := func(list []Chunk, i int) (Chunk, bool) {
		for ; i < len(list); i++ {
			if !seen[list[i].ID] {
				return list[i], true
			}
		}
		return Chunk{}, false
	}
	si, li := 0, 0
	for si < len(semantic) || li < len(lexical) {
		if c, ok := take(semantic, si); ok {
			seen[c.ID] = true
			// Score descends with output position so rerankChunks has a sane
			// magnitude to apply class weights to.
			c.RawScore = float32(1.0 / (k + float64(len(out)+1)))
			c.Score = c.RawScore
			out = append(out, c)
		}
		for si < len(semantic) && seen[semantic[si].ID] {
			si++
		}
		if c, ok := take(lexical, li); ok {
			seen[c.ID] = true
			c.RawScore = float32(1.0 / (k + float64(len(out)+1)))
			c.Score = c.RawScore
			out = append(out, c)
		}
		for li < len(lexical) && seen[lexical[li].ID] {
			li++
		}
		if si >= len(semantic) && li >= len(lexical) {
			break
		}
	}
	return out
}

type retrievalArm struct {
	name       string
	semPool    int // 0 = production's rerankPoolSize(defaultK)
	lexPool    int // 0 = production's lexicalPoolSize(defaultK)
	perFileCap int // 0 = no cap, which is production today
	fuse       func(semantic, lexical []Chunk, k float64) []Chunk
}

func TestRetrievalPolicySweep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}
	logger := log.New(os.Stderr, "retrieval-sweep: ", log.LstdFlags)
	ctx := context.Background()

	modelCacheDir, err := defaultModelCacheDir()
	if err != nil {
		t.Fatalf("defaultModelCacheDir: %v", err)
	}
	modelDir, err := EnsureModelFiles(ctx, modelCacheDir, bgeModelAssets, logger)
	if err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}
	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		t.Fatalf("defaultONNXRuntimeCacheDir: %v", err)
	}
	onnxRuntimeLib, err := EnsureONNXRuntimeLib(ctx, ortCacheDir, logger)
	if err != nil {
		t.Fatalf("EnsureONNXRuntimeLib: %v", err)
	}
	helper := NewHelperProcess(buildRealHelperBinary(t), modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		t.Fatalf("starting real embedder helper: %v", err)
	}
	defer helper.Stop()
	embedder := NewBgeEmbedder(helper)

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	indexDir := filepath.Join(t.TempDir(), "index")
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	lexical, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer lexical.Close()

	scan, err := indexRepoExcludingSelfReference(ctx, repoRoot, embedder, store, lexical, logger)
	if err != nil {
		t.Fatalf("indexing: %v", err)
	}
	t.Logf("indexed %s: scanned=%d chunks=%d", repoRoot, scan.FilesScanned, len(scan.Chunks))

	exact := resolveExactChunks(t, scan.Chunks)
	if t.Failed() {
		t.Fatal("ground truth did not resolve; every number below would be measured against nothing")
	}

	// POOL ONCE. This is the six minutes.
	type pools struct{ sem, lex []Chunk }
	pooled := make([]pools, len(rerankEvalQueries))
	for i, q := range rerankEvalQueries {
		vecs, err := embedder.EmbedQuery(ctx, []string{q.query})
		if err != nil {
			t.Fatalf("EmbedQuery(%q): %v", q.query, err)
		}
		sem, err := store.Query(ctx, vecs[0], sweepPoolDepth)
		if err != nil {
			t.Fatalf("Query(%q): %v", q.query, err)
		}
		lex, err := lexical.Search(ctx, q.query, sweepPoolDepth)
		if err != nil {
			t.Fatalf("Search(%q): %v", q.query, err)
		}
		pooled[i] = pools{sem, lex}
	}

	prodSem, prodLex := rerankPoolSize(defaultK), lexicalPoolSize(defaultK)
	arms := []retrievalArm{
		// The guard row: production, exactly as configured today.
		{"SHIPPED (max, no cap)", prodSem, prodLex, 0, fuseRRF},
	}
	// FUSION, the lever the deep-pool probe pointed at. Swept with and without a
	// cap because the two interact: interleaving changes WHICH file fills the
	// slots, which is the same thing a cap tries to do by a different route.
	for _, f := range []struct {
		name string
		fn   func([]Chunk, []Chunk, float64) []Chunk
	}{{"sum", fuseSum}, {"interleave", fuseInterleave}} {
		for _, cap := range []int{0, 3} {
			arms = append(arms, retrievalArm{
				fmt.Sprintf("%s cap=%d", f.name, cap), prodSem, prodLex, cap, f.fn})
		}
	}
	// Pool depth under each fusion. Depth was inert under max (60 through 300
	// byte-identical, because RRF scores decay as 1/(60+rank) and class weights
	// span barely 2x); under interleave a deeper pool changes which tier's
	// candidates are still available to alternate into, so it is re-asked here
	// rather than assumed to stay inert.
	for _, pool := range [][2]int{{60, 60}, {300, 300}} {
		arms = append(arms,
			retrievalArm{fmt.Sprintf("max pool=%d", pool[0]), pool[0], pool[1], 0, fuseRRF},
			retrievalArm{fmt.Sprintf("interleave pool=%d", pool[0]), pool[0], pool[1], 0, fuseInterleave})
	}
	// The cap, kept as a row so the rejection stays visible in the table rather
	// than only in rerank.go's comment.
	for _, cap := range []int{2, 3} {
		arms = append(arms, retrievalArm{fmt.Sprintf("max cap=%d", cap), prodSem, prodLex, cap, fuseRRF})
	}

	type result struct {
		name                             string
		fileLevel, chunkLevel, delivered int
		perShape                         map[string][2]int
	}
	var results []result

	for _, arm := range arms {
		var fileLevel, chunkLevel, delivered int
		shape := map[string][2]int{}
		for i, q := range rerankEvalQueries {
			sem, lex := pooled[i].sem, pooled[i].lex
			if arm.semPool < len(sem) {
				sem = sem[:arm.semPool]
			}
			if arm.lexPool < len(lex) {
				lex = lex[:arm.lexPool]
			}
			// rerankChunks is production's own (class weighting, top-k). Only
			// the fusion and the optional cap are candidate policies, and the
			// guard row runs both at production's setting.
			fused := arm.fuse(sem, lex, rrfK)
			hits := rerankChunks(fused, defaultK, q.query)
			if arm.perFileCap > 0 {
				// Applied to a WIDER rerank so the cap has candidates to promote
				// into the freed slots; capping the already-truncated top-10
				// would only ever reorder ten chunks among themselves.
				hits = selectTopKCapped(rerankChunks(fused, len(fused), q.query), defaultK, arm.perFileCap)
			}

			if rankOfFile(hits, q.expectedFiles) != 0 {
				fileLevel++
			}
			if rankOfChunk(hits, exact[i]) != 0 {
				chunkLevel++
			}
			expanded := expandToNeighbours(hits, repoRoot, defaultExpandPolicy)
			kept, _ := truncateToBudget(
				fuseDirectSpans(nil, expanded, defaultK, false).Chunks, defaultContextBudgetChars, false)
			ok := rankOfChunk(kept, exact[i]) != 0
			s := shape[q.shape]
			s[1]++
			if ok {
				delivered++
				s[0]++
			}
			shape[q.shape] = s
		}
		results = append(results, result{arm.name, fileLevel, chunkLevel, delivered, shape})
	}

	total := len(rerankEvalQueries)
	fmt.Println("\n=== Retrieval policy sweep (one index build, every policy) ===")
	fmt.Printf("%-24s %10s %11s %10s   per shape\n", "policy", "file-lvl", "chunk-lvl", "DELIVERED")
	for _, r := range results {
		var shapes []string
		for _, s := range evalQueryShapeOrder {
			if v, ok := r.perShape[s]; ok {
				shapes = append(shapes, fmt.Sprintf("%s %d/%d", s, v[0], v[1]))
			}
		}
		fmt.Printf("%-24s %7d/%d %8d/%d %7d/%d   %v\n",
			r.name, r.fileLevel, total, r.chunkLevel, total, r.delivered, total, shapes)
	}

	shipped := results[0]
	fmt.Printf("\nSHIPPED row: file-level %d/%d, DELIVERED %d/%d -- this must match "+
		"TestRerankEvalRetrievalRanking on the same tree.\n",
		shipped.fileLevel, total, shipped.delivered, total)
	if shipped.delivered < 20 {
		t.Errorf("the SHIPPED row delivered only %d/%d. Either retrieval regressed badly or this "+
			"harness is no longer running production's ranking path -- and a sweep that is not "+
			"measuring the product is worse than no sweep.", shipped.delivered, total)
	}

	best := append([]result(nil), results...)
	sort.SliceStable(best, func(a, b int) bool { return best[a].delivered > best[b].delivered })
	fmt.Printf("best arm: %s at DELIVERED %d/%d (file-level %d/%d)\n",
		best[0].name, best[0].delivered, total, best[0].fileLevel, total)
	fmt.Println("\nA cap only ever REORDERS the top-k (selectTopK refills displaced slots), so a")
	fmt.Println("shape that drops under one is losing to a genuinely competing file, not to truncation.")
}
