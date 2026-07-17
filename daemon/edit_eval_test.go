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
		name:      "editapply-apply-extraction",
		fixCommit: "b885f504ff667019d1d58ecfda09ea8361186410",
		testFile:  "editapply/apply_test.go",
		buildDir:  "editapply",
		runFilter: "TestApply_",
		exactChunks: []string{
			"editapply/apply.go:61-100", // where the extracted Apply() function belongs (right after PrepareEdit)
		},
		note: "pre-fix editapply/apply.go has PrepareEdit and ResolveSafeTargetPath but no Apply -- this is a CREATE-shaped gap (the answer chunk is where the missing function belongs, not a chunk that already contains it)",
	},
	{
		name:      "tui-header-collision",
		fixCommit: "1befc860a86adbfd4c3943359ccf3498d950d449",
		testFile:  "clients/tui/chat_test.go",
		buildDir:  "clients/tui",
		runFilter: "TestChat_HeaderLineCountTracksActiveNotices|TestChat_ViewportShrinksWhenNoticeLinesGrow",
		exactChunks: []string{
			"clients/tui/chat.go:571-610", // renderHeader/View pre-fix -- the header-rendering code the fix splits into headerLineCount/noticeLines
		},
		note: "pre-fix renderHeader/View computed header line count ad hoc, causing the P4 grounding+redaction notice collision; fix extracts headerLineCount()/noticeLines() as the single source of truth",
	},
	{
		name:      "zdr-refusal-phrasing",
		fixCommit: "d84524eb52559bd1821a507ec611fe2d988ba30a",
		testFile:  "daemon/provider_test.go",
		buildDir:  "daemon",
		runFilter: "TestIsZDRRoutingRefusal_MatchesLiveObservedDataPolicyPhrasing",
		exactChunks: []string{
			"daemon/provider.go:91-130", // zdrRefusalSubstrings + isZDRRoutingRefusal
		},
		note: "pre-fix zdrRefusalSubstrings is missing the live-observed \"zero data retention\" phrasing OpenRouter actually returns, so a real ZDR refusal surfaced as a raw error instead of the friendly message -- this case is a TEST-assertion failure, not a build failure, and its query text contains a real TestXxx function name (testFuncPattern's own trigger), giving this eval a second, distinct false-positive shape beyond the go-test/.test one",
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

// rankOfChunk returns the 1-based rank of the first hit whose ID is in
// exactChunks within hits, or 0 if none is present.
func rankOfChunk(hits []Chunk, exactChunks []string) int {
	for i, h := range hits {
		if matchesAny(chunkID(h), exactChunks) {
			return i + 1
		}
	}
	return 0
}

// editEvalResult is one case's measured outcome.
type editEvalResult struct {
	name  string
	query string
	rank  int  // 0 = not found even in the full ranked list
	hit   bool // rank != 0 && rank <= displayK
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
			results[i] = editEvalResult{name: c.name, query: query, rank: rank, hit: hit}

			mark := "MISS"
			if hit {
				mark = "hit"
			}
			rankStr := "not found"
			if rank != 0 {
				rankStr = fmt.Sprintf("#%d of %d", rank, len(scan.Chunks))
			}
			t.Logf("\n=== %s ===\nquery (%d bytes, raw captured):\n%s\n\nexpected chunks: %v\nrank: %s -> %s\n", c.name, len(query), query, c.exactChunks, rankStr, mark)
			if hit {
				for j, h := range hits[:displayK] {
					t.Logf("  top-5 #%d: %-40s class=%-6s raw=%.4f weighted=%.4f", j+1, chunkID(h), h.Class, h.RawScore, h.Score)
				}
			} else {
				for j, h := range hits[:min(displayK, len(hits))] {
					t.Logf("  top-5 #%d: %-40s class=%-6s raw=%.4f weighted=%.4f", j+1, chunkID(h), h.Class, h.RawScore, h.Score)
				}
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
	fmt.Fprintf(&buf, "%-32s %-8s %-14s\n", "case", "hit", "rank")
	hitCount := 0
	sorted := make([]editEvalResult, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	for _, r := range sorted {
		mark := "MISS"
		if r.hit {
			mark = "hit"
			hitCount++
		}
		rankStr := "not found"
		if r.rank != 0 {
			rankStr = fmt.Sprintf("#%d", r.rank)
		}
		fmt.Fprintf(&buf, "%-32s %-8s %-14s\n", r.name, mark, rankStr)
	}
	fmt.Fprintf(&buf, "\nchunk-level recall: %d/%d\n", hitCount, len(results))
	t.Log(buf.String())
}
