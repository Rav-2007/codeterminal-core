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
	"sort"
	"strings"
	"testing"
)

// rerankEvalQuery is one query from the measured stress tests, with the
// repo-relative file(s) that count as a file-level match (reported for
// context only) and the specific chunk ID(s) — file:startLine-endLine,
// matching Chunk.ID from chunker.go's chunkContent — that actually contain
// the symbol/logic in question. exactChunks is the real, gating check;
// expectedFiles exists only so the eval's printed table can show the old,
// looser signal alongside the new one.
// anchor is a literal substring of the code that actually answers the query. It
// is what makes exactChunks CHECKABLE rather than merely asserted: the harness
// verifies, before running any query, that the anchor really does appear inside
// one of the named chunks.
//
// This exists because the 2026-07-30 launch-gate review found this eval RED at
// 4/9 and the cause was not retrieval at all -- it was that exactChunks had gone
// STALE. Chunk IDs are file:startLine-endLine, so every edit to a named file
// shifts them, and five of the nine queries were pointing at line ranges whose
// contents had moved. Query 8 ("where is the ZDR refusal string matched")
// expected daemon/provider.go:91-130, which holds the chatCompletionChunk struct;
// the ZDR matching it names lives ~150 lines further down. Retrieval was
// returning the correct chunk and being scored wrong.
//
// A stale expectation and a retrieval regression are indistinguishable in the
// pass/fail signal but demand opposite responses, so the harness now tells them
// apart by name. Without this the eval measures how much the repo has been
// edited since the expectations were written.
type rerankEvalQuery struct {
	query string
	// expectedFiles are the files that legitimately answer this query. DECLARED,
	// because a file changes name only when something is deliberately moved, and
	// that is a change a human should have to acknowledge here.
	expectedFiles []string
	// anchors are literal substrings of the code that actually answers the
	// query -- at least one per expected file that has a chunk-level answer.
	// DECLARED, because what counts as the answer is a judgement, not a fact the
	// harness can derive.
	anchors []string
}

