//go:build eval

// See eval_test.go's header for why this is gated behind the "eval" build
// tag (real BGE model + onnxruntime + a freshly built helper binary — not
// part of the fast, offline `go test ./...` path). Run it explicitly with:
//
//	go test -tags eval -run TestRerankEvalRetrievalRanking -v ./...
//
// This is the acceptance test for retrieval RANKING specifically: it indexes
// the actual CodeTerminal repo (not a curated testdata/ subset — the
// measured failures this fixes were found against this real repo, and
// expected files/chunks are real repo-relative paths/line ranges) and runs
// the 9 queries from the two stress tests (5 original implementation-seeking
// queries + the _test.go down-weight pair + the 2 symbol/string queries that
// motivated hybrid retrieval), asserting the fix actually recovers them.
//
// CHUNK-LEVEL, not file-level: exactChunks below names the specific
// chunk(s) (file:startLine-endLine) that actually contain the relevant
// symbol/logic, and TestRerankEvalRetrievalRanking's hit criterion checks
// THAT, not merely whether some chunk from the expected file(s) appears.
// This distinction is load-bearing, not cosmetic — file-level checking is
// exactly what hid the original defect (see below).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// rerankEvalQuery is one query from the measured stress tests, with the
// repo-relative file(s) that count as a file-level match (reported for
// context only) and the specific chunk ID(s) — file:startLine-endLine,
// matching Chunk.ID from chunker.go's chunkContent — that actually contain
// the symbol/logic in question. exactChunks is the real, gating check;
// expectedFiles exists only so the eval's printed table can show the old,
// looser signal alongside the new one.
type rerankEvalQuery struct {
	query         string
	expectedFiles []string
	exactChunks   []string
}

var rerankEvalQueries = []rerankEvalQuery{
	// FILE-LEVEL PASSED, CHUNK-LEVEL WAS A MISS UNTIL NOW: daemon/main.go's
	// top-ranked chunk (1-40) is its package doc comment ("...proxies
	// prompts to it over a Unix domain socket"), which reads as an excellent
	// semantic match for this query without containing the actual
	// net.Listen("unix", ...) call at all — that's at line 129, in a
	// different chunk entirely. Discovered while upgrading this harness to
	// chunk-level checking; the same blind spot that hid the ZDR/
	// SearchRequest failures below was already hiding this one too.
	{"where does the daemon open the unix socket", []string{"daemon/main.go"},
		[]string{"daemon/main.go:91-130", "daemon/main.go:121-160"}},

	// editblock.go moved from daemon/ to editapply/ in an earlier, unrelated
	// refactor (c516479) — this expected path is corrected to match, since
	// it's pre-existing drift, not something the test-file down-weight
	// fixes or causes. ParseEditBlocks starts at line 31, itself the
	// boundary between the first two chunks — either legitimately answers
	// the query.
	{"how are edit blocks parsed from the model response", []string{"editapply/editblock.go"},
		[]string{"editapply/editblock.go:1-40", "editapply/editblock.go:31-70"}},

	// Route (daemon/router.go) starts at line 30, inside the file's only
	// first chunk (router.go is 44 lines total).
	{"where is the model tier routing decided", []string{"daemon/router.go"},
		[]string{"daemon/router.go:1-40"}},

	// Query 4 accepts either file: the failure report itself named both
	// chunker.go (where the actual secret-skipping logic —
	// MatchesSecretName/SkipSecret — lives, in shouldSkipFile at line 165)
	// and index_cmd.go (whose formatSkipCounts skip-count summary at line
	// 279-280 straddles two chunks) as acceptable.
	{"how does secret skipping work during indexing", []string{"daemon/chunker.go", "daemon/index_cmd.go"},
		[]string{"daemon/chunker.go:151-190", "daemon/index_cmd.go:241-280", "daemon/index_cmd.go:271-310"}},

	// The Skill/SkillStore struct definitions and DefaultSkillsDBPath live
	// in skills.go's second chunk (31-70); the CREATE TABLE skills DDL and
	// its surrounding schema at line 128 sit in the chunks straddling it.
	{"where are skills stored in sqlite", []string{"daemon/skills.go"},
		[]string{"daemon/skills.go:31-70", "daemon/skills.go:91-130", "daemon/skills.go:121-160"}},

	// Added for the _test.go down-weight fix (FileClassTest, rerank.go):
	// the measured live-repo failure this fix targets — provider.go never
	// made the top-5 at all because provider_test.go/config_test.go
	// out-ranked it despite identical class weight. Implementation-seeking,
	// so the test down-weight must apply here. This is one of the two
	// queries that motivated hybrid retrieval in the first place:
	// zdrRefusalSubstrings + isZDRRoutingRefusal live at lines 103-120,
	// inside chunk 91-130 — which the file-level check alone couldn't tell
	// apart from provider.go's OTHER chunks (e.g. the ErrZDRRefused
	// sentinel/doc comment) that rank better semantically but don't contain
	// the actual matched substrings.
	{"where in the code is the ZDR refusal string matched, and what substring does it match on?",
		[]string{"daemon/provider.go"},
		[]string{"daemon/provider.go:91-130"}},

	// The paired regression guard for the same fix: a genuinely
	// test-seeking query must still find the test files. looksTestSeeking
	// must recognize this and skip the down-weight, or this query would
	// start failing the moment the down-weight above is added. The relevant
	// isZDRRoutingRefusal test funcs span provider_test.go lines 261-311
	// (three overlapping chunks); the ZDR config tests span config_test.go
	// lines 19-31 (two overlapping chunks).
	{"how is the ZDR refusal detection logic tested end to end",
		[]string{"daemon/provider_test.go", "daemon/config_test.go"},
		[]string{
			"daemon/provider_test.go:241-280", "daemon/provider_test.go:271-310", "daemon/provider_test.go:301-340",
			"daemon/config_test.go:1-40", "daemon/config_test.go:31-70",
		}},

	// The second symbol/string query that motivated hybrid retrieval: a
	// natural-language question with essentially no shared vocabulary with
	// any single chunk's dominant semantic content, whose three real answer
	// chunks (the struct definition, its search implementation, and its
	// server-side dispatch) were measured at raw semantic ranks #273, #352,
	// and #164 out of 708 respectively — nowhere near the ~30-candidate
	// semantic rerank pool for k=5.
	{"where is the ZDR refusal string matched", []string{"daemon/provider.go"},
		[]string{"daemon/provider.go:91-130"}},
	{"what files does SearchRequest touch",
		[]string{"protocol/protocol.go", "daemon/search.go", "daemon/server.go"},
		[]string{
			"protocol/protocol.go:271-310",
			"daemon/search.go:91-130", "daemon/search.go:121-153",
			"daemon/server.go:391-430", "daemon/server.go:421-460",
		}},
}

