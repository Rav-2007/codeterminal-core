//go:build eval

// Phase 4.5: THE LOOP GATE.
//
// Phase 0 measured whether Flash can EMIT a tool call. It did not measure
// whether Flash can run a LOOP -- every multistep scenario there said "Start
// with the first step only". Consuming a tool result, deciding whether to
// continue, and knowing when to stop are different and harder properties, and
// they are the ones that decide whether this product works.
//
// Two Phase 0 findings point straight at the risk: list_directory is an
// attractor the model reaches for when uncertain, and tool selection degrades
// as the menu widens. In a loop the context grows with every tool result, so
// both pressures compound, and the characteristic failure is a turn that never
// stops.
//
// So this runs the REAL loop with the REAL built-in tools against the REAL
// model, over this repository, and gates Phases 5-7 on the result. Building
// consent UX for three clients on top of an unmeasured loop is the expensive
// version of the mistake Phase 0 exists to prevent.
//
//	export CODETERMINAL_API_BASE=https://openrouter.ai/api/v1 CODETERMINAL_API_KEY=...
//	go test -tags eval -run TestAgentLoopReliability -v ./daemon
//
// Direct to OpenRouter, not through the managed proxy: Phase 0's first run was
// VOIDED by quota exhaustion partway through, and this run is several times
// more expensive per scenario.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// ---------------------------------------------------------------------------
// The bar, declared before the measurement.
// ---------------------------------------------------------------------------

// Same discipline as Phase 0: written down before the first run, and the
// documented consequence of a failure is rerouting or redesigning, never
// lowering the number. See the plan's Phase 4.5 decision rule.
var loopGates = []struct {
	name      string
	threshold float64
	why       string
}{
	{"terminates", 0.95,
		"the turn ended because the model was done, not because a budget stopped it (a loop's characteristic failure is not stopping)"},
	{"no_repeated_call", 0.95,
		"no identical call (same tool, same arguments) was made twice in one turn (the second-most-characteristic failure: spinning on one tool)"},
	{"uses_tool_output", 0.90,
		"the final answer contains something only a successful tool call could have supplied (otherwise the loop is not earning its tokens)"},
	{"within_four_iterations", 0.80,
		"the answer was reached in 4 model calls or fewer (the economics assume a handful, not a dozen)"},
}

const (
	loopTrials             = 2
	loopMaxTransportErrors = 0.10
)

// ---------------------------------------------------------------------------
// Scenarios.
// ---------------------------------------------------------------------------

// loopScenario is one task that CANNOT be answered without chaining at least
// two tool calls. The prompts deliberately do not name the file to read: a task
// whose first step is "read this exact path" tests one call, not a loop.
type loopScenario struct {
	name     string
	category string
	prompt   string
	// wantInAnswer are strings that can only appear if the model actually read
	// what it needed. Matched case-insensitively, ANY one is enough -- this
	// measures "did tool output reach the answer", not phrasing.
	wantInAnswer []string
	// minCalls is the number of tool calls a correct approach needs at minimum.
	minCalls int
}

