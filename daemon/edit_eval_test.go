//go:build eval

// See eval_test.go's header for why this is gated behind the "eval" build
// tag (real BGE model + onnxruntime + a freshly built helper binary — not
// part of the fast, offline `go test ./...` path). Run it explicitly with:
//
//	go test -tags eval -run TestEditShapedRetrievalEval -v ./...
//
// ⚠️ READ THIS BEFORE READING THE NUMBER BELOW.
//
// rerank_eval_test.go and token_efficiency_eval_test.go measure retrieval
// against LOCATE-shaped queries ("where is X implemented") — clean,
// hand-phrased natural-language questions. That is not the query shape the
// product's core action (fixing a failing test/build) actually produces.
// This file measures a DIFFERENT, narrower thing: given the RAW text a real
// `go vet`/`go test` failure prints, does retrieval hand the model the file
// it needs to edit? Two constraints are load-bearing and were proven, not
// assumed, during the investigation this file's cases came from (see
// BACKLOG.md item (b) and the P3 convergence-experiment follow-up):
//
//  1. The query MUST be the raw captured go vet/go test failure text, not a
//     paraphrase. A clean hand-written sentence describing the same bug
//     scores very differently (usually better) than the tool's real output
//     — testing the paraphrase would silently measure the wrong thing.
//  2. The corpus indexed MUST be the PRE-FIX tree (the commit that fixed
//     the bug reverted on top of current HEAD), not HEAD. The fix commit is
//     frequently what adds the comments/structure that make a chunk easy to
//     find — indexing HEAD would test retrieval against an answer it never
//     has to find in production, where the bug is still unfixed.
//
// ⚠️ RESOLUTION LIMIT — n=4, not n=5. This eval's own population is small
// enough to be read as "the fix worked" or "the fix failed" on noise, and
// smaller than intended. It was independently re-mined in this session (the
// prior session's exact same working set was not recoverable — its scratch
// notes lived in a worktree that no longer exists on this machine) by the
// same method the prior diagnosis describes: enumerate every commit that
// MODIFIES both an existing _test.go file and its paired non-test source
// file in the same commit, keep only the ones that are genuine defect
// fixes (not new-feature commits — reverting a brand-new feature doesn't
// demonstrate a pre-existing bug, it just deletes the feature), and
// mechanically test-revert each one against current HEAD. Five commits
// matched that filter. One (2ee3545, "Fix embedding timeout: batch
// buildIndex") does NOT revert cleanly — it conflicts with later rewrites
// of daemon/index_cmd.go, the same "later commits rewrote the file" failure
// mode the prior diagnosis hit on index_cmd.go/proxy/main.go. It is
// EXCLUDED, not faked. The remaining four are real, revert cleanly (one
// with a single doc-only conflict in BACKLOG.md, auto-resolved by keeping
// HEAD's copy — the code files under test had no conflict), and are used
// as-is. Do NOT read 4/4 or 2/4 as a general retrieval quality number —
// read it only as "does the specific H4 mechanism regression, and does it
// alone, explain these four."
//
// A fifth genuine case would sharpen this real. Growing the set
// opportunistically as real fix commits land (per the prior diagnosis's
// §10.4c note) is the right way to do that — not padding with synthetic
// bugs.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// editEvalCase is one real, git-derived edit-shaped bug: a commit that
// fixed a genuine defect in already-shipped code, in the same commit as a
// modification to that code's existing test file. Reverting fixCommit and
// restoring testFile to its post-fix (HEAD) content reconstructs exactly
// the state a developer/model would face while the bug was still live: a
// test (or a build) that fails, for a reason a human can read straight out
// of go vet/go test's own output.
type editEvalCase struct {
	name string

	// fixCommit is reverted on top of current HEAD in a scratch worktree to
	// reconstruct the pre-fix source. Full hash, not shortened -- this must
	// keep resolving even if a future rebase makes a short hash ambiguous.
	fixCommit string

	// testFile is restored to its HEAD (post-fix) content after the revert,
	// so the captured failure is real: a test that asserts the FIXED
	// behavior, run against the UNFIXED source.
	testFile string

	// buildDir is the module directory (repo-relative) go vet/go test runs
	// in -- daemon, editapply, and clients/tui are separate Go modules
	// (go.work), so this must match fixCommit's own module.
	buildDir string

	// runFilter narrows `go test -run` to the test(s) this case's fix
	// touched, so capturing the failure doesn't run every test in the
	// module. Only used if `go vet` itself reports no error (i.e. the bug
	// is a wrong-answer/test-assertion failure, not a compile failure).
	runFilter string

	// exactChunks are the chunk IDs (file:startLine-endLine, chunkContent's
	// ID format) that actually contain the code the fix touched, computed
	// from fixCommit's own diff hunk headers (the pre-fix side, "-N,M") and
	// this repo's real chunkLines=40/overlapLines=10 windowing -- not
	// eyeballed. A hit is any of these landing in the real top-5.
	exactChunks []string

	note string
}

