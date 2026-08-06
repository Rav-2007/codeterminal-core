//go:build eval

// Phase 0 of the MCP / agentic-loop plan: does the model in the `primary`
// tier emit tool calls reliably enough to carry agent mode?
//
// See eval_test.go's header for why this is gated behind the "eval" build
// tag. This one is gated for a DIFFERENT reason than its neighbours: the
// retrieval evals are offline-but-heavy (ONNX, CGO), whereas this one makes
// REAL, BILLED calls to the live model API. It is the first eval in the repo
// that does. Run it explicitly with credentials in the environment:
//
//	export CODETERMINAL_API_BASE=... CODETERMINAL_API_KEY=...
//	go test -tags eval -run TestToolCallReliability -v ./daemon
//
// It skips (does not fail) when credentials are absent, so `make check` and
// the scheduled CI eval job stay green without secrets.
//
// # WHY THIS EXISTS, AND WHY IT COMES FIRST
//
// Every later phase of the plan — the MCP client, the agentic loop, the
// consent protocol, three clients' worth of approval UX — is worthless if
// deepseek-v4-flash cannot produce well-formed tool calls. Flash is `primary`
// in models.json precisely because it is cheap; the reasoning tier costs ~3x
// output. Discovering the reliability problem after building the loop means
// rebuilding the economics with the loop already written. So this measures
// first, against a bar declared BEFORE the numbers are known (see the
// toolCallGates table), so the result cannot be rationalized after the fact.
//
// DELIBERATELY SELF-CONTAINED. This file does not call streamCompletion and
// does not need chatCompletionRequest to have grown a Tools field. Phase 0
// must be runnable before Phase 3's provider refactor exists, otherwise the
// gate cannot gate anything. The tool_call delta accumulator below (see
// phase0Accumulator) is the prototype of the one Phase 3 promotes into
// provider.go — writing it here first means the accumulator is measured
// against real provider output before it becomes production code.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The bar, declared before the measurement.
// ---------------------------------------------------------------------------

// toolCallGate is one pass/fail criterion. The thresholds are the ones written
// into the plan before this file was run for the first time. Changing a number
// here after seeing a result is exactly the move this table exists to prevent:
// if a gate fails, the documented consequence is routing agent-mode turns to
// the reasoning tier, NOT lowering the gate.
type toolCallGate struct {
	name      string
	threshold float64
	// describe explains what the metric means in the failure message, so a red
	// run reports what is actually broken rather than a bare number.
	describe string
}

var toolCallGates = []toolCallGate{
	{"well_formed_arguments", 0.98,
		"tool-call arguments parsed as JSON (a malformed argument blob is unusable: the loop cannot dispatch it)"},
	{"schema_valid", 0.95,
		"arguments carried every required property with the declared type (a missing required arg means the MCP server rejects the call)"},
	{"correct_tool", 0.90,
		"the expected tool was selected (picking the wrong tool sends the loop down a wrong branch that costs a full extra iteration)"},
	{"clean_termination", 0.95,
		"no spurious extra calls, and no call at all when none was warranted (over-calling is the failure mode that burns the token budget)"},
}

// toolCallTrials is how many times each scenario runs. Reliability is a
// property of the distribution, not of one sample: a 98% bar cannot be
// measured with one draw per scenario. Override with TOOLCALL_EVAL_TRIALS.
const toolCallTrials = 3

// evalRequestTimeout bounds a single probe call. Deliberately shorter than
// provider.go's 5-minute requestTimeout — these are small, tool-only prompts,
// and an eval that hangs for five minutes per scenario is an eval nobody runs.
const evalRequestTimeout = 90 * time.Second

// ---------------------------------------------------------------------------
// Wire types for the probe (OpenAI-compatible "tools" shape).
// ---------------------------------------------------------------------------

type evalToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type evalTool struct {
	Type     string           `json:"type"` // always "function"
	Function evalToolFunction `json:"function"`
}

type evalMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// evalProviderRouting mirrors daemon/provider.go's providerRouting. Duplicated
// rather than imported so this probe stays independent of the production
// request struct while Phase 3 reshapes it — but the VALUES come from the real
// config below, because a probe that reaches a non-ZDR provider is measuring
// something we would never ship.
type evalProviderRouting struct {
	ZDR            bool     `json:"zdr"`
	DataCollection string   `json:"data_collection"`
	AllowFallbacks bool     `json:"allow_fallbacks"`
	Ignore         []string `json:"ignore,omitempty"`
}

