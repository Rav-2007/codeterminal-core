//go:build eval

// The delivery-policy sweep.
//
// WHY THIS EXISTS. Retrieval is expensive -- indexing this repository with the
// real BGE model takes about six minutes -- and everything after it is not.
// Expansion, merging and the character budget are pure computation over hits
// that are already in hand, and retrieval is deterministic, so one index build
// can score EVERY delivery policy instead of one.
//
// Before this, tuning delivery meant a full run per cell: the k/budget grid and
// the neighbour-expansion grid each cost most of an hour, and the six-arm
// chunk-representation sweep cost fifty minutes. The same grid runs here in the
// time it takes to build one index, which is what makes it affordable to ask the
// question properly rather than guess and confirm.
//
// IT SCORES THROUGH PRODUCTION'S OWN FUNCTIONS -- expandToNeighbours,
// fuseDirectSpans, truncateToBudget -- and not through a reimplementation. That
// is deliberate and it is the lesson of this package: `const displayK = 5` went
// on reporting a configuration that no longer shipped, the character budget went
// unmeasured entirely, and two eval harnesses embedded raw Content after
// production had stopped. An instrument that reimplements what it measures
// eventually measures something else. The SHIPPED row below is the guard: it
// must reproduce the gated eval's number, or nothing under it counts.
//
//	go test -tags eval -count=1 -timeout 40m -v -run TestDeliveryPolicySweep ./
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

// deliveryArm is one cell of the grid.
type deliveryArm struct {
	name   string
	policy expandPolicy
	budget int
}

func TestDeliveryPolicySweep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}
	logger := log.New(os.Stderr, "delivery-sweep: ", log.LstdFlags)
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

	// RETRIEVE ONCE. This is the six minutes; everything after is microseconds.
	hits := make([][]Chunk, len(rerankEvalQueries))
	for i, q := range rerankEvalQueries {
		h, err := retrieveTopK(ctx, q.query, defaultK, embedder, store, lexical, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%q): %v", q.query, err)
		}
		hits[i] = h
	}

	arms := []deliveryArm{
		// The guard row: whatever production is configured as today.
		{"SHIPPED (defaultExpandPolicy)", defaultExpandPolicy, defaultContextBudgetChars},
	}
	for _, budget := range []int{16000, 20000, 24000, 28000} {
		for _, p := range []struct {
			name string
			pol  expandPolicy
		}{
			{"+-1 top3", expandPolicy{TopN: 3}},
			{"+-1 top5", expandPolicy{TopN: 5}},
			{"construct<=60 top3", expandPolicy{TopN: 3, ConstructCap: 60}},
			{"construct<=160 top5", expandPolicy{TopN: 5, ConstructCap: 160}},
			{"construct<=300 top5", expandPolicy{TopN: 5, ConstructCap: 300}},
			{"construct<=300 top10", expandPolicy{TopN: 10, ConstructCap: 300}},
		} {
			arms = append(arms, deliveryArm{fmt.Sprintf("%-21s b=%d", p.name, budget), p.pol, budget})
		}
	}

	type result struct {
		name      string
		delivered int
		meanChars int
		perShape  map[string][2]int
	}
	results := make([]result, 0, len(arms))
	for _, arm := range arms {
		delivered, totalChars := 0, 0
		shape := map[string][2]int{}
		for i, q := range rerankEvalQueries {
			expanded := expandToNeighbours(hits[i], repoRoot, arm.policy)
			kept, _ := truncateToBudget(
				fuseDirectSpans(nil, expanded, defaultK, false).Chunks, arm.budget, false)
			for _, c := range kept {
				totalChars += len(renderChunk(1, c, false))
			}
			ok := rankOfChunk(kept, exact[i]) != 0
			s := shape[q.shape]
			s[1]++
			if ok {
				delivered++
				s[0]++
			}
			shape[q.shape] = s
		}
		results = append(results, result{arm.name, delivered, totalChars / len(rerankEvalQueries), shape})
	}

	fmt.Println("\n=== Delivery policy sweep (one index build, every policy) ===")
	fmt.Printf("%-34s %10s %12s   per shape\n", "policy", "delivered", "mean chars")
	for _, r := range results {
		shapes := make([]string, 0, len(r.perShape))
		for _, s := range evalQueryShapeOrder {
			if v, ok := r.perShape[s]; ok {
				shapes = append(shapes, fmt.Sprintf("%s %d/%d", s, v[0], v[1]))
			}
		}
		fmt.Printf("%-34s %7d/%d %12d   %v\n", r.name, r.delivered, len(rerankEvalQueries), r.meanChars, shapes)
	}

	// THE GUARD, and it is the reason the first arm is production's own
	// configuration rather than a nice round baseline. This harness is only
	// worth anything while it is running the same delivery path the product
	// runs, and the cheapest proof of that is that it reproduces the number the
	// gated eval reports for the same tree. Compare the SHIPPED row against
	// TestRerankEvalRetrievalRanking's DELIVERED before believing any row below.
	shipped := results[0]
	fmt.Printf("\nSHIPPED row: %d/%d delivered, %d mean chars -- this must match "+
		"TestRerankEvalRetrievalRanking on the same tree.\n",
		shipped.delivered, len(rerankEvalQueries), shipped.meanChars)
	if shipped.delivered < 20 {
		t.Errorf("the SHIPPED row delivered only %d/%d. Either retrieval regressed badly or this "+
			"harness is no longer running production's delivery path -- and a sweep that is not "+
			"measuring the product is worse than no sweep.", shipped.delivered, len(rerankEvalQueries))
	}

	best := append([]result(nil), results...)
	sort.SliceStable(best, func(a, b int) bool { return best[a].delivered > best[b].delivered })
	fmt.Printf("best arm: %s at %d/%d (%d mean chars)\n",
		best[0].name, best[0].delivered, len(rerankEvalQueries), best[0].meanChars)
}