var editEvalCases = []editEvalCase{
	{
		name:      "helperproc-env-allowlist",
		fixCommit: "3d5647a567f10160f888a59db41e4902d3df6ed2",
		testFile:  "daemon/helperproc_test.go",
		buildDir:  "daemon",
		runFilter: "TestHelperProcess_RestartPolicyIsBounded",
		exactChunks: []string{
			"daemon/helperproc.go:31-70",   // HelperProcess struct -- extraEnv field the fix adds
			"daemon/helperproc.go:121-160", // spawnLocked -- where cmd.Env = append(helperEnv(), h.extraEnv...) belongs
		},
		note: "pre-fix spawnLocked leaves cmd.Env nil (inherits the daemon's FULL env, incl. API keys, into the helper subprocess); fix adds helperEnv() + HelperProcess.extraEnv",
	},
	{
		name:      "mac-eexist-addrinuse",
		fixCommit: "10b0e0a036a4a053c96c3e5fa8e9b9cd5c07c9f2",
		testFile:  "daemon/addrinuse_unix_test.go",
		buildDir:  "daemon",
		runFilter: "TestIsAddrInUse_ReturnsTrueForEEXIST|TestIsAddrInUse_StillRejectsUnrelatedErrnos",
		exactChunks: []string{
			"daemon/addrinuse_unix.go:1-40",
		},
		note: "macOS returns EEXIST when bind fails due to address in use; pre-fix only EADDRINUSE was checked",
	},
	{
		name:      "watcher-symlink-escape",
		fixCommit: "0f3a411680f5676cfa2a81e48d0620c813820ade",
		testFile:  "daemon/watcher_test.go",
		buildDir:  "daemon",
		runFilter: "TestWorkspaceWatcher_RefusesToWatchThroughASymlinkedDirectory",
		exactChunks: []string{
			"daemon/watcher.go:91-130",
			"daemon/watcher.go:101-140",
		},
		note: "watcher used os.Stat instead of os.Lstat on Create events, watching symlinked directories outside the workspace",
	},
	{
		name:      "undo-file-separator",
		fixCommit: "80bd2eff547efb667ce73afa8d0a35ef2e7a8f60",
		testFile:  "daemon/displayrel_test.go",
		buildDir:  "daemon",
		runFilter: "TestUndoOutputAlwaysGoesThroughDisplayRel",
		exactChunks: []string{
			"daemon/apply_cmd.go:341-380",
			"daemon/apply_cmd.go:371-410",
			"daemon/apply_cmd.go:521-560",
			"daemon/apply_cmd.go:591-630",
			"daemon/apply_cmd.go:631-670",
		},
		note: "undo report named files with backslashes on Windows; fix adds and uses displayRel() (filepath.ToSlash)",
	},
}