// resolveExactChunks computes each query's chunk-level ground truth FROM THE
// INDEX, rather than reading it from a list written by hand.
//
// WHY THE GROUND TRUTH IS NOW DERIVED. It used to be a literal list of chunk
// IDs, and a chunk ID is file:startLine-endLine -- so every edit to a named
// file shifted the ranges and the expectation silently stopped describing the
// code. The 2026-07-30 launch-gate review found this eval red at 4/9 with
// retrieval working perfectly; the answer key had rotted. A staleness CHECK was
// added then, which was the right first move: it made the harness say which of
// the two it was. But a check only converts a wrong number into a chore, and
// the chore came due again five queries at a time -- on 2026-08-07 the scheduled
// job was red with STALE EXPECTATION on 5 of 9, one of them because a startup
// refactor had moved the code the first query points at.
//
// Deriving removes the failure mode instead of reporting it. expectedFiles and
// anchors are stable facts about the codebase, and the volatile part -- which
// chunk holds the anchor today -- is computed from the same index the queries
// are run against, every run.
//
// It is NOT a weakening. The check is still chunk-level, which is the whole
// point of this eval (file-level checking is what hid the original defect): the
// derived set is the chunks that contain the anchor, not every chunk in the
// file. The three guards below are what keep it that way.
func resolveExactChunks(t *testing.T, chunks []Chunk) [][]string {
	t.Helper()

	// PER ANCHOR, not per query, because "is this anchor specific?" is the
	// property that matters and it does not get weaker just because a query has
	// three expected files.
	//
	// A single line falls inside at most two chunks (40-line windows on a
	// 30-line stride), so an anchor occurring once yields 1-2. Three allows for
	// one that legitimately appears twice, and refuses to let the ground truth
	// quietly become "anywhere in the file" -- which would make every query pass
	// and mean nothing.
	const maxChunksPerAnchor = 3

	resolved := make([][]string, len(rerankEvalQueries))
	for i, q := range rerankEvalQueries {
		if len(q.anchors) == 0 {
			t.Errorf("query %d (%q) declares no anchors, so it has no checkable "+
				"chunk-level ground truth at all", i+1, q.query)
			continue
		}

		seen := make(map[string]bool)
		var ids []string
		for _, a := range q.anchors {
			var here []string
			elsewhere := make(map[string]bool)
			for _, c := range chunks {
				if !strings.Contains(c.Content, a) {
					continue
				}
				if matchesAny(c.FilePath, q.expectedFiles) {
					here = append(here, chunkID(c))
				} else {
					elsewhere[c.FilePath] = true
				}
			}

			if len(here) == 0 {
				t.Errorf("query %d (%q): anchor %q appears in no indexed chunk of %v. This is a "+
					"DECLARED fact that stopped being true -- the code was moved or renamed -- "+
					"and it is NOT a retrieval regression. It currently appears in: %v",
					i+1, q.query, a, q.expectedFiles, sortedFileNames(elsewhere))
				continue
			}
			if len(here) > maxChunksPerAnchor {
				t.Errorf("query %d (%q): anchor %q resolves to %d chunks (%v), past the %d "+
					"ceiling. An anchor that broad makes the ground truth 'somewhere in the "+
					"file', which is the file-level check this eval exists to be stricter than.",
					i+1, q.query, a, len(here), here, maxChunksPerAnchor)
			}
			// Reported, never failed. A shared helper name legitimately appears
			// in several callers; it matters only if one of them should have been
			// declared in expectedFiles.
			if len(elsewhere) > 0 {
				t.Logf("query %d (%q): anchor %q also appears outside expectedFiles, in %v -- "+
					"harmless unless one of those is a better answer than what is declared",
					i+1, q.query, a, sortedFileNames(elsewhere))
			}

			for _, id := range here {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		sort.Strings(ids)
		resolved[i] = ids
	}
	return resolved
}

func sortedFileNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var rerankEvalQueries = []rerankEvalQuery{
	// THE ONE REMAINING KNOWN GAP (not gated -- see the note after mustHit).
	// The top-ranked chunk for this query is a package doc comment that reads as
	// an excellent semantic match AND contains the same words the real call
	// does, without containing the call.
	//
	// The answer used to be in daemon/main.go and is now in protocol: the
	// transport seam moved every net.Listen behind protocol.Listen so the
	// Windows named-pipe implementation could sit beside it. The old
	// expectation pointed at daemon/main.go:181-220 and had been scoring
	// correct retrieval as a miss ever since.
	{"where does the daemon open the unix socket",
		[]string{"protocol/transport_unix.go"},
		[]string{`net.Listen("unix"`}},

	// editblock.go moved from daemon/ to editapply/ in an earlier, unrelated
	// refactor (c516479).
	{"how are edit blocks parsed from the model response",
		[]string{"editapply/editblock.go"},
		[]string{"func ParseEditBlocks"}},

	// The tier decision is made in Route, not in the const/type block at the top
	// of the file -- which is what the file-level check could not tell apart.
	{"where is the model tier routing decided",
		[]string{"daemon/router.go"},
		[]string{"func Route(cfg *Config"}},

	// The actual secret-skipping decision is editapply.MatchesSecretName, called
	// from shouldSkipFile. index_cmd.go only REPORTS skip counts, so it stays an
	// acceptable file-level match with no chunk-level answer of its own.
	{"how does secret skipping work during indexing",
		[]string{"daemon/chunker.go", "daemon/index_cmd.go"},
		[]string{"editapply.MatchesSecretName"}},

	// Two things legitimately answer this: where the DB lives, and its schema.
	{"where are conversation turns stored in sqlite",
		[]string{"daemon/memory.go"},
		[]string{"func OpenMemoryStore", "CREATE TABLE IF NOT EXISTS"}},

	// Added for the _test.go down-weight fix (FileClassTest, rerank.go): the
	// measured live-repo failure this fix targets -- provider.go never made the
	// top-5 at all because provider_test.go/config_test.go out-ranked it despite
	// identical class weight. Implementation-seeking, so the test down-weight
	// must apply here. One of the two queries that motivated hybrid retrieval:
	// the answer is the substring TABLE, which the file-level check could not
	// tell apart from provider.go's other chunks (the ErrZDRRefused sentinel and
	// its doc comment) that rank better semantically and contain none of it.
	{"where in the code is the ZDR refusal string matched, and what substring does it match on?",
		[]string{"daemon/provider.go"},
		[]string{"zdrRefusalSubstrings"}},

	// The paired regression guard for the same fix: a genuinely test-seeking
	// query must still find the test files, or it would start failing the moment
	// the down-weight above is added. Two anchors, because two files carry a
	// real chunk-level answer.
	{"how is the ZDR refusal detection logic tested end to end",
		[]string{"daemon/provider_test.go", "daemon/config_test.go"},
		[]string{
			"func TestIsZDRRoutingRefusal_MatchesKnownPhrasings",
			"func TestZDRConfig_ZeroValueResolvesToStrictEnforcement",
		}},

	{"where is the ZDR refusal string matched",
		[]string{"daemon/provider.go"},
		[]string{"zdrRefusalSubstrings"}},

	// The second symbol/string query that motivated hybrid retrieval: a
	// natural-language question with essentially no shared vocabulary with any
	// single chunk's dominant semantic content, whose three real answer chunks
	// (the struct, its search implementation, and its server-side dispatch) were
	// measured at raw semantic ranks #273, #352 and #164 out of 708 -- nowhere
	// near the ~30-candidate rerank pool for k=5.
	{"what files does SearchRequest touch",
		[]string{"protocol/protocol.go", "daemon/search.go", "daemon/server.go"},
		[]string{
			"type SearchRequest struct",
			"func (s *MemoryStore) SearchTurns",
			// BOTH halves of the server-side dispatch. isSearchRequest is the
			// sniffer that routes the message and handleSearch is what runs it;
			// the expectation this replaces named two adjacent server.go chunks
			// for exactly that reason, and listing only the handler was a
			// transcription slip that scored a correct hit as a miss.
			"func isSearchRequest",
			"func (s *Server) handleSearch",
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
func runEvalPass(ctx context.Context, t *testing.T, embedder Embedder, store VectorStore, lexicalStore LexicalStore, exact [][]string, label string) []bool {
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

		exactHit := hitsExactChunk(hits, displayK, exact[i])
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
		mergedHit := rankOfChunk(mergeAdjacentChunks(hits), exact[i]) != 0
		if exactHit && !mergedHit {
			t.Errorf("query %q: hit before merging and MISSES after -- Fix 11 regressed question-shaped recall", q.query)
		}

		mark := "MISS"
		if exactHit {
			mark = "hit"
		}
		fmt.Printf("\n%d. query=%q\n   expected files=%v exact chunks=%v\n   chunk-level=%s file-level=%t merged-path=%t\n", i+1, q.query, q.expectedFiles, exact[i], mark, fileHit, mergedHit)
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

	// The chunk-level ground truth, computed from the index that was just built
	// rather than read from a list of line ranges written weeks ago. See
	// resolveExactChunks.
	exact := resolveExactChunks(t, scan.Chunks)

	semanticOnlyHits := runEvalPass(ctx, t, embedder, store, nil, exact, "SEMANTIC-ONLY (lexicalStore=nil, today's behavior)")
	hybridHits := runEvalPass(ctx, t, embedder, store, lexicalStore, exact, "HYBRID (semantic + lexical, fused via RRF)")

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

	// A floor on overall recall, so a regression that spares the three gated
	// queries is still caught.
	//
	// MEASURED 2026-07-30 against this repo: 8/9 under both hybrid and
	// semantic-only, the one miss being the known query-1 gap documented below.
	// This restores the figure fuseRRF's doc comment records from the feature's
	// original grid search; the intervening 4/9 was a STALE-HARNESS artifact,
	// not a retrieval regression (see rerankEvalQuery.anchor).
	//
	// It is a FLOOR, not an equality: a change that improves recall should not
	// fail. Raise it when a real improvement makes 8/9 the new normal.
	if hybridCount < evalChunkRecallFloor {
		t.Errorf("hybrid chunk-level recall %d/%d is below the floor of %d/%d measured on "+
			"2026-07-30. Read resolveExactChunks' output first. If it reported an anchor that "+
			"appears in no chunk of its expectedFiles, the DECLARED half of the ground truth "+
			"has gone stale (code was moved or renamed) and retrieval is fine. If it reported "+
			"nothing, this is a real retrieval regression: the derived half cannot go stale, "+
			"because it is computed from the index this run just built",
			hybridCount, total, evalChunkRecallFloor, total)
	}
	if semanticOnlyCount > hybridCount {
		t.Errorf("hybrid recall %d/%d is WORSE than semantic-only %d/%d -- fusion is losing "+
			"results the semantic tier alone finds", hybridCount, total, semanticOnlyCount, total)
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

// evalChunkRecallFloor is the minimum chunk-level recall (out of the 9 queries)
// TestRerankEvalRetrievalRanking accepts, in the same named-constant style as
// evalTop3RecallThreshold in eval_test.go. Measured at 8/9 on 2026-07-30; the
// single miss is the known doc-comment-vs-implementation gap on query 1.
const evalChunkRecallFloor = 8

func truncateEval(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
