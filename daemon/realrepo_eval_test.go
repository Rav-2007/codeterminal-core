//go:build eval

package main

// REAL REPOSITORIES, REAL BUGS (2026-10-26). The owner: "test this agent in
// the real repo in github, to check its strength", in C, C++, Java and Python.
//
// Each task is a real fix from a real GitHub repository, SWE-bench style: the
// project at the commit before the fix (with a fresh one-commit history, so the
// fix cannot be looked up), the bug told as a user reports it, and the fix's
// own tests added only to grade. scripts/realrepo/build_tasks.py builds the
// tasks and proves each one fails as given and passes with the real fix; its
// README says how to set a machine up to run them.
//
// THE TURN IS THE PRODUCT'S. Each turn is built as runAgentTurn builds it --
// the working copy, the registry, what the tools reach, the agent's setup, a
// compact agent's short instructions -- and a "continue" carries what the
// user's chat carries: the last answer, and Mochiii's record under it where the
// product writes one (turnreport.go, through realrepo_record_eval_test.go). The
// first version of this harness left out three of those steps and measured an
// agent no user runs.
//
// EVERY ARM BY ONE MEASURE. The metrics are read with turnevidence.go's
// definitions and with this harness's own look at the real project before and
// after each turn, never with anything only one view has: Round 6's arm A is
// main as it was before one view, plus turnevidence.go, shellline.go and this
// eval (scripts/realrepo/baseline_tree.sh), and it is measured as B is.
//
// The agent runs with a model picked from a provider's list (providerTierConfig):
// the shipped agent config, every call approved. Three tools are off: search_code
// (these repositories have no index), and web_search, web_fetch and github,
// which could find the upstream fix itself.
//
//	REALREPO_TASKS=<tasks dir> REALREPO_MODEL=<provider's model id> \
//	MOCHIII_API_BASE=... MOCHIII_API_KEY=... \
//	  go test -tags eval -run TestRealRepoTasks -v -timeout 900m .
//
// Knobs:
//   - REALREPO_ONLY: comma-separated task names.
//   - REALREPO_CONTINUES: how many times "continue" is said after a turn that
//     stopped on its budget (default 2).
//   - REALREPO_PROVIDER_STOPS: how many turns the provider may stop part-way,
//     each asked again, before the attempt is the provider's (default 2).
//   - REALREPO_PROVIDER_RETRIES: how many times a trial the provider ended is
//     run again from scratch (default 2).
//   - REALREPO_MODE: "" (the single agent, as a turn runs) or a long-task mode
//     such as "fix" (the /fix command: one request in segments).
//   - REALREPO_MAX_ITERATIONS, REALREPO_MAX_TURN_ITERATIONS,
//     REALREPO_TURN_TIMEOUT, REALREPO_MAX_TURN_TOKENS: the turn's budget, when
//     an arm measures another one than the shipped config's.
//   - REALREPO_MAX_USD: stop starting tasks once the run's bill passes this.
//     The bill is the provider's own where it reports one; otherwise
//     REALREPO_USD_PER_MTOK_IN, _OUT and _CACHED price the tokens, and a cap
//     with neither is refused rather than left unable to bind.
//   - REALREPO_OUT (a JSON file for the results), REALREPO_KEEP (a folder to copy
//     each trial's workspace to), REALREPO_LOG (the daemon log).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"mochiii/editapply"
	"mochiii/protocol"
)