// editEvalSelfReferenceFiles extends rerank_eval_test.go's
// evalSelfReferenceFiles for this file's own oracle-leak risk: its case
// table embeds the exact chunk IDs and file paths that are the ground
// truth, so if this file were indexed alongside the corpus it would be a
// near-perfect lexical match for its own queries.
func init() {
	evalSelfReferenceFiles["daemon/edit_eval_test.go"] = true
}

// gitRevertPreFixTree creates a scratch git worktree at repoRoot's current
// HEAD, reverts fixCommit in it, and restores testFile to its HEAD (post-
// fix) content -- reconstructing exactly the tree a developer/model would
// face while fixCommit's bug was still live. Any conflict outside a .go
// file (e.g. BACKLOG.md prose describing the very fix being reverted) is
// resolved by keeping the HEAD copy, since it has no bearing on the code
// under test; a conflict IN a .go file fails the case loudly rather than
// faking a pre-fix state, per this eval's own discipline (see the P3
// convergence experiment, which excluded two cases outright for exactly
// this reason instead of hand-authoring a synthetic revert).
func gitRevertPreFixTree(t *testing.T, repoRoot, fixCommit, testFile string) string {
	t.Helper()

	wt := filepath.Join(t.TempDir(), "prefix-worktree")
	if out, err := exec.Command("git", "-C", repoRoot, "worktree", "add", "--detach", "-q", wt, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", wt).CombinedOutput(); err != nil {
			// The worktree may be left mid-revert (we never commit or abort
			// it -- see below); fall back to a plain removal + prune so a
			// failed case doesn't leak scratch worktrees across runs.
			os.RemoveAll(wt)
			exec.Command("git", "-C", repoRoot, "worktree", "prune").Run()
			t.Logf("git worktree remove %s (falling back to manual cleanup): %v\n%s", wt, err, out)
		}
	})

	revertCmd := exec.Command("git", "revert", "--no-commit", "--no-edit", fixCommit)
	revertCmd.Dir = wt
	revertOut, revertErr := revertCmd.CombinedOutput()

	unmergedOut, err := exec.Command("git", "-C", wt, "diff", "--name-only", "--diff-filter=U").CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --diff-filter=U: %v\n%s", err, unmergedOut)
	}
	for _, f := range strings.Fields(string(unmergedOut)) {
		if strings.HasSuffix(f, ".go") {
			t.Fatalf("case %s: fixCommit %s does not revert cleanly -- real conflict in %s (not a doc file), refusing to fake a pre-fix state:\n%s", t.Name(), fixCommit, f, revertOut)
		}
		// Non-Go conflict (docs, etc.): keep HEAD's copy, it has no bearing
		// on the code under test.
		if out, err := exec.Command("git", "-C", wt, "checkout", "HEAD", "--", f).CombinedOutput(); err != nil {
			t.Fatalf("resolving non-Go conflict in %s: %v\n%s", f, err, out)
		}
		if out, err := exec.Command("git", "-C", wt, "add", f).CombinedOutput(); err != nil {
			t.Fatalf("git add %s: %v\n%s", f, err, out)
		}
	}
	if revertErr != nil && len(unmergedOut) == 0 {
		// revert failed for a reason other than a mergeable conflict.
		t.Fatalf("git revert %s: %v\n%s", fixCommit, revertErr, revertOut)
	}

	if out, err := exec.Command("git", "-C", wt, "checkout", "HEAD", "--", testFile).CombinedOutput(); err != nil {
		t.Fatalf("restoring %s to post-fix content: %v\n%s", testFile, err, out)
	}

	return wt
}

