package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// loopServer builds a Server wired for agent mode against a scripted upstream.
func loopServer(t *testing.T, apiBase string, mcpCfg MCPConfig) *Server {
	t.Helper()
	s := builtinTestServer(t)
	s.apiBase = apiBase
	s.apiKey = "k"
	s.cfg = &Config{MCP: mcpCfg}
	s.logger = log.New(os.Stderr, "loop-test: ", 0)
	return s
}

// agentUpstream replies with one canned SSE response per request, in order,
// so a multi-iteration loop can be driven deterministically.
func agentUpstream(t *testing.T, responses ...[]string) (base string, requests *atomic.Int64, bodies *[][]byte) {
	t.Helper()
	var count atomic.Int64
	var captured [][]byte
	srv := rawSSEServerFunc(t, func(body []byte) []string {
		n := int(count.Add(1)) - 1
		captured = append(captured, body)
		if n >= len(responses) {
			// A loop that asks more times than the script expects is a runaway;
			// answer with plain text so the test fails on its assertions rather
			// than hanging.
			return []string{`data: {"choices":[{"delta":{"content":"unscripted"},"finish_reason":"stop"}]}`, `data: [DONE]`}
		}
		return responses[n]
	})
	return srv, &count, &captured
}

func toolCallSSE(id, name, args string) []string {
	escaped, _ := json.Marshal(args)
	return []string{
		fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]}}]}`,
			id, name, string(escaped)),
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
}

func textSSE(text string) []string {
	escaped, _ := json.Marshal(text)
	return []string{
		fmt.Sprintf(`data: {"choices":[{"delta":{"content":%s}}]}`, string(escaped)),
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
}

// runLoop drives one agent turn with no approval channel: every "ask" policy
// is therefore refused. Tests that exercise consent use runLoopWith.
func runLoop(t *testing.T, s *Server) (agentResult, []protocol.ToolActivity, error) {
	t.Helper()
	return runLoopWith(t, s, nil)
}

// lastDegradations collects the degradations the most recent runLoopWith saw.
// Package-level because the helper's signature is used by dozens of tests and
// widening it for the two that care would be noise; the daemon's tests do not
// run in parallel.
var lastDegradations []protocol.Degradation

func runLoopWith(t *testing.T, s *Server, appr approver) (agentResult, []protocol.ToolActivity, error) {
	t.Helper()
	return runLoopPrompt(t, s, appr, "go")
}

// runLoopWithPrompt drives a turn with a SPECIFIC user message, for the tests
// that turn on what the user actually asked -- the live-question classifier
// cannot be exercised through the fixed "go" prompt the other helpers send.
func runLoopWithPrompt(t *testing.T, s *Server, prompt string) (agentResult, []protocol.ToolActivity, error) {
	t.Helper()
	return runLoopPrompt(t, s, nil, prompt)
}

func runLoopPrompt(t *testing.T, s *Server, appr approver, prompt string) (agentResult, []protocol.ToolActivity, error) {
	t.Helper()
	lastDegradations = nil
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	var activity []protocol.ToolActivity
	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: prompt}}, providerRouting{}, appr,
		func(string) error { return nil },
		func(a protocol.ToolActivity) { activity = append(activity, a) },
		nil, nil,
		func(d protocol.Degradation) { lastDegradations = append(lastDegradations, d) }, nil, nil)
	return res, activity, err
}

// THE PHASE-4 SAFETY PROPERTY.
//
// When there is nobody to ask, a tool whose policy is "ask" must be REFUSED,
// not run. The tempting alternative -- treat an un-askable ask as an allow,
// since we cannot ask -- is precisely the bug the whole consent design exists
// to prevent, and it would be invisible: the tool would simply work.
//
// This is not a hypothetical state now that the channel exists. A nil approver
// is what every caller with no client on the other end has, including the loop
// eval, and it is what a client that never declared CapToolApproval would get
// if agentModeEngaged ever stopped requiring it.
func TestAskPolicyIsRefusedWhenThereIsNobodyToAsk(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("I could not read it."),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAsk}},
	})
	if err := os.WriteFile(s.workspace+"/inside.txt", []byte("SENSITIVE"), 0600); err != nil {
		t.Fatal(err)
	}

	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	// The refusal must reach the model as a tool result, so it can explain
	// itself rather than silently retry.
	if len(res.ToolNames) != 0 {
		t.Errorf("an ask-policy tool RAN: %v. With no approval channel it must be refused", res.ToolNames)
	}
	var denied bool
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseDenied {
			denied = true
			if !strings.Contains(a.Detail, "approval") {
				t.Errorf("denial detail %q should say approval was needed", a.Detail)
			}
		}
	}
	if !denied {
		t.Errorf("no denial was reported; activity was %+v", activity)
	}
}

func TestDenyPolicyIsRefused(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyDeny}},
	})

	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(res.ToolNames) != 0 {
		t.Errorf("a denied tool ran: %v", res.ToolNames)
	}
}

// An allowed tool runs, its result goes back, and the loop continues to a
// normal answer. The happy path, which everything else is a deviation from.
func TestAllowedToolRunsAndTheLoopContinues(t *testing.T) {
	base, requests, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("The file says hello."),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})
	if err := os.WriteFile(s.workspace+"/inside.txt", []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}

	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("made %d model calls, want 2 (one asking for the tool, one answering)", got)
	}
	if len(res.ToolNames) != 1 || res.ToolNames[0] != "builtin__read_file" {
		t.Errorf("ToolNames = %v", res.ToolNames)
	}
	if !strings.Contains(res.FinalText, "The file says hello") {
		t.Errorf("FinalText = %q", res.FinalText)
	}
	if res.Incomplete != nil {
		t.Errorf("a naturally-finished turn reported Incomplete = %+v", res.Incomplete)
	}

	// The second request must carry the assistant's tool_calls AND the tool
	// result, in that order: without the assistant message the provider has
	// nothing to pair the result with.
	second := string((*bodies)[1])
	for _, want := range []string{`"tool_calls"`, `"role":"tool"`, `"tool_call_id":"c1"`, "hello"} {
		if !strings.Contains(second, want) {
			t.Errorf("the follow-up request is missing %s:\n%s", want, second)
		}
	}

	phases := map[string]bool{}
	for _, a := range activity {
		phases[a.Phase] = true
	}
	for _, want := range []string{protocol.ToolPhaseRequested, protocol.ToolPhaseRunning, protocol.ToolPhaseSucceeded} {
		if !phases[want] {
			t.Errorf("phase %q was never reported; a user watching a multi-second loop needs to see it", want)
		}
	}
}

// A loop's characteristic failure is not stopping. This proves the ceiling
// actually stops it, and that the user keeps the work done so far.
func TestIterationBudgetStopsARunawayLoop(t *testing.T) {
	// The model asks for a tool every single time, forever.
	always := make([][]string, 0, 20)
	for range 20 {
		always = append(always, toolCallSSE("c", "builtin__read_file", `{"path":"inside.txt"}`))
	}
	base, requests, _ := agentUpstream(t, always...)

	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
		Budget:  MCPBudgetConfig{MaxIterations: 3},
	})
	if err := os.WriteFile(s.workspace+"/inside.txt", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("a budget stop must not be an error: %v", err)
	}
	if res.Incomplete == nil || res.Incomplete.Reason != protocol.IncompleteAgentBudget {
		t.Fatalf("Incomplete = %+v, want reason %q", res.Incomplete, protocol.IncompleteAgentBudget)
	}
	if got := requests.Load(); got > 4 {
		t.Errorf("made %d model calls with max_iterations=3; the ceiling did not bite", got)
	}
	if !strings.Contains(res.Incomplete.Detail, "steps") {
		t.Errorf("detail %q should say which ceiling stopped it", res.Incomplete.Detail)
	}
}

func TestTurnDeadlineStopsTheLoop(t *testing.T) {
	base, _, _ := agentUpstream(t, toolCallSSE("c", "builtin__read_file", `{"path":"x"}`))
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})

	// A deadline already in the past: the loop must stop before its first call.
	bud := resolveBudget(MCPBudgetConfig{}, time.Now().Add(-time.Hour))
	stop := s.budgetStop(&agentTurn{iteration: 1}, bud)
	if stop == nil || stop.Reason != protocol.IncompleteAgentBudget {
		t.Fatalf("an expired deadline gave %+v, want an agent-budget stop", stop)
	}
	if !strings.Contains(stop.Detail, "time") {
		t.Errorf("detail %q should say it ran out of time", stop.Detail)
	}
}

func TestCumulativeToolByteBudgetStopsTheLoop(t *testing.T) {
	bud := resolveBudget(MCPBudgetConfig{MaxTotalToolBytes: 100}, time.Now())
	stop := (&Server{logger: log.New(os.Stderr, "", 0)}).budgetStop(
		&agentTurn{iteration: 1, toolBytes: 100}, bud)
	if stop == nil || stop.Reason != protocol.IncompleteAgentBudget {
		t.Fatalf("exhausted tool-byte budget gave %+v, want an agent-budget stop", stop)
	}
}

// THE EGRESS TEST. Tool output is appended to the next request, so it leaves
// the machine. It must go through the same scrub the retrieval path uses.
//
// Neuter renderToolResult's scrub call and this fails.
func TestToolOutputIsScrubbedOnTheEgressPath(t *testing.T) {
	const leaked = "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCD"

	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"config.txt"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})
	// A file the indexer would have skipped by name, but that the model asked
	// for directly by a harmless one.
	if err := os.WriteFile(s.workspace+"/config.txt", []byte("api_key = "+leaked+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runLoop(t, s); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	if len(*bodies) < 2 {
		t.Fatalf("expected a follow-up request carrying the tool result, got %d requests", len(*bodies))
	}
	follow := string((*bodies)[1])
	if strings.Contains(follow, leaked) {
		t.Errorf("a secret-shaped string from a tool's output was POSTed to the model provider "+
			"verbatim. Tool output is EGRESS and must go through scrub() exactly as retrieved "+
			"chunks do.\nrequest body:\n%s", follow)
	}
	if !strings.Contains(follow, "REDACTED") {
		t.Errorf("the tool result reached the wire without a redaction marker:\n%s", follow)
	}
}

func TestToolResultIsTruncatedAndSaysSo(t *testing.T) {
	big := strings.Repeat("A", 5000)
	rendered, _, emitted := renderToolResult(big, 1000, false, false)

	if emitted > 1200 {
		t.Errorf("emitted %d bytes with a 1000-byte cap", emitted)
	}
	if !strings.Contains(rendered, "truncated") {
		t.Error("a truncated result must say so, or the model reasons about the missing part " +
			"as though it were absent and may call the same tool again to get 'the rest'")
	}
	if !strings.Contains(rendered, "5000") {
		t.Error("the notice should state the original size")
	}
}

// Truncation must not split a multi-byte rune: json.Marshal would re-encode the
// fragment as U+FFFD, splicing a replacement character into the model's view of
// its own tool output.
func TestTruncationDoesNotSplitRunes(t *testing.T) {
	// Each "é" is two bytes, so a 5-byte cap lands mid-rune.
	rendered, _, _ := renderToolResult(strings.Repeat("é", 10), 5, false, false)
	for _, r := range rendered {
		if r == '\uFFFD' {
			t.Fatalf("truncation split a rune, producing U+FFFD: %q", rendered)
		}
	}
}

// A tool the model invents must be refused by name, so it can pick a real one
// rather than retry the same wrong string.
func TestUnknownToolIsRefusedWithAReadableReason(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__delete_everything", `{}`),
		textSSE("ok"),
	)
	s := loopServer(t, base, MCPConfig{Enabled: true})

	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(res.ToolNames) != 0 {
		t.Errorf("an invented tool ran: %v", res.ToolNames)
	}
	found := false
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseDenied && strings.Contains(a.Detail, "no tool called") {
			found = true
		}
	}
	if !found {
		t.Errorf("the refusal should name the missing tool; activity was %+v", activity)
	}
}

// propose_edit must file a proposal rather than write, and must validate
// eagerly so the model can fix its own SEARCH text.
func TestProposeEditProposesAndNeverWrites(t *testing.T) {
	s := builtinTestServer(t)
	path := s.workspace + "/main.go"
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	sink := &proposalSink{}
	res, err := s.builtinProposeEdit(context.Background(),
		json.RawMessage(`{"path":"main.go","search":"func main() {}","replace":"func main() { println(1) }"}`), sink)
	if err != nil {
		t.Fatalf("builtinProposeEdit: %v", err)
	}
	if res.IsError {
		t.Fatalf("a valid edit was refused: %s", res.Content)
	}

	// THE PROPERTY: the file on disk is untouched.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Errorf("propose_edit WROTE to the file. It must only propose: the user reviews a diff "+
			"and decides.\n before: %q\n after: %q", original, string(after))
	}
	if len(sink.blocks) != 1 {
		t.Fatalf("the proposal was not filed for review: %+v", sink.blocks)
	}
	// The model must be told plainly that nothing happened yet, or it will
	// assume the edit took effect and build on it.
	if !strings.Contains(res.Content, "NOT applied") {
		t.Errorf("result %q must tell the model the edit has not taken effect", res.Content)
	}

	t.Run("a SEARCH that does not match is refused immediately", func(t *testing.T) {
		res, err := s.builtinProposeEdit(context.Background(),
			json.RawMessage(`{"path":"main.go","search":"not in the file","replace":"x"}`), &proposalSink{})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !res.IsError {
			t.Error("a non-matching SEARCH was accepted; the model should learn now, while it " +
				"still has the file in context, rather than at review time")
		}
	})

	t.Run("confinement applies", func(t *testing.T) {
		res, err := s.builtinProposeEdit(context.Background(),
			json.RawMessage(`{"path":"../escape.go","search":"","replace":"x"}`), &proposalSink{})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !res.IsError {
			t.Error("propose_edit accepted a path outside the workspace")
		}
	})
}

// Tool messages must never reach conversation memory: validTurn rejects the
// role, and re-injecting tool output through history is exactly what D11
// forbids.
func TestToolMessagesNeverReachPersistedHistory(t *testing.T) {
	summary := summariseToolActivity([]string{"builtin__read_file", "builtin__search_code"})
	if strings.Contains(summary, "package main") {
		t.Error("the summary carries tool OUTPUT; it must carry only which tools ran")
	}
	if !strings.Contains(summary, "2 tool call(s)") {
		t.Errorf("summary = %q, want a count and the names", summary)
	}

	// And the role a tool message uses is one prepareHistory refuses.
	turns := []protocol.Turn{{Role: "tool", Content: "secret tool output"}}
	outcome := prepareHistory(turns, false)
	for _, m := range outcome.Messages {
		if m.Role == "tool" || strings.Contains(m.Content, "secret tool output") {
			t.Errorf("a tool-role turn survived prepareHistory: %+v", m)
		}
	}
}

// agentModeEngaged is the three-condition gate. A client that cannot answer an
// approval must never be put in a position where one is needed.
func TestAgentModeRequiresBothConfigAndCapability(t *testing.T) {
	capable := protocol.HandshakeRequest{Capabilities: []string{protocol.CapToolApproval}}
	oldClient := protocol.HandshakeRequest{}

	cases := []struct {
		name string
		cfg  *Config
		hs   protocol.HandshakeRequest
		want bool
	}{
		{"enabled + capable", &Config{MCP: MCPConfig{Enabled: true}}, capable, true},
		{"enabled + old client", &Config{MCP: MCPConfig{Enabled: true}}, oldClient, false},
		{"disabled + capable", &Config{MCP: MCPConfig{Enabled: false}}, capable, false},
		{"no config at all", nil, capable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: tc.cfg}
			if got := s.agentModeEngaged(tc.hs); got != tc.want {
				t.Errorf("agentModeEngaged = %t, want %t", got, tc.want)
			}
		})
	}
}

// A registry with tools registered still advertises nothing the model can use
// when every policy denies, and the loop then behaves like an ordinary turn.
func TestAgentTurnWithNoUsableToolsIsAnOrdinaryTurn(t *testing.T) {
	base, requests, bodies := agentUpstream(t, textSSE("just an answer"))
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Disabled: true},
	})

	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("made %d model calls, want 1", got)
	}
	if res.FinalText != "just an answer" {
		t.Errorf("FinalText = %q", res.FinalText)
	}
	if strings.Contains(string((*bodies)[0]), `"tools"`) {
		t.Error("a turn with no usable tools still sent a tools array")
	}
}

var _ = mcp.BuiltinServerName

// Time spent connecting MCP servers must come out of the turn budget.
//
// Servers are connected by runAgentTurn BEFORE the loop exists, and a server
// that starts and never answers initialize costs a full connect_timeout_seconds
// (20 s by default). The budget used to be resolved from time.Now() inside the
// loop, so that wait was charged to nothing: a user with a 60 s turn budget
// could wait 20 s to connect and then 60 s more, and the setting that was
// supposed to bound their wait bounded only part of it.
//
// Passing turnStart makes the deadline cover the turn the user experienced.
// Here the turn "began" longer ago than the whole budget, which is what a slow
// connect looks like from the loop's side: the deadline has already passed and
// the very first iteration must stop on it.
func TestConnectTimeIsChargedToTheTurnBudget(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
		Budget:  MCPBudgetConfig{TurnTimeoutSeconds: 30},
	})

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	// 45 s of connect against a 30 s budget: the budget is already spent.
	turnStart := time.Now().Add(-45 * time.Second)
	res, err := s.runAgentLoop(context.Background(), turnStart, registry, "m", "auto",
		[]chatMessage{{Role: "user", Content: "go"}}, providerRouting{}, nil,
		func(string) error { return nil }, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	if res.Incomplete == nil {
		t.Fatal("turn completed normally: the connect time was not charged to the deadline, so turn_timeout_seconds bounds only part of the user's wait")
	}
	if res.Incomplete.Reason != protocol.IncompleteAgentBudget {
		t.Errorf("Incomplete.Reason = %q, want %q", res.Incomplete.Reason, protocol.IncompleteAgentBudget)
	}
	if res.Iterations != 0 {
		t.Errorf("Iterations = %d, want 0 — the budget was gone before the first model call", res.Iterations)
	}
}
