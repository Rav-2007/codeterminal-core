//go:build eval

// THE DEEP-POOL PROBE: is a missed file absent, or merely out-ranked?
//
// The gated eval reports "right file never retrieved: 4" and stops there. That
// number is a label, not a diagnosis, and the two things it can mean want
// opposite fixes:
//
//   - the file IS in the candidate pool, below the cut -> a RANKING problem.
//     Fixed by how candidates are ordered and selected: fusion weights, a
//     per-file cap, pool depth. Cheap, no re-index, no new document type.
//
//   - the file is absent from a pool hundreds deep -> a REPRESENTATION problem.
//     The embedding cannot see the file as an answer to the question at all, and
//     no amount of reordering reaches it. That is the only situation in which
//     something like a synthesised file-skeleton document is justified.
//
// Guessing wrong here is expensive in a specific way this repository has already
// paid for: structure-in-the-vector has now lost SIX measured arms
// (chunkcontext.go), and a skeleton document is structure in a vector. Building
// one to fix a ranking problem would be the seventh.
//
// So this probe pulls a pool five times production's depth and reports where the
// ground truth actually sits in each tier, before and after fusion. It costs one
// index build and answers the question with a rank instead of an argument.
//
//	go test -tags eval -count=1 -timeout 40m -v -run TestDeepPoolProbe ./
package main

import (
	"context"
	"fmt"
	"testing"
)

// probeDepth is how deep to look. Production fetches rerankPoolSize(10)=60
// semantic and lexicalPoolSize(10)=100 lexical; this takes 300 of each so that
// "not in the pool" means something much stronger than "not in production's
// pool".
const probeDepth = 300

// rankOfFile returns the 1-based rank of the first chunk drawn from one of
// want, or 0 if no chunk in hits comes from any of them.
//
// FILE-level, deliberately, and separate from rankOfChunk's exact-chunk grade.
// The question this probe asks is whether the embedding can see the FILE as
// relevant at all -- if it cannot, which chunk of it would have won is moot.
func rankOfFile(hits []Chunk, want []string) int {
	for i, h := range hits {
		if matchesAny(h.FilePath, want) {
			return i + 1
		}
	}
	return 0
}

func fmtRank(r int) string {
	if r == 0 {
		return "   --"
	}
	return fmt.Sprintf("%5d", r)
}

func TestDeepPoolProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}
	// ONE INDEX FOR THE WHOLE EVAL SUITE -- see evalcorpus_test.go. Every
	// whole-repo eval in this package used to scan, embed and upsert this
	// repository for itself, so a full `-tags eval` run paid for the same
	// ~4,700 embeddings six times over for identical vectors.
	//
	// READ-ONLY. sharedEvalCorpus re-checks the store's document count on
	// every call, so a test that upserted here would be caught by the next
	// one to ask for the corpus.
	corpus := sharedEvalCorpus(t)
	embedder, store, lexical := corpus.Embedder, corpus.Store, corpus.LexicalStore
	repoRoot, scan := corpus.RepoRoot, corpus.Scan
	ctx := context.Background()
	t.Logf("shared corpus at %s: scanned=%d chunks=%d", repoRoot, scan.FilesScanned, len(scan.Chunks))

	exact := resolveExactChunks(t, scan.Chunks)
	if t.Failed() {
		t.Fatal("ground truth did not resolve; every rank below would be measured against nothing")
	}

	type row struct {
		i                         int
		q                         rerankEvalQuery
		semFile, lexFile, fusFile int
		semExact                  int
		shippedTop10              bool
	}
	rows := make([]row, 0, len(rerankEvalQueries))

	for i, q := range rerankEvalQueries {
		vecs, err := embedder.EmbedQuery(ctx, []string{q.query})
		if err != nil {
			t.Fatalf("EmbedQuery(%q): %v", q.query, err)
		}
		sem, err := store.Query(ctx, vecs[0], probeDepth)
		if err != nil {
			t.Fatalf("deep semantic Query(%q): %v", q.query, err)
		}
		lex, err := lexical.Search(ctx, q.query, probeDepth)
		if err != nil {
			t.Fatalf("deep lexical Search(%q): %v", q.query, err)
		}
		fused := fuseRRF(sem, lex, rrfK)

		// What production actually delivers to the top-10, for reference.
		shipped, err := retrieveTopK(ctx, q.query, defaultK, embedder, store, lexical, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%q): %v", q.query, err)
		}

		rows = append(rows, row{
			i: i + 1, q: q,
			semFile:      rankOfFile(sem, q.expectedFiles),
			lexFile:      rankOfFile(lex, q.expectedFiles),
			fusFile:      rankOfFile(fused, q.expectedFiles),
			semExact:     rankOfChunk(sem, exact[i]),
			shippedTop10: rankOfFile(shipped, q.expectedFiles) != 0,
		})
	}

	fmt.Printf("\n=== Deep-pool probe (pool depth %d per tier; production uses %d/%d) ===\n",
		probeDepth, rerankPoolSize(defaultK), lexicalPoolSize(defaultK))
	fmt.Printf("%-3s %-8s %6s %6s %6s %6s  %-7s %s\n",
		"#", "shape", "sem", "lex", "fused", "semEx", "top10", "expected file")
	for _, r := range rows {
		if r.shippedTop10 {
			continue // only the failures are interesting here
		}
		fmt.Printf("%-3d %-8s %s %s %s %s  %-7s %v\n",
			r.i, r.q.shape, fmtRank(r.semFile), fmtRank(r.lexFile), fmtRank(r.fusFile),
			fmtRank(r.semExact), "MISS", r.q.expectedFiles)
	}

	// THE VERDICT THIS PROBE EXISTS TO PRODUCE.
	var reachable, unreachable []int
	for _, r := range rows {
		if r.shippedTop10 {
			continue
		}
		if r.semFile != 0 || r.lexFile != 0 {
			reachable = append(reachable, r.i)
		} else {
			unreachable = append(unreachable, r.i)
		}
	}
	fmt.Printf("\nfile-level misses at production depth: %d\n", len(reachable)+len(unreachable))
	fmt.Printf("  RANKING problem   (in a %d-deep pool, below the cut): %v\n", probeDepth, reachable)
	fmt.Printf("  REPRESENTATION problem (absent from both tiers):      %v\n", unreachable)
	fmt.Println("\nA query in the first list is fixable by ordering -- fusion weights, a per-file")
	fmt.Println("cap, pool depth. A query in the second is not: nothing reorders a candidate that")
	fmt.Println("was never a candidate, and only that list can justify a new document type.")
}
