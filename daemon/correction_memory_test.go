package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// textAndToolSSE is one reply that says something and then calls a tool.
func textAndToolSSE(text, id, name, args string) []string {
	t, _ := json.Marshal(text)
	a, _ := json.Marshal(args)
	return []string{
		fmt.Sprintf(`data: {"choices":[{"delta":{"content":%s}}]}`, t),
		fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]}}]}`,
			id, name, a),
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
}

// toolResultOf is the content of the tool result a captured request carried
// for one call.
func toolResultOf(t *testing.T, body []byte, callID string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Messages {
		if m.Role == "tool" && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no tool result for %s in the request", callID)
	return ""
}

// segmentOpening is the content of a captured request's last user message: a
// segment's opening, with its task state.
func segmentOpening(t *testing.T, body []byte) string {
	t.Helper()
	msgs := sentMessages(t, body)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	t.Fatal("no user message in the request")
	return ""
}

// correctingServer is a long-task server on the vet project with sandbox_exec
// allowed and the judge in the given mode.
func correctingServer(t *testing.T, base, mode string, segmentCalls int) *Server {
	t.Helper()
	_, dir := vetProject(t)
	cfg := taskPolicies()
	cfg.Builtin.Tools["sandbox_exec"] = PolicyAllow
	cfg.Correction.Mode = mode
	if segmentCalls > 0 {
		cfg.Budget.Task = &MCPTaskBudgetConfig{SegmentCalls: segmentCalls}
	}
	s := loopServer(t, base, cfg)
	s.workspace = dir
	return s
}

// IN MODE "ON" THE MODEL READS WHAT THE JUDGE MEASURED, under the failed check
// itself: how the run compares, where to repair, and the ask for its own word
// on why. Its next words are kept with the check's frame -- Reflexion's
// reflection, folded into the reply it was going to write anyway.
//
// Neuter checks: drop the note in builtinSandboxExecStaged; drop the said
// hook in runAgentLoop (the frame keeps no reflection).
func TestOnTheModelReadsTheVerdictUnderTheCheck(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go vet ./..."}`),
		textAndToolSSE("The verb is %d for a string; %s is right.", "c2", "builtin__propose_edit",
			`{"path":"m.go","search":"%d","replace":"%s"}`),
		toolCallSSE("c3", "builtin__sandbox_exec", `{"command":"go vet ./..."}`),
		toolCallSSE("c4", "builtin__finish_task", `{"status":"done","summary":"Fixed the verb."}`),
	)
	s := correctingServer(t, base, correctionOn, 0)
	run, _, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := toolResultOf(t, (*bodies)[1], "c1")
	for _, want := range []string{"[Mochiii measured this run: the first run of this command in the task.]",
		"[Next: the build is fine, a check of it is not: fix what it reports at m.go:6]",
		"[Before your next edit, say in one line why it failed and what you will change.]"} {
		if !strings.Contains(failed, want) {
			t.Errorf("the failed check's result lacks %q:\n%s", want, failed)
		}
	}
	if passed := toolResultOf(t, (*bodies)[3], "c3"); strings.Contains(passed, "[Mochiii measured") {
		t.Errorf("a passing check carried a verdict note:\n%s", passed)
	}
	c := run.ledger.Correction
	if c == nil || len(c.Frames) != 1 || c.Frames[0].Reflection != "The verb is %d for a string; %s is right." {
		t.Fatalf("the ledger's frames: %+v", c)
	}
	if len(c.Tried) != 2 || !strings.Contains(c.Tried[0], "1 build error at m.go:6") || !strings.Contains(c.Tried[1], "passed") {
		t.Errorf("tried: %q", c.Tried)
	}
}

