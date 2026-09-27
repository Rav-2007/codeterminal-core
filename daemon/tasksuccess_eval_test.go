//go:build eval

// THE TASK-SUCCESS EVAL: did the agent actually get the job done?
//
// Every other agent eval here measures a property of the loop -- it stopped, it
// did not repeat itself, its answer used what a tool returned, a judge liked
// it. None of them asks the question a user asks: after I accept what it
// proposed, does the project do what I asked? This does, objectively:
//
//  1. copy a small fixture project (testdata/tasks/<name>/workspace) to a
//     scratch directory;
//  2. run the REAL agent loop on it -- production system prompt, production
//     tool policies and budgets (models.agent.json), the real model;
//  3. apply every edit it proposed, in order, exactly as a user pressing y on
//     each one would;
//  4. add the task's hidden tests (testdata/tasks/<name>/hidden), which the
//     model never saw, and grade: `go build ./...` and `go test ./...` inside
//     the same sandbox sandbox_exec uses, plus per-task checks (the test file
//     the model was told to make pass is unchanged, the renamed symbol is gone).
//
// No model judge anywhere. Pass means the hidden tests pass.
//
// Every milestone of the agent-workflow plan (staged workspace, specs, build
// from spec) is measured by this, before and after. TestTaskFixturesAreValid
// below checks the fixtures themselves without a model: each check must FAIL
// on the untouched project and PASS on the reference solution, so no task can
// be passed by doing nothing.
//
//	export PATH=$HOME/.local/go/bin:$PATH
//	MOCHIII_API_BASE=https://openrouter.ai/api/v1 MOCHIII_API_KEY=... \
//	  go test -tags eval -run TestTaskSuccess -v -timeout 90m .
//
// Knobs: TASK_EVAL_TRIALS (default 2), TASK_EVAL_ONLY (comma-separated task
// names), TASK_EVAL_MAX_TOKENS (stop starting trials past this many tokens,
// default 4,000,000), TASK_EVAL_TIER (models.json tier, default primary),
// TASK_EVAL_OUT (append a JSON summary line to this file), TASK_EVAL_LABEL,
// TASK_EVAL_MAX_ITERATIONS (override the per-turn model-call ceiling),
// TASK_EVAL_NO_WORKING_COPY (run without the working copy, as before M1),
// TASK_EVAL_PIPELINE (e.g. "planner,coder": run the trials through those
// specialist phases, as /team:planner,coder does).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"mochiii/editapply"
	"mochiii/protocol"
)

const taskFixtureRoot = "testdata/tasks"

// taskSpec is one task: its extra checks beyond "the hidden tests pass".
type taskSpec struct {
	name string
	// goProject is false for a task whose result is not code (create_file).
	goProject bool
	// unchanged lists files the model must NOT edit -- the tests it was told to
	// make pass. Editing them is the cheapest way to "pass", and not the task.
	unchanged []string
	// extra is any further check; nil means none.
	extra func(dir string) error
}

var taskSpecs = []taskSpec{
	{name: "create_file", extra: func(dir string) error {
		got, err := os.ReadFile(filepath.Join(dir, "docs", "todo.md"))
		if err != nil {
			return fmt.Errorf("docs/todo.md: %v", err)
		}
		if strings.TrimSpace(string(got)) != "Ship the release notes" {
			return fmt.Errorf("docs/todo.md is %q", got)
		}
		return nil
	}},
	{name: "fix_failing_test", goProject: true, unchanged: []string{"mathx/mathx_test.go"}},
	{name: "add_function_with_test", goProject: true, extra: func(dir string) error {
		return ownTestMentions(dir, "strutil", "IsPalindrome")
	}},
	{name: "two_edits_one_file", goProject: true},
	{name: "rename_across_files", goProject: true, extra: func(dir string) error {
		return noGoFileContains(dir, "CalcTotal")
	}},
	{name: "edge_case_bug", goProject: true, extra: func(dir string) error {
		return ownTestsChanged(dir, "stats", "stats/stats_test.go")
	}},
	{name: "feature_from_spec", goProject: true, extra: func(dir string) error {
		return ownTestMentions(dir, "slug", "Slugify")
	}},
	{name: "needs_iteration", goProject: true, unchanged: []string{"units/units_test.go"}},
}

// ---------------------------------------------------------------------------
// Fixture handling and grading -- shared by both tests in this file.
// ---------------------------------------------------------------------------