var loopScenarios = []loopScenario{
	// --- find then read: the model must locate a file before it can answer ---
	{"chain/find_const", "chain",
		"What is the value of the ProtocolVersion constant? Look it up in the protocol package rather than guessing.",
		[]string{"1"}, 1},
	{"chain/read_after_list", "chain",
		"Look inside the editapply directory, then tell me the name of the file that contains the workspace-root resolver.",
		[]string{"workspace.go"}, 2},
	{"chain/find_default_tier", "chain",
		"Find models.json in this repo and tell me which model slug the primary tier uses.",
		[]string{"deepseek"}, 1},
	{"chain/count_modules", "chain",
		"Look at go.work in the repository root and list the modules it includes.",
		[]string{"daemon", "protocol"}, 1},

	// --- multi-file: two reads before an answer is possible ---
	{"multifile/compare_gomod", "multifile",
		"Compare the go directive in daemon/go.mod with the one in protocol/go.mod. Which Go version does each declare?",
		[]string{"1.25", "1.23"}, 2},
	{"multifile/two_readmes", "multifile",
		"Look at both mcp-servers/README.md and clients/vscode/README.md, and say in one sentence what each is about.",
		[]string{"mcp"}, 2},

	// --- explore: the model must navigate structure it cannot guess ---
	{"explore/find_scripts", "explore",
		"What scripts are in the scripts/ directory? Name three of them.",
		[]string{".sh"}, 1},
	{"explore/daemon_layout", "explore",
		"List the Go files in the daemon/mcp directory.",
		[]string{"registry.go", "client.go", "mcp.go"}, 1},
	{"explore/find_prompts", "explore",
		"There is a system prompt file somewhere under daemon. Find it and tell me the format it asks the model to use for edits.",
		[]string{"SEARCH", "REPLACE"}, 2},

	// --- propose: read, then propose a concrete edit ---
	{"propose/add_comment", "propose",
		"Read the file eval_target.txt in the workspace root, then propose an edit that changes the word BEFORE to AFTER.",
		[]string{"propos"}, 2},

	// --- termination: tools are available but the task needs none ---
	{"stop/no_tool_needed", "termination",
		"In one sentence, and without looking at any files, what is a Unix domain socket?",
		[]string{"socket"}, 0},
	{"stop/answer_then_stop", "termination",
		"Read go.work in the repository root, tell me how many modules it lists, and then stop.",
		[]string{"6", "six"}, 1},
	{"stop/refuse_impossible", "termination",
		"Read the file definitely_not_here_xyz.txt and tell me what it says. If it does not exist, say so and stop.",
		[]string{"not", "exist"}, 1},
	{"stop/greeting", "termination",
		"Thanks, that's all I needed.",
		nil, 0},
	{"stop/bounded_search", "termination",
		"Is there a file called LICENSE in the repository root? Answer yes or no.",
		[]string{"no", "yes"}, 1},
}

// ---------------------------------------------------------------------------
// Grading.
// ---------------------------------------------------------------------------

type loopGrade struct {
	terminated       bool
	noRepeat         bool
	usedOutput       bool
	withinFour       bool
	usesOutputApplie bool
	note             string
}

func gradeLoop(sc loopScenario, res agentResult) loopGrade {
	g := loopGrade{}

	g.terminated = res.Incomplete == nil || res.Incomplete.Reason != protocol.IncompleteAgentBudget
	if !g.terminated {
		g.note = "stopped on a budget: " + res.Incomplete.Reason
	}

	seen := map[string]int{}
	g.noRepeat = true
	for _, sig := range res.ToolSignatures {
		seen[sig]++
		if seen[sig] > 1 {
			g.noRepeat = false
			if g.note == "" {
				g.note = fmt.Sprintf("repeated an identical call %dx: %s", seen[sig], truncateForLog(sig))
			}
		}
	}

	g.withinFour = res.Iterations <= 4
	if !g.withinFour && g.note == "" {
		g.note = fmt.Sprintf("took %d iterations", res.Iterations)
	}

	// Only applies where tool output was actually required. A scenario needing
	// no tools cannot demonstrate it used one, and counting it as a pass would
	// inflate the number.
	if len(sc.wantInAnswer) > 0 && sc.minCalls > 0 {
		g.usesOutputApplie = true
		answer := strings.ToLower(res.FinalText)
		for _, want := range sc.wantInAnswer {
			if strings.Contains(answer, strings.ToLower(want)) {
				g.usedOutput = true
				break
			}
		}
		if !g.usedOutput && g.note == "" {
			g.note = fmt.Sprintf("answer lacked any of %v", sc.wantInAnswer)
		}
	}
	return g
}