// EVERY SEGMENT OPENS WITH THE MEMORY IN MODE "ON", and with none of it in
// "shadow" -- the same run, the same ledger record, never sent.
//
// Neuter checks: render the memory whatever the mode (pass true for
// correcting in segmentMessages); drop the Tried section from render.
func TestTheNextSegmentOpensWithTheMemoryOnlyWhenOn(t *testing.T) {
	for _, mode := range []string{correctionOn, correctionShadow} {
		base, _, bodies := agentUpstream(t,
			toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go vet ./..."}`),
			textSSE("HANDOFF: vet fails at m.go:6."),
			toolCallSSE("c2", "builtin__finish_task", `{"status":"blocked","summary":"Stopping here."}`),
		)
		s := correctingServer(t, base, mode, 2)
		run, _, runIt := directTaskRun(t, s, modeFix)
		if _, err := runIt(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(*bodies) != 3 || run.ledger.Correction == nil || len(run.ledger.Correction.Frames) != 1 {
			t.Fatalf("%s: %d requests, record %+v", mode, len(*bodies), run.ledger.Correction)
		}
		opening := segmentOpening(t, (*bodies)[2])
		shown := strings.Contains(opening, "Your last failed checks, as the daemon measured them") &&
			strings.Contains(opening, "Next: the build is fine") &&
			strings.Contains(opening, "What this task has tried")
		if shown != (mode == correctionOn) {
			t.Errorf("mode %s: segment 2 shows the memory = %v:\n%s", mode, shown, opening)
		}
		if mode == correctionShadow && strings.Contains(opening, "measured") {
			t.Errorf("shadow sent part of the memory:\n%s", opening)
		}
	}
}

// WHAT FAILED WITHOUT BEING A CHECK IS REMEMBERED TOO: an edit the gates
// refused and a finish the gate refused -- both lost at a segment boundary
// before, so the next segment could make the same edit and the same claim.
//
// Neuter checks: drop the noteTried in applyInWorkingCopy; drop the one in
// the finish refusal.
func TestRefusedEditsAndFinishesAreRemembered(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__propose_edit", `{"path":"m.go","search":"no such text","replace":"x"}`),
		toolCallSSE("c2", "builtin__finish_task", `{"status":"done","summary":"Done, surely."}`),
		toolCallSSE("c3", "builtin__finish_task", `{"status":"blocked","summary":"I need help."}`),
	)
	s := correctingServer(t, base, correctionShadow, 0)
	run, _, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := run.ledger.Correction
	if c == nil || len(c.Tried) != 2 ||
		!strings.HasPrefix(c.Tried[0], "an edit to m.go was refused: search text not found in m.go") ||
		!strings.HasPrefix(c.Tried[1], "finish_task was refused: ") {
		t.Errorf("tried: %+v", c)
	}
}

// A RESUMED TASK IS TOLD HOW ITS LAST RUN ENDED, in mode "on" only.
func TestAResumedTaskIsToldHowItsLastRunEnded(t *testing.T) {
	l := &taskLedger{ID: "t1", Mode: modeFix, PriorStop: "stuck: two segments in a row moved nothing forward"}
	b := taskBudget{calls: 10, usd: 1, minutes: 5}
	if on := l.renderTaskState(b, taskSpend{}, 3, true); !strings.Contains(on, "The run before this one ended stuck: two segments") {
		t.Errorf("on:\n%s", on)
	}
	if off := l.renderTaskState(b, taskSpend{}, 3, false); strings.Contains(off, "ended stuck") {
		t.Errorf("not correcting, but told:\n%s", off)
	}
}

// THE LEDGER NEVER HOLDS THE DAEMON'S OWN KEY, even with the scrub switched
// off: a command that prints it -- from a .env in the project, say -- would
// otherwise send it to the model again in every later segment, through the
// one path that did not redact it as every tool result is.
//
// Neuter check: drop credRedact from cleanForLedger.
func TestTheLedgerRedactsTheDaemonsOwnKey(t *testing.T) {
	key := "opaque-provider-key-6f0c2a9d41b7e58c"
	s := credServer(t, key)
	s.cfg.NoScrub = true
	p := &proposalSink{checkOutput: "--- FAIL: TestUpload\n    upload_test.go:9: sent key " + key}
	p.checks = []ranCheck{{command: "go test ./upload", passed: false}}
	run := &taskRun{ledger: &taskLedger{}}
	run.absorbSegment(p, s.cleanForLedger)
	if c := run.ledger.LastCheck; c == nil || strings.Contains(c.Output, key) || !strings.Contains(c.Output, "FAIL") {
		t.Errorf("the ledger's check output = %+v", c)
	}
	if got := s.cleanForLedger("I saw " + key); strings.Contains(got, key) {
		t.Errorf("cleanForLedger kept the key: %q", got)
	}
}

// THE SAME FAILURE GETS THE SAME REPAIR, aimed where the output points.
func TestRepairIsTypedAndAimed(t *testing.T) {
	cases := map[string]checkOutcome{
		"it does not build: fix the error at a.go:3 first, and change only that block": {Kind: checkBuild, Errors: 1, Locations: []string{"a.go:3"}},
		"TestX fails at a_test.go:9: fix the code it tests, not the test -- unless it is the test you are writing": {
			Kind: checkTest, Failing: []string{"TestX"}, Tests: 1, Locations: []string{"a_test.go:9"}},
		"it panics at a.go:14: fix the cause there -- a nil, an index, a closed channel -- before anything else": {
			Kind: checkPanic, Failing: []string{"TestX"}, Tests: 1, Locations: []string{"a.go:14"}},
		"the check timed out: look for a loop or a blocking call in what you changed, or run a narrower test": {Kind: checkTimeout},
	}
	for want, o := range cases {
		if got := repairFor(o); got != want {
			t.Errorf("%s: %q", o.Kind, got)
		}
	}
	if repairFor(checkOutcome{Kind: checkPass}) != "" || repairFor(checkOutcome{Kind: checkOther}) != "" {
		t.Error("a pass or an unreadable failure was given a repair")
	}
}