// copyTree copies src into dst (created). Plain files only; the fixtures have
// nothing else.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// freshTaskWorkspace resets the ONE scratch workspace every trial uses and
// fills it with a task's starting project.
//
// ONE FIXED PATH, on purpose: sandbox_exec gives each workspace its own HOME,
// keyed on the path, and Go's build cache lives there. A new path per trial
// would compile the standard library from cold every time and push `go test`
// toward sandbox_exec's 30-second limit -- measuring the cache, not the agent.
// Under the user cache dir rather than /tmp, because bwrap mounts its own /tmp.
func freshTaskWorkspace(t *testing.T, task string) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cache, "mochiii-taskeval", "ws")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(filepath.Join(taskFixtureRoot, task, "workspace"), dir); err != nil {
		t.Fatalf("copying %s: %v", task, err)
	}
	real, err := editapply.ResolveRealWorkspaceRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// sandboxGo runs one go command through sandbox_exec itself, so grading uses
// the same confinement the agent's own commands get -- the graded code is
// model-written and is not run on the host.
func sandboxGo(dir, command string) (bool, string) {
	srv := &Server{workspace: dir, logger: log.New(io.Discard, "", 0), cfg: &Config{}}
	args, _ := json.Marshal(map[string]string{"command": command})
	res, err := srv.builtinSandboxExec(context.Background(), args)
	if err != nil {
		return false, err.Error()
	}
	failed := res.IsError || strings.HasPrefix(res.Content, "Command exited with error") ||
		strings.HasPrefix(res.Content, "Command timed out")
	return !failed, res.Content
}