type evalRequest struct {
	Model    string              `json:"model"`
	Messages []evalMessage       `json:"messages"`
	Tools    []evalTool          `json:"tools,omitempty"`
	Stream   bool                `json:"stream"`
	Provider evalProviderRouting `json:"provider"`
}

// evalChunk is the streaming response shape, including the tool_calls delta
// the production chatCompletionChunk does not yet model.
type evalChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

// ---------------------------------------------------------------------------
// The delta accumulator — the PROTOTYPE, kept.
//
// Phase 3 promoted this logic into provider.go's toolCallAccumulator. This copy
// stays, renamed, because the file's whole premise is that Phase 0 runs BEFORE
// Phase 3 exists: a gate that depends on the thing it gates cannot gate it. If
// the production accumulator is ever rewritten, this one is the independent
// second opinion that says whether the rewrite still matches real provider
// output.
// ---------------------------------------------------------------------------

// phase0Call is one tool call rebuilt from its streamed fragments.
type phase0Call struct {
	ID   string
	Name string
	Args strings.Builder
	// sawName records whether any chunk ever supplied a function name.
	// Providers send the name once, on the first fragment; a call that
	// never got one is structurally incomplete and must not be executed.
	sawName bool
}

// phase0Accumulator rebuilds whole tool calls from streamed deltas, keyed by
// the provider's `index` field. This is the piece that has to be right: the
// arguments arrive as arbitrary string fragments that are only valid JSON once
// concatenated, so any per-chunk parsing attempt fails on every chunk but the
// last.
type phase0Accumulator struct {
	calls map[int]*phase0Call
	order []int
}

func newPhase0Accumulator() *phase0Accumulator {
	return &phase0Accumulator{calls: make(map[int]*phase0Call)}
}

func (a *phase0Accumulator) ingest(c evalChunk) {
	for _, ch := range c.Choices {
		for _, tc := range ch.Delta.ToolCalls {
			call, ok := a.calls[tc.Index]
			if !ok {
				call = &phase0Call{}
				a.calls[tc.Index] = call
				a.order = append(a.order, tc.Index)
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Function.Name != "" {
				call.Name = tc.Function.Name
				call.sawName = true
			}
			call.Args.WriteString(tc.Function.Arguments)
		}
	}
}

// finished returns the accumulated calls in provider index order.
func (a *phase0Accumulator) finished() []*phase0Call {
	sort.Ints(a.order)
	out := make([]*phase0Call, 0, len(a.order))
	for _, i := range a.order {
		out = append(out, a.calls[i])
	}
	return out
}

// ---------------------------------------------------------------------------
// Scenarios.
// ---------------------------------------------------------------------------

// argCheck describes one required argument and the JSON type it must carry.
// Values are checked by TYPE and PRESENCE, never by exact equality against an
// expected string: asserting the model produces one specific city name would
// measure paraphrase, not tool-calling.
type argCheck struct {
	path string // dotted path, e.g. "filter.language"
	kind string // "string" | "number" | "bool" | "object" | "array"
}

type toolCallScenario struct {
	name     string
	category string // grouping for the per-category report
	tools    []evalTool
	prompt   string
	// wantTool is the tool the model should select, or "" when the correct
	// behaviour is to call nothing at all.
	wantTool string
	// wantArgs are checked only when wantTool is non-empty.
	wantArgs []argCheck
	// wantCalls is how many calls a correct answer makes. Almost always 1;
	// 0 for the refusal cases.
	wantCalls int
}

// --- tool definitions reused across scenarios ---

func toolReadFile() evalTool {
	return evalTool{Type: "function", Function: evalToolFunction{
		Name:        "read_file",
		Description: "Read the full contents of a file from the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Workspace-relative path to the file."},
			},
			"required": []string{"path"},
		},
	}}
}