func truncateForLog(s string) string {
	if len(s) > 90 {
		return s[:89] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// The test.
// ---------------------------------------------------------------------------

type loopTally struct{ applied, passed int }

func (t loopTally) rate() float64 {
	if t.applied == 0 {
		panic("loopTally.rate on an empty tally: check applied > 0 and report n/a")
	}
	return float64(t.passed) / float64(t.applied)
}

func TestAgentLoopReliability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live loop eval in -short mode")
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

	// Grounded against this repository: real structure the model cannot guess.
	root, err := editapply.ResolveRealWorkspaceRoot("..")
	if err != nil {
		t.Fatalf("resolving workspace: %v", err)
	}

	// A file the propose scenario can edit without touching anything real.
	target := root + "/eval_target.txt"
	if err := os.WriteFile(target, []byte("This line says BEFORE and nothing else.\n"), 0600); err != nil {
		t.Fatalf("writing eval target: %v", err)
	}
	defer os.Remove(target)

	trials := loopTrials
	if v := os.Getenv("LOOP_EVAL_TRIALS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("LOOP_EVAL_TRIALS=%q is not a positive integer", v)
		}
		trials = n
	}

	total := len(loopScenarios) * trials
	t.Logf("probing model=%s over %s: %d scenarios x %d trials = %d agent turns",
		model, root, len(loopScenarios), trials, total)

	gates := map[string]*loopTally{}
	for _, g := range loopGates {
		gates[g.name] = &loopTally{}
	}
	byCategory := map[string]*loopTally{}
	var failures []string
	transportErrors := 0
	var iterationCounts, callCounts []int
	start := time.Now()

	for _, sc := range loopScenarios {
		if _, ok := byCategory[sc.category]; !ok {
			byCategory[sc.category] = &loopTally{}
		}
		for trial := 1; trial <= trials; trial++ {
			// search_code is deliberately DENIED: it needs the ONNX embedder,
			// which this eval does not start, so leaving it on the menu would
			// measure the model burning an iteration on an unavailable tool
			// rather than measuring the loop.
			srv := &Server{
				apiBase:   apiBase,
				apiKey:    apiKey,
				workspace: root,
				logger:    log.New(os.Stderr, "loopeval: ", 0),
				cfg: &Config{MCP: MCPConfig{
					Enabled: true,
					Builtin: MCPBuiltinConfig{Tools: map[string]string{
						"read_file":      PolicyAllow,
						"list_directory": PolicyAllow,
						"propose_edit":   PolicyAllow,
						"search_code":    PolicyDeny,
					}},
					Budget: MCPBudgetConfig{MaxIterations: 8},
				}},
			}

			registry, _ := srv.buildRegistry(context.Background(), srv.logger, &proposalSink{})
			messages := buildChatMessages(agentEvalSystemPrompt, nil, sc.prompt)
			// nil approver: every tool in this harness is config-"allow", so
			// nothing should ever reach an ask. If a scenario's policy ever
			// changes, the nil approver denies rather than silently running --
			// the eval would report the failure instead of measuring a loop
			// that quietly got permissions it was never granted.
			res, err := srv.runAgentLoop(context.Background(), time.Now(), registry, model, messages, routing, nil,
				func(string) error { return nil }, nil, nil, nil, nil)
			registry.Close()

			if err != nil {
				transportErrors++
				failures = append(failures, fmt.Sprintf("%s trial %d: TRANSPORT: %v", sc.name, trial, err))
				continue
			}

			g := gradeLoop(sc, res)
			iterationCounts = append(iterationCounts, res.Iterations)
			callCounts = append(callCounts, len(res.ToolSignatures))

			record := func(name string, applies, passed bool) {
				if !applies {
					return
				}
				gates[name].applied++
				if passed {
					gates[name].passed++
				}
			}
			record("terminates", true, g.terminated)
			record("no_repeated_call", true, g.noRepeat)
			record("within_four_iterations", true, g.withinFour)
			record("uses_tool_output", g.usesOutputApplie, g.usedOutput)

			ok := g.terminated && g.noRepeat && g.withinFour && (!g.usesOutputApplie || g.usedOutput)
			byCategory[sc.category].applied++
			if ok {
				byCategory[sc.category].passed++
			} else {
				failures = append(failures, fmt.Sprintf("%s trial %d [%d iters, %d calls]: %s",
					sc.name, trial, res.Iterations, len(res.ToolSignatures), g.note))
			}
		}
	}

	t.Logf("--- %d turns in %s ---", total-transportErrors, time.Since(start).Round(time.Second))

	t.Log("--- per-category end-to-end pass rate ---")
	cats := make([]string, 0, len(byCategory))
	for c := range byCategory {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	var starved []string
	for _, c := range cats {
		tally := byCategory[c]
		if tally.applied == 0 {
			starved = append(starved, c)
			t.Logf("  %-12s    n/a  (0 trials survived -- NOT a pass)", c)
			continue
		}
		t.Logf("  %-12s %5.1f%%  (%d/%d)", c, tally.rate()*100, tally.passed, tally.applied)
	}

	t.Log("--- gates ---")
	failed := 0
	var starvedGates []string
	for _, gate := range loopGates {
		tally := gates[gate.name]
		if tally.applied == 0 {
			starvedGates = append(starvedGates, gate.name)
			t.Logf("  %-24s   n/a  (never exercised -- NOT a pass)", gate.name)
			continue
		}
		status := "PASS"
		if tally.rate() < gate.threshold {
			status = "FAIL"
			failed++
		}
		t.Logf("  %-24s %5.1f%%  (%d/%d)  threshold %.0f%%  %s",
			gate.name, tally.rate()*100, tally.passed, tally.applied, gate.threshold*100, status)
	}

	if len(iterationCounts) > 0 {
		sort.Ints(iterationCounts)
		sort.Ints(callCounts)
		t.Logf("--- recorded, not gated ---")
		t.Logf("  median iterations/turn: %d   median tool calls/turn: %d",
			iterationCounts[len(iterationCounts)/2], callCounts[len(callCounts)/2])
	}

	if len(failures) > 0 {
		t.Log("--- failures ---")
		for _, f := range failures {
			t.Logf("  %s", f)
		}
	}

	// Validity before verdict, exactly as in Phase 0.
	errRate := float64(transportErrors) / float64(total)
	if transportErrors > 0 {
		t.Logf("NOTE: %d/%d turns (%.0f%%) failed at the transport layer", transportErrors, total, errRate*100)
	}
	if len(starved) > 0 {
		t.Fatalf("VOID: %d categories got zero graded trials (%s). These are absences, not passes.",
			len(starved), strings.Join(starved, ", "))
	}
	if len(starvedGates) > 0 {
		t.Fatalf("VOID: %d gate(s) were never exercised (%s)", len(starvedGates), strings.Join(starvedGates, ", "))
	}
	if errRate > loopMaxTransportErrors {
		t.Fatalf("VOID: %.0f%% transport failures (ceiling %.0f%%) -- the surviving trials are a biased sample",
			errRate*100, loopMaxTransportErrors*100)
	}

	if failed > 0 {
		t.Fatalf("%d/%d loop gate(s) failed for %s.\n\n"+
			"PER THE PHASE 4.5 DECISION RULE, DO NOT LOWER THE GATE. If termination or repetition "+
			"failed, iterate once on the system prompt and tool descriptions and re-measure; still "+
			"failing means routing agentic turns to the reasoning tier. If uses_tool_output failed, "+
			"the loop is not earning its tokens -- stop and reconsider the feature. Phases 5-7 "+
			"(consent UX across three clients) do not start until this passes.",
			failed, len(loopGates), model)
	}
	t.Logf("all %d loop gates passed over %d turns: %s can carry the agent loop",
		len(loopGates), total-transportErrors, model)
}

// agentEvalSystemPrompt is minimal on purpose. The production system prompt is
// written for the single-turn edit-block flow and would confuse the measurement
// with instructions about a format this eval does not use; what is measured
// here is the model's loop behaviour, not prompt engineering.
const agentEvalSystemPrompt = `You are a coding assistant with tools for exploring a workspace.
Use them when you need information you do not have, then answer.
Stop calling tools once you can answer. Do not repeat a call you have already made.`
