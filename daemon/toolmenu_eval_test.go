//go:build eval

// Does a bigger tool menu make the model pick worse, and by how much?
//
// max_advertised_tools defaulted to 12 and moved to 5 on 2026-08-01, on
// evidence that stopped at 5: accuracy measured 100% at a menu of 1 and 85.7%
// at a menu of 5, and nothing had ever been measured above that. So the default
// was set to the widest menu anyone had numbers for, which is defensible and is
// not the same as knowing what 8 or 12 costs. This measures the rest of the
// curve (register item 20 / P2-2).
//
// Run it the same way as its neighbour:
//
//	export CODETERMINAL_API_BASE=... CODETERMINAL_MOCHIII_KEY=... CODETERMINAL_USE_PROXY=true
//	go test -tags eval -run TestToolMenuSizeCurve -v ./daemon
//
// METHOD, and the two things it holds fixed.
//
//  1. THE PROMPTS DO NOT CHANGE across menu sizes. Every size answers the same
//     seven questions with the same correct tool. If the prompts varied, a
//     difference between sizes would be a difference between question sets.
//  2. THE CORRECT TOOL IS ALWAYS PRESENT, and always one of the same five. The
//     tools added to reach 8 and 12 are DISTRACTORS — plausible, well-described
//     tools a real MCP server would advertise, none of which is a defensible
//     answer to any prompt here. That is the actual production hazard: a user
//     connects a filesystem server and a git server, and the menu fills with
//     things that are not wrong to have, only wrong to pick.
//
// Selection accuracy is the only metric. Argument quality is measured by
// TestToolCallReliability and does not depend on menu size.
//
// This file declares NO gate. It is a measurement whose result decides a
// default, and inventing a pass mark for it would let the default be
// rationalised after the fact — the opposite of what toolCallGates does, for
// the opposite reason.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// menuTrials is per scenario per menu size. Three sizes x seven prompts x this
// many trials is the whole billed cost; at the measured ~$0.0000017 per short
// call it stays far inside any sane ceiling.
const menuTrials = 5

// distractorTools are the padding. Each is a tool a real server would plausibly
// advertise, described well enough to be tempting, and none is a defensible
// answer to any prompt in menuScenarios.
//
// Deliberately NOT nonsense tools. A menu padded with "frobnicate_widget" would
// measure whether the model can spot gibberish, which is not the question; the
// question is whether a longer list of REASONABLE options degrades the choice.
func distractorTools() []evalTool {
	mk := func(name, desc string, params map[string]any, required []string) evalTool {
		return evalTool{Type: "function", Function: evalToolFunction{
			Name:        name,
			Description: desc,
			Parameters: map[string]any{
				"type":       "object",
				"properties": params,
				"required":   required,
			},
		}}
	}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }

	return []evalTool{
		mk("git_blame", "Show which commit last modified each line of a file.",
			map[string]any{"path": str("Workspace-relative path.")}, []string{"path"}),
		mk("git_diff", "Show the working-tree diff for a file or the whole workspace.",
			map[string]any{"path": str("Workspace-relative path, or empty for everything.")}, nil),
		mk("format_code", "Run the language's standard formatter over a file.",
			map[string]any{"path": str("Workspace-relative path.")}, []string{"path"}),
		mk("build_project", "Compile the project and report any build errors.",
			map[string]any{"target": str("Build target, or empty for the default.")}, nil),
		mk("list_dependencies", "List the project's declared dependencies and their versions.",
			map[string]any{"manifest": str("Path to the manifest, or empty to detect it.")}, nil),
		mk("read_env", "Read a configuration value from the project's settings.",
			map[string]any{"key": str("Setting name.")}, []string{"key"}),
		mk("open_issue", "Open a new issue in the project's tracker.",
			map[string]any{"title": str("Issue title.")}, []string{"title"}),
	}
}