func toolListDir() evalTool {
	return evalTool{Type: "function", Function: evalToolFunction{
		Name: "list_directory",
		// Explicitly not a test runner: the menu-size curve's only systematic
		// miss was run_tests → list_directory on "run the … test suite", because
		// "suite" alone is too weak against a directory-listing tool.
		Description: "List file and subdirectory names in a workspace directory. Does not execute tests, builds, or other commands.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": "Workspace-relative directory path."},
				"recursive": map[string]any{"type": "boolean", "description": "Whether to recurse into subdirectories."},
			},
			"required": []string{"path"},
		},
	}}
}

func toolSearchCode() evalTool {
	return evalTool{Type: "function", Function: evalToolFunction{
		Name:        "search_code",
		Description: "Search the workspace for a regular expression and return matching lines.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Regular expression to search for."},
				"filter": map[string]any{
					"type":        "object",
					"description": "Optional narrowing of the search.",
					"properties": map[string]any{
						"language":    map[string]any{"type": "string"},
						"max_results": map[string]any{"type": "integer"},
					},
				},
			},
			"required": []string{"pattern"},
		},
	}}
}

func toolRunTests() evalTool {
	return evalTool{Type: "function", Function: evalToolFunction{
		Name: "run_tests",
		// Named for the action (execute go test), not the noun "suite", which
		// the model otherwise maps to list_directory. Kept as an eval fixture —
		// production Lane A does not ship this tool (see mcpbuiltin.go).
		Description: "Execute `go test` (or the package's test runner) for a Go package and return the command output. Use this when the user asks to run tests or a test suite — not for listing files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"package": map[string]any{"type": "string", "description": "Package path, e.g. ./daemon or ./editapply"},
				"verbose": map[string]any{"type": "boolean"},
			},
			"required": []string{"package"},
		},
	}}
}

func toolGitLog() evalTool {
	return evalTool{Type: "function", Function: evalToolFunction{
		Name:        "git_log",
		Description: "Return recent commits touching a path.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer", "description": "Maximum commits to return."},
			},
			"required": []string{"path", "limit"},
		},
	}}
}

func allFiveTools() []evalTool {
	return []evalTool{toolReadFile(), toolListDir(), toolSearchCode(), toolRunTests(), toolGitLog()}
}

