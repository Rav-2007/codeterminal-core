//go:build eval

// DOES THE EXTRA CONTEXT EARN ITS TOKENS?
//
// On 2026-08-28 delivered recall went from 31/49 to 39/49 by widening every hit
// to its enclosing declaration and raising the character budget 16000 -> 24000.
// That is +47% retrieved context, and the entire case for it rests on an
// assumption nobody on this stack has ever measured: that more of the right
// context produces a better answer.
//
// It is not a safe assumption. The opposite effect is well attested -- a longer
// prompt can bury the relevant span among plausible neighbours and make a model
// LESS grounded, not more. The locate eval cannot see any of this. It scores
// what reaches the prompt and stops there, so a change that improved retrieval
// and degraded answers would show up as a clean win.
//
// WHAT THIS HARNESS DOES AND DOES NOT MEASURE. Tools are OFF. Both arms get the
// retrieved context and nothing else, so the only thing that differs between
// them is how much context there is -- which is the variable under test. With
// tools on, an arm handed a thin context can go and read the file itself, and
// the difference this harness exists to detect is exactly what that would hide.
//
// The cost of that choice, stated plainly: this is an UPPER BOUND on how much
// the context budget matters. In production the agent has tools and can
// compensate for a thin context, so a win here would not transfer at full size.
// A LOSS here, though, transfers completely -- if more context makes the answer
// worse when context is all there is, more context is not helping.
//
//	export CODETERMINAL_API_BASE=https://openrouter.ai/api/v1 CODETERMINAL_API_KEY=...
//	go test -tags eval -count=1 -timeout 60m -v -run TestContextDilutionEarnsItsTokens ./
//
// Direct to OpenRouter rather than the managed proxy, for the reason the other
// live evals give: a quota exhaustion once voided a run partway through.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The decision rule, written down BEFORE the first run.
// ---------------------------------------------------------------------------

const dilutionDecisionRule = `
DECIDED BEFORE THE RUN:

  WIDE (24000 chars, widen to the enclosing declaration) earns its context over
  NARROW (16000, +-1 window) only if it wins materially more often than it
  loses, on answers judged blind.

  A TIE AT MATERIALLY MORE PROMPT TOKENS IS A LOSS. Paying more for the same
  answer is not neutral. (The measured overhead is printed with the verdict; it
  was +47% when this rule was written.)

  If WIDE does not win, defaultContextBudgetChars is walked back to the sweep's
  next-best cell and the widening cap revisited. It is deliberately a single
  constant so that is one edit. The run is NOT repeated until it wins.

  A result of "more context did not help" is a SUCCESSFUL run of this harness.
  It retires an assumption that four commits currently rest on.
`

// The two arms. They differ in exactly two fields, and those two fields are the
// whole of what 2026-08-28's delivery work changed.
//
// WIDE IS READ FROM PRODUCTION, not written as 24000/{10,300}. If the budget is
// ever walked back -- which is precisely what this harness exists to trigger --
// a hardcoded WIDE would go on measuring a configuration that no longer ships,
// and the next person would read a stale verdict as a current one. Bound to the
// live constants, the question stays "does what we ship today earn its tokens
// over what we shipped before 2026-08-28", which is the question worth asking
// on every run rather than only the first.
var (
	narrowPolicy = expandPolicy{TopN: expandNeighbourTopN} // pre-2026-08-28
	widePolicy   = defaultExpandPolicy                     // whatever ships now
)

const (
	narrowBudget = 16000 // the budget that shipped alongside narrowPolicy
	wideBudget   = defaultContextBudgetChars
)

const dilutionSystemPrompt = `You are answering a question about a Go codebase.

Context retrieved from that codebase appears above the question. Answer FROM THAT
CONTEXT. Name the specific files and functions involved.

If the retrieved context does not contain the answer, say so plainly. Naming a
file or function that is not in the context is the worst possible answer -- a
confident wrong path is more damaging than an honest "not in what I was given".

Be brief: a few sentences. You are being graded on whether you name the right
code, not on presentation.`