// evalSelfReferenceFiles are repo-relative paths this eval test itself must
// exclude from the indexed corpus: they store the literal query strings
// above (or, for lexicalstore_test.go, one used as a unit-test fixture) as
// Go string literals, so when the real repo is indexed they'd otherwise
// contribute a chunk that's a near-perfect lexical (and often semantic)
// match for its own query — an oracle-leaks-into-the-corpus problem, not a
// property of the retrieval design under test. rerank_test.go's
// TestLooksTestSeeking already documents the same concern for the semantic
// tier alone ("this file is itself indexed by that real-repo eval harness,
// and a near-verbatim copy of an eval query embeds as a near-duplicate of
// it"); the lexical tier makes the effect far stronger (an exact substring
// match beats any embedding similarity), so exclusion — not just rewording
// — is required for this test to measure the real defect rather than its
// own reflection.
var evalSelfReferenceFiles = map[string]bool{
	"daemon/rerank_eval_test.go":  true,
	"daemon/lexicalstore_test.go": true,
}

// indexRepoExcludingSelfReference scans root, drops any chunk whose
// FilePath is in evalSelfReferenceFiles, and embeds/upserts the rest into
// store and lexicalStore in indexEmbedBatchSize-sized batches — the same
// batching buildIndex (index_cmd.go) uses, duplicated here only because
// buildIndex has no hook to filter chunks between scanning and embedding.
func indexRepoExcludingSelfReference(ctx context.Context, root string, embedder Embedder, store VectorStore, lexicalStore LexicalStore, logger *log.Logger) (*ScanResult, error) {
	scan, err := ScanWorkspace(root)
	if err != nil {
		return nil, fmt.Errorf("scanning workspace: %w", err)
	}

	filtered := scan.Chunks[:0]
	for _, c := range scan.Chunks {
		if !evalSelfReferenceFiles[c.FilePath] {
			filtered = append(filtered, c)
		}
	}
	scan.Chunks = filtered

	for start := 0; start < len(scan.Chunks); start += indexEmbedBatchSize {
		end := min(start+indexEmbedBatchSize, len(scan.Chunks))
		batch := scan.Chunks[start:end]

		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding batch [%d:%d]: %w", start, end, err)
		}
		for i := range batch {
			batch[i].Vector = vecs[i]
		}

		if err := store.Upsert(ctx, batch); err != nil {
			return nil, fmt.Errorf("upserting batch [%d:%d]: %w", start, end, err)
		}
		if err := lexicalStore.Upsert(ctx, batch); err != nil {
			return nil, fmt.Errorf("upserting lexical batch [%d:%d]: %w", start, end, err)
		}
	}
	return scan, nil
}

