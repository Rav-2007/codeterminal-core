//go:build eval

// THE ORCHESTRATION GATE: the multi-agent pipeline against a REAL model.
//
// Every other test of this feature drives a scripted SSE upstream, which proves
// the plumbing and proves nothing about the product. A scripted Planner always
// produces a plan; a scripted Researcher always reports findings; a scripted
// Coder always uses what it was handed. The questions that decide whether this
// feature should exist are exactly the ones a script cannot answer:
//
//   - Does a real Planner, given NO TOOLS, produce a plan -- or does it refuse,
//     hallucinate file paths, or try to call a tool it cannot see?
//   - Does a real Coder USE the research it was handed, or re-derive it and pay
//     for the same reads twice? (If it re-derives, the pipeline is pure cost.)
//   - Does role scoping hold when the model, not a script, chooses the tool?
//
// Run:
//
//	export CODETERMINAL_API_BASE=https://openrouter.ai/api/v1 CODETERMINAL_API_KEY=...
//	export CODETERMINAL_HELPER_BIN=$PWD/dist/codeterminal-helper
//	go test -tags eval -run TestOrchestrationLive -v -timeout 30m ./daemon
//
// Direct to the provider, not the managed proxy: a four-phase turn is roughly
// four times the cost of a single-agent turn, and the proxy's quota is for
// pilot users.
package main

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"codeterminal/protocol"
)

// ---------------------------------------------------------------------------
// The bar, written down before the first run.
//
// Same discipline as the loop gate: a failure here reroutes or redesigns the
// feature. It never lowers the number. The pipeline costs ~4x a single turn,
// so "it produced words" is not the bar -- it has to produce words a single
// agent would not have.
// ---------------------------------------------------------------------------

var orchestrationGates = []struct {
	name string
	why  string
}{
	{"every_phase_ran",
		"a turn that was NOT budget-stopped ran every configured specialist (a phase that silently no-ops is cost with no output)"},
	{"partial_work_reached_user",
		"a turn the budget DID stop still streamed the work done so far -- returning it is not showing it (found live: 698 bytes returned, 0 streamed)"},
	{"planner_called_nothing",
		"the Planner has an EMPTY tool list; a real model must not get a tool call past it (scoping is enforced, not advertised)"},
	{"researcher_proposed_nothing",
		"the Researcher may not write: propose_edit is absent from its list, so a real model reaching for it must be refused"},
	{"answer_reached_user",
		"the answering phase's prose actually streamed (a pipeline that streams nothing looks identical to a crash)"},
	{"tester_verdict_reached_user",
		"the phase AFTER the answering one reached the user too -- this is the G5 regression, live"},
}

// orchestrationScenarios are grounded in THIS repository: questions whose
// answers cannot be guessed from the prompt, so an answer that is right is
// evidence the pipeline actually read the code.
var orchestrationScenarios = []struct {
	name   string
	prompt string
	// maxIterations is the per-phase ceiling. A deliberately tight one is how
	// the budget-stop path gets exercised against a real model instead of only
	// against a script -- that path is where the worst bug in this feature was.
	maxIterations int
	// wantInAnswer: any one of these appearing is evidence the answer came
	// from the workspace rather than from the model's priors.
	wantInAnswer []string
}{
	{
		name:          "budget_stops_the_pipeline_early",
		maxIterations: 2,
		prompt: "In this repository, how does the daemon stop two different processes applying " +
			"an edit to the same workspace at the same time? Name the function and the file.",
		wantInAnswer: []string{"LockWorkspaceApply", "applylock", "flock"},
	},
	{
		name:          "explain_a_real_mechanism",
		maxIterations: 20,
		prompt: "In this repository, how does the daemon stop two different processes applying " +
			"an edit to the same workspace at the same time? Name the function and the file.",
		wantInAnswer: []string{"LockWorkspaceApply", "applylock", "flock"},
	},
	{
		name:         "locate_and_describe",
		prompt:       "What does truncateHandoff do in this repository, and what problem does it exist to solve?",
		wantInAnswer: []string{"truncateHandoff", "handoff", "orchestrator"},
	},
}

// ---------------------------------------------------------------------------
// Observation.
//
// runOrchestrated returns ONE combined agentResult, so per-phase facts have to
// be recovered from the activity stream. narratePhase emits a "step i/n --
// Display" marker before each phase, which is exactly the boundary needed: every
// tool activity between two markers belongs to the phase the earlier one named.
// ---------------------------------------------------------------------------

type phaseObservation struct {
	display string
	// calls is every tool the model ASKED for in this phase, including the ones
	// that were refused -- which is the whole point. A scoping test that counted
	// only executed calls could not tell "the model never tried" from "the model
	// tried and was stopped", and only the second proves enforcement.
	calls []string
	// denied is the subset that was refused.
	denied []string
}

