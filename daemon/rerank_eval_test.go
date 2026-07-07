//go:build eval

// See eval_test.go's header for why this is gated behind the "eval" build
// tag (real BGE model + onnxruntime + a freshly built helper binary — not
// part of the fast, offline `go test ./...` path). Run it explicitly with:
//
//	go test -tags eval -run TestRerankEvalRetrievalRanking -v ./...
//
// This is the acceptance test for retrieval RANKING specifically: it
// indexes the actual CodeTerminal repo (not a curated testdata/ subset —
// the measured failure this fixes was found against this real repo, and its
// expected files are real repo-relative paths) and runs the 5 queries from
// that stress test, asserting the fix actually recovers them.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// rerankEvalQuery is one query from the measured stress test, with the
// repo-relative file(s) that count as a correct answer. Query 4 accepts
// either file: the failure report itself named both chunker.go (where the
// actual secret-skipping logic — matchesSecretName/secretSubstrings/
// SkipSecret — lives) and index_cmd.go (which also references SkipSecret in
// its skip-count summary) as acceptable.
type rerankEvalQuery struct {
	query         string
	expectedFiles []string
}

var rerankEvalQueries = []rerankEvalQuery{
	{"where does the daemon open the unix socket", []string{"daemon/main.go"}},
	{"how are edit blocks parsed from the model response", []string{"daemon/editblock.go"}},
	{"where is the model tier routing decided", []string{"daemon/router.go"}},
	{"how does secret skipping work during indexing", []string{"daemon/chunker.go", "daemon/index_cmd.go"}},
	{"where are skills stored in sqlite", []string{"daemon/skills.go"}},
}

func matchesAny(path string, candidates []string) bool {
	for _, c := range candidates {
		if path == c {
			return true
		}
	}
	return false
}

func TestRerankEvalRetrievalRanking(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	logger := log.New(os.Stderr, "rerank-eval: ", log.LstdFlags)

	modelCacheDir, err := defaultModelCacheDir()
	if err != nil {
		t.Fatalf("defaultModelCacheDir: %v", err)
	}
	modelDir, err := EnsureModelFiles(context.Background(), modelCacheDir, bgeModelAssets, logger)
	if err != nil {
		t.Fatalf("EnsureModelFiles (downloads only if not already cached): %v", err)
	}

	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		t.Fatalf("defaultONNXRuntimeCacheDir: %v", err)
	}
	onnxRuntimeLib, err := EnsureONNXRuntimeLib(context.Background(), ortCacheDir, logger)
	if err != nil {
		t.Fatalf("EnsureONNXRuntimeLib (downloads only if not already cached): %v", err)
	}

	helperBin := buildRealHelperBinary(t)
	helper := NewHelperProcess(helperBin, modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		t.Fatalf("starting real embedder helper: %v", err)
	}
	defer helper.Stop()

	embedder := NewBgeEmbedder(helper)

	// Index the actual repo root (one level up from daemon/), not a
	// curated subset — this is what makes the test faithful to the
	// measured, real-repo failure.
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	indexDir := filepath.Join(t.TempDir(), "index")
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	ctx := context.Background()
	scan, err := buildIndex(ctx, repoRoot, embedder, store, logger)
	if err != nil {
		t.Fatalf("indexing repo: %v", err)
	}
	t.Logf("indexed repo root %s: scanned=%d chunks=%d", repoRoot, scan.FilesScanned, len(scan.Chunks))

	const displayK = 5

	type row struct {
		query    string
		expected string
		hits     []Chunk
		top1Hit  bool
		top3Hit  bool
	}
	rows := make([]row, 0, len(rerankEvalQueries))

	var top1Hits, top3Hits int
	for _, q := range rerankEvalQueries {
		hits, err := retrieveTopK(ctx, q.query, displayK, embedder, store, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%q): %v", q.query, err)
		}

		r := row{query: q.query, expected: fmt.Sprintf("%v", q.expectedFiles), hits: hits}
		if len(hits) > 0 {
			r.top1Hit = matchesAny(hits[0].FilePath, q.expectedFiles)
		}
		for _, h := range hits[:min(3, len(hits))] {
			if matchesAny(h.FilePath, q.expectedFiles) {
				r.top3Hit = true
			}
		}

		if r.top1Hit {
			top1Hits++
		}
		if r.top3Hit {
			top3Hits++
		}
		rows = append(rows, r)
	}

	fmt.Println()
	fmt.Println("=== Retrieval ranking eval (real repo, re-ranking ON) ===")
	for i, r := range rows {
		mark := "MISS"
		if r.top3Hit {
			mark = "hit"
		}
		fmt.Printf("\n%d. query=%q expected=%s top3=%s\n", i+1, r.query, r.expected, mark)
		for j, h := range r.hits {
			fmt.Printf("   %d. %-40s class=%-6s raw=%.4f weighted=%.4f\n", j+1, fmt.Sprintf("%s:%d-%d", h.FilePath, h.StartLine, h.EndLine), h.Class, h.RawScore, h.Score)
		}
	}

	total := len(rerankEvalQueries)
	top1Accuracy := float64(top1Hits) / float64(total)
	top3Recall := float64(top3Hits) / float64(total)

	fmt.Println()
	fmt.Printf("top-1 accuracy: %d/%d = %.2f (reported, not a hard gate)\n", top1Hits, total, top1Accuracy)
	fmt.Printf("top-3 recall:   %d/%d = %.2f (must be 5/5)\n", top3Hits, total, top3Recall)
	fmt.Println()

	if top3Recall < 1.0 {
		t.Errorf("top-3 recall %.2f (%d/%d) is below the required 5/5 — see per-query table above for which query(ies) missed and their raw/weighted scores", top3Recall, top3Hits, total)
	}

	// Query 2 (edit blocks) is the query that fully missed in the original
	// measured failure (lost to README.md/system.txt) — called out
	// explicitly since it's the specific regression this step must fix.
	if !rows[1].top3Hit {
		t.Errorf("query 2 (%q) is still missing from the top 3 — this is the specific miss this step was required to fix; see the table above", rows[1].query)
	}
}