// gradeTask adds the hidden tests and decides pass or fail. The reason is
// empty on a pass.
func gradeTask(spec taskSpec, dir string) string {
	fixture := filepath.Join(taskFixtureRoot, spec.name)
	for _, rel := range spec.unchanged {
		want, _ := os.ReadFile(filepath.Join(fixture, "workspace", rel))
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || string(got) != string(want) {
			return rel + " was changed; the task was to make it pass, not to edit it"
		}
	}
	if spec.extra != nil {
		if err := spec.extra(dir); err != nil {
			return err.Error()
		}
	}
	if !spec.goProject {
		return ""
	}
	if hidden := filepath.Join(fixture, "hidden"); dirExists(hidden) {
		if err := copyTree(hidden, dir); err != nil {
			return "adding hidden tests: " + err.Error()
		}
	}
	for _, cmd := range []string{"go build ./...", "go test ./..."} {
		if ok, out := sandboxGo(dir, cmd); !ok {
			return cmd + " failed: " + lastLines(out, 6)
		}
	}
	return ""
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// ownTestMentions requires a test the MODEL wrote (not the hidden one) in pkg
// that mentions symbol: the task asked for one.
func ownTestMentions(dir, pkg, symbol string) error {
	matches, _ := filepath.Glob(filepath.Join(dir, pkg, "*_test.go"))
	for _, m := range matches {
		if strings.HasPrefix(filepath.Base(m), "zz_hidden") {
			continue
		}
		if data, err := os.ReadFile(m); err == nil && strings.Contains(string(data), symbol) {
			return nil
		}
	}
	return fmt.Errorf("no test in %s/ mentions %s; the task asked for one", pkg, symbol)
}

// ownTestsChanged requires the model to have added or changed a test in pkg.
func ownTestsChanged(dir, pkg, original string) error {
	want, _ := os.ReadFile(filepath.Join(taskFixtureRoot, "edge_case_bug", "workspace", original))
	matches, _ := filepath.Glob(filepath.Join(dir, pkg, "*_test.go"))
	for _, m := range matches {
		if strings.HasPrefix(filepath.Base(m), "zz_hidden") {
			continue
		}
		rel, _ := filepath.Rel(dir, m)
		if rel != original {
			return nil
		}
		if got, _ := os.ReadFile(m); string(got) != string(want) {
			return nil
		}
	}
	return fmt.Errorf("no test was added or changed in %s/; the task asked for one", pkg)
}

// noGoFileContains fails if any .go file of the project still contains s.
//
// NOT .mochiii/: applying an edit backs the original up there, so the old name
// is always in a backup -- a grader that counted it failed a correct rename
// (it did, in the first baseline run).
func noGoFileContains(dir, s string) error {
	var found []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && editapply.IsProtectedDirName(d.Name()) {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if data, _ := os.ReadFile(path); strings.Contains(string(data), s) {
			rel, _ := filepath.Rel(dir, path)
			found = append(found, rel)
		}
		return nil
	})
	if len(found) > 0 {
		return fmt.Errorf("%s is still in %s", s, strings.Join(found, ", "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// The fixtures are valid: no model, no key.
// ---------------------------------------------------------------------------

// Each check FAILS on the untouched project and PASSES on the reference
// solution. Without the first half a task can be passed by doing nothing;
// without the second the check may be impossible.
func TestTaskFixturesAreValid(t *testing.T) {
	for _, spec := range taskSpecs {
		t.Run(spec.name, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(taskFixtureRoot, spec.name, "task.txt")); err != nil {
				t.Fatalf("task.txt: %v", err)
			}
			dir := freshTaskWorkspace(t, spec.name)
			why := gradeTask(spec, dir)
			if why == "" {
				t.Fatal("the untouched project already passes; this task measures nothing")
			}
			t.Logf("untouched fails as it should: %s", why)
			// The solution goes in THROUGH THE EDIT PATH the eval uses, backups
			// and all -- the first grader failed a correct rename because it
			// read the backup of the original, which copying files never makes.
			dir = freshTaskWorkspace(t, spec.name)
			solution := filepath.Join(taskFixtureRoot, spec.name, "solution")
			var blocks []editapply.EditBlock
			_ = filepath.WalkDir(solution, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(solution, path)
				after, _ := os.ReadFile(path)
				before, _ := os.ReadFile(filepath.Join(dir, rel))
				blocks = append(blocks, editapply.EditBlock{FilePath: filepath.ToSlash(rel),
					Search: string(before), Replace: string(after)})
				return nil
			})
			if refused := applyProposals(dir, blocks); refused != 0 {
				t.Fatalf("%d of the solution's edits were refused", refused)
			}
			if why := gradeTask(spec, dir); why != "" {
				t.Fatalf("the reference solution fails: %s", why)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The live eval.
// ---------------------------------------------------------------------------

// approveAll says yes to every question, as a user pressing y would, and
// counts them: how often a real user would have been asked is part of the cost.
type approveAll struct{ asked int }

func (a *approveAll) Ask(context.Context, protocol.ToolApprovalRequest) approvalDecision {
	a.asked++
	return approvalDecision{Decision: protocol.ApprovalApprove}
}

type taskTrial struct {
	Task       string  `json:"task"`
	Pass       bool    `json:"pass"`
	Why        string  `json:"why,omitempty"`
	Calls      int     `json:"model_calls"`
	ToolCalls  int     `json:"tool_calls"`
	Asked      int     `json:"approvals_asked"`
	Proposed   int     `json:"edits_proposed"`
	Refused    int     `json:"edits_refused"`
	Tokens     int     `json:"tokens"`
	Seconds    float64 `json:"seconds"`
	BudgetStop bool    `json:"budget_stop"`
	// What the agent last ran in its working copy, and the tools it called in
	// order -- so a failure can be read, not only counted.
	Checked     string   `json:"checked,omitempty"`
	CheckPassed bool     `json:"check_passed,omitempty"`
	Tools       []string `json:"tools,omitempty"`
}

// toolNames reduces call signatures (name + arguments) to names.
func toolNames(sigs []string) []string {
	out := make([]string, 0, len(sigs))
	for _, sig := range sigs {
		name, _, _ := strings.Cut(sig, "(")
		name, _, _ = strings.Cut(name, " ")
		out = append(out, name)
	}
	return out
}

// applyProposals applies every block in order, as a user pressing y on each,
// and returns how many were refused. Edits outside the workspace are refused
// here: an eval must not write to the real home folder.
func applyProposals(dir string, blocks []editapply.EditBlock) (refused int) {
	if len(blocks) == 0 {
		return 0
	}
	backup, err := editapply.NewBackupSessionDir(dir)
	if err != nil {
		return len(blocks)
	}
	for _, b := range blocks {
		p, err := editapply.PrepareEditAnywhere(dir, b)
		if err != nil || p.OutsideRoot != "" {
			refused++
			continue
		}
		if err := editapply.Apply(dir, p, backup); err != nil {
			refused++
		}
	}
	return refused
}

func TestTaskSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live task eval in -short mode")
	}
	apiBase, apiKey := os.Getenv("MOCHIII_API_BASE"), os.Getenv("MOCHIII_API_KEY")
	if apiBase == "" || apiKey == "" {
		t.Skip("MOCHIII_API_BASE/KEY unset -- this eval makes real billed calls and will not guess")
	}

	// PRODUCTION configuration, so the number describes the product: the
	// agent config run-tui.sh starts with, its tool policies and budgets.
	cfg, err := LoadConfig("../models.agent.json")
	if err != nil {
		t.Fatalf("loading models.agent.json: %v", err)
	}
	tier := envOr("TASK_EVAL_TIER", "primary")
	model := cfg.Tiers[tier].Slug
	if model == "" {
		t.Fatalf("no tier %q in models.agent.json", tier)
	}
	routing := cfg.ZDR.resolvedProviderRouting()
	mcpCfg := cfg.MCP
	tools := map[string]string{}
	for k, v := range mcpCfg.Builtin.Tools {
		tools[k] = v
	}
	// A fixture has no index, so search_code could only fail; leaving it on
	// would measure the model spending calls on a tool that cannot answer here.
	tools["search_code"] = PolicyDeny
	mcpCfg.Builtin.Tools = tools
	// TASK_EVAL_MAX_ITERATIONS overrides the per-turn model-call ceiling, to
	// ask whether the budget is what a failure ran into.
	if os.Getenv("TASK_EVAL_MAX_ITERATIONS") != "" {
		mcpCfg.Budget.MaxIterations = envInt(t, "TASK_EVAL_MAX_ITERATIONS", mcpCfg.Budget.MaxIterations)
	}

	trials := envInt(t, "TASK_EVAL_TRIALS", 2)
	maxTokens := envInt(t, "TASK_EVAL_MAX_TOKENS", 4_000_000)
	only := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("TASK_EVAL_ONLY"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			only[n] = true
		}
	}

	// TASK_EVAL_PIPELINE runs every trial through the named specialist phases
	// (as /team:a,b does) instead of the single agent.
	var phases []*agentRole
	if names := strings.TrimSpace(os.Getenv("TASK_EVAL_PIPELINE")); names != "" {
		var unknown []string
		phases, unknown = resolvePipeline(strings.Split(names, ","))
		if len(unknown) > 0 || len(phases) == 0 {
			t.Fatalf("TASK_EVAL_PIPELINE=%q: unknown phase(s) %v", names, unknown)
		}
	}

	rec := newCostRecorder(t, apiBase)
	logger := log.New(io.Discard, "", 0)
	t.Logf("model=%s  budget=%+v  trials=%d", model, mcpCfg.Budget, trials)

	var results []taskTrial
	totalTokens := 0
	for _, spec := range taskSpecs {
		if len(only) > 0 && !only[spec.name] {
			continue
		}
		prompt, err := os.ReadFile(filepath.Join(taskFixtureRoot, spec.name, "task.txt"))
		if err != nil {
			t.Fatal(err)
		}
		for trial := 1; trial <= trials; trial++ {
			if totalTokens > maxTokens {
				t.Logf("STOPPING: %d tokens spent, over TASK_EVAL_MAX_TOKENS=%d", totalTokens, maxTokens)
				break
			}
			dir := freshTaskWorkspace(t, spec.name)
			srv := &Server{apiBase: rec.base(), apiKey: apiKey, workspace: dir, logger: logger,
				cfg: &Config{MCP: mcpCfg}}
			// The same sink and messages a real turn gets (newTurnSink), so the
			// working copy is measured exactly as it ships. TASK_EVAL_NO_WORKING_COPY
			// runs the pre-working-copy behaviour for comparison.
			srv.cfg.MCP.NoWorkingCopy = os.Getenv("TASK_EVAL_NO_WORKING_COPY") != ""
			sink, messages := srv.newTurnSink("auto", nil,
				buildChatMessages(defaultSystemPrompt, nil, strings.TrimSpace(string(prompt))))
			registry, _ := srv.buildRegistry(context.Background(), logger, sink, "")
			appr := &approveAll{}

			rec.reset()
			start := time.Now()
			var res agentResult
			var err error
			if len(phases) == 0 {
				res, err = srv.runAgentLoop(context.Background(), start, registry, model, "auto",
					messages, routing, appr,
					func(string) error { return nil }, nil, nil, nil, nil, nil, nil)
			} else {
				res, err = srv.runOrchestrated(context.Background(), start, registry, model, "auto",
					messages, routing, appr,
					func(string) error { return nil }, nil, nil, nil, nil, phases)
			}
			elapsed := time.Since(start)
			_ = registry.Close()
			calls, pt, ct := rec.snapshot()
			totalTokens += pt + ct

			tr := taskTrial{Task: spec.name, Calls: calls, Tokens: pt + ct, Seconds: elapsed.Seconds(), Asked: appr.asked}
			if err != nil {
				sink.discard()
				tr.Why = "TRANSPORT: " + err.Error()
				results = append(results, tr)
				continue
			}
			textBlocks, _ := srv.parseAndLogEditBlocks(res.FinalText)
			filed, wc, _ := sink.finish()
			blocks := append(filed, textBlocks...)
			if wc != nil {
				tr.Checked, tr.CheckPassed = wc.Checked, wc.Passed
			}
			tr.Tools = toolNames(res.ToolSignatures)
			tr.ToolCalls = len(res.ToolSignatures)
			tr.Proposed = len(blocks)
			tr.BudgetStop = res.Incomplete != nil
			tr.Refused = applyProposals(dir, blocks)
			tr.Why = gradeTask(spec, dir)
			tr.Pass = tr.Why == ""
			results = append(results, tr)
			t.Logf("%-24s trial %d  %-4s  calls=%2d tools=%2d edits=%d/%d refused  %5.0fs  %s",
				spec.name, trial, passWord(tr.Pass), tr.Calls, tr.ToolCalls, tr.Refused, tr.Proposed,
				tr.Seconds, tr.Why)
			t.Logf("    tools: %s", strings.Join(tr.Tools, " "))
			if !tr.Pass {
				// The arguments too, on a failure: "read the same file five times"
				// and "guessed five paths that do not exist" look identical as names.
				for _, sig := range res.ToolSignatures {
					t.Logf("      %s", truncateForLog(strings.TrimPrefix(sig, "builtin__")))
				}
			}
		}
	}
	reportTaskSuccess(t, model, results)
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func reportTaskSuccess(t *testing.T, model string, results []taskTrial) {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("VOID: no trials ran")
	}
	passed, transport, budget := 0, 0, 0
	var calls, tokens []int
	var seconds []float64
	byTask := map[string][2]int{}
	for _, r := range results {
		if strings.HasPrefix(r.Why, "TRANSPORT") {
			transport++
			continue
		}
		c := byTask[r.Task]
		c[1]++
		if r.Pass {
			passed++
			c[0]++
		}
		byTask[r.Task] = c
		if r.BudgetStop {
			budget++
		}
		calls = append(calls, r.Calls)
		tokens = append(tokens, r.Tokens)
		seconds = append(seconds, r.Seconds)
	}
	graded := len(results) - transport
	t.Log("--- per task ---")
	names := make([]string, 0, len(byTask))
	for n := range byTask {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("  %-24s %d/%d", n, byTask[n][0], byTask[n][1])
	}
	if graded == 0 {
		t.Fatalf("VOID: every trial failed at the transport layer")
	}
	sort.Ints(calls)
	sort.Ints(tokens)
	sort.Float64s(seconds)
	summary := map[string]any{
		"when": time.Now().Format(time.RFC3339), "label": os.Getenv("TASK_EVAL_LABEL"), "model": model,
		"passed": passed, "graded": graded, "transport_errors": transport, "budget_stops": budget,
		"median_calls": calls[len(calls)/2], "median_tokens": tokens[len(tokens)/2],
		"median_seconds": seconds[len(seconds)/2], "trials": results,
	}
	t.Logf("=== TASK SUCCESS %d/%d (%.0f%%)  median calls %d, tokens %d, %.0fs  budget stops %d  transport errors %d ===",
		passed, graded, 100*float64(passed)/float64(graded), calls[len(calls)/2], tokens[len(tokens)/2],
		seconds[len(seconds)/2], budget, transport)
	if out := os.Getenv("TASK_EVAL_OUT"); out != "" {
		line, _ := json.Marshal(summary)
		f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	if float64(transport)/float64(len(results)) > 0.10 {
		t.Fatalf("VOID: %d/%d trials failed at the transport layer", transport, len(results))
	}
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func envInt(t *testing.T, name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("%s=%q is not a positive integer", name, v)
	}
	return n
}
