package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"mochiii/editapply"
	"mochiii/protocol"
)

// fixedAnswer answers every approval the same way, and counts the asks.
type fixedAnswer struct {
	decision string
	asked    int
}

func (a *fixedAnswer) Ask(context.Context, protocol.ToolApprovalRequest) approvalDecision {
	a.asked++
	return approvalDecision{Decision: a.decision}
}

// taskRunAnswering is directTaskRun with an approver.
func taskRunAnswering(t *testing.T, s *Server, mode string, appr approver) (*taskRun, *proposalSink, func(context.Context) (agentResult, error)) {
	t.Helper()
	root, err := editapply.ResolveRealWorkspaceRoot(s.workspace)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := &taskRun{ledger: newTaskLedger(root, mode, "the goal", now), budget: resolveTaskBudget(s.cfg.MCP.Budget.Task, nil), start: now}
	run.deadline = now.Add(time.Hour)
	s.runningTasks.Store(run.ledger.ID, true)
	p := &proposalSink{stageFrom: root, task: run}
	registry, _ := s.buildRegistry(context.Background(), s.logger, p, mode)
	t.Cleanup(func() { _ = registry.Close(); p.discard() })
	return run, p, func(ctx context.Context) (agentResult, error) {
		return s.runLongTask(ctx, now, registry, "test-model",
			[]chatMessage{{Role: "system", Content: "S"}, {Role: "user", Content: "the goal"}},
			providerRouting{}, appr, p, func(string) error { return nil }, nil, nil, nil, nil,
			func(protocol.TaskStatus) {})
	}
}

// fixThenTalk runs the tests (TestA fails), fixes A, and ends the segment in
// words -- no check after the edit -- then tries to finish, and failing that,
// says it is blocked.
func fixThenTalk(t *testing.T) (string, func() int64) {
	t.Helper()
	base, calls, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c2", "builtin__propose_edit", `{"path":"m.go","search":"return 0","replace":"return 1"}`),
		textSSE("Fixed A."),
		toolCallSSE("c3", "builtin__finish_task", `{"status":"done","summary":"A returns 1."}`),
		toolCallSSE("c4", "builtin__finish_task", `{"status":"blocked","summary":"The check is stale."}`),
	)
	return base, calls.Load
}

func recheckServer(t *testing.T, base, mode, sandboxPolicy string) *Server {
	t.Helper()
	_, dir := threeTests(t)
	cfg := taskPolicies()
	if sandboxPolicy != "" {
		cfg.Builtin.Tools["sandbox_exec"] = sandboxPolicy
	}
	cfg.Correction.Mode = mode
	s := loopServer(t, base, cfg)
	s.workspace = dir
	return s
}

// A STALE CHECK IS RUN AGAIN AT THE SEGMENT'S END, so the next segment opens on
// a measured result: the edit after the failing run is checked by the daemon,
// passes, and the finish the model asks for next is accepted on that pass.
//
// Neuter check: drop the call to recheck in runLongTask.
func TestTheJudgeRunsAStaleCheckAgain(t *testing.T) {
	base, calls := fixThenTalk(t)
	s := recheckServer(t, base, correctionOn, PolicyAllow)
	run, p, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run.rechecks != 1 || len(p.checks) != 2 || !p.checks[1].passed {
		t.Fatalf("rechecks %d, checks %+v", run.rechecks, p.checks)
	}
	if run.ledger.State != protocol.TaskStateFinished || calls() != 4 {
		t.Errorf("state %q after %d calls; want finished after 4, on the re-check's pass", run.ledger.State, calls())
	}
	if c := run.ledger.LastCheck; c == nil || !c.Passed || !c.AfterLastEdit {
		t.Errorf("the ledger's last check: %+v", c)
	}
}

// A RE-CHECK RUNS ON THE TASK'S GRANT AND ASKS NOBODY. "Yes, for this task" to
// the command covers running it again; "yes" to one run does not -- and the
// daemon then neither runs it nor asks, so the user is never prompted for a
// call the model did not make.
//
// Neuter check: run the re-check whatever resolveExecutable decides.
func TestARecheckRunsOnlyOnTheTasksGrant(t *testing.T) {
	for _, c := range []struct {
		decision string
		rechecks int
		state    string
	}{
		{protocol.ApprovalApprove, 0, protocol.TaskStateBlocked},
		{protocol.ApprovalApproveForTurn, 1, protocol.TaskStateFinished},
	} {
		base, _ := fixThenTalk(t)
		s := recheckServer(t, base, correctionOn, "")
		appr := &fixedAnswer{decision: c.decision}
		run, _, runIt := taskRunAnswering(t, s, modeFix, appr)
		if _, err := runIt(context.Background()); err != nil {
			t.Fatal(err)
		}
		if run.rechecks != c.rechecks || appr.asked != 1 || run.ledger.State != c.state {
			t.Errorf("%s: %d re-checks, %d asks, state %q; want %d, 1, %q",
				c.decision, run.rechecks, appr.asked, run.ledger.State, c.rechecks, c.state)
		}
	}
}

// NOTHING IS RUN AGAIN THAT IS NOT STALE, AND NOTHING AT ALL OUTSIDE MODE "ON".
//
// Neuter checks: drop the stale condition; drop the mode condition.
func TestNoRecheckWhenCurrentOrNotOn(t *testing.T) {
	base, _ := fixThenTalk(t)
	s := recheckServer(t, base, correctionShadow, PolicyAllow)
	run, _, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run.rechecks != 0 {
		t.Errorf("shadow ran %d re-check(s)", run.rechecks)
	}

	current, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		textSSE("TestA fails; looking further."),
		toolCallSSE("c2", "builtin__finish_task", `{"status":"blocked","summary":"Stopping."}`),
	)
	s = recheckServer(t, current, correctionOn, PolicyAllow)
	run, _, runIt = directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run.rechecks != 0 || !strings.Contains(run.ledger.Detail, "Stopping") {
		t.Errorf("a current check was run again (%d), or the task did not end as scripted: %q", run.rechecks, run.ledger.Detail)
	}
}
