//go:build eval

// The outside-repository sweep: levers and a cross-encoder, scored on five
// question sets in one process.
//
// WHAT IT DECIDES. Two rounds share it, each pre-registered in
// docs/RETRIEVAL_EVAL_TREND.md before it ran:
//
//   - 2026-10-04, first: L1 (test files beyond Go), L2 (widening to the method
//     inside an oversized construct) and L3 (setup files beyond Go) on this
//     repository's 49 and 28 and the outside 40 and 20. Nothing passed; L3 was
//     inert and is no longer swept.
//   - 2026-10-04, confirmation: L1 again, L2c (L2 without Go) and three
//     cross-encoder arms, on the same four sets plus the 60 fresh questions
//     (freshEvalQuestions) that were written after the first round and are
//     scored here once.
//
// IT SCORES THROUGH PRODUCTION'S OWN FUNCTIONS: the stores' Query and Search,
// fuseRRF, rerankChunksWith, crossEncoderReorder and deliverWithinBudget. A
// candidate is a policy value, never a re-implementation, and the SHIPPED row
// must reproduce TestRerankEvalRetrievalRanking, EVALHELDOUT and EVALEXTERNAL
// on the same tree.
//
//	scripts/fetch-eval-repos.sh
//	go test -tags eval -count=1 -timeout 150m -v -run TestExternalLeverSweep ./
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type leverArm struct {
	name   string
	rank   rankPolicy
	expand expandPolicy
	ce     *crossEncoderPolicy // nil: no cross-encoder
	kind   string              // "lever" (may ship by the rule) or "reranker" (may only be recommended)
}

// sweepQuery is one question with everything that does not depend on the arm
// already computed: its corpus, its ground truth, and both candidate pools.
type sweepQuery struct {
	set, repo string
	index     int // 1-based, within its repository's or set's own list
	query     string
	exact     []string
	root      string
	sem, kw   []Chunk
	ce        *cachedScorer
}

// cachedScorer remembers every passage the cross-encoder has scored for one
// question, so arms that rank the same chunk differently do not pay for it
// twice, and records how long the first scoring took.
type cachedScorer struct {
	real    pairScorer
	mu      sync.Mutex
	scores  map[string]float32
	firstMS time.Duration
}