// menuOfSize returns the five real tools plus enough distractors to reach n.
// The real tools come FIRST at every size, so any degradation measured is not
// the correct answer being pushed down the list — which would be a different
// finding (position bias) wearing this one's clothes.
//
// FIVE IS THE FLOOR, and that is a correction to this file's first run. It
// originally accepted n=4 by truncating the real tools, which silently dropped
// git_log — so the "history" prompt had no correct answer available and five of
// its thirty-five trials could not be got right by any model. The 71.4% that
// produced was a measurement of the harness. A menu smaller than the number of
// distinct tools the prompts need is not a smaller menu, it is a different
// experiment.
func menuOfSize(n int) []evalTool {
	tools := allFiveTools()
	if n < len(tools) {
		panic(fmt.Sprintf("menu size %d is below the %d tools the scenarios need; see menuOfSize", n, len(tools)))
	}
	if n == len(tools) {
		return tools
	}
	d := distractorTools()
	need := n - len(tools)
	if need > len(d) {
		need = len(d)
	}
	return append(tools, d[:need]...)
}

// menuScenarios are the seven tool_choice prompts from toolCallScenarios,
// verbatim. Reusing them rather than writing new ones means this measurement is
// directly comparable with the 5-tool number already on record.
var menuScenarios = []struct {
	name     string
	prompt   string
	wantTool string
}{
	{"read", "Open daemon/scrub.go and tell me what it does.", "read_file"},
	{"list", "What's inside the clients directory?", "list_directory"},
	{"search", "Which files mention SO_PEERCRED?", "search_code"},
	{"tests", "Please run the editapply test suite.", "run_tests"},
	{"history", "Show me the last 5 commits that touched daemon/server.go.", "git_log"},
	{"search_not_read", "I don't know which file it's in — locate the definition of LockWorkspaceApply.", "search_code"},
	{"read_not_search", "Print the whole of protocol/protocol.go for me.", "read_file"},
}

type menuResult struct {
	size       int
	correct    int
	attempted  int
	transport  int            // trials that never completed, after retries
	retried    int            // transport failures that a retry recovered
	timeouts   int            // of those, ones that blew evalRequestTimeout
	wrongTool  map[string]int // wanted -> got, counted
	noCallAtAl int
}

// menuTransportRetries is how many times one trial is retried before it is
// written off. It exists because the first run lost 13 of 35 trials at menu=12
// to request timeouts while losing NONE at 4, 5 or 8 — which biased the sample
// badly enough that the guard correctly refused to report it.
//
// The timeouts are not noise to be papered over: they are counted and reported
// separately, because "a twelve-tool menu makes requests slow enough to time
// out at 90s" is a cost of a wide menu in its own right, and one that no
// accuracy number would ever show.
const menuTransportRetries = 2