type liveObserver struct {
	mu     sync.Mutex
	phases []*phaseObservation
	// streamed is only what reached onToken -- i.e. what the user would have
	// seen. Captured separately from agentResult.FinalText so the two can be
	// compared: they must agree, and a divergence is the G5 bug class.
	streamed strings.Builder
	// callPhase maps a call ID to the phase it started in, so a "denied" or
	// "succeeded" arriving later is attributed to the right specialist even if
	// a phase boundary has since gone by.
	callPhase map[string]int
}

func newLiveObserver() *liveObserver {
	return &liveObserver{callPhase: map[string]int{}}
}

func (o *liveObserver) onActivity(a protocol.ToolActivity) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// The narration marker: its own phase, no CallID, Tool is the display name.
	if a.Phase == protocol.ToolPhaseStep {
		o.phases = append(o.phases, &phaseObservation{display: a.Tool})
		return
	}
	if len(o.phases) == 0 {
		return // activity before any narration: not possible today, ignored rather than panicking
	}
	idx, known := o.callPhase[a.CallID]
	if !known {
		idx = len(o.phases) - 1
		o.callPhase[a.CallID] = idx
		o.phases[idx].calls = append(o.phases[idx].calls, a.Tool)
	}
	if a.Phase == protocol.ToolPhaseDenied {
		o.phases[idx].denied = append(o.phases[idx].denied, a.Tool)
	}
}

func (o *liveObserver) onToken(tok string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.streamed.WriteString(tok)
	return nil
}

