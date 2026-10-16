package main

import (
	"context"
	"time"

	"mochiii/daemon/mcp"
)

// THE JUDGE'S OWN SENSOR. A segment that edited the code after its last check
// ends on code nobody measured, and the next segment would open on the old
// result -- or on the model's own word for how the edits went. So at that
// boundary the daemon runs the last check again: the same command, the same
// argument bytes, in the same working copy. Every attempt is then measured,
// not only the ones the model chose to measure; Reflexion's ablation is why
// that matters -- reflection with no grounded check scored below no
// reflection at all (0.52 against 0.60).
//
// IT ASKS NOBODY AND RUNS NOTHING NEW. The call goes through resolveExecutable
// with no approver, so it runs only where a call needs no one to say yes: the
// user's config "allow" for sandbox_exec, or the grant they gave THIS task for
// exactly these bytes (grantKey binds the arguments for a tool that runs
// code). A command the user approved once, not for the task, is not run again
// -- and no prompt is raised, because the approver that would raise it is
// absent. It never runs in place: resolveExecutable asks about any command
// that would run in the real project, and with no approver that is a no.
//
// IT IS BOUNDED. At most one run a segment, never in a run with less than a
// minute left, and all of them together within a quarter of the run's minutes:
// a test suite that takes five minutes must not spend a half-hour run on
// checks the model did not ask for.
const (
	recheckShare   = 4
	recheckMinLeft = time.Minute
	recheckMinRun  = 10 * time.Second
)

// recheck runs the task's last check again when edits came after it, and
// reports whether it ran. Mode "on" only. What it finds is judged like any
// check (recordCheck): its verdict reaches the next segment through the
// ledger, and a restore it calls for is done before that segment opens.
func (s *Server) recheck(ctx context.Context, run *taskRun, p *proposalSink, registry *mcp.Registry, grants map[string]bool) bool {
	c := p.lastCheck()
	if !run.correcting() || run.closed() || p.stage == nil || c == nil || c.edits == p.editCount() || len(c.args) == 0 {
		return false
	}
	left := time.Until(run.deadline)
	allowance := time.Duration(run.budget.minutes)*time.Minute/recheckShare - run.recheckTime
	if left < recheckMinLeft || allowance < recheckMinRun {
		return false
	}
	qualified := mcp.BuiltinServerName + mcp.QualifiedNameSeparator + "sandbox_exec"
	call := toolCall{ID: "judge-recheck", Type: "function", Function: toolCallFunction{Name: qualified, Arguments: string(c.args)}}
	turn := &agentTurn{mode: run.ledger.Mode, grants: grants}
	decision := s.resolveExecutable(ctx, registry, turn, budget{}, call, nil, nil)
	if !decision.run {
		s.logger.Printf("task %s: the last check is stale, and was not run again: %s", run.ledger.ID, decision.reason)
		return false
	}

	limit := min(left, allowance)
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	start := time.Now()
	res, err := registry.Call(runCtx, qualified, c.args)
	elapsed := time.Since(start)
	run.recheckTime += elapsed
	run.rechecks++

	audit := auditFor(0, run.ledger.Mode, qualified, decision.tool, decision.policy, string(c.args))
	audit.Source, audit.Initiator = decision.source, auditInitiatorJudge
	audit.DurationMS = elapsed.Milliseconds()
	audit.Outcome = auditOutcomeOK
	if err != nil || res.IsError {
		audit.Outcome = auditOutcomeError
	}
	s.toolAudit.record(audit)
	s.logger.Printf("task %s: ran the stale check `%s` again (%s)", run.ledger.ID, c.command, elapsed.Round(time.Second))
	return true
}
