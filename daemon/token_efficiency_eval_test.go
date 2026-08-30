//go:build eval

// See eval_test.go's header for why this is gated behind the "eval" build
// tag (real BGE model + onnxruntime + a freshly built helper binary — not
// part of the fast, offline `go test ./...` path). Run it explicitly with:
//
//	go test -tags eval -run TestTokenEfficiencyEval -v ./...
//
// Measures the "tightly packed context, fewer tokens" half of the
// hybrid-retrieval pillar that BACKLOG.md's Phase 3 hybrid-retrieval entry
// explicitly flags as UNMEASURED ("Token-cost/efficiency claim is
// UNMEASURED... Do not make that claim externally... until it's actually
// measured"). No percentage for this claim exists anywhere else in the
// repo — this is the first real measurement of it.
//
// Compares the real retrieval path's actual injected-context size
// (retrieveTopK + truncateToBudget, the exact two calls gatherContext
// makes in context.go, using real production defaults: defaultK=5,
// defaultContextBudgetChars=8000) against a naive whole-file-inclusion
// baseline, across a fresh, independently-chosen 20-query set spanning
// "find where X is implemented", "why does Y behave this way", and "what
// calls Z" intents — evaluated against this real repo, not a curated
// testdata/ subset.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tokenEffQuery is one benchmark query with its ground-truth relevant
// file(s), established independently and in advance of running retrieval
// (same discipline as rerankEvalQueries in rerank_eval_test.go) — not
// derived from what retrieval happens to return, which would make the
// comparison meaningless.
type tokenEffQuery struct {
	query         string
	intent        string // "implementation" | "why" | "calls" -- reporting only
	expectedFiles []string
}

var tokenEffQueries = []tokenEffQuery{
	{"where does the daemon open the unix socket", "implementation",
		[]string{"daemon/main.go"}},
	{"how are edit blocks parsed from the model response", "implementation",
		[]string{"editapply/editblock.go"}},
	{"where is the model tier routing decided", "implementation",
		[]string{"daemon/router.go"}},
	{"how does secret skipping work during indexing", "implementation",
		[]string{"daemon/chunker.go", "daemon/index_cmd.go"}},
	{"where are conversation turns stored in sqlite", "implementation",
		[]string{"daemon/memory.go"}},
	{"where in the code is the ZDR refusal string matched", "implementation",
		[]string{"daemon/provider.go"}},
	{"what files does SearchRequest touch", "calls",
		[]string{"protocol/protocol.go", "daemon/search.go", "daemon/server.go"}},
	{"why does the proxy reserve tokens before forwarding the request instead of checking quota after", "why",
		[]string{"proxy/main.go"}},
	{"what calls reserveQuota", "calls",
		[]string{"proxy/main.go"}},
	{"how does the proxy authorize a caller's Mochiii key", "implementation",
		[]string{"proxy/main.go"}},
	{"what happens when Supabase is unreachable during proxy auth", "why",
		[]string{"proxy/main.go"}},
	{"how is the OpenRouter response streamed back to the client", "implementation",
		[]string{"proxy/main.go"}},
	{"where are the read and write timeouts configured for the proxy's http server", "implementation",
		[]string{"proxy/main.go"}},
	{"how does the TUI escalate a prompt to /reason or /refactor", "calls",
		[]string{"clients/tui/chat.go", "clients/tui/stream.go", "daemon/router.go", "daemon/server.go"}},
	{"where is outbound secret scrubbing implemented", "implementation",
		[]string{"daemon/scrub.go"}},
	{"what does the proxy health endpoint report", "implementation",
		[]string{"proxy/main.go"}},
	{"how are files skipped during indexing to avoid leaking secrets", "implementation",
		[]string{"daemon/chunker.go"}},
	{"why is retrieved context wrapped in retrieved_context tags", "why",
		[]string{"daemon/context.go"}},
	{"what happens when a quota reservation would exceed the token limit", "why",
		[]string{"proxy/main.go", "proxy/migrations/0001_reserve_usage.sql"}},
	{"how does the live prompt path differ from the CLI retrieve command", "why",
		[]string{"daemon/context.go", "daemon/index_cmd.go"}},
}

