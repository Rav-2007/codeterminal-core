//go:build eval

// The 2026-10-04 outside-repository lever sweep.
//
// WHAT IT DECIDES. The levers pre-registered in docs/RETRIEVAL_EVAL_TREND.md
// ("Pre-registered 2026-10-04: outside repositories") -- test files beyond Go,
// widening to the method inside an oversized construct, setup files beyond Go
// -- each scored alone and in combination, on all four question sets: this
// repository's 49 and 28, and the outside repositories' 40 tuning and 20
// held-out (externaleval_test.go). One process, one build per corpus.
//
// IT SCORES THROUGH PRODUCTION'S OWN FUNCTIONS: the stores' Query and Search,
// fuseRRF, rerankChunksWith and deliverWithinBudget. A candidate is a policy
// value, never a re-implementation, and the shipped row must reproduce
// TestRerankEvalRetrievalRanking, TestExternalRepoRetrieval and the held-out
// line on the same tree.
//
//	go test -tags eval -count=1 -timeout 120m -v -run TestExternalLeverSweep ./
package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

type leverArm struct {
	name   string
	rank   rankPolicy
	expand expandPolicy
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
}

func TestExternalLeverSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}
	ctx := context.Background()

	var units []sweepQuery
	add := func(set, repo string, c *evalCorpus, qs []rerankEvalQuery) {
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
			units = append(units, sweepQuery{set, repo, i + 1, q.query, exact[i], c.RepoRoot, sem, kw})
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
	}

	nested := defaultExpandPolicy
	nested.NestedConstructs = true
	l1 := rankPolicy{TestPathsBeyondGo: true}
	l3 := rankPolicy{SetupFilesBeyondGo: true}
	l13 := rankPolicy{TestPathsBeyondGo: true, SetupFilesBeyondGo: true}
	arms := []leverArm{
		{"SHIPPED", defaultRankPolicy, defaultExpandPolicy},
		{"L1 tests", l1, defaultExpandPolicy},
		{"L2 nested", defaultRankPolicy, nested},
		{"L3 setup", l3, defaultExpandPolicy},
		{"L1+L2", l1, nested},
		{"L1+L3", l13, defaultExpandPolicy},
		{"L2+L3", l3, nested},
		{"L1+L2+L3", l13, nested},
	}

	type result struct{ retrieved, delivered map[string]bool }
	key := func(u sweepQuery) string { return fmt.Sprintf("%s/%s#%d", u.set, u.repo, u.index) }
	results := make([]result, len(arms))
	for ai, a := range arms {
		res := result{map[string]bool{}, map[string]bool{}}
		for _, u := range units {
			hits := rerankChunksWith(fuseRRF(u.sem, u.kw, rrfK), defaultK, u.query, a.rank)
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
	diff := func(base, arm map[string]bool, sets ...string) string {
		var gained, lost []string
		for _, u := range units {
			if !slices.Contains(sets, u.set) {
				continue
			}
			k := key(u)
			switch {
			case arm[k] && !base[k]:
				gained = append(gained, strings.TrimPrefix(k, u.set+"/"))
			case base[k] && !arm[k]:
				lost = append(lost, strings.TrimPrefix(k, u.set+"/"))
			}
		}
		return fmt.Sprintf("+[%s] -[%s]", strings.Join(gained, " "), strings.Join(lost, " "))
	}

	ref := results[0]
	fmt.Println("\n=== Outside-repository lever sweep (one build per corpus, four sets) ===")
	fmt.Printf("%-10s %8s %8s %8s %8s %8s  %-26s %-8s %s\n", "arm", "in49", "in28", "ext40", "ext20", "ext ret",
		"per repo (tune+held)", "rule", "outside delivered vs SHIPPED ; in-repo delivered vs SHIPPED")
	for ai, a := range arms {
		r := results[ai]
		var perRepo []string
		repoOK := true
		for _, repo := range repos {
			n := count(r.delivered, "ext40", repo.name) + count(r.delivered, "ext20", repo.name)
			base := count(ref.delivered, "ext40", repo.name) + count(ref.delivered, "ext20", repo.name)
			perRepo = append(perRepo, fmt.Sprintf("%s %d", repo.name, n))
			if n < base-1 {
				repoOK = false
			}
		}
		verdict := "-"
		if count(r.delivered, "ext40", "") >= count(ref.delivered, "ext40", "")+3 &&
			count(r.delivered, "ext20", "") >= count(ref.delivered, "ext20", "") &&
			count(r.delivered, "in49", "") >= count(ref.delivered, "in49", "") &&
			count(r.delivered, "in28", "") >= count(ref.delivered, "in28", "") && repoOK {
			verdict = "PASSES"
		}
		fmt.Printf("%-10s %5d/49 %5d/28 %5d/40 %5d/20 %5d/60  %-26s %-8s %s ; %s\n", a.name,
			count(r.delivered, "in49", ""), count(r.delivered, "in28", ""),
			count(r.delivered, "ext40", ""), count(r.delivered, "ext20", ""),
			count(r.retrieved, "ext40", "")+count(r.retrieved, "ext20", ""),
			strings.Join(perRepo, " "), verdict,
			diff(ref.delivered, r.delivered, "ext40", "ext20"), diff(ref.delivered, r.delivered, "in49", "in28"))
	}
	fmt.Println("\nrule: ext40 >= SHIPPED+3, ext20 >= SHIPPED, in49 and in28 >= SHIPPED, no repository down by more " +
		"than 1 (tune+held), and each lever no worse alone on any set -- read the single-lever rows")
	fmt.Printf("SHIPPED: in49 %d, in28 %d, ext40 %d, ext20 %d -- must match TestRerankEvalRetrievalRanking, "+
		"EVALHELDOUT and EVALEXTERNAL on the same tree.\n",
		count(ref.delivered, "in49", ""), count(ref.delivered, "in28", ""),
		count(ref.delivered, "ext40", ""), count(ref.delivered, "ext20", ""))
	if count(ref.delivered, "in49", "") < 20 {
		t.Errorf("the SHIPPED row delivered only %d/49: this harness is not measuring the product", count(ref.delivered, "in49", ""))
	}
}