func (c *cachedScorer) Rerank(ctx context.Context, query string, texts []string) ([]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var missing []string
	for _, t := range texts {
		if _, ok := c.scores[t]; !ok {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		start := time.Now()
		got, err := c.real.Rerank(ctx, query, missing)
		if err != nil {
			return nil, err
		}
		if c.firstMS == 0 {
			c.firstMS = time.Since(start)
		}
		for i, t := range missing {
			c.scores[t] = got[i]
		}
	}
	out := make([]float32, len(texts))
	for i, t := range texts {
		out[i] = c.scores[t]
	}
	return out, nil
}

// crossEncoderHelper makes sure the cross-encoder's files are cached and
// returns the shared helper, which opens them on its first rerank request.
func crossEncoderHelper(t *testing.T) *HelperProcess {
	t.Helper()
	ctx := context.Background()
	logger := log.New(os.Stderr, "cross-encoder: ", log.LstdFlags)
	if _, err := sharedEvalEmbedder(); err != nil {
		t.Fatal(err)
	}
	dir, err := defaultCrossEncoderCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureModelFiles(ctx, dir, crossEncoderModelAssets, logger); err != nil {
		t.Fatalf("the cross-encoder's model files: %v", err)
	}
	return evalHelper
}

func TestExternalLeverSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}
	ctx := context.Background()
	ce := crossEncoderHelper(t)

	var units []sweepQuery
	add := func(set, repo string, c *evalCorpus, qs []rerankEvalQuery) {
		if len(qs) == 0 {
			return
		}
		exact := resolveExactChunks(t, qs, c.Scan.Chunks)
		if t.Failed() {
			t.Fatalf("%s/%s: ground truth did not resolve", set, repo)
		}
		for i, q := range qs {
			vecs, err := c.Embedder.EmbedQuery(ctx, []string{q.query})
			if err != nil {
				t.Fatalf("EmbedQuery(%q): %v", q.query, err)
			}
			sem, err := c.Store.Query(ctx, vecs[0], rerankPoolSize(defaultK))
			if err != nil {
				t.Fatalf("Query(%q): %v", q.query, err)
			}
			kw, err := c.LexicalStore.Search(ctx, q.query, lexicalPoolSize(defaultK))
			if err != nil {
				t.Fatalf("Search(%q): %v", q.query, err)
			}
			units = append(units, sweepQuery{set, repo, i + 1, q.query, exact[i], c.RepoRoot, sem, kw,
				&cachedScorer{real: ce, scores: map[string]float32{}}})
		}
	}

	own := sharedEvalCorpus(t)
	add("in49", "self", own, rerankEvalQueries)
	add("in28", "self", own, heldOutEvalQueries)
	repos := loadExternalRepos(t)
	for _, r := range repos {
		c := externalCorpus(t, r)
		tuning, heldOut := splitExternal(externalEvalQuestions[r.name])
		add("ext40", r.name, c, tuning)
		add("ext20", r.name, c, heldOut)
		add("fresh60", r.name, c, freshEvalQuestions[r.name])
	}

	nested := defaultExpandPolicy
	nested.NestedConstructs = true
	nestedNoGo := nested
	nestedNoGo.NestedSkipGo = true
	l1 := rankPolicy{TestPathsBeyondGo: true}
	ce20 := &crossEncoderPolicy{TopN: 20}
	ce30 := &crossEncoderPolicy{TopN: 30}
	ce30b := &crossEncoderPolicy{TopN: 30, Blend: true}
	arms := []leverArm{
		{"SHIPPED", defaultRankPolicy, defaultExpandPolicy, nil, "-"},
		{"L1 tests", l1, defaultExpandPolicy, nil, "lever"},
		{"L2 nested", defaultRankPolicy, nested, nil, "reference"},
		{"L2c nested-no-Go", defaultRankPolicy, nestedNoGo, nil, "lever"},
		{"L1+L2c", l1, nestedNoGo, nil, "lever"},
		{"CE top20", defaultRankPolicy, defaultExpandPolicy, ce20, "reranker"},
		{"CE top30", defaultRankPolicy, defaultExpandPolicy, ce30, "reranker"},
		{"CE top30 blend", defaultRankPolicy, defaultExpandPolicy, ce30b, "reranker"},
		{"L1+L2c+CE30b", l1, nestedNoGo, ce30b, "reference"},
	}

	type result struct{ retrieved, delivered map[string]bool }
	key := func(u sweepQuery) string { return fmt.Sprintf("%s/%s#%d", u.set, u.repo, u.index) }
	results := make([]result, len(arms))
	for ai, a := range arms {
		res := result{map[string]bool{}, map[string]bool{}}
		for _, u := range units {
			fused := fuseRRF(u.sem, u.kw, rrfK)
			var hits []Chunk
			if a.ce == nil {
				hits = rerankChunksWith(fused, defaultK, u.query, a.rank)
			} else {
				pool := rerankChunksWith(fused, a.ce.TopN, u.query, a.rank)
				var err error
				hits, err = crossEncoderReorder(ctx, u.ce, u.query, pool, *a.ce, defaultK)
				if err != nil {
					t.Fatalf("%s: cross-encoder on %q: %v", a.name, u.query, err)
				}
			}
			res.retrieved[key(u)] = rankOfChunk(hits, u.exact) != 0
			kept := deliverWithinBudget(nil, hits, u.root, a.expand, defaultK, defaultContextBudgetChars, false).Chunks
			res.delivered[key(u)] = rankOfChunk(kept, u.exact) != 0
		}
		results[ai] = res
	}

	count := func(m map[string]bool, set, repo string) int {
		n := 0
		for _, u := range units {
			if u.set == set && (repo == "" || u.repo == repo) && m[key(u)] {
				n++
			}
		}
		return n
	}
	outside := func(m map[string]bool, repo string) int {
		return count(m, "ext40", repo) + count(m, "ext20", repo) + count(m, "fresh60", repo)
	}
	diff := func(base, arm map[string]bool, sets ...string) string {
		var gained, lost []string
		for _, u := range units {
			if !slices.Contains(sets, u.set) {
				continue
			}
			// repo#n, with /h for a held-out question and /f for a fresh one:
			// each list is numbered separately.
			k, label := key(u), fmt.Sprintf("%s#%d", u.repo, u.index)
			switch u.set {
			case "in28", "ext20":
				label += "/h"
			case "fresh60":
				label += "/f"
			}
			switch {
			case arm[k] && !base[k]:
				gained = append(gained, label)
			case base[k] && !arm[k]:
				lost = append(lost, label)
			}
		}
		return fmt.Sprintf("+[%s] -[%s]", strings.Join(gained, " "), strings.Join(lost, " "))
	}

	// Cross-encoder latency: the first scoring of each question's top 30
	// candidates, which is what a production query would pay.
	var lat []time.Duration
	for _, u := range units {
		if u.ce.firstMS > 0 {
			lat = append(lat, u.ce.firstMS)
		}
	}
	slices.Sort(lat)
	var p50, p95 time.Duration
	if len(lat) > 0 {
		p50, p95 = lat[len(lat)/2], lat[min(len(lat)-1, len(lat)*95/100)]
	}

	ref := results[0]
	fmt.Println("\n=== Outside-repository sweep (one build per corpus, five sets) ===")
	fmt.Printf("%-17s %7s %7s %7s %7s %8s  %-46s %-12s %s\n", "arm", "in49", "in28", "ext40", "ext20", "fresh60",
		"per repo, all outside questions", "verdict", "outside and fresh delivered vs SHIPPED ; in-repo")
	for ai, a := range arms {
		r := results[ai]
		var perRepo []string
		repoOK := true
		for _, repo := range repos {
			n, base := outside(r.delivered, repo.name), outside(ref.delivered, repo.name)
			perRepo = append(perRepo, fmt.Sprintf("%s %d", repo.name, n))
			if n < base-1 {
				repoOK = false
			}
		}
		ext60 := count(r.delivered, "ext40", "") + count(r.delivered, "ext20", "")
		refExt60 := count(ref.delivered, "ext40", "") + count(ref.delivered, "ext20", "")
		inOK := count(r.delivered, "in49", "") >= count(ref.delivered, "in49", "") &&
			count(r.delivered, "in28", "") >= count(ref.delivered, "in28", "")
		fresh, refFresh := count(r.delivered, "fresh60", ""), count(ref.delivered, "fresh60", "")
		verdict := "-"
		switch a.kind {
		case "lever":
			if fresh >= refFresh+2 && ext60 >= refExt60 && inOK && repoOK {
				verdict = "SHIPS"
			} else {
				verdict = "fails"
			}
		case "reranker":
			if fresh >= refFresh+3 && ext60 >= refExt60+3 && inOK && p95 <= 400*time.Millisecond {
				verdict = "RECOMMEND"
			} else {
				verdict = "not rec."
			}
		case "reference":
			verdict = "(reference)"
		}
		fmt.Printf("%-17s %4d/49 %4d/28 %4d/40 %4d/20 %5d/60  %-46s %-12s %s ; %s\n", a.name,
			count(r.delivered, "in49", ""), count(r.delivered, "in28", ""),
			count(r.delivered, "ext40", ""), count(r.delivered, "ext20", ""), fresh,
			strings.Join(perRepo, " "), verdict,
			diff(ref.delivered, r.delivered, "ext40", "ext20", "fresh60"), diff(ref.delivered, r.delivered, "in49", "in28"))
	}
	fmt.Printf("\ncross-encoder latency, first scoring of a question's top 30: p50=%s p95=%s over %d questions\n",
		p50.Round(time.Millisecond), p95.Round(time.Millisecond), len(lat))
	fmt.Println("rules (pre-registered): a lever SHIPS if fresh60 >= SHIPPED+2, ext40+ext20 and in49, in28 no lower, " +
		"no repository down by more than 1; a reranker is RECOMMENDED if fresh60 >= +3, ext40+ext20 >= +3, " +
		"in-repo no lower, p95 <= 400ms")
	fmt.Printf("SHIPPED: in49 %d, in28 %d, ext40 %d, ext20 %d, fresh60 %d -- must match TestRerankEvalRetrievalRanking, "+
		"EVALHELDOUT and EVALEXTERNAL on the same tree.\n",
		count(ref.delivered, "in49", ""), count(ref.delivered, "in28", ""),
		count(ref.delivered, "ext40", ""), count(ref.delivered, "ext20", ""), count(ref.delivered, "fresh60", ""))
	if count(ref.delivered, "in49", "") < 20 {
		t.Errorf("the SHIPPED row delivered only %d/49: this harness is not measuring the product", count(ref.delivered, "in49", ""))
	}
}

// TestCrossEncoderThroughTheSharedHelper is the sweep's wiring, checked in
// seconds and without building a corpus: the shared helper answers a rerank
// request, and the passage that answers outranks the one that does not.
func TestCrossEncoderThroughTheSharedHelper(t *testing.T) {
	h := crossEncoderHelper(t)
	scores, err := h.Rerank(context.Background(), "how are failed requests retried",
		[]string{"func retry(req) { for attempt := 1; attempt <= 3; attempt++ { if send(req) == nil { return } } }",
			"func parseDate(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }"})
	if err != nil {
		t.Fatalf("the shared helper could not rerank: %v", err)
	}
	if len(scores) != 2 || scores[0] <= scores[1] {
		t.Errorf("scores %v: the retry passage should outrank the date parser", scores)
	}
}