func TestToolMenuSizeCurve(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live model eval in -short mode")
	}

	apiBase := os.Getenv("CODETERMINAL_API_BASE")
	apiKey := os.Getenv("CODETERMINAL_API_KEY")
	if v := os.Getenv("CODETERMINAL_MOCHIII_KEY"); v != "" && os.Getenv("CODETERMINAL_USE_PROXY") == "true" {
		apiKey = v
	}
	if apiBase == "" {
		t.Skip("CODETERMINAL_API_BASE is unset -- this eval makes real billed calls and will not guess an endpoint")
	}

	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("loading models.json: %v", err)
	}
	tier, ok := cfg.Tiers["primary"]
	if !ok {
		t.Fatal("models.json has no primary tier")
	}
	model := tier.Slug
	prod := cfg.ZDR.resolvedProviderRouting()
	routing := evalProviderRouting{
		ZDR:            prod.ZDR,
		DataCollection: prod.DataCollection,
		AllowFallbacks: prod.AllowFallbacks,
		Ignore:         prod.Ignore,
	}

	trials := menuTrials
	if v := os.Getenv("TOOLMENU_EVAL_TRIALS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("TOOLMENU_EVAL_TRIALS=%q is not a positive integer", v)
		}
		trials = n
	}

	sizes := []int{5, 8, 12}
	total := len(sizes) * len(menuScenarios) * trials
	t.Logf("model=%s via %s: %d menu sizes x %d prompts x %d trials = %d billed calls",
		model, apiBase, len(sizes), len(menuScenarios), trials, total)

	var results []menuResult
	for _, size := range sizes {
		menu := menuOfSize(size)
		if len(menu) != size {
			t.Fatalf("menu of size %d has %d tools; add more distractors", size, len(menu))
		}
		res := menuResult{size: size, wrongTool: map[string]int{}}

		for _, sc := range menuScenarios {
			for trial := 1; trial <= trials; trial++ {
				scenario := toolCallScenario{
					name:      fmt.Sprintf("menu%d/%s", size, sc.name),
					category:  "menu",
					tools:     menu,
					prompt:    sc.prompt,
					wantTool:  sc.wantTool,
					wantCalls: 1,
				}
				var out probeOutcome
				var err error
				for attempt := 0; attempt <= menuTransportRetries; attempt++ {
					out, err = probeToolCalls(context.Background(), apiBase, apiKey, model, routing, scenario)
					if err == nil {
						if attempt > 0 {
							res.retried++
						}
						break
					}
					if strings.Contains(err.Error(), "context deadline exceeded") ||
						strings.Contains(err.Error(), "timeout") {
						res.timeouts++
					}
					t.Logf("menu=%2d %-16s trial %d attempt %d: %v", size, sc.name, trial, attempt+1, err)
				}
				if err != nil {
					res.transport++
					continue
				}
				res.attempted++
				if len(out.calls) == 0 {
					res.noCallAtAl++
					continue
				}
				got := out.calls[0].Name
				if got == sc.wantTool {
					res.correct++
				} else {
					res.wrongTool[sc.wantTool+" -> "+got]++
				}
			}
		}
		results = append(results, res)

		pct := 0.0
		if res.attempted > 0 {
			pct = 100 * float64(res.correct) / float64(res.attempted)
		}
		t.Logf("menu=%2d: %d/%d correct (%.1f%%), %d no-call, %d unrecoverable, %d recovered by retry, %d request timeouts",
			size, res.correct, res.attempted, pct, res.noCallAtAl, res.transport, res.retried, res.timeouts)
	}

	t.Log("")
	t.Log("=== tool-menu size curve ===")
	t.Logf("%-6s %-12s %-8s %-10s %s", "size", "accuracy", "no-call", "timeouts", "n")
	for _, r := range results {
		pct := 0.0
		if r.attempted > 0 {
			pct = 100 * float64(r.correct) / float64(r.attempted)
		}
		t.Logf("%-6d %-12s %-8d %-10d %d", r.size, fmt.Sprintf("%.1f%%", pct), r.noCallAtAl, r.timeouts, r.attempted)
	}

	t.Log("")
	t.Log("=== confusions, by menu size ===")
	for _, r := range results {
		if len(r.wrongTool) == 0 {
			t.Logf("menu=%2d: none", r.size)
			continue
		}
		keys := make([]string, 0, len(r.wrongTool))
		for k := range r.wrongTool {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("menu=%2d: %-40s x%d", r.size, k, r.wrongTool[k])
		}
	}

	// The only hard failure here is a run too damaged to read. Everything else
	// is a number for a human to act on -- see the file header for why this
	// declares no accuracy gate.
	for _, r := range results {
		if r.attempted == 0 {
			t.Fatalf("menu=%d: every trial failed in transport; the run measured nothing", r.size)
		}
		if float64(r.transport)/float64(r.transport+r.attempted) > maxTransportErrorRate {
			t.Fatalf("menu=%d: %d transport errors against %d completed trials, above the %.0f%% ceiling -- the surviving trials are a biased sample, not a measurement",
				r.size, r.transport, r.attempted, maxTransportErrorRate*100)
		}
	}
}