type realRepoTrial struct {
	Task       string   `json:"task"`
	Lang       string   `json:"lang"`
	Arm        string   `json:"arm,omitempty"`
	Pass       bool     `json:"pass"`
	Outcome    string   `json:"outcome"` // pass, fail, rate-limited, provider-error
	Grade      string   `json:"grade,omitempty"`
	Turns      int      `json:"turns"`
	Calls      int      `json:"model_calls"`
	ToolCalls  int      `json:"tool_calls"`
	Tools      []string `json:"tools,omitempty"`
	CallsMade  []string `json:"calls_made,omitempty"` // each call with its arguments, shortened
	Edits      int      `json:"edits_proposed"`
	Refused    int      `json:"edits_refused"`
	Tokens     int      `json:"tokens"`
	Seconds    float64  `json:"seconds"`
	BudgetStop bool     `json:"budget_stop"`
	Checked    string   `json:"agent_last_check,omitempty"`
	CheckOK    bool     `json:"agent_last_check_passed,omitempty"`
	Error      string   `json:"error,omitempty"`
	FinalWords string   `json:"final_words,omitempty"`
	// ProviderStops are the turns the provider stopped part-way, each asked
	// again as a user would ("Ask again to continue"), not counted among the
	// agent's own continues.
	ProviderStops int `json:"provider_stops,omitempty"`
	// ProviderCuts are the earlier attempts at this task that the provider
	// ended, each run again from scratch: what is graded above is the agent's
	// work, and these say what the provider cost it.
	ProviderCuts []string `json:"provider_cuts,omitempty"`

	// THE PRE-REGISTERED BEHAVIOUR METRICS (Round 6, docs/AGENT_WORKFLOW_EVAL.md),
	// read from what the turns did -- the checks run in their working copies,
	// the edits they offered, what changed in the real project -- never from
	// their words. A trial is graded on its final state, so these are too.
	//
	// TestedFinal: the last build or test run that saw the final version of
	// the changes passed (turnRuns.final), as of the last turn that changed or
	// tested anything.
	TestedFinal bool `json:"tested_final"`
	// ClaimUnsupported: the last answer says the tests pass, the trial changed
	// something, and it is not TestedFinal or it changed existing tests.
	ClaimUnsupported bool `json:"claim_unsupported"`
	// TestsChanged: existing test files whose existing lines the agent changed,
	// by an offered edit or outside review, in any turn.
	TestsChanged []string `json:"tests_changed,omitempty"`
	// OutsideReview: files changed, created or removed in the real project
	// while a turn ran -- before anything it offered was applied -- as git
	// shows the project (ignored build output left out), in any turn.
	OutsideReview []string `json:"outside_review,omitempty"`
	// USD is the trial's bill: the provider's own, or priced from its tokens
	// when USDEstimated says so.
	USD          float64 `json:"usd,omitempty"`
	USDEstimated bool    `json:"usd_estimated,omitempty"`
}

// turnRecord writes Mochiii's record under a turn's answer as the tree being
// measured writes it. realrepo_record_eval_test.go sets it; main as it was
// before the record existed had none, and arm A's tree leaves that file out,
// so its "continue" carries the answer alone, as that product's chat did.
var turnRecord func(res agentResult, offered []editapply.EditBlock, wc *protocol.WorkingCopyInfo,
	sink *proposalSink, editsNow int) string

// realRepoPrices prices a provider's tokens when it reports no cost.
type realRepoPrices struct{ in, out, cached float64 } // USD per million tokens

func (p realRepoPrices) set() bool { return p.in > 0 || p.out > 0 }

// bill is one turn's cost: the provider's own when every call reported one,
// else priced from its tokens (estimated true), or 0 with nothing to go on.
func (p realRepoPrices) bill(u *protocol.TurnUsage) (usd float64, estimated bool) {
	if u == nil {
		return 0, false
	}
	if !u.CostMissing && u.CostUSD > 0 {
		return u.CostUSD, false
	}
	if !p.set() {
		return u.CostUSD, u.CostMissing
	}
	fresh := u.PromptTokens - u.CachedTokens
	return (float64(fresh)*p.in + float64(u.CachedTokens)*p.cached + float64(u.CompletionTokens)*p.out) / 1e6, true
}