// toolCallScenarios is the fixed scenario set. Fixed, not generated: the same
// prompts must run on every future re-measurement so results are comparable
// across model changes. Categories mirror the plan's Phase 0 table.
var toolCallScenarios = []toolCallScenario{
	// --- single tool available: can it call at all, and shape the argument ---
	{"single/read_named_file", "single_tool", []evalTool{toolReadFile()},
		"Show me what's in daemon/router.go.", "read_file",
		[]argCheck{{"path", "string"}}, 1},
	{"single/read_config", "single_tool", []evalTool{toolReadFile()},
		"I need to see the contents of models.json.", "read_file",
		[]argCheck{{"path", "string"}}, 1},
	{"single/list_dir", "single_tool", []evalTool{toolListDir()},
		"What files are in the editapply directory?", "list_directory",
		[]argCheck{{"path", "string"}}, 1},
	{"single/search", "single_tool", []evalTool{toolSearchCode()},
		"Find everywhere we call EvalSymlinks.", "search_code",
		[]argCheck{{"pattern", "string"}}, 1},
	{"single/run_tests", "single_tool", []evalTool{toolRunTests()},
		"Run the tests for the protocol package.", "run_tests",
		[]argCheck{{"package", "string"}}, 1},

	// --- five tools available: can it pick the right one ---
	{"choice/read", "tool_choice", allFiveTools(),
		"Open daemon/scrub.go and tell me what it does.", "read_file",
		[]argCheck{{"path", "string"}}, 1},
	{"choice/list", "tool_choice", allFiveTools(),
		"What's inside the clients directory?", "list_directory",
		[]argCheck{{"path", "string"}}, 1},
	{"choice/search", "tool_choice", allFiveTools(),
		"Which files mention SO_PEERCRED?", "search_code",
		[]argCheck{{"pattern", "string"}}, 1},
	{"choice/tests", "tool_choice", allFiveTools(),
		"Please run the editapply test suite.", "run_tests",
		[]argCheck{{"package", "string"}}, 1},
	{"choice/history", "tool_choice", allFiveTools(),
		"Show me the last 5 commits that touched daemon/server.go.", "git_log",
		[]argCheck{{"path", "string"}, {"limit", "number"}}, 1},
	{"choice/search_not_read", "tool_choice", allFiveTools(),
		"I don't know which file it's in — locate the definition of LockWorkspaceApply.", "search_code",
		[]argCheck{{"pattern", "string"}}, 1},
	{"choice/read_not_search", "tool_choice", allFiveTools(),
		"Print the whole of protocol/protocol.go for me.", "read_file",
		[]argCheck{{"path", "string"}}, 1},

	// --- required-argument fidelity: both required args, right types ---
	{"required/git_log_both_args", "required_args", allFiveTools(),
		"Give me the 10 most recent commits on README.md.", "git_log",
		[]argCheck{{"path", "string"}, {"limit", "number"}}, 1},
	{"required/git_log_implicit_limit", "required_args", []evalTool{toolGitLog()},
		"What's the commit history for editapply/apply.go? Just the last three.", "git_log",
		[]argCheck{{"path", "string"}, {"limit", "number"}}, 1},
	{"required/run_tests_pkg", "required_args", []evalTool{toolRunTests()},
		"Run go test on ./helper with verbose output.", "run_tests",
		[]argCheck{{"package", "string"}, {"verbose", "bool"}}, 1},
	{"required/list_recursive", "required_args", []evalTool{toolListDir()},
		"List everything under daemon, including subdirectories.", "list_directory",
		[]argCheck{{"path", "string"}, {"recursive", "bool"}}, 1},

	// --- nested object arguments ---
	{"nested/search_with_filter", "nested_args", []evalTool{toolSearchCode()},
		"Search the Go files for the pattern 'func Test' and cap it at 20 results.", "search_code",
		[]argCheck{{"pattern", "string"}, {"filter", "object"}}, 1},
	{"nested/search_language_filter", "nested_args", []evalTool{toolSearchCode()},
		"Look for 'json.Unmarshal' but only in Go source.", "search_code",
		[]argCheck{{"pattern", "string"}, {"filter", "object"}}, 1},
	{"nested/search_max_results", "nested_args", allFiveTools(),
		"Find at most 5 occurrences of 'context.Background'.", "search_code",
		[]argCheck{{"pattern", "string"}, {"filter", "object"}}, 1},

	// --- correct refusal: no tool fits, the model must answer in prose ---
	{"refusal/general_question", "refusal", allFiveTools(),
		"In one sentence, what is the difference between a mutex and a semaphore?", "",
		nil, 0},
	{"refusal/opinion", "refusal", allFiveTools(),
		"Do you think Go's error handling is better than exceptions? One sentence.", "",
		nil, 0},
	{"refusal/greeting", "refusal", allFiveTools(),
		"Thanks, that's all for now.", "",
		nil, 0},
	{"refusal/no_matching_tool", "refusal", []evalTool{toolRunTests(), toolGitLog()},
		"What's the capital of France? Answer in one word.", "",
		nil, 0},
	{"refusal/explain_not_act", "refusal", allFiveTools(),
		"Without looking anything up, explain in one sentence what a Unix domain socket is.", "",
		nil, 0},

	// --- multi-step: first call of a dependent chain ---
	{"multistep/find_then_read", "multistep", allFiveTools(),
		"Find which file defines ProtectedDirNames, then read it. Start with the first step only.", "search_code",
		[]argCheck{{"pattern", "string"}}, 1},
	{"multistep/list_then_read", "multistep", allFiveTools(),
		"Look at what's in the proxy directory so you can then open the main file. Do the first step.", "list_directory",
		[]argCheck{{"path", "string"}}, 1},
	{"multistep/test_then_log", "multistep", allFiveTools(),
		"Run the daemon tests, and if they fail check recent commits. Begin with the first action.", "run_tests",
		[]argCheck{{"package", "string"}}, 1},
	{"multistep/read_then_search", "multistep", allFiveTools(),
		"Read daemon/config.go and then find its callers. Do the first part now.", "read_file",
		[]argCheck{{"path", "string"}}, 1},
}

// ---------------------------------------------------------------------------
// Probe.
// ---------------------------------------------------------------------------

