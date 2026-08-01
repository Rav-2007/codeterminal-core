package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codeterminal/protocol"
)

// scriptedApprover answers a fixed sequence of decisions and records every
// question it was asked -- which is half the point: several properties here are
// about what the loop does NOT ask.
type scriptedApprover struct {
	answers []approvalDecision
	asked   []protocol.ToolApprovalRequest
}

func (a *scriptedApprover) Ask(_ context.Context, req protocol.ToolApprovalRequest) approvalDecision {
	a.asked = append(a.asked, req)
	if len(a.asked) > len(a.answers) {
		// Running off the end of the script means the loop asked more than the
		// test expected. Denying makes that show up as a failed assertion
		// rather than an index panic.
		return denied(denyByTimeout, 0)
	}
	return a.answers[len(a.asked)-1]
}

// deliberatingApprover approves, but only after really spending the time --
// which is the only way to test that human thinking time is given back to the
// turn deadline. A fake that reported a duration it never spent would leave the
// property untested.
type deliberatingApprover struct{ think time.Duration }

func (a *deliberatingApprover) Ask(_ context.Context, _ protocol.ToolApprovalRequest) approvalDecision {
	started := time.Now()
	time.Sleep(a.think)
	return approvalDecision{Decision: protocol.ApprovalApprove, Waited: time.Since(started)}
}

func says(decision string) approvalDecision {
	switch decision {
	case protocol.ApprovalApprove, protocol.ApprovalApproveForTurn:
		return approvalDecision{Decision: decision}
	case protocol.ApprovalCancelTurn:
		return approvalDecision{Decision: decision, Cause: denyByUser}
	default:
		return denied(denyByUser, 0)
	}
}

// askServer builds a loop server whose read tools are all on "ask" (the default
// every unlisted tool resolves to) with an audit sink pointed at a temp file.
func askServer(t *testing.T, base string) (*Server, string) {
	t.Helper()
	s := loopServer(t, base, MCPConfig{Enabled: true})
	s.counters = &counters{}
	auditPath := filepath.Join(t.TempDir(), "toolcalls.jsonl")
	s.toolAudit = newToolAuditSink(auditPath)
	if err := os.WriteFile(filepath.Join(s.workspace, "inside.txt"), []byte("HELLO"), 0600); err != nil {
		t.Fatal(err)
	}
	return s, auditPath
}

func readAudit(t *testing.T, path string) []toolAuditEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var events []toolAuditEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev toolAuditEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("audit line is not valid JSON: %v", err)
		}
		events = append(events, ev)
	}
	return events
}

// The happy path, which is also the one that proves consent is load-bearing
// rather than decorative: the SAME config that refused the call in
// TestAskPolicyIsRefusedWhenThereIsNobodyToAsk runs it once somebody says yes.
func TestAnApprovedAskPolicyToolRuns(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("It says HELLO."),
	)
	s, auditPath := askServer(t, base)
	appr := &scriptedApprover{answers: []approvalDecision{says(protocol.ApprovalApprove)}}

	res, activity, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	if len(res.ToolNames) != 1 || res.ToolNames[0] != "builtin__read_file" {
		t.Fatalf("the approved tool did not run; tools were %v", res.ToolNames)
	}
	if len(appr.asked) != 1 {
		t.Fatalf("expected exactly one approval prompt, got %d", len(appr.asked))
	}

	// The prompt must show what will actually run, in full, and must not soften
	// the lane it belongs to.
	ask := appr.asked[0]
	if ask.Arguments != `{"path":"inside.txt"}` {
		t.Errorf("the prompt showed %q, not the arguments that ran", ask.Arguments)
	}
	if ask.ArgumentsSHA256 != argumentsDigest(ask.Arguments) {
		t.Errorf("the prompt's digest does not match the arguments it displayed")
	}
	if ask.Lane != protocol.LaneFirstParty || !ask.Confined {
		t.Errorf("a built-in tool was described as lane=%q confined=%t", ask.Lane, ask.Confined)
	}
	if ask.Iteration != 1 || ask.MaxIterations == 0 {
		t.Errorf("the prompt gave no usable sense of loop progress: iteration %d of %d", ask.Iteration, ask.MaxIterations)
	}

	var approved bool
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseApproved {
			approved = true
		}
	}
	if !approved {
		t.Errorf("no approved phase was narrated; activity was %+v", activity)
	}
	if got := s.counters.snapshot().ToolCallsApproved; got != 1 {
		t.Errorf("ToolCallsApproved = %d, want 1", got)
	}
	if calls.Load() != 2 {
		t.Errorf("the loop made %d model calls, want 2", calls.Load())
	}

	events := readAudit(t, auditPath)
	if len(events) != 1 {
		t.Fatalf("expected one audit record, got %d: %+v", len(events), events)
	}
	if events[0].Source != auditUserApprove || events[0].Outcome != auditOutcomeOK {
		t.Errorf("audit recorded source=%q outcome=%q", events[0].Source, events[0].Outcome)
	}
}