// captureRealFailureText runs `go test -run runFilter ./...` against the
// pre-fix tree and returns the raw combined stdout+stderr exactly as the
// tool printed it: no trimming beyond leading/trailing whitespace, no
// paraphrasing. Deliberately `go test`, not `go vet`: an earlier version of
// this harness ran vet first as a speed shortcut, but vet's own failure
// format omits the "[pkg.test]"/"[build failed]" annotations `go test`
// itself prints for a compile failure -- exactly the substrings this file's
// cases exist to probe (see looksTestSeeking's false-positive shape in
// rerank.go). Using vet would have silently measured a DIFFERENT, easier
// query than the one production/a developer actually sees.
func captureRealFailureText(t *testing.T, wt, buildDir, runFilter string) string {
	t.Helper()

	testCmd := exec.Command("go", "test", "-run", runFilter, "./...")
	testCmd.Dir = filepath.Join(wt, buildDir)
	out, err := testCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("case in %s: expected go test to fail against the pre-fix tree (that's the whole premise -- the bug must still be live), but it passed:\n%s", buildDir, out)
	}
	return strings.TrimSpace(string(out))
}

// rankOfChunk returns the 1-based rank of the first hit that DELIVERS one of
// exactChunks within hits, or 0 if none does.
//
// "Delivers" is containment, not string equality on the chunk ID. Exact-ID
// matching was correct while every retrieved chunk was verbatim an indexer
// window, and became WRONG the moment retrieval started merging overlapping
// windows into contiguous spans (Fix 11, chunkmerge.go): a span covering
// provider.go:61-130 contains the whole of the expected provider.go:91-130 and
// shows the model strictly more of the right code, yet scored as a MISS purely
// because its ID string differs. That is a grading artifact, not a retrieval
// result, and reading it as a regression would have been reading noise as
// signal.
//
// Containment is required to be FULL: a span that only clips part of the
// expected chunk is not credited. So this cannot inflate the number either --
// every exact-ID match is trivially a containment match, and nothing that
// fails to deliver the expected lines can pass.
func rankOfChunk(hits []Chunk, exactChunks []string) int {
	for i, h := range hits {
		if matchesAny(chunkID(h), exactChunks) || containsAnyChunk(h, exactChunks) {
			return i + 1
		}
	}
	return 0
}

// containsAnyChunk reports whether h fully covers any of the expected chunk
// IDs (each formatted "path:start-end", chunkContent's own format).
func containsAnyChunk(h Chunk, exactChunks []string) bool {
	for _, want := range exactChunks {
		path, start, end, ok := parseChunkID(want)
		if !ok {
			continue
		}
		if h.FilePath == path && h.StartLine <= start && end <= h.EndLine {
			return true
		}
	}
	return false
}

// parseChunkID splits "path/to/file.go:12-51" back into its parts.
func parseChunkID(id string) (path string, start, end int, ok bool) {
	colon := strings.LastIndex(id, ":")
	if colon < 0 {
		return "", 0, 0, false
	}
	dash := strings.LastIndex(id[colon+1:], "-")
	if dash < 0 {
		return "", 0, 0, false
	}
	start, err1 := strconv.Atoi(id[colon+1 : colon+1+dash])
	end, err2 := strconv.Atoi(id[colon+1+dash+1:])
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return id[:colon], start, end, true
}