type probeOutcome struct {
	calls        []*phase0Call
	textContent  string
	finishReason string
}

func probeToolCalls(ctx context.Context, apiBase, apiKey, model string, routing evalProviderRouting, sc toolCallScenario) (probeOutcome, error) {
	ctx, cancel := context.WithTimeout(ctx, evalRequestTimeout)
	defer cancel()

	// stream:true is not a preference. The managed proxy REFUSES non-streaming
	// requests outright (403 stream_required, proxy/main.go's streamRequested),
	// so a buffered probe would measure the proxy's gate, not the model.
	body, err := json.Marshal(evalRequest{
		Model:    model,
		Messages: []evalMessage{{Role: "user", Content: sc.prompt}},
		Tools:    sc.tools,
		Stream:   true,
		Provider: routing,
	})
	if err != nil {
		return probeOutcome{}, fmt.Errorf("encoding request: %w", err)
	}

	url := strings.TrimRight(apiBase, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return probeOutcome{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeOutcome{}, fmt.Errorf("calling model API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		return probeOutcome{}, fmt.Errorf("model API returned %s: %s", resp.Status, strings.TrimSpace(string(buf[:n])))
	}

	acc := newPhase0Accumulator()
	var text strings.Builder
	finish := ""

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk evalChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return probeOutcome{}, fmt.Errorf("in-band stream error: %s", string(chunk.Error))
		}
		acc.ingest(chunk)
		for _, ch := range chunk.Choices {
			text.WriteString(ch.Delta.Content)
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return probeOutcome{}, fmt.Errorf("reading stream: %w", err)
	}

	return probeOutcome{calls: acc.finished(), textContent: text.String(), finishReason: finish}, nil
}

// ---------------------------------------------------------------------------
// Grading.
// ---------------------------------------------------------------------------

// trialGrade records which gates one trial satisfied. A gate that does not
// apply to a scenario (schema validity when no call was expected) is recorded
// as not-applicable rather than as a pass, so refusal scenarios cannot inflate
// the argument-quality numbers.
type trialGrade struct {
	wellFormedApplies, wellFormed bool
	schemaApplies, schema         bool
	correctToolApplies, correct   bool
	terminationApplies, clean     bool
	note                          string
}

func gradeTrial(sc toolCallScenario, out probeOutcome) trialGrade {
	g := trialGrade{terminationApplies: true}

	if sc.wantTool == "" {
		// Refusal scenario: the only thing that matters is that no tool ran.
		g.clean = len(out.calls) == 0
		if !g.clean {
			names := make([]string, 0, len(out.calls))
			for _, c := range out.calls {
				names = append(names, c.Name)
			}
			g.note = "called " + strings.Join(names, ",") + " when no tool was warranted"
		}
		return g
	}

	g.correctToolApplies = true
	g.wellFormedApplies = true
	g.schemaApplies = true

	if len(out.calls) == 0 {
		g.note = "expected a call to " + sc.wantTool + ", model answered in prose instead"
		return g
	}

	// Exactly the expected number of calls, no padding.
	g.clean = len(out.calls) == sc.wantCalls
	if !g.clean {
		g.note = fmt.Sprintf("expected %d call(s), got %d", sc.wantCalls, len(out.calls))
	}

	first := out.calls[0]
	g.correct = first.sawName && first.Name == sc.wantTool
	if !g.correct && g.note == "" {
		g.note = "expected " + sc.wantTool + ", got " + first.Name
	}

	var args map[string]any
	raw := strings.TrimSpace(first.Args.String())
	if raw == "" {
		// A call with no arguments at all is well-formed only if the tool
		// genuinely needs none — none of these scenarios do.
		g.note = "tool call carried no arguments"
		return g
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		g.note = "arguments were not valid JSON: " + err.Error()
		return g
	}
	g.wellFormed = true

	missing := make([]string, 0, len(sc.wantArgs))
	for _, want := range sc.wantArgs {
		if !hasArgOfKind(args, want.path, want.kind) {
			missing = append(missing, want.path+":"+want.kind)
		}
	}
	g.schema = len(missing) == 0
	if !g.schema && g.note == "" {
		g.note = "missing/mistyped arguments: " + strings.Join(missing, ", ")
	}
	return g
}

// hasArgOfKind resolves a dotted path in the decoded arguments and checks the
// JSON type. Numbers are checked as float64 because encoding/json decodes every
// JSON number that way — asserting on int would fail on a correct answer.
func hasArgOfKind(args map[string]any, path, kind string) bool {
	cur := any(args)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[seg]
		if !ok {
			return false
		}
	}
	switch kind {
	case "string":
		s, ok := cur.(string)
		return ok && strings.TrimSpace(s) != ""
	case "number":
		_, ok := cur.(float64)
		return ok
	case "bool":
		_, ok := cur.(bool)
		return ok
	case "object":
		m, ok := cur.(map[string]any)
		return ok && len(m) > 0
	case "array":
		a, ok := cur.([]any)
		return ok && len(a) > 0
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// The test.
// ---------------------------------------------------------------------------

type gateTally struct{ applied, passed int }

// rate is only meaningful when applied > 0. Callers MUST check applied first
// and report "n/a" rather than calling this -- the first version of this file
// returned 1 for an empty tally and printed "100.0% (0/0)" for two categories
// that a mid-run quota exhaustion had wiped out, then concluded "all gates
// passed". A measurement that lost a third of its trials reported itself as a
// clean sweep. Zero data is not a passing grade, and this comment is here so
// nobody restores that default.
func (t gateTally) rate() float64 {
	if t.applied == 0 {
		panic("gateTally.rate called on an empty tally: check applied > 0 and report n/a instead")
	}
	return float64(t.passed) / float64(t.applied)
}

// maxTransportErrorRate is how much of the run may fail at the transport layer
// before the whole measurement is void. A handful of retryable blips is normal;
// losing a third of the run to quota exhaustion (as the first run of this file
// did) means the surviving trials are a biased sample -- they are whatever ran
// BEFORE the budget went, which is the front of a fixed scenario list, not a
// random draw from it.
const maxTransportErrorRate = 0.10

func TestToolCallReliability(t *testing.T) {
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

	// The slug and the ZDR routing both come from the real config, so this
	// measures the model we actually ship through the routing we actually
	// ship. A probe against a non-ZDR provider would be measuring a
	// configuration we would never let a user run.
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

	trials := toolCallTrials
	if v := os.Getenv("TOOLCALL_EVAL_TRIALS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("TOOLCALL_EVAL_TRIALS=%q is not a positive integer", v)
		}
		trials = n
	}

	total := len(toolCallScenarios) * trials
	t.Logf("probing model=%s via %s: %d scenarios x %d trials = %d billed calls (zdr=%t ignore=%v)",
		model, apiBase, len(toolCallScenarios), trials, total, routing.ZDR, routing.Ignore)

	gates := map[string]*gateTally{}
	for _, g := range toolCallGates {
		gates[g.name] = &gateTally{}
	}
	byCategory := map[string]*gateTally{}
	var failures []string
	transportErrors := 0

	for _, sc := range toolCallScenarios {
		if _, ok := byCategory[sc.category]; !ok {
			byCategory[sc.category] = &gateTally{}
		}
		for trial := 1; trial <= trials; trial++ {
			out, err := probeToolCalls(context.Background(), apiBase, apiKey, model, routing, sc)
			if err != nil {
				// A transport failure is not a model failure. Counted and
				// reported separately so an outage cannot masquerade as a
				// reliability verdict either way.
				transportErrors++
				failures = append(failures, fmt.Sprintf("%s trial %d: TRANSPORT: %v", sc.name, trial, err))
				continue
			}

			g := gradeTrial(sc, out)
			record := func(name string, applies, passed bool) {
				if !applies {
					return
				}
				gates[name].applied++
				if passed {
					gates[name].passed++
				}
			}
			record("well_formed_arguments", g.wellFormedApplies, g.wellFormed)
			record("schema_valid", g.schemaApplies, g.schema)
			record("correct_tool", g.correctToolApplies, g.correct)
			record("clean_termination", g.terminationApplies, g.clean)

			// A scenario trial counts as a category pass only if every gate
			// that applied to it passed -- the end-to-end view the loop
			// actually experiences.
			ok := (!g.wellFormedApplies || g.wellFormed) &&
				(!g.schemaApplies || g.schema) &&
				(!g.correctToolApplies || g.correct) &&
				(!g.terminationApplies || g.clean)
			byCategory[sc.category].applied++
			if ok {
				byCategory[sc.category].passed++
			} else {
				failures = append(failures, fmt.Sprintf("%s trial %d: %s", sc.name, trial, g.note))
			}
		}
	}

	t.Log("--- per-category end-to-end pass rate ---")
	cats := make([]string, 0, len(byCategory))
	for c := range byCategory {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	var starvedCategories []string
	for _, c := range cats {
		tally := byCategory[c]
		if tally.applied == 0 {
			starvedCategories = append(starvedCategories, c)
			t.Logf("  %-14s    n/a  (0 trials survived -- NOT a pass)", c)
			continue
		}
		t.Logf("  %-14s %5.1f%%  (%d/%d)", c, tally.rate()*100, tally.passed, tally.applied)
	}

	t.Log("--- gates ---")
	failedGates := 0
	var starvedGates []string
	for _, gate := range toolCallGates {
		tally := gates[gate.name]
		if tally.applied == 0 {
			starvedGates = append(starvedGates, gate.name)
			t.Logf("  %-24s   n/a  (no trial exercised this gate -- NOT a pass)", gate.name)
			continue
		}
		rate := tally.rate()
		status := "PASS"
		if rate < gate.threshold {
			status = "FAIL"
			failedGates++
		}
		t.Logf("  %-24s %5.1f%%  (%d/%d)  threshold %.0f%%  %s",
			gate.name, rate*100, tally.passed, tally.applied, gate.threshold*100, status)
	}

	if len(failures) > 0 {
		t.Log("--- failures ---")
		for _, f := range failures {
			t.Logf("  %s", f)
		}
	}

	// --- validity checks, BEFORE any verdict ---
	//
	// These run first and fail hard. A run that lost trials cannot be read as a
	// pass no matter how good the surviving numbers look, because the trials it
	// lost are not a random sample: they are whichever scenarios came after the
	// budget ran out. The first run of this file lost `refusal` and `multistep`
	// entirely -- and `refusal` is the category that carries the
	// clean_termination gate's most important case, "call nothing when nothing
	// fits". It still printed PASS.
	errRate := float64(transportErrors) / float64(total)
	if transportErrors > 0 {
		t.Logf("NOTE: %d/%d probes (%.0f%%) failed at the transport layer and were excluded from every rate above",
			transportErrors, total, errRate*100)
	}
	if transportErrors == total {
		t.Fatalf("every probe failed at the transport layer -- credentials or endpoint are wrong, no reliability conclusion can be drawn:\n  %s",
			strings.Join(failures[:min(len(failures), 3)], "\n  "))
	}
	if len(starvedCategories) > 0 {
		t.Fatalf("VOID: %d scenario categories got zero graded trials (%s). "+
			"These are not passes; they are absences. Re-run with a working budget "+
			"before drawing any conclusion about %s.",
			len(starvedCategories), strings.Join(starvedCategories, ", "), model)
	}
	if len(starvedGates) > 0 {
		t.Fatalf("VOID: %d gate(s) were never exercised (%s) -- no conclusion can be drawn about them",
			len(starvedGates), strings.Join(starvedGates, ", "))
	}
	if errRate > maxTransportErrorRate {
		t.Fatalf("VOID: %.0f%% of probes failed at the transport layer (ceiling %.0f%%). "+
			"The surviving trials are a biased sample -- whatever ran before the failures started -- "+
			"so the rates above measure scenario ORDER as much as model reliability.",
			errRate*100, maxTransportErrorRate*100)
	}

	if failedGates > 0 {
		t.Fatalf("%d/%d gate(s) failed for model %s.\n\n"+
			"THE DOCUMENTED CONSEQUENCE IS NOT TO LOWER THE GATE. Per the Phase 0 decision rule, "+
			"agent-mode turns route to the reasoning tier via an explicit PromptKind (daemon/router.go), "+
			"and the cost delta gets written into the plan before Phase 4 starts.",
			failedGates, len(toolCallGates), model)
	}
	t.Logf("all %d gates passed over %d graded trials: %s carries agent mode",
		len(toolCallGates), total-transportErrors, model)
}