// A denial must reach the MODEL as a tool result, not just stop the call.
// Silence makes the model repeat itself; being told no lets it explain.
func TestADeniedToolDoesNotRunAndTheModelIsTold(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("You declined, so I cannot say."),
	)
	s, auditPath := askServer(t, base)
	appr := &scriptedApprover{answers: []approvalDecision{says(protocol.ApprovalDeny)}}

	res, _, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(res.ToolNames) != 0 {
		t.Fatalf("a denied tool ran anyway: %v", res.ToolNames)
	}

	// The second request carries the tool message the loop fed back.
	if len(*bodies) < 2 {
		t.Fatalf("the loop stopped instead of telling the model it was denied")
	}
	if second := string((*bodies)[1]); !strings.Contains(second, "declined") {
		t.Errorf("the refusal sent to the model does not say the user declined: %s", second)
	}

	events := readAudit(t, auditPath)
	if len(events) != 1 || events[0].Source != auditDeniedUser || events[0].DenyCause != denyByUser {
		t.Fatalf("a user denial was not recorded as one: %+v", events)
	}
	if events[0].Outcome != auditOutcomeRefused {
		t.Errorf("outcome = %q, want %q", events[0].Outcome, auditOutcomeRefused)
	}
}

// APPROVE-FOR-TURN IS SCOPED TO THE TOOL, NOT WIDENED TO THE TURN.
//
// Saying yes to reading files must not become yes to listing directories. The
// neuter check is direct: key turn.grants by anything coarser (the server, or a
// bare bool) and the third call stops prompting.
func TestApproveForTurnCoversTheSameToolAndNothingElse(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"inside.txt"}`),
		toolCallSSE("c3", "builtin__list_directory", `{"path":"."}`),
		textSSE("done"),
	)
	s, _ := askServer(t, base)
	appr := &scriptedApprover{answers: []approvalDecision{
		says(protocol.ApprovalApproveForTurn), // covers read_file for the rest of the turn
		says(protocol.ApprovalDeny),           // the list_directory prompt, refused
	}}

	res, _, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	if len(appr.asked) != 2 {
		t.Fatalf("expected 2 prompts (read_file once, then list_directory), got %d: %+v", len(appr.asked), appr.asked)
	}
	if appr.asked[1].Tool != "list_directory" {
		t.Errorf("the second prompt was for %q -- a read_file grant did not cover the second read", appr.asked[1].Tool)
	}
	if len(res.ToolNames) != 2 {
		t.Fatalf("expected the two read_file calls to run and list_directory to be refused, got %v", res.ToolNames)
	}
	for _, name := range res.ToolNames {
		if name != "builtin__read_file" {
			t.Errorf("a tool the grant did not cover ran: %s", name)
		}
	}
}

// A grant dies with its turn. It is held on agentTurn and written nowhere, so
// the next turn asks again -- which is the whole difference between
// approve-for-turn and an "always allow" the user would have to write in config
// and could read back later.
func TestAGrantDoesNotSurviveIntoTheNextTurn(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("first turn done"),
		toolCallSSE("c2", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("second turn done"),
	)
	s, _ := askServer(t, base)
	appr := &scriptedApprover{answers: []approvalDecision{
		says(protocol.ApprovalApproveForTurn),
		says(protocol.ApprovalApproveForTurn),
	}}

	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	if len(appr.asked) != 2 {
		t.Errorf("a grant leaked across turns: %d prompts for two turns, want 2", len(appr.asked))
	}
}

// Cancelling stops the turn there and then -- no further model call -- and says
// so honestly. Reusing IncompleteAgentBudget would tell a user who pressed
// cancel that they had run out of some ceiling, and send them to raise a limit
// nothing had reached.
func TestCancelTurnStopsTheLoopAndSaysWhy(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("this must never be reached"),
	)
	s, auditPath := askServer(t, base)
	appr := &scriptedApprover{answers: []approvalDecision{says(protocol.ApprovalCancelTurn)}}

	res, _, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("a cancelled turn came back as an error rather than an incomplete result: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("the loop made %d model calls after a cancel; it must stop at 1", calls.Load())
	}
	if len(res.ToolNames) != 0 {
		t.Errorf("a cancelled call ran: %v", res.ToolNames)
	}
	if res.Incomplete == nil || res.Incomplete.Reason != protocol.IncompleteUserCancelled {
		t.Fatalf("a cancelled turn was not reported as user-cancelled: %+v", res.Incomplete)
	}
	if strings.Contains(res.Incomplete.Detail, "budget") || strings.Contains(res.Incomplete.Detail, "limit") {
		t.Errorf("the cancel message blames a limit the user never hit: %q", res.Incomplete.Detail)
	}
	if got := s.counters.snapshot().BudgetTerminations; got != 0 {
		t.Errorf("a user cancel was counted as %d budget termination(s)", got)
	}

	events := readAudit(t, auditPath)
	if len(events) != 1 || events[0].Outcome != auditOutcomeCancel {
		t.Fatalf("the cancel was not audited as one: %+v", events)
	}
}

// THE TURN CLOCK BOUNDS MACHINE WORK, NOT HUMAN THOUGHT.
//
// mcp.budget.turn_timeout_seconds exists so a loop cannot run away. A user who
// pauses to actually read the arguments is doing exactly what the prompt asks
// of them, and killing their turn for it would punish the care the whole
// feature depends on. So deliberation time is given back to the deadline.
func TestHumanDeliberationDoesNotConsumeTheTurnDeadline(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("read it"),
	)
	s, _ := askServer(t, base)
	// A one-second turn budget, and a user who really does take longer than
	// that to answer. The wait must be REAL wall time: an approver that merely
	// reports a duration it did not spend would leave this test passing with
	// the deadline extension deleted.
	s.cfg.MCP.Budget.TurnTimeoutSeconds = 1
	appr := &deliberatingApprover{think: 1300 * time.Millisecond}

	res, _, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(res.ToolNames) != 1 {
		t.Fatalf("the approved tool did not run: %v", res.ToolNames)
	}
	if res.Incomplete != nil {
		t.Errorf("the turn was cut short after the user thought about it: %+v", res.Incomplete)
	}
}

// THE AUDIT RECORDS EVERY DECISION AND NEVER THE ARGUMENTS.
//
// Every decision, because an audit covering only the calls a user was prompted
// about is a log of things they already knew. Never the arguments, because they
// are unscrubbed model output -- paths, queries, source text -- and the digest
// is what actually binds a record to the call that ran.
func TestAuditRecordsEveryDecisionAndNeverTheArguments(t *testing.T) {
	const canary = "TOTALLY-UNIQUE-CANARY-VALUE"
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"`+canary+`.txt"}`),
		toolCallSSE("c2", "builtin__list_directory", `{"path":"."}`),
		toolCallSSE("c3", "builtin__no_such_tool", `{}`),
		textSSE("done"),
	)
	s, auditPath := askServer(t, base)
	// list_directory runs without a prompt; read_file is asked about and denied.
	s.cfg.MCP.Builtin.Tools = map[string]string{"list_directory": PolicyAllow}
	appr := &scriptedApprover{answers: []approvalDecision{says(protocol.ApprovalDeny)}}

	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("no audit log was written: %v", err)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatalf("THE AUDIT LOG CONTAINS RAW TOOL ARGUMENTS:\n%s", raw)
	}

	events := readAudit(t, auditPath)
	if len(events) != 3 {
		t.Fatalf("expected a record for all three decisions (denied, allowed, unknown), got %d: %+v", len(events), events)
	}

	bySource := map[string]toolAuditEvent{}
	for _, ev := range events {
		bySource[ev.Source] = ev
		if ev.ArgumentsSHA256 == "" || ev.Ts == "" || ev.Outcome == "" {
			t.Errorf("an audit record is missing its binding or outcome: %+v", ev)
		}
	}
	for _, want := range []string{auditDeniedUser, auditConfigAllow, auditDeniedConfig} {
		if _, ok := bySource[want]; !ok {
			t.Errorf("no audit record with source %q; got %v", want, bySource)
		}
	}
	// The digest must be the one the user's client was asked to echo.
	if got := bySource[auditDeniedUser].ArgumentsSHA256; got != argumentsDigest(`{"path":"`+canary+`.txt"}`) {
		t.Errorf("the audit digest does not match the arguments that were shown: %s", got)
	}
	// The allowed call was never prompted, so it must not claim a human said so.
	if ev := bySource[auditConfigAllow]; ev.Policy != PolicyAllow || ev.Lane != protocol.LaneFirstParty || !ev.Confined {
		t.Errorf("a built-in allow was recorded as policy=%q lane=%q confined=%t", ev.Policy, ev.Lane, ev.Confined)
	}
}