func (o *liveObserver) phaseNamed(display string) *phaseObservation {
	for _, p := range o.phases {
		if p.display == display {
			return p
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The test.
// ---------------------------------------------------------------------------

func TestOrchestrationLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live orchestration eval in -short mode")
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

	workspace, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving workspace: %v", err)
	}
	workspace = strings.TrimSuffix(workspace, "/daemon")

	logger := log.New(os.Stderr, "orcheval: ", 0)

	// REAL retrieval, not a stub. The Researcher's primary tool is search_code,
	// and denying it (as the loop eval does, for its own good reasons) would
	// measure a Researcher with its main instrument removed -- which is a
	// measurement of something this pipeline never ships as.
	rs := setupRetrieval(cfg, workspace, false, logger, newActiveEmbedder)
	defer rs.Stop()
	if rs.DisabledReason != "" {
		t.Fatalf("retrieval unavailable (%s) -- run `index .` first; a Researcher without search_code is not the thing under test", rs.DisabledReason)
	}

	newServer := func(pipeline []string, maxIterations int) *Server {
		return &Server{
			apiBase:            apiBase,
			apiKey:             apiKey,
			workspace:          workspace,
			logger:             logger,
			embedder:           rs.Embedder,
			store:              rs.Store,
			lexicalStore:       rs.LexicalStore,
			retrievalTopK:      rs.TopK,
			contextBudgetChars: rs.ContextBudgetChars,
			cfg: &Config{MCP: MCPConfig{
				Enabled:  true,
				Pipeline: pipeline,
				Builtin: MCPBuiltinConfig{Tools: map[string]string{
					"read_file":      PolicyAllow,
					"list_directory": PolicyAllow,
					"search_code":    PolicyAllow,
					// Deliberately NOT allowed. The Researcher must be refused
					// if it reaches for propose_edit, and a config-"allow" would
					// make a pass here prove nothing about ROLE scoping.
					"propose_edit": PolicyAsk,
					"sandbox_exec": PolicyAsk,
				}},
				Budget: MCPBudgetConfig{MaxIterations: maxIterations, MaxTurnIterations: maxIterations * 4},
			}},
		}
	}

	pipeline := []string{roleNamePlanner, roleNameResearcher, roleNameCoder, roleNameTester}
	phases, unknown := resolvePipeline(pipeline)
	if len(unknown) > 0 {
		t.Fatalf("pipeline names unknown roles %v -- the eval's own config is wrong", unknown)
	}

	results := map[string]map[string]bool{}
	for _, g := range orchestrationGates {
		results[g.name] = map[string]bool{}
	}

	for _, sc := range orchestrationScenarios {
		t.Run(sc.name, func(t *testing.T) {
			srv := newServer(pipeline, sc.maxIterations)
			registry, _ := srv.buildRegistry(context.Background(), logger, &proposalSink{}, "")
			defer registry.Close()

			obs := newLiveObserver()
			messages := buildChatMessages(defaultSystemPrompt, nil, sc.prompt)

			start := time.Now()
			// nil approver: "ask" means refused here, which is what makes the
			// scoping gates meaningful -- nothing can slip through on a human's
			// reflexive yes because there is no human.
			res, err := srv.runOrchestrated(context.Background(), time.Now(), registry, model, "auto",
				messages, routing, nil, obs.onToken, obs.onActivity, nil, nil, nil, phases)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("orchestrated turn failed: %v", err)
			}

			streamed := obs.streamed.String()
			t.Logf("--- %s: %d phases, %d iterations, %d tool calls, %s ---",
				sc.name, len(obs.phases), res.Iterations, len(res.ToolSignatures), elapsed.Round(time.Millisecond))
			for i, p := range obs.phases {
				t.Logf("  phase %d %-11s calls=%v denied=%v", i+1, p.display, p.calls, p.denied)
			}
			t.Logf("  answer STREAMED to the user (%d bytes):\n%s", len(streamed), indentBlock(streamed))
			t.Logf("  FinalText RETURNED to the caller (%d bytes)", len(res.FinalText))
			if res.Incomplete != nil {
				t.Logf("  INCOMPLETE: reason=%q detail=%q", res.Incomplete.Reason, res.Incomplete.Detail)
			}

			record := func(gate string, passed bool) { results[gate][sc.name] = passed }

			// A budget stop is a LEGITIMATE outcome, not a failure -- so the
			// two gates are mutually exclusive rather than one being fudged.
			// What is never legitimate is stopping and telling the user nothing.
			if res.Incomplete == nil {
				record("every_phase_ran", len(obs.phases) == len(phases))
			} else {
				record("partial_work_reached_user", strings.TrimSpace(streamed) != "")
			}

			if p := obs.phaseNamed("Planner"); p != nil {
				// Zero EXECUTED calls. A refusal recorded here is still a pass
				// for scoping -- it means the model tried and the allowlist
				// stopped it -- but it is worth seeing in the log, so it is
				// counted separately rather than folded into the gate.
				executed := len(p.calls) - len(p.denied)
				record("planner_called_nothing", executed == 0)
				if len(p.denied) > 0 {
					t.Logf("  NOTE: the Planner ATTEMPTED %d call(s) and was refused: %v", len(p.denied), p.denied)
				}
			} else {
				record("planner_called_nothing", false)
			}

			if p := obs.phaseNamed("Researcher"); p != nil {
				wrote := false
				for _, c := range p.calls {
					if strings.Contains(c, "propose_") && !containsAt(p.denied, c) {
						wrote = true
					}
				}
				record("researcher_proposed_nothing", !wrote)
			} else {
				record("researcher_proposed_nothing", false)
			}

			record("answer_reached_user", strings.TrimSpace(streamed) != "")

			// G5, live: the Tester runs AFTER the answering Coder, so its header
			// must appear in what the user saw. Its absence is the exact bug the
			// scripted test pins -- this is the same assertion against a model
			// that chooses its own words.
			if res.Incomplete == nil {
				record("tester_verdict_reached_user", strings.Contains(streamed, "**Tester step:**"))
			}

			// Grounding: not a gate (a model may phrase a right answer without
			// the literal token), but reported, because a pipeline that costs 4x
			// and answers no better is the finding that matters most.
			lower := strings.ToLower(streamed)
			grounded := false
			for _, want := range sc.wantInAnswer {
				if strings.Contains(lower, strings.ToLower(want)) {
					grounded = true
					break
				}
			}
			t.Logf("  grounded=%t (looked for any of %v)", grounded, sc.wantInAnswer)

			// The streamed text and the returned FinalText must agree. They are
			// produced by different lines in runOrchestrated, and G5 was exactly
			// a case where one carried something the other did not.
			if strings.TrimSpace(streamed) != strings.TrimSpace(res.FinalText) {
				t.Errorf("streamed text and FinalText disagree (%d vs %d bytes): the user saw something different from what was persisted",
					len(strings.TrimSpace(streamed)), len(strings.TrimSpace(res.FinalText)))
			}
		})
	}

	t.Log("=== ORCHESTRATION GATES ===")
	failed := 0
	for _, g := range orchestrationGates {
		var bad []string
		for name, ok := range results[g.name] {
			if !ok {
				bad = append(bad, name)
			}
		}
		status := "PASS"
		if len(results[g.name]) == 0 {
			status = "n/a "
		}
		if len(bad) > 0 {
			status, failed = "FAIL", failed+1
		}
		t.Logf("%-4s %-28s %s", status, g.name, g.why)
		if len(bad) > 0 {
			t.Errorf("gate %s failed on: %v", g.name, bad)
		}
	}
}

func containsAt(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func indentBlock(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "    | " + l
	}
	return strings.Join(lines, "\n")
}