// editEvalResult is one case's measured outcome.
type editEvalResult struct {
	name  string
	query string
	rank  int  // 0 = not found even in the full ranked list (k=all, fused+reranked)
	hit   bool // rank != 0 && rank <= displayK -- top-5 of the FULL ordering (POOL-INSENSITIVE)

	// prodRank/prodHit are the POOL-SENSITIVE verdict: does an exactChunk land
	// in the real top-5 when retrieveTopK is called the way production calls it
	// (k = displayK, so rerankPoolSize(k) imposes the same overfetch cutoff)?
	// This is the ONLY field a pool-width constant can move -- the k=all `rank`
	// above always fetches the whole corpus (rerankPoolSize(len(chunks)) far
	// exceeds it), so widening the pool provably cannot change it.
	prodRank int  // 1-based position within the production-shaped top-5; 0 if absent
	prodHit  bool // prodRank != 0

	// semRank is the raw SEMANTIC rank (rerank=false, k=all == pure store.Query;
	// no lexical tier, no class reweighting) -- the rank the semantic pool must
	// reach to fetch this chunk at all. Diagnostic: lets a prodHit MISS be read
	// as "outside the pool" (semRank > semantic pool) vs "in the pool but
	// reranked out of the top-5".
	semRank int // 0 = not found

	// --- Fix 12: direct file:line resolution ---------------------------------

	// fusedRank/fusedHit are prodRank/prodHit measured through the FULL
	// production pipeline as it exists after Fix 12: similarity retrieval, plus
	// spans resolved deterministically from the file:line pointers the pasted
	// failure already contains, fused and merged (fuseDirectSpans, fileref.go).
	// This is the "after" number for the graded implementation-chunk metric.
	fusedRank int
	fusedHit  bool

	// refsResolved is how many file:line pointers in the query resolved to an
	// eligible workspace file. refsCoveredBefore/After are how many of those
	// referenced LOCATIONS actually reach the top-5 -- before (similarity only)
	// and after (with direct resolution). This is a DIFFERENT question from
	// fusedHit and must not be confused with it: it asks "did the model get the
	// line the tool complained about?", not "did it get the line that has to be
	// edited". For Go's "undefined:" errors those are different files, since the
	// compiler reports the USE site, not the definition site.
	refsResolved      int
	refsCoveredBefore int
	refsCoveredAfter  int

	// mergeSavedBytes is Fix 11 measured on this case's REAL retrieved set:
	// rendered bytes reclaimed by folding same-file overlap out of the
	// production top-5.
	mergeSavedBytes int
	mergedSpans     int
}

// coversRef reports whether any chunk in hits contains relPath's given line --
// i.e. whether the location a compiler/test runner pointed at actually reached
// the prompt.
func coversRef(hits []Chunk, r resolvedRef) bool {
	for _, h := range hits {
		if h.FilePath == r.RelPath && h.StartLine <= r.Line && r.Line <= h.EndLine {
			return true
		}
	}
	return false
}