func TestRealRepoTasks(t *testing.T) {
	root, model := os.Getenv("REALREPO_TASKS"), os.Getenv("REALREPO_MODEL")
	apiBase, apiKey := os.Getenv("MOCHIII_API_BASE"), os.Getenv("MOCHIII_API_KEY")
	if root == "" || model == "" || apiBase == "" || apiKey == "" {
		t.Skip("REALREPO_TASKS, REALREPO_MODEL, MOCHIII_API_BASE and MOCHIII_API_KEY are all needed")
	}
	cfg, err := LoadConfig("../models.agent.json")
	if err != nil {
		t.Fatalf("loading models.agent.json: %v", err)
	}
	// As /model <id> selects a model from a connected provider's list: a tier
	// named by the id, nothing guessed about it (providerTierConfig).
	cfg.Tiers[model] = ModelTier{Slug: model, Active: true}
	routing := cfg.routingFor(model)
	mcpCfg := cfg.MCP
	tools := map[string]string{}
	for k, v := range mcpCfg.Builtin.Tools {
		tools[k] = v
	}
	for _, off := range []string{"search_code", "web_search", "web_fetch", "github"} {
		tools[off] = PolicyDeny
	}
	mcpCfg.Builtin.Tools = tools
	// AN ARM'S OWN BUDGET, when it measures one (Round 6's 40-call arm).
	for name, field := range map[string]*int{
		"REALREPO_MAX_ITERATIONS":      &mcpCfg.Budget.MaxIterations,
		"REALREPO_MAX_TURN_ITERATIONS": &mcpCfg.Budget.MaxTurnIterations,
		"REALREPO_TURN_TIMEOUT":        &mcpCfg.Budget.TurnTimeoutSeconds,
		"REALREPO_MAX_TURN_TOKENS":     &mcpCfg.Budget.MaxTurnTokens,
	} {
		if v := envInt(t, name, 0); v > 0 {
			*field = v
		}
	}
	continues := envInt(t, "REALREPO_CONTINUES", 2)
	retries := envInt(t, "REALREPO_PROVIDER_RETRIES", 2)
	maxProviderStops := envInt(t, "REALREPO_PROVIDER_STOPS", 2)
	keep := os.Getenv("REALREPO_KEEP")
	longMode := strings.TrimSpace(os.Getenv("REALREPO_MODE"))
	if longMode != "" && !isLongTaskMode(longMode) {
		t.Fatalf("REALREPO_MODE=%q: want a long-task mode (task, debug, fix, refactor, hunt) or nothing", longMode)
	}
	prices := realRepoPrices{in: envFloat(t, "REALREPO_USD_PER_MTOK_IN"), out: envFloat(t, "REALREPO_USD_PER_MTOK_OUT"),
		cached: envFloat(t, "REALREPO_USD_PER_MTOK_CACHED")}
	maxUSD := envFloat(t, "REALREPO_MAX_USD")

	only := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("REALREPO_ONLY"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			only[n] = true
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && (len(only) == 0 || only[e.Name()]) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	shell := watchShell(t)
	logger := log.New(io.Discard, "", 0)
	if path := os.Getenv("REALREPO_LOG"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		logger = log.New(f, "", log.LstdFlags)
	}
	arm := "turn"
	if longMode != "" {
		arm = "/" + longMode
	}
	t.Logf("model=%s  arm=%s  budget=%+v  continues=%d  provider retries=%d  max usd=%.2f  tasks=%d",
		model, arm, mcpCfg.Budget, continues, retries, maxUSD, len(names))

	// runTrial is one attempt at one task, from a fresh copy of its workspace,
	// graded whatever happened, and kept as keepAs.
	runTrial := func(name, lang, prompt, keepAs string) realRepoTrial {
		taskDir := filepath.Join(root, name)
		dir := filepath.Join(t.TempDir(), "w") // short: the agent's commands see this path
		if err := copyTree(filepath.Join(taskDir, "workspace"), dir); err != nil {
			t.Fatal(err)
		}
		// The provider's own address, as the product calls it: the request's form
		// (providerdialect.go) is chosen by it, so a recording proxy in between
		// would send another provider's fields. Calls and tokens come from the
		// turn's own usage tally instead.
		srv := &Server{apiBase: apiBase, apiKey: apiKey, workspace: dir, logger: logger, cfg: &Config{MCP: mcpCfg}}
		system := planModeSystemPrompt(defaultSystemPrompt, "auto")
		mode := "auto"
		var run *taskRun
		if longMode != "" {
			mode = longMode
			run = &taskRun{budget: resolveTaskBudget(mcpCfg.Budget.Task, nil)}
			system += "\n\n" + longTaskDirective(longMode, run.budget.segmentCalls) // as server.go adds it
		}
		appr := &approveAll{}
		start := time.Now()
		tr := realRepoTrial{Task: name, Lang: lang, Arm: arm}
		var (
			res               agentResult
			err               error
			sigs              []string
			stoppedByProvider bool
		)
		history, ask := []chatMessage(nil), prompt
		for {
			tr.Turns++
			// Each turn on its own clock, as agentturn.go starts one per message:
			// timed from the task's start, a "continue" lost what the turns before
			// it had used of turn_timeout_seconds, and was cut off mid-work.
			turnStart := time.Now()
			projectBefore := scanProject(t, dir) // to find what the turn changes outside review
			sink, messages := srv.newTurnSink(mode, nil, buildChatMessages(system, history, ask))
			registry, _ := srv.buildRegistry(context.Background(), logger, sink, mode)
			// As the product's turn does (agentturn.go): what its tools reach on
			// this machine, what Mochiii is set up with, and a compact agent's
			// short instructions where they apply.
			messages = srv.withMachineReach(context.Background(), registry, mode, messages)
			messages = srv.withAgentSetup(context.Background(), registry, mode, model, messages)
			messages = compactTurnMessages(context.Background(), messages)
			ctx, tally := withUsageTally(context.Background())
			runs := &turnRuns{sink: sink, shell: shell, project: physicalDir(t, dir)}
			shell.skip() // what ran before this turn is not its
			if run != nil {
				run.ledger = newTaskLedger(dir, longMode, prompt, turnStart)
				run.start, run.deadline = turnStart, turnStart.Add(run.budget.duration())
				sink.task = run
				res, err = srv.runLongTask(ctx, turnStart, registry, model, messages, routing, appr, sink,
					func(string) error { return nil }, runs.observe, nil, nil, nil, func(protocol.TaskStatus) {})
			} else {
				res, err = srv.runAgentLoop(ctx, turnStart, registry, model, mode, messages, routing, appr,
					func(string) error { return nil }, runs.observe, nil, nil, nil, nil, nil)
			}
			_ = registry.Close()
			outside, outsideTests := scanProject(t, dir).changedSince(projectBefore) // nothing offered is applied yet
			calls, _ := tally.spent()
			tokens, _ := tally.tokens()
			usd, estimated := prices.bill(tally.report(model, 0))
			tr.Calls, tr.Tokens, tr.USD = tr.Calls+calls, tr.Tokens+tokens, tr.USD+usd
			tr.USDEstimated = tr.USDEstimated || estimated
			sigs = append(sigs, res.ToolSignatures...) // a turn the provider ended still made its calls
			if err != nil {
				tr.noteOutside(outside, outsideTests)
				sink.discard()
				break
			}
			textBlocks, _ := srv.parseAndLogEditBlocks(res.FinalText)
			textBlocks = sink.absorbText(textBlocks)
			editsNow := sink.editCount() // before finish, which closes the copy (agentturn.go)
			filed, wc, _ := sink.finish()
			blocks := append(filed, textBlocks...)
			// MOCHIII'S RECORD, as the product writes it under the answer -- and
			// so as a "continue" next sees it.
			report := ""
			if run == nil && turnRecord != nil {
				report = turnRecord(res, blocks, wc, sink, editsNow)
			}
			tr.observe(res, blocks, wc, runs, editsNow, outside, outsideTests)
			tr.Edits += len(blocks)
			tr.Refused += applyProposals(dir, blocks)
			// A turn the provider stopped part-way -- a rate limit or a stall
			// that outlasted its retries mid-turn -- returns what it had done,
			// not an error, and the user is told "Ask again to continue". So it
			// is asked again, without spending the agent's own continues; when
			// the provider keeps stopping it, the attempt is the provider's.
			if res.Incomplete != nil && res.Incomplete.Reason == protocol.IncompleteProviderError {
				// A long task is one request: asked again it would start over,
				// so its provider stop is the provider's attempt, run again whole.
				if tr.ProviderStops++; tr.ProviderStops > maxProviderStops || run != nil {
					stoppedByProvider = true
					break
				}
			} else if run != nil || tr.Turns-tr.ProviderStops > continues || res.Incomplete == nil {
				// "continue" only after a turn that stopped on its budget: a
				// turn that ended with an answer is what the user gets. A long
				// task is one request: it runs its own segments.
				break
			}
			history = append(history, chatMessage{Role: "user", Content: ask},
				chatMessage{Role: "assistant", Content: res.FinalText + report})
			ask = "continue"
		}
		tr.Seconds = time.Since(start).Seconds()
		tr.Tools, tr.ToolCalls = toolNames(sigs), len(sigs)
		for _, sig := range sigs {
			tr.CallsMade = append(tr.CallsMade, shortCall(sig))
		}
		tr.BudgetStop = err == nil && res.Incomplete != nil && !stoppedByProvider
		tr.FinalWords = lastRunes(res.FinalText, 600)

		// Graded whatever happened: a run cut short by the provider may still
		// have fixed the bug before it was.
		grade := exec.Command(filepath.Join(taskDir, "check.sh"), dir)
		// Python's cached bytecode kept apart, so a .pyc a run left in the project
		// cannot stand in for a source file changed in the same second, at the
		// same size (a one-character fix): graded is the code as it is.
		grade.Env = append(os.Environ(), "PYTHONPYCACHEPREFIX="+t.TempDir())
		out, gerr := grade.CombinedOutput()
		tr.Pass = gerr == nil
		tr.Grade = lastRunes(string(out), 800)
		if keep != "" { // the trial as it ended, to read afterwards
			if err := copyTree(dir, filepath.Join(keep, keepAs)); err != nil {
				t.Logf("keeping %s: %v", keepAs, err)
			}
		}
		switch {
		case tr.Pass:
			tr.Outcome = "pass"
		case err != nil && asModelError(err).Class == ClassRateLimited:
			tr.Outcome, tr.Error = "rate-limited", asModelError(err).Detail()
		case err != nil:
			tr.Outcome, tr.Error = "provider-error", asModelError(err).Detail()
		case stoppedByProvider:
			tr.Outcome, tr.Error = "provider-error", fmt.Sprintf("the provider stopped %d turns part-way: %s",
				tr.ProviderStops, res.Incomplete.Detail)
		default:
			tr.Outcome = "fail"
		}
		return tr
	}

	var results []realRepoTrial
	spent := 0.0
	for _, name := range names {
		if maxUSD > 0 && spent >= maxUSD {
			t.Logf("REALREPO stopping: $%.2f spent of the $%.2f cap; %s and the tasks after it not started", spent, maxUSD, name)
			break
		}
		taskDir := filepath.Join(root, name)
		prompt, err := os.ReadFile(filepath.Join(taskDir, "task.txt"))
		if err != nil {
			t.Fatal(err)
		}
		meta, _ := os.ReadFile(filepath.Join(taskDir, "meta.txt"))
		lang := ""
		for _, line := range strings.Split(string(meta), "\n") {
			if v, ok := strings.CutPrefix(line, "lang="); ok {
				lang = v
			}
		}
		// A trial the provider ended -- a stall, or a rate limit that outlasted
		// its retries -- is run again from scratch, up to retries times, so what
		// is graded is the agent's work rather than the provider's luck. Each
		// cut is kept to report.
		var (
			tr   realRepoTrial
			cuts []string
		)
		attempt := 0
		for {
			attempt++
			tr = runTrial(name, lang, strings.TrimSpace(string(prompt)), fmt.Sprintf("%s.attempt%d", name, attempt))
			spent += tr.USD
			// A CAP THAT CANNOT BIND IS NOT A CAP: the provider sent no cost and
			// no prices were given for its tokens, so stop rather than spend on.
			if maxUSD > 0 && tr.Calls > 0 && tr.USD == 0 {
				t.Fatalf("REALREPO_MAX_USD is set, but %s billed nothing this harness can see: the provider reports "+
					"no cost, so give REALREPO_USD_PER_MTOK_IN, _OUT and _CACHED", name)
			}
			if tr.Pass || (tr.Outcome != "provider-error" && tr.Outcome != "rate-limited") || len(cuts) >= retries ||
				(maxUSD > 0 && spent >= maxUSD) {
				break
			}
			cuts = append(cuts, fmt.Sprintf("after %d model calls and %.0f s: %s", tr.Calls, tr.Seconds, lastRunes(tr.Error, 200)))
			t.Logf("%-34s PROVIDER-CUT after %d model calls and %.0f s, so it runs again from scratch: %s",
				name, tr.Calls, tr.Seconds, lastRunes(tr.Error, 160))
		}
		tr.ProviderCuts = cuts
		if keep != "" { // the attempt that counts keeps the task's own name
			_ = os.Rename(filepath.Join(keep, fmt.Sprintf("%s.attempt%d", name, attempt)), filepath.Join(keep, name))
		}
		results = append(results, tr)
		t.Logf("%-34s %-6s %-14s turns=%d calls=%2d tools=%2d edits=%d/%d refused  %5.0fs  tokens=%d  $%.3f  "+
			"tested-final=%v claim-unsupported=%v tests-changed=%d outside=%d  provider stops=%d cuts=%d",
			name, lang, strings.ToUpper(tr.Outcome), tr.Turns, tr.Calls, tr.ToolCalls, tr.Refused, tr.Edits,
			tr.Seconds, tr.Tokens, tr.USD, tr.TestedFinal, tr.ClaimUnsupported, len(tr.TestsChanged), len(tr.OutsideReview),
			tr.ProviderStops, len(cuts))
		t.Logf("    tools: %s", strings.Join(tr.Tools, " "))
		if tr.Error != "" {
			t.Logf("    error: %s", lastRunes(tr.Error, 300))
		}
		if !tr.Pass {
			for _, c := range tr.CallsMade {
				t.Logf("      %s", truncateForLog(c))
			}
			t.Logf("    grade: %s", lastRunes(strings.ReplaceAll(tr.Grade, "\n", " | "), 400))
		}
		t.Logf("    final words: %q", lastRunes(tr.FinalWords, 300))
		if path := os.Getenv("REALREPO_OUT"); path != "" {
			data, _ := json.MarshalIndent(results, "", "  ")
			_ = os.WriteFile(path, data, 0o644)
		}
	}
	passed := 0
	for _, r := range results {
		if r.Pass {
			passed++
		}
	}
	t.Logf("REALREPO model=%s arm=%s passed=%d/%d spent=$%.2f", model, arm, passed, len(results), spent)
}

// observe records the pre-registered metrics from one turn that ended with an
// answer, before what it offered is applied (tr.Edits still counts the turns
// before it). The trial's final state is what is graded, so a turn that
// neither changed nor tested anything leaves TestedFinal as it was.
func (tr *realRepoTrial) observe(res agentResult, offered []editapply.EditBlock, wc *protocol.WorkingCopyInfo,
	runs *turnRuns, editsNow int, outside, outsideTests []string) {
	if passed, ran := runs.final(len(offered) > 0, editsNow); ran {
		tr.TestedFinal = passed
	} else if len(offered) > 0 {
		tr.TestedFinal = false
	}
	if wc != nil {
		tr.Checked, tr.CheckOK = wc.Checked, wc.Passed
	}
	tr.noteOutside(outside, outsideTests)
	tr.TestsChanged = unionSorted(tr.TestsChanged, testsChangedIn(offered))
	tr.ClaimUnsupported = claimsTestsPass(res.FinalText) && tr.Edits+len(offered) > 0 &&
		(!tr.TestedFinal || len(tr.TestsChanged) > 0)
}

// A RUN'S RESULT, WHEREVER IT RAN. A build or test run in a working copy is in
// the turn's record (proposalSink.checks) in every arm: sandbox_exec's, and
// run_command's under one view. A run_command in the real project -- every one
// before one view -- is in no record, and its exit code went only to the model.
// Yet once a turn's edits are accepted, the next turn's run in the real project
// tests them: "continue" after "Now let's run the tests" is that turn exactly.
// Counting only the records would call every such run untested, and every
// claim after it unsupported, in arm A alone. So the eval stands a wrapper in
// for the user's shell (watchShell) and reads each run_command's folder and
// exit code from it; the activity of each finished call (onActivity) puts the
// two kinds of run in the order they happened.

// trialRun is one build or test run, or any other command, of a turn.
type trialRun struct {
	command string
	passed  bool
	real    bool // in the real project; otherwise in the turn's working copy...
	edits   int  // ...when the copy had this many edits
}

// turnRuns collects a turn's runs as its calls finish.
type turnRuns struct {
	sink    *proposalSink
	shell   *shellWatch
	project string
	checks  int // sink.checks already taken
	list    []trialRun
}

// observe is the turn's onActivity: when a call finishes, the working-copy
// check it recorded and the shell it ran in the real project, if any. Calls
// run one at a time, and a check is recorded before its call is reported.
func (r *turnRuns) observe(a protocol.ToolActivity) {
	if a.Phase != protocol.ToolPhaseSucceeded && a.Phase != protocol.ToolPhaseFailed {
		return
	}
	for ; r.checks < len(r.sink.checks); r.checks++ {
		c := r.sink.checks[r.checks]
		r.list = append(r.list, trialRun{command: c.command, passed: c.passed, edits: c.edits})
	}
	for _, sh := range r.shell.since() {
		if isWithin(r.project, sh.dir) { // one in the working copy is in the checks above
			r.list = append(r.list, trialRun{command: sh.command, passed: sh.status == 0, real: true})
		}
	}
}

// final says whether the last build or test run that saw the final version of
// the trial's changes passed, and whether there was one. A turn that offered
// changes saw them only in its working copy, at its last edit count. A turn
// that offered none left the project as the turns before it made it, so a run
// in its copy at its last edit count, or in the real project, saw that.
func (r *turnRuns) final(changed bool, editsNow int) (passed, ran bool) {
	for i := len(r.list) - 1; i >= 0; i-- {
		run := r.list[i]
		if !isVerifyingLine(run.command) || (run.real && changed) || (!run.real && run.edits != editsNow) {
			continue
		}
		return run.passed, true
	}
	return false, false
}

// shellWatch is the eval's stand-in for the user's shell: a script, named so
// that run_command picks it as it picks bash or zsh (shellPath), that runs the
// shell run_command would have run, with the same arguments and SHELL as the
// user had it, and notes in a log where each run started and how it ended.
type shellWatch struct {
	log  string
	read int64
}

// shellRun is one line of the shell log.
type shellRun struct {
	dir, command string
	status       int
}

func watchShell(t *testing.T) *shellWatch {
	t.Helper()
	dir := t.TempDir()
	user := os.Getenv("SHELL")
	run := `/bin/sh -c "${@: -1}"` // what shellPath runs for any other shell
	if base := filepath.Base(user); base == "bash" || base == "zsh" {
		run = shellQuote(user) + ` "$@"`
	}
	setShell := "env -u SHELL "
	if user != "" {
		setShell = "SHELL=" + shellQuote(user) + " "
	}
	w := &shellWatch{log: filepath.Join(dir, "shell.log")}
	script := "#!/bin/bash\n" + setShell + run + "\nstatus=$?\n" +
		`printf '%s\t%s\t%s\n' "$(pwd -P)" "$status" "$(printf '%s' "${@: -1}" | tr '\n\t' '  ')" >> ` +
		shellQuote(w.log) + "\nexit \"$status\"\n"
	wrapper := filepath.Join(dir, "bash")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", wrapper)
	return w
}

// skip passes over what is in the log so far.
func (w *shellWatch) skip() { _ = w.since() }

// since is what ran since it was last asked.
func (w *shellWatch) since() []shellRun {
	f, err := os.Open(w.log)
	if err != nil {
		return nil // nothing has run yet
	}
	defer f.Close()
	data, err := io.ReadAll(io.NewSectionReader(f, w.read, 1<<40))
	if err != nil {
		return nil
	}
	end := strings.LastIndexByte(string(data), '\n') + 1 // a line still being written waits
	w.read += int64(end)
	var runs []shellRun
	for _, line := range strings.Split(string(data[:end]), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		status, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		runs = append(runs, shellRun{dir: fields[0], status: status, command: fields[2]})
	}
	return runs
}

// physicalDir is dir with its links resolved, as the shell log's pwd -P has it.
func physicalDir(t *testing.T, dir string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// shellQuote is s as one word of a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// noteOutside adds what a turn changed in the real project outside review.
func (tr *realRepoTrial) noteOutside(files, tests []string) {
	tr.OutsideReview = unionSorted(tr.OutsideReview, files)
	tr.TestsChanged = unionSorted(tr.TestsChanged, tests)
}

// unionSorted is the sorted set of every list's items.
func unionSorted(lists ...[]string) []string {
	set := map[string]bool{}
	for _, list := range lists {
		for _, item := range list {
			set[item] = true
		}
	}
	return sortedSet(set)
}

// projectScan is the real project as git shows it -- tracked files, and
// untracked ones it does not ignore -- as each file's content hash, with the
// text of its test files kept.
type projectScan struct {
	sums  map[string][sha256.Size]byte
	tests map[string]string
}

// scanProject looks at the project the agent works on, as the user would see
// it changed: build output the project ignores is left out, as the working
// copy and its review leave it out.
func scanProject(t *testing.T, dir string) projectScan {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("listing the project's files: %v", err)
	}
	scan := projectScan{sums: map[string][sha256.Size]byte{}, tests: map[string]string{}}
	for _, rel := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			continue // a tracked file that is gone
		}
		scan.sums[rel] = sha256.Sum256(data)
		if isTestPath(rel) {
			scan.tests[rel] = string(data)
		}
	}
	return scan
}