// approxCharsPerToken is the repo's own documented approximation for code
// (see daemon/config.go's defaultContextBudgetChars comment: "code
// averages ~3-4 chars/token"), used here only to render a human-readable
// token estimate alongside the authoritative char counts. Not a real
// tokenizer — the daemon deliberately has none (same comment: pulling one
// in would break the CGO-free-by-design boundary) — so every token figure
// in this report is an estimate, and the char counts are what's actually
// measured.
const approxCharsPerToken = 3.5

type tokenEffResult struct {
	query          string
	intent         string
	expectedFiles  []string
	retrievedFiles []string
	covered        int // how many expectedFiles appear among retrievedFiles
	actualChars    int
	naiveChars     int
	truncated      bool
}

func (r tokenEffResult) classification() string {
	switch {
	case r.covered == len(r.expectedFiles):
		return "FULL HIT"
	case r.covered > 0:
		return "PARTIAL HIT"
	default:
		return "MISS"
	}
}

func TestTokenEfficiencyEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	// ONE INDEX FOR THE WHOLE EVAL SUITE -- see evalcorpus_test.go. This test
	// used to repeat, verbatim, the scan/filter/embed/upsert that
	// TestRerankEvalRetrievalRanking had just finished, for the same ~4,700
	// chunks and therefore the same vectors.
	//
	// THE ONE THING THAT CHANGED WHEN THEY WERE MERGED, stated plainly because
	// it is a change to an instrument: the shared corpus excludes the UNION of
	// the two former exclusion sets, so six files are held out here where one
	// used to be. That is safe for this eval's ground truth by inspection --
	// none of the five newly-excluded files is named in any tokenEffQueries
	// expectedFiles list, and TestSharedCorpusExclusionsAreNotAnswerFiles keeps
	// it that way -- but it does remove five files' worth of DISTRACTORS from
	// the retrieval pool, which can only move the numbers in this eval's
	// favour. The measured before/after is recorded in BACKLOG.md.
	corpus := sharedEvalCorpus(t)
	embedder, store, lexicalStore := corpus.Embedder, corpus.Store, corpus.LexicalStore
	repoRoot, scan := corpus.RepoRoot, corpus.Scan
	ctx := context.Background()
	t.Logf("shared corpus at %s: scanned=%d chunks=%d (self-referential chunks excluded) scan=%s embed=%s",
		repoRoot, scan.FilesScanned, len(scan.Chunks),
		corpus.ScanTime.Round(time.Millisecond), corpus.EmbedTime.Round(time.Millisecond))

	// Real production defaults -- not tuned for this benchmark.
	const topK = defaultK                    // 10 since 2026-08-28
	const budget = defaultContextBudgetChars // 32000 since 2026-08-30

	var results []tokenEffResult
	fmt.Println()
	fmt.Println("=== Token efficiency eval: real retrieval path vs. naive whole-file baseline ===")
	fmt.Printf("(topK=%d, contextBudgetChars=%d -- real production defaults from daemon/config.go)\n", topK, budget)

	for _, q := range tokenEffQueries {
		hits, err := retrieveTopK(ctx, q.query, topK, embedder, store, lexicalStore, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%q): %v", q.query, err)
		}
		kept, truncated := truncateToBudget(hits, budget, false)

		actualChars := 0
		retrievedFileSet := map[string]bool{}
		var retrievedFiles []string
		for i, c := range kept {
			actualChars += len(renderChunk(i+1, c, false))
			if !retrievedFileSet[c.FilePath] {
				retrievedFileSet[c.FilePath] = true
				retrievedFiles = append(retrievedFiles, c.FilePath)
			}
		}

		covered := 0
		for _, f := range q.expectedFiles {
			if retrievedFileSet[f] {
				covered++
			}
		}

		naiveChars := 0
		for _, f := range q.expectedFiles {
			data, err := os.ReadFile(filepath.Join(repoRoot, f))
			if err != nil {
				t.Fatalf("reading ground-truth file %s for naive baseline: %v", f, err)
			}
			naiveChars += len(data)
		}

		r := tokenEffResult{
			query:          q.query,
			intent:         q.intent,
			expectedFiles:  q.expectedFiles,
			retrievedFiles: retrievedFiles,
			covered:        covered,
			actualChars:    actualChars,
			naiveChars:     naiveChars,
			truncated:      truncated,
		}
		results = append(results, r)

		fmt.Printf("\nquery=%q (intent=%s)\n", q.query, q.intent)
		fmt.Printf("  expected files=%v\n", q.expectedFiles)
		fmt.Printf("  retrieved files=%v\n", retrievedFiles)
		fmt.Printf("  coverage=%d/%d -> %s\n", covered, len(q.expectedFiles), r.classification())
		fmt.Printf("  actual context: %d chars (~%.0f tokens), truncated=%t\n", actualChars, float64(actualChars)/approxCharsPerToken, truncated)
		fmt.Printf("  naive whole-file baseline: %d chars (~%.0f tokens)\n", naiveChars, float64(naiveChars)/approxCharsPerToken)
		if r.classification() != "MISS" {
			fmt.Printf("  savings: %.1f%%\n", 100*(1-float64(actualChars)/float64(naiveChars)))
		} else {
			fmt.Printf("  savings: N/A (miss -- no meaningful comparison)\n")
		}
	}

	var fullHits, partialHits, misses int
	var hitActualChars, hitNaiveChars int // FULL HIT only -- the only apples-to-apples comparison
	var allActualChars, allNaiveChars int // every query, including misses -- shown for transparency, not the headline
	for _, r := range results {
		switch r.classification() {
		case "FULL HIT":
			fullHits++
			hitActualChars += r.actualChars
			hitNaiveChars += r.naiveChars
		case "PARTIAL HIT":
			partialHits++
		case "MISS":
			misses++
		}
		allActualChars += r.actualChars
		allNaiveChars += r.naiveChars
	}

	total := len(results)
	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("queries: %d\n", total)
	fmt.Printf("full hit (all expected files retrieved):    %d/%d\n", fullHits, total)
	fmt.Printf("partial hit (some expected files retrieved): %d/%d\n", partialHits, total)
	fmt.Printf("miss (no expected files retrieved):          %d/%d\n", misses, total)
	fmt.Println()
	if fullHits > 0 {
		fmt.Printf("HEADLINE: token savings over %d full-hit quer(ies) (the only queries where 'savings' is a\n", fullHits)
		fmt.Printf("meaningful comparison -- retrieval actually found what it was supposed to):\n")
		fmt.Printf("  actual context total:  %d chars (~%.0f tokens)\n", hitActualChars, float64(hitActualChars)/approxCharsPerToken)
		fmt.Printf("  naive baseline total:  %d chars (~%.0f tokens)\n", hitNaiveChars, float64(hitNaiveChars)/approxCharsPerToken)
		fmt.Printf("  savings: %.1f%%\n", 100*(1-float64(hitActualChars)/float64(hitNaiveChars)))
	} else {
		fmt.Println("HEADLINE: no full hits -- no meaningful savings figure can be reported.")
	}
	fmt.Println()
	fmt.Printf("ALL-QUERY total (including misses/partials, where a 'savings' number can be misleading --\n")
	fmt.Printf("shown for transparency only, NOT the headline number):\n")
	fmt.Printf("  actual context total: %d chars (~%.0f tokens)\n", allActualChars, float64(allActualChars)/approxCharsPerToken)
	fmt.Printf("  naive baseline total: %d chars (~%.0f tokens)\n", allNaiveChars, float64(allNaiveChars)/approxCharsPerToken)
	fmt.Printf("  raw ratio: %.1f%%\n", 100*(1-float64(allActualChars)/float64(allNaiveChars)))
}