// dilutionScenarios picks a shape-proportional sample of the locate eval's
// queries.
//
// PROPORTIONAL AND DETERMINISTIC, rather than hand-picked, because a live A/B is
// exactly where cherry-picking is most tempting and least visible: choosing the
// queries retrieval already handles would flatter WIDE, and choosing the ones it
// misses would measure nothing but noise in both arms.
//
// EVENLY SPACED WITHIN EACH SHAPE, not the first n of it. The first version took
// all[:n] and the 2026-08-29 run showed why that is not good enough: it selected
// q1-q18, which is disproportionately the ORIGINAL nine-query set plus its
// neighbours -- queries chosen years earlier as the ones hybrid retrieval was
// built to rescue, and therefore the ones BOTH arms already handle. Both arms
// scored 13/14 on naming the right file while the full 49-query eval delivers
// only 39. A sample that cannot separate the arms cannot answer the question,
// and "deterministic" is not the same as "representative".
func dilutionScenarios(want int) []int {
	byShape := map[string][]int{}
	for i, q := range rerankEvalQueries {
		byShape[q.shape] = append(byShape[q.shape], i)
	}
	var picked []int
	for _, shape := range evalQueryShapeOrder {
		all := byShape[shape]
		if len(all) == 0 {
			continue
		}
		n := (len(all)*want + len(rerankEvalQueries) - 1) / len(rerankEvalQueries)
		if n < 1 {
			n = 1
		}
		if n > len(all) {
			n = len(all)
		}
		// Stride through the shape so early and late queries are equally
		// represented.
		for j := 0; j < n; j++ {
			picked = append(picked, all[j*len(all)/n])
		}
	}
	sort.Ints(picked)
	return picked
}

// knownPaths is every file the indexer can see, by relative path AND by base
// name, because a model writes "context.go" as readily as "daemon/context.go".
func knownPaths(scan *ScanResult) map[string]bool {
	seen := map[string]bool{}
	for _, c := range scan.Chunks {
		seen[strings.ToLower(c.FilePath)] = true
		seen[strings.ToLower(filepath.Base(c.FilePath))] = true
	}
	return seen
}

// pathish matches file-path-shaped tokens in prose. Same construction as
// livequestion.go's filePathish, kept local so tightening one does not silently
// change the other.
var pathish = regexp.MustCompile(`[\w./-]+\.(?:go|md|json|ya?ml|toml|sh|ts|tsx)\b`)

// hallucinatedPaths returns the file-shaped tokens in answer that name nothing
// in the repository.
//
// This is the specific harm dilution is supposed to cause, and it is free to
// measure: groundedness is what the judge is told to weigh first, and this is
// the half of groundedness that does not need a model's opinion.
func hallucinatedPaths(answer, root string, known map[string]bool) []string {
	var bad []string
	seen := map[string]bool{}
	for _, m := range pathish.FindAllString(answer, -1) {
		key := strings.ToLower(strings.Trim(m, "./"))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if known[key] || known[strings.ToLower(filepath.Base(key))] {
			continue
		}
		// Second chance against the filesystem, because `known` is built from
		// INDEXED chunks and the indexer skips things (gitignored, too large,
		// binary). A real file the indexer declined to read is not a
		// hallucination, and counting it as one would put noise straight into
		// the only assertion this harness makes.
		if _, err := os.Stat(filepath.Join(root, strings.Trim(m, "./"))); err == nil {
			continue
		}
		bad = append(bad, m)
	}
	return bad
}

// namesExpectedFile reports whether the answer names one of the files declared
// as answering the query -- full path or base name.
func namesExpectedFile(answer string, want []string) bool {
	low := strings.ToLower(answer)
	for _, f := range want {
		if strings.Contains(low, strings.ToLower(f)) ||
			strings.Contains(low, strings.ToLower(filepath.Base(f))) {
			return true
		}
	}
	return false
}