// changedSince lists the files that are new, changed or gone since before, and
// the existing tests among them whose existing lines changed (or that are gone).
func (s projectScan) changedSince(before projectScan) (files, tests []string) {
	for rel, sum := range s.sums {
		if was, ok := before.sums[rel]; !ok || was != sum {
			files = append(files, rel)
			if text, ok := before.tests[rel]; ok && changesExistingLines(text, s.tests[rel]) {
				tests = append(tests, rel)
			}
		}
	}
	for rel := range before.sums {
		if _, ok := s.sums[rel]; !ok {
			files = append(files, rel)
			if before.tests[rel] != "" {
				tests = append(tests, rel)
			}
		}
	}
	sort.Strings(files)
	sort.Strings(tests)
	return files, tests
}

// envFloat is a float knob: 0 when unset; a bad one fails the run.
func envFloat(t *testing.T, name string) float64 {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		t.Fatalf("%s=%q is not a non-negative number", name, v)
	}
	return f
}

// shortCall is one call as CallsMade keeps it: long enough to show a command
// and the folder it ran in, which truncateForLog's 90 bytes can cut off.
func shortCall(sig string) string {
	sig = strings.TrimPrefix(sig, "builtin__")
	if r := []rune(sig); len(r) > 240 {
		return string(r[:239]) + "…"
	}
	return sig
}