// runEditEvalPass runs every case in editEvalCases against a fresh git-
// reverted pre-fix worktree, real embedder, real chromem + FTS5 stores, and
// the actual retrieveTopK/rerankChunks path production uses (gatherContext,
// context.go:102). Returns one result per case, in editEvalCases order.
func runEditEvalPass(ctx context.Context, t *testing.T, repoRoot string, embedder Embedder, logger *log.Logger) []editEvalResult {
	t.Helper()
	const displayK = 5

	results := make([]editEvalResult, len(editEvalCases))
	for i, c := range editEvalCases {
		t.Run(c.name, func(t *testing.T) {
			wt := gitRevertPreFixTree(t, repoRoot, c.fixCommit, c.testFile)
			query := captureRealFailureText(t, wt, c.buildDir, c.runFilter)
			if query == "" {
				t.Fatalf("captured empty failure text for case %s", c.name)
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

			scan, err := indexRepoExcludingSelfReference(ctx, wt, embedder, store, lexicalStore, logger)
			if err != nil {
				t.Fatalf("indexing pre-fix tree: %v", err)
			}

			// k = every indexed chunk, so the returned order is the FULL
			// post-rerank ranking, not just the top displayK -- this is
			// what lets a miss report an actual rank (like "#212 of 884")
			// instead of merely ">5".
			hits, err := retrieveTopK(ctx, query, len(scan.Chunks), embedder, store, lexicalStore, true)
			if err != nil {
				t.Fatalf("retrieveTopK(%q): %v", query, err)
			}

			rank := rankOfChunk(hits, c.exactChunks)
			hit := rank != 0 && rank <= displayK

			// Production-shaped retrieval: k = displayK, so rerankPoolSize(k)
			// (rerank.go) imposes the SAME overfetch cutoff production uses
			// (context.go; defaultK == displayK). This is the ONLY measurement
			// in this eval a pool-width constant can move -- the full-corpus
			// call above passes k = len(scan.Chunks), so rerankPoolSize far
			// exceeds the corpus and everything is fetched regardless of the
			// constant. The pool-width experiment lives or dies on prodHit.
			prodHits, err := retrieveTopK(ctx, query, displayK, embedder, store, lexicalStore, true)
			if err != nil {
				t.Fatalf("retrieveTopK(prod k=%d, %q): %v", displayK, query, err)
			}
			prodRank := rankOfChunk(prodHits, c.exactChunks)
			prodHit := prodRank != 0

			// Raw semantic ordering (rerank=false, k=all: pure store.Query --
			// no lexical tier, no class reweighting): the rank the semantic
			// pool must reach to fetch this chunk at all. Lets a prodHit MISS
			// be read as "outside the pool" (semRank > semantic pool) vs
			// "fetched into the pool but reranked out of the top-5".
			semHits, err := retrieveTopK(ctx, query, len(scan.Chunks), embedder, store, nil, false)
			if err != nil {
				t.Fatalf("retrieveTopK(semantic k=all, %q): %v", query, err)
			}
			semRank := rankOfChunk(semHits, c.exactChunks)

			// Fix 12: the production pipeline as it now stands. resolveRefs
			// reads the pre-fix worktree (wt) -- the same tree that was
			// indexed -- so a resolved span is real code from the corpus under
			// test, not from HEAD.
			refs := resolveRefs(query, wt, logger)
			direct := make([]Chunk, len(refs))
			for j, r := range refs {
				direct[j] = r.Span
			}
			fused := fuseDirectSpans(direct, prodHits, displayK, false)
			fusedRank := rankOfChunk(fused.Chunks, c.exactChunks)

			coveredBefore, coveredAfter := 0, 0
			for _, r := range refs {
				if coversRef(prodHits, r) {
					coveredBefore++
				}
				if coversRef(fused.Chunks, r) {
					coveredAfter++
				}
			}

			// Fix 11 measured on this case's real similarity top-5.
			mergedOnly := mergeAdjacentChunks(prodHits)

			results[i] = editEvalResult{
				name: c.name, query: query, rank: rank, hit: hit,
				prodRank: prodRank, prodHit: prodHit, semRank: semRank,
				fusedRank: fusedRank, fusedHit: fusedRank != 0,
				refsResolved: len(refs), refsCoveredBefore: coveredBefore, refsCoveredAfter: coveredAfter,
				mergeSavedBytes: mergeSavings(prodHits, mergedOnly, false),
				mergedSpans:     len(mergedOnly),
			}

			fullStr := "not found"
			if rank != 0 {
				fullStr = fmt.Sprintf("#%d of %d", rank, len(scan.Chunks))
			}
			semStr := "not found"
			if semRank != 0 {
				semStr = fmt.Sprintf("#%d of %d", semRank, len(scan.Chunks))
			}
			prodStr := fmt.Sprintf("not in top-%d", displayK)
			prodMark := "MISS"
			if prodHit {
				prodStr = fmt.Sprintf("#%d", prodRank)
				prodMark = "hit"
			}
			t.Logf("\n=== %s ===\nquery (%d bytes, raw captured):\n%s\n\nexpected chunks: %v\n"+
				"full-ordering rank (k=all, POOL-INSENSITIVE diagnostic): %s\n"+
				"raw semantic rank (rerank=false):                      %s\n"+
				"PRODUCTION-SHAPED verdict (k=%d, pool-gated):           %s -> %s\n",
				c.name, len(query), query, c.exactChunks, fullStr, semStr, displayK, prodStr, prodMark)
			for j, h := range prodHits[:min(displayK, len(prodHits))] {
				t.Logf("  prod top-%d #%d: %-40s class=%-6s raw=%.4f weighted=%.4f", displayK, j+1, chunkID(h), h.Class, h.RawScore, h.Score)
			}

			fusedStr := fmt.Sprintf("not in top-%d", displayK)
			if fusedRank != 0 {
				fusedStr = fmt.Sprintf("#%d", fusedRank)
			}
			t.Logf("\nFIX 12 (direct file:line resolution):\n"+
				"  file:line refs resolved:        %d %v\n"+
				"  referenced location in top-5:   before %d/%d -> after %d/%d\n"+
				"  graded impl-chunk verdict:      %s\n"+
				"FIX 11 (merge) on this real top-5: %d chunks -> %d spans, %d rendered bytes reclaimed",
				len(refs), refDescriptions(refs),
				coveredBefore, len(refs), coveredAfter, len(refs),
				fusedStr,
				len(prodHits), len(mergedOnly), mergeSavings(prodHits, mergedOnly, false))
			for j, h := range fused.Chunks {
				t.Logf("  fused top-%d #%d: %-40s", displayK, j+1, chunkID(h))
			}
		})
	}
	return results
}

func TestEditShapedRetrievalEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	logger := log.New(os.Stderr, "edit-eval: ", log.LstdFlags)

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

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	ctx := context.Background()
	results := runEditEvalPass(ctx, t, repoRoot, embedder, logger)

	var buf bytes.Buffer
	fmt.Fprintln(&buf)
	fmt.Fprintln(&buf, "=== Edit-shaped retrieval eval: summary (n=4, see file header for the resolution-limit caveat) ===")
	fmt.Fprintf(&buf, "%-28s %-11s %-11s %-12s %-12s %-14s\n", "case", "prod(k=5)", "fused(k=5)", "full-rank", "sem-rank", "prod-pos")
	hitCount := 0
	prodHitCount := 0
	fusedHitCount := 0
	refsTotal, refsBefore, refsAfter := 0, 0, 0
	savedBytes := 0
	sorted := make([]editEvalResult, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	for _, r := range sorted {
		if r.hit {
			hitCount++
		}
		prodMark := "MISS"
		if r.prodHit {
			prodMark = "hit"
			prodHitCount++
		}
		fusedMark := "MISS"
		if r.fusedHit {
			fusedMark = "hit"
			fusedHitCount++
		}
		refsTotal += r.refsResolved
		refsBefore += r.refsCoveredBefore
		refsAfter += r.refsCoveredAfter
		savedBytes += r.mergeSavedBytes
		fullStr := "not found"
		if r.rank != 0 {
			fullStr = fmt.Sprintf("#%d", r.rank)
		}
		semStr := "not found"
		if r.semRank != 0 {
			semStr = fmt.Sprintf("#%d", r.semRank)
		}
		prodPos := "not in top-5"
		if r.prodRank != 0 {
			prodPos = fmt.Sprintf("#%d", r.prodRank)
		}
		fmt.Fprintf(&buf, "%-28s %-11s %-11s %-12s %-12s %-14s\n", r.name, prodMark, fusedMark, fullStr, semStr, prodPos)
	}
	fmt.Fprintf(&buf, "\nPRODUCTION-SHAPED recall, similarity only (k=5, pool-gated): %d/%d\n", prodHitCount, len(results))
	fmt.Fprintf(&buf, "PRODUCTION-SHAPED recall, WITH direct file:line resolution (Fix 12): %d/%d\n", fusedHitCount, len(results))
	fmt.Fprintf(&buf, "full-ordering recall (k=all, pool-INSENSITIVE, legacy metric): %d/%d\n", hitCount, len(results))
	fmt.Fprintf(&buf, "\nREFERENCED-LOCATION recall (Fix 12's own metric -- did the line the tool\n"+
		"complained about reach the top-5 at all?): before %d/%d -> after %d/%d\n",
		refsBefore, refsTotal, refsAfter, refsTotal)
	fmt.Fprintf(&buf, "FIX 11 merge, summed over the real production top-5 sets: %d rendered bytes reclaimed\n", savedBytes)
	t.Log(buf.String())
}

// refDescriptions renders resolved refs compactly for the per-case log.
func refDescriptions(refs []resolvedRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = fmt.Sprintf("%s:%d", r.RelPath, r.Line)
	}
	return out
}
