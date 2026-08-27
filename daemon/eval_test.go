//go:build eval

// This file is gated behind the "eval" build tag so `go test ./...` (the
// fast, offline unit path) never compiles or runs it. Run it explicitly
// with:
//
//	go test -tags eval -run TestEvalRetrievalQuality -v ./...
//
// It needs the real BGE model + onnxruntime shared library, downloading
// them (via the same EnsureModelFiles/EnsureONNXRuntimeLib cache used by
// `download-model`) only if they aren't already cached, and it builds the
// real helper binary fresh from source. Both of those are real network/CGO
// operations that have no place in the fast unit-test path.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// evalTop3RecallThreshold is the quality bar this test enforces. Tunable:
// raise it as embedding/retrieval quality improves, or lower it if a
// deliberate tradeoff (e.g. a smaller/faster model) regresses it — but
// don't hand-tune the queries themselves to hit this number.
const evalTop3RecallThreshold = 0.80

type evalQuery struct {
	Query     string `json:"query"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Function  string `json:"function"`
}

func loadEvalQueries(t *testing.T) []evalQuery {
	t.Helper()
	data, err := os.ReadFile("../testdata/queries.json")
	if err != nil {
		t.Fatalf("reading eval queries: %v", err)
	}
	var queries []evalQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		t.Fatalf("parsing eval queries: %v", err)
	}
	if len(queries) == 0 {
		t.Fatal("eval queries file is empty")
	}
	return queries
}

// buildRealHelperBinary compiles the actual helper/ module fresh, so this
// test exercises current source rather than a possibly-stale prebuilt
// binary. It cannot reuse helperproc_test.go's TestMain (which builds the
// fakehelper fixture instead) since a package may only have one TestMain.
func buildRealHelperBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), exeName("codeterminal-embedder-helper"))

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = "../helper"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building real helper binary: %v\n%s", err, out)
	}
	return binPath
}

func TestEvalRetrievalQuality(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	logger := log.New(os.Stderr, "eval: ", log.LstdFlags)

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

	evalRoot, err := filepath.Abs("../testdata/evalset")
	if err != nil {
		t.Fatalf("resolving eval set path: %v", err)
	}
	indexDir := filepath.Join(t.TempDir(), "index")
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}

	ctx := context.Background()
	scan, err := buildIndex(ctx, evalRoot, embedder, store, nil, logger, false)
	if err != nil {
		t.Fatalf("indexing eval set: %v", err)
	}
	t.Logf("indexed eval set: scanned=%d chunks=%d", scan.FilesScanned, len(scan.Chunks))

	queries := loadEvalQueries(t)

	type result struct {
		query    string
		expected string
		got1     string
		top3     []string
		score    float32
		top1Hit  bool
		top3Hit  bool
	}
	results := make([]result, 0, len(queries))

	var top1Hits, top3Hits int
	for _, q := range queries {
		hits, err := retrieveTopK(ctx, q.Query, 3, embedder, store, nil, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%q): %v", q.Query, err)
		}

		r := result{query: q.Query, expected: q.File}
		if len(hits) > 0 {
			r.got1 = hits[0].FilePath
			r.score = hits[0].Score
			r.top1Hit = hits[0].FilePath == q.File
		}
		for _, h := range hits {
			r.top3 = append(r.top3, h.FilePath)
			if h.FilePath == q.File {
				r.top3Hit = true
			}
		}

		if r.top1Hit {
			top1Hits++
		}
		if r.top3Hit {
			top3Hits++
		}
		results = append(results, r)
	}

	total := len(queries)
	top1Accuracy := float64(top1Hits) / float64(total)
	top3Recall := float64(top3Hits) / float64(total)

	fmt.Println()
	fmt.Println("=== Retrieval quality eval ===")
	fmt.Printf("%-70s %-22s %-22s %-6s %s\n", "query", "expected", "got@1", "top3?", "score@1")
	for _, r := range results {
		top3Mark := "no"
		if r.top3Hit {
			top3Mark = "yes"
		}
		fmt.Printf("%-70s %-22s %-22s %-6s %.4f\n", truncate(r.query, 70), r.expected, r.got1, top3Mark, r.score)
	}
	fmt.Println()
	fmt.Printf("top-1 accuracy: %d/%d = %.2f\n", top1Hits, total, top1Accuracy)
	fmt.Printf("top-3 recall:   %d/%d = %.2f (threshold %.2f)\n", top3Hits, total, top3Recall, evalTop3RecallThreshold)
	fmt.Println()

	if top3Recall < evalTop3RecallThreshold {
		t.Errorf("top-3 recall %.2f is below threshold %.2f", top3Recall, evalTop3RecallThreshold)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