// A nil sink is the ordinary state of a test server and of any daemon whose
// log directory will not take writes. It must be a silent no-op, never a panic
// and never a failed turn.
func TestANilAuditSinkIsHarmless(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("done"),
	)
	s, _ := askServer(t, base)
	s.toolAudit = nil

	if _, _, err := runLoopWith(t, s, &scriptedApprover{answers: []approvalDecision{says(protocol.ApprovalApprove)}}); err != nil {
		t.Fatalf("a nil audit sink broke the turn: %v", err)
	}
}

// A FAILURE PART-WAY THROUGH IS AN INCOMPLETE TURN, NOT A VOID ONE.
//
// QA gate 2026-08-01, P1-1. runAgentLoop used to return agentResult{} on a
// stream error, discarding text the user had ALREADY WATCHED ARRIVE -- so any
// edit blocks in it were never offered, and the turn never reached conversation
// memory. budgetStop, in the same function, keeps all of that explicitly
// "because the work done so far is real and the user keeps it".
//
// Neuter check: restore `return agentResult{}, err` and both halves fail.
func TestAProviderFailureMidTurnKeepsTheWorkAlreadyDone(t *testing.T) {
	const edit = "\n```edit\nFILE: a.txt\n<<<<<<< SEARCH\nalpha\n=======\nomega\n>>>>>>> REPLACE\n```\n"

	scripted := [][]string{
		{
			`data: {"choices":[{"delta":{"content":"Reading it now. "}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"builtin__read_file","arguments":"{\"path\":\"inside.txt\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		},
		{
			fmt.Sprintf(`data: {"choices":[{"delta":{"content":%s}}]}`, mustJSON(edit)),
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c2","type":"function","function":{"name":"builtin__read_file","arguments":"{\"path\":\"inside.txt\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		},
	}

	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(scripted) {
			// Iteration 3: the provider has a bad minute. Retryable, so
			// streamWithRetry exhausts its attempts and gives up.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range scripted[i] {
			w.Write([]byte(line + "\n\n"))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)

	s, _ := askServer(t, srv.URL)
	s.cfg.MCP.Builtin.Tools = map[string]string{"read_file": PolicyAllow}

	var streamed strings.Builder
	registry, _ := s.buildRegistry(t.Context(), s.logger, &proposalSink{})
	t.Cleanup(func() { _ = registry.Close() })

	res, err := s.runAgentLoop(t.Context(), registry, "m",
		[]chatMessage{{Role: "user", Content: "go"}}, providerRouting{}, nil,
		func(tok string) error { streamed.WriteString(tok); return nil },
		nil, nil, nil)

	if err != nil {
		t.Fatalf("a turn with real work behind it came back as a bare error: %v", err)
	}
	if streamed.Len() == 0 {
		t.Fatal("premise broken: nothing streamed before the failure")
	}
	if res.FinalText != streamed.String() {
		t.Errorf("the user watched %d byte(s) arrive but the turn handed back %d",
			streamed.Len(), len(res.FinalText))
	}
	if res.Incomplete == nil || res.Incomplete.Reason != protocol.IncompleteProviderError {
		t.Fatalf("a provider failure was not reported as one: %+v", res.Incomplete)
	}
	// The load-bearing half: the edit block the user saw must still become a
	// proposal they can act on.
	if blocks := s.parseAndLogEditBlocks(res.FinalText); len(blocks) != 1 {
		t.Errorf("the edit block produced before the failure is lost: %d block(s) survived", len(blocks))
	}
}

// The first iteration is the exception: there is no work to preserve, so the
// error IS the story. Dressing a bare failure up as an "incomplete answer"
// would tell a user they have something when they have nothing.
func TestAFailureBeforeAnyOutputIsStillAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	s, _ := askServer(t, srv.URL)
	registry, _ := s.buildRegistry(t.Context(), s.logger, &proposalSink{})
	t.Cleanup(func() { _ = registry.Close() })

	res, err := s.runAgentLoop(t.Context(), registry, "m",
		[]chatMessage{{Role: "user", Content: "go"}}, providerRouting{}, nil,
		func(string) error { return nil }, nil, nil, nil)

	if err == nil {
		t.Fatalf("a turn that produced nothing was reported as a result: %+v", res)
	}
	if res.FinalText != "" {
		t.Errorf("nothing was produced, yet FinalText is %q", res.FinalText)
	}
}

// THE ID IS WHAT AN APPROVAL BINDS TO, so a call without one must be refused
// rather than dispatched (QA gate 2026-08-01, P1-2 -- found by the fuzzer).
//
// An empty id makes verifyApproval's call-id check vacuous, leaving consent
// bound by the argument digest alone. The digest covers the arguments and NOT
// the tool name, so an approval collected for one tool would verify for another
// called with the same arguments.
func TestAToolCallWithNoIDIsRefused(t *testing.T) {
	acc := newToolCallAccumulator()
	var chunk chatCompletionChunk
	if err := json.Unmarshal([]byte(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"builtin__list_directory","arguments":"{\"path\":\".\"}"}}]}}]}`,
	), &chunk); err != nil {
		t.Fatal(err)
	}
	acc.ingest(chunk)

	calls, err := acc.finish()
	if err == nil {
		t.Fatalf("a call with no id was assembled and is dispatchable: %+v", calls)
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("the refusal does not say what was missing: %v", err)
	}

	// And the reason it matters, asserted directly rather than left implied: with
	// no id to echo, one approval verifies for a different tool taking the same
	// arguments.
	const args = `{"path":"."}`
	answer, _ := json.Marshal(protocol.ToolApprovalResponse{
		Approval: true, CallID: "", ArgumentsSHA256: argumentsDigest(args),
		Decision: protocol.ApprovalApprove,
	})
	other := protocol.ToolApprovalRequest{
		CallID: "", Tool: "delete_everything",
		Arguments: args, ArgumentsSHA256: argumentsDigest(args),
	}
	if d, _ := verifyApproval(other, answer); d != protocol.ApprovalApprove {
		t.Fatal("premise broken: the id check is not vacuous when both ids are empty, " +
			"so refusing the empty id upstream is no longer the thing protecting this")
	}
	t.Log("confirmed: the id check IS vacuous when both ids are empty, which is " +
		"why finish() must refuse an id-less call rather than defend downstream")
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