func matchesAny(path string, candidates []string) bool {
	for _, c := range candidates {
		if path == c {
			return true
		}
	}
	return false
}

// chunkID mirrors chunker.go's chunkContent ID format exactly.
func chunkID(c Chunk) string {
	return fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine)
}

// hitsExactChunk reports whether any of hits' first n entries has an ID
// matching one of exactChunks.
func hitsExactChunk(hits []Chunk, n int, exactChunks []string) bool {
	for _, h := range hits[:min(n, len(hits))] {
		if matchesAny(chunkID(h), exactChunks) {
			return true
		}
	}
	return false
}

// runEvalPass runs every query in rerankEvalQueries through retrieveTopK
// with the given lexicalStore (nil for semantic-only, a real store for
// hybrid) and returns, per query, whether the exact chunk containing the
// relevant symbol/logic reached the final top-k (k=displayK=5, the real
// production default — not an arbitrary top-3 subset of a wider fetch,
// since what matters is whether the chunk actually gets injected into the
// prompt).
func runEvalPass(ctx context.Context, t *testing.T, embedder Embedder, store VectorStore, lexicalStore LexicalStore, label string) []bool {
	t.Helper()
	const displayK = 5

	hitFlags := make([]bool, len(rerankEvalQueries))
	fmt.Println()
	fmt.Printf("=== Retrieval ranking eval: %s ===\n", label)
	for i, q := range rerankEvalQueries {
		hits, err := retrieveTopK(ctx, q.query, displayK, embedder, store, lexicalStore, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%q): %v", q.query, err)
		}

		exactHit := hitsExactChunk(hits, displayK, q.exactChunks)
		fileHit := false
		for _, h := range hits[:min(displayK, len(hits))] {
			if matchesAny(h.FilePath, q.expectedFiles) {
				fileHit = true
			}
		}
		hitFlags[i] = exactHit

		// Guardrail for Fix 11: production folds same-file overlapping chunks
		// into contiguous spans before rendering (mergeAdjacentChunks), so this
		// re-scores the SAME hits through that fold. It must never be worse
		// than the unmerged verdict -- a merged span only ever covers MORE
		// lines, so a query that hit before must still hit. Graded by
		// containment (rankOfChunk, edit_eval_test.go), because merging changes
		// chunk IDs by design.
		mergedHit := rankOfChunk(mergeAdjacentChunks(hits), q.exactChunks) != 0
		if exactHit && !mergedHit {
			t.Errorf("query %q: hit before merging and MISSES after -- Fix 11 regressed question-shaped recall", q.query)
		}

		mark := "MISS"
		if exactHit {
			mark = "hit"
		}
		fmt.Printf("\n%d. query=%q\n   expected files=%v exact chunks=%v\n   chunk-level=%s file-level=%t merged-path=%t\n", i+1, q.query, q.expectedFiles, q.exactChunks, mark, fileHit, mergedHit)
		for j, h := range hits {
			fmt.Printf("   %d. %-40s class=%-6s raw=%.4f weighted=%.4f\n", j+1, chunkID(h), h.Class, h.RawScore, h.Score)
		}
	}
	return hitFlags
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
	// measured, real-repo failures. Both the semantic (chromem) and lexical
	// (FTS5) stores are built from the same chunks, exactly as production
	// indexing does — except this test scans and filters manually instead
	// of calling buildIndex directly, to drop self-referential chunks (see
	// evalSelfReferenceFiles) before they're ever embedded/upserted.
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	indexDir := filepath.Join(t.TempDir(), "index")
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	lexicalStore, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer lexicalStore.Close()

	ctx := context.Background()
	scan, err := indexRepoExcludingSelfReference(ctx, repoRoot, embedder, store, lexicalStore, logger)
	if err != nil {
		t.Fatalf("indexing repo: %v", err)
	}
	t.Logf("indexed repo root %s: scanned=%d chunks=%d (self-referential chunks excluded)", repoRoot, scan.FilesScanned, len(scan.Chunks))

	semanticOnlyHits := runEvalPass(ctx, t, embedder, store, nil, "SEMANTIC-ONLY (lexicalStore=nil, today's behavior)")
	hybridHits := runEvalPass(ctx, t, embedder, store, lexicalStore, "HYBRID (semantic + lexical, fused via RRF)")

	fmt.Println()
	fmt.Println("=== Before/after summary (chunk-level hit = exact symbol-containing chunk reached top-5) ===")
	fmt.Printf("%-3s %-70s %-12s %-8s\n", "#", "query", "semantic", "hybrid")
	var semanticOnlyCount, hybridCount int
	var regressions []string
	for i, q := range rerankEvalQueries {
		if semanticOnlyHits[i] {
			semanticOnlyCount++
		}
		if hybridHits[i] {
			hybridCount++
		}
		before := "MISS"
		if semanticOnlyHits[i] {
			before = "hit"
		}
		after := "MISS"
		if hybridHits[i] {
			after = "hit"
		}
		fmt.Printf("%-3d %-70s %-12s %-8s\n", i+1, truncateEval(q.query, 70), before, after)

		if semanticOnlyHits[i] && !hybridHits[i] {
			regressions = append(regressions, q.query)
		}
	}
	total := len(rerankEvalQueries)
	fmt.Println()
	fmt.Printf("semantic-only chunk-level recall: %d/%d\n", semanticOnlyCount, total)
	fmt.Printf("hybrid chunk-level recall:         %d/%d\n", hybridCount, total)
	fmt.Println()

	if len(regressions) > 0 {
		t.Errorf("hybrid retrieval REGRESSED %d quer(ies) that passed semantic-only: %v", len(regressions), regressions)
	}

	// The two queries that actually motivated this feature (see the design
	// doc/PR description) MUST hit under hybrid retrieval — this is the
	// feature's real acceptance bar, not "every query in the set,
	// unconditionally". Indices: 5 = the original verbose ZDR-string
	// phrasing, 7 = the live-failing short ZDR-string phrasing, 8 =
	// SearchRequest cross-file.
	mustHit := []int{5, 7, 8}
	for _, i := range mustHit {
		if !hybridHits[i] {
			t.Errorf("query %d (%q) — one of the two measured live failures this feature exists to fix — is still MISS under hybrid retrieval", i+1, rerankEvalQueries[i].query)
		}
	}

	// Query 1 (index 0, "where does the daemon open the unix socket") is a
	// KNOWN, PRE-EXISTING, SEPARATE gap discovered while upgrading this
	// harness to chunk-level checking (see that query's comment above): the
	// package doc comment in main.go:1-40 literally contains "Unix" and
	// "socket", so it wins on BOTH the semantic tier (reads as an excellent
	// paraphrase of the query) AND the lexical tier (contains the same
	// substrings the real net.Listen("unix", ...) call does) — hybrid
	// retrieval cannot distinguish "a comment describing the concept" from
	// "the code that does it" when both literally contain the same words.
	// Fixing that needs chunk-level content classification (not just
	// per-file classification by extension, fileclass.go's current design),
	// which is out of this feature's scope (retrieval MERGE, not
	// reclassification granularity) — flagged here, deliberately not gated,
	// so it isn't silently lost.
	if hybridHits[0] {
		t.Logf("NOTE: query 1 unexpectedly now hits — the known pre-existing doc-comment-vs-implementation gap may have been incidentally resolved; safe to add index 0 to mustHit above if this is stable")
	} else {
		t.Logf("KNOWN GAP (not gated, pre-existing, out of scope): query 1 (%q) still misses — see comment above mustHit for why", rerankEvalQueries[0].query)
	}
}

func truncateEval(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