func TestContextDilutionEarnsItsTokens(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live A/B in -short mode")
	}
	apiBase := os.Getenv("CODETERMINAL_API_BASE")
	apiKey := os.Getenv("CODETERMINAL_API_KEY")
	if apiBase == "" || apiKey == "" {
		t.Skip("CODETERMINAL_API_BASE/KEY unset -- this eval makes real billed calls and will not guess")
	}

	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("loading models.json: %v", err)
	}
	model := cfg.Tiers["primary"].Slug
	routing := cfg.ZDR.resolvedProviderRouting()

	logger := log.New(os.Stderr, "dilution: ", log.LstdFlags)
	ctx := context.Background()

	// A FRESH INDEX THAT EXCLUDES THE SELF-REFERENTIAL FILES, not the workspace's
	// live one. rerank_eval_test.go contains all 49 queries verbatim next to
	// their answers; retrieving it would hand both arms the answer key, and the
	// arm with the bigger budget would get more of it. That is not a subtle
	// confound, it is the measurement inverted.
	modelCacheDir, err := defaultModelCacheDir()
	if err != nil {
		t.Fatalf("defaultModelCacheDir: %v", err)
	}
	modelDir, err := EnsureModelFiles(ctx, modelCacheDir, bgeModelAssets, logger)
	if err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}
	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		t.Fatalf("defaultONNXRuntimeCacheDir: %v", err)
	}
	onnxRuntimeLib, err := EnsureONNXRuntimeLib(ctx, ortCacheDir, logger)
	if err != nil {
		t.Fatalf("EnsureONNXRuntimeLib: %v", err)
	}
	helper := NewHelperProcess(buildRealHelperBinary(t), modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		t.Fatalf("starting real embedder helper: %v", err)
	}
	defer helper.Stop()
	embedder := NewBgeEmbedder(helper)

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	indexDir := filepath.Join(t.TempDir(), "index")
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	lexical, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer lexical.Close()

	scan, err := indexRepoExcludingSelfReference(ctx, repoRoot, embedder, store, lexical, logger)
	if err != nil {
		t.Fatalf("indexing: %v", err)
	}
	t.Logf("indexed %s: scanned=%d chunks=%d", repoRoot, scan.FilesScanned, len(scan.Chunks))
	known := knownPaths(scan)

	rec := newCostRecorder(t, apiBase)
	rng := rand.New(rand.NewSource(20260828))
	t.Log(dilutionDecisionRule)

	newServer := func(budget int, pol expandPolicy) *Server {
		p := pol
		return &Server{
			apiBase: rec.base(), apiKey: apiKey, workspace: repoRoot, logger: log.New(os.Stderr, "", 0),
			embedder: embedder, store: store, lexicalStore: lexical,
			retrievalTopK: defaultK, contextBudgetChars: budget, expandPolicy: &p,
			cfg: &Config{},
		}
	}
	narrow := newServer(narrowBudget, narrowPolicy)
	wide := newServer(wideBudget, widePolicy)

	promptChars := map[string]int{}

	// One model call per arm per scenario: no tools, so there is no loop.
	runArm := func(t *testing.T, srv *Server, name, prompt string) armResult {
		t.Helper()
		outcome := srv.gatherContext(ctx, prompt)
		augmented := prompt
		if !outcome.Skipped {
			// scrubDisabled=false: production scrubs retrieved chunks inside
			// renderChunk, and scrubbing changes the bytes the model reads. An
			// arm that skipped it would be measuring a prompt the product never
			// sends.
			augmented = buildAugmentedUserMessage(prompt, outcome.Chunks, false)
		}
		var out strings.Builder
		rec.reset()
		_, err := streamCompletion(ctx, srv.apiBase, srv.apiKey, model,
			buildChatMessages(dilutionSystemPrompt, nil, augmented), nil, routing,
			func(tok string) error { out.WriteString(tok); return nil },
			nil, nil, nil)
		if err != nil {
			t.Fatalf("%s arm failed: %v", name, err)
		}
		calls, pt, ct := rec.snapshot()
		// promptChars is recorded ALONGSIDE promptTokens, not instead of it,
		// because the two fail differently and the 2026-08-29 run showed the
		// token side failing: NARROW totalled 0 prompt tokens across 14
		// scenarios while WIDE totalled 5744.
		//
		// That asymmetry is a RACE, not a missing usage chunk. costRecorder does
		// its accounting after the upstream body is fully drained, in the
		// httptest server's goroutine, while streamCompletion returns as soon as
		// IT has finished reading -- so snapshot() can run before the handler has
		// added anything, and the first call of a pair loses consistently. (The
		// same pattern is in orchestration_ab_eval_test.go, whose cost table is
		// therefore also suspect; flagged there, not fixed from here.)
		//
		// The character count of the augmented prompt is computed locally, is
		// exact, races nothing, and is the quantity the decision rule actually
		// cares about: how much more context the wide arm was handed.
		res := armResult{
			name: name, answer: out.String(),
			calls: calls, promptTokens: pt, completionTokens: ct,
		}
		promptChars[name] += len(augmented)
		return res
	}

	type tally struct {
		wins, losses, ties int
		namedRight         map[string]int
		hallucinated       map[string]int
		promptTokens       map[string]int
	}
	tl := tally{
		namedRight:   map[string]int{},
		hallucinated: map[string]int{},
		promptTokens: map[string]int{},
	}

	picked := dilutionScenarios(12)
	t.Logf("model=%s  scenarios=%d (shape-proportional, deterministic)", model, len(picked))

	for _, qi := range picked {
		q := rerankEvalQueries[qi]
		t.Run(fmt.Sprintf("q%02d_%s", qi+1, q.shape), func(t *testing.T) {
			n := runArm(t, narrow, "narrow", q.query)
			w := runArm(t, wide, "wide", q.query)

			for _, a := range []armResult{n, w} {
				tl.promptTokens[a.name] += a.promptTokens
				if namesExpectedFile(a.answer, q.expectedFiles) {
					tl.namedRight[a.name]++
				}
				if bad := hallucinatedPaths(a.answer, repoRoot, known); len(bad) > 0 {
					tl.hallucinated[a.name] += len(bad)
					t.Logf("  %s named %d path(s) that do not exist: %v", a.name, len(bad), bad)
				}
			}

			winner, reason := judge(t, apiBase, apiKey, model, routing, q.query, n, w, rng)
			switch winner {
			case "wide":
				tl.wins++
			case "narrow":
				tl.losses++
			default:
				tl.ties++
			}
			t.Logf("  q%d [%s] %q\n    narrow: %d prompt tok, names-right=%t\n    wide:   %d prompt tok, names-right=%t\n    judge: %s -- %s",
				qi+1, q.shape, truncateEval(q.query, 60),
				n.promptTokens, namesExpectedFile(n.answer, q.expectedFiles),
				w.promptTokens, namesExpectedFile(w.answer, q.expectedFiles),
				winner, reason)
		})
	}

	// ---------------------------------------------------------------------
	// The verdict.
	// ---------------------------------------------------------------------
	n := len(picked)
	fmt.Printf("\n=== Does the extra context earn its tokens? (%d scenarios, tools off) ===\n", n)
	fmt.Printf("%-8s %14s %12s %14s %14s\n",
		"arm", "names right", "bad paths", "prompt chars", "prompt tokens")
	for _, arm := range []string{"narrow", "wide"} {
		fmt.Printf("%-8s %11d/%-2d %12d %14d %14d\n",
			arm, tl.namedRight[arm], n, tl.hallucinated[arm], promptChars[arm], tl.promptTokens[arm])
	}
	// Overhead from CHARACTERS, which are always available, falling back to
	// nothing rather than to a misleading zero.
	overhead := 0.0
	if promptChars["narrow"] > 0 {
		overhead = 100 * float64(promptChars["wide"]-promptChars["narrow"]) /
			float64(promptChars["narrow"])
	}
	if tl.promptTokens["narrow"] == 0 {
		fmt.Println("NOTE: the provider returned no usage chunk, so token counts are unavailable; " +
			"the overhead below is measured in prompt CHARACTERS.")
	}
	fmt.Printf("\nblind pairwise: wide won %d, lost %d, tied %d\n", tl.wins, tl.losses, tl.ties)
	fmt.Printf("context overhead of wide: %+.1f%%\n", overhead)

	// MATERIALITY, declared rather than eyeballed. On a sample this size a
	// one-or-two win margin is noise: 7-5-2 is roughly what a fair coin does.
	// The rule says "materially more often", so the threshold has to be written
	// down or "materially" quietly becomes "at all" the moment a run comes back
	// +2. A quarter of the scenarios is the bar.
	material := (tl.wins-tl.losses)*4 > n
	groundedness := tl.hallucinated["narrow"] - tl.hallucinated["wide"] // >0 means wide is better

	// THE RULE, APPLIED. Reported rather than failed: this harness measures a
	// product decision, and a decision that went the other way is information,
	// not a broken build. It is loud so it cannot be skimmed past.
	switch {
	case material && groundedness >= 0 && tl.namedRight["wide"] >= tl.namedRight["narrow"]:
		fmt.Printf("\nVERDICT: WIDE EARNS IT (+%d net on judging at %+.1f%% context).\n",
			tl.wins-tl.losses, overhead)
	case tl.hallucinated["wide"] > tl.hallucinated["narrow"]:
		fmt.Printf("\nVERDICT: WIDE DOES NOT EARN IT -- it is LESS GROUNDED.\n"+
			"It named %d non-existent paths to NARROW's %d while being handed %+.1f%% more\n"+
			"context. A judged margin of %d-%d-%d does not buy that back: fabricating a\n"+
			"plausible path is the one failure mode retrieval exists to prevent.\n",
			tl.hallucinated["wide"], tl.hallucinated["narrow"], overhead,
			tl.wins, tl.losses, tl.ties)
	case !material:
		fmt.Printf("\nVERDICT: NOT PROVEN. Wide won %d, lost %d, tied %d -- inside noise on %d\n"+
			"scenarios -- at %+.1f%% context. The rule calls an unearned premium a LOSS, so this\n"+
			"does NOT justify the budget on its own. Do NOT re-run it until it wins; either\n"+
			"widen the sample deliberately or walk defaultContextBudgetChars back.\n",
			tl.wins, tl.losses, tl.ties, n, overhead)
	default:
		fmt.Printf("\nVERDICT: WIDE DOES NOT EARN IT (won %d, lost %d at %+.1f%% context).\n"+
			"Walk defaultContextBudgetChars back. Do NOT re-run this until it wins.\n",
			tl.wins, tl.losses, overhead)
	}

	// The one thing that IS red: an arm handed MORE context that invents MORE
	// paths is the dilution effect itself, measured deterministically rather
	// than judged. It fails the build so it cannot be read past as a table row.
	if tl.hallucinated["wide"] > tl.hallucinated["narrow"] {
		t.Errorf("the WIDE arm named %d non-existent paths against NARROW's %d. More context made "+
			"the model LESS grounded, which is the specific harm the budget increase was "+
			"assumed not to cause", tl.hallucinated["wide"], tl.hallucinated["narrow"])
	}
}
