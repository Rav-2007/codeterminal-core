package main

import (
	"fmt"
	"regexp"
	"strings"
)

// THE "TIME TO ACT" NUDGE.
//
// MEASURED (docs/AGENT_WORKFLOW_EVAL.md): asked to change code, the agent
// spent its whole budget reading -- the same few files three and four times,
// repo_map twice, list_directory on folders repo_map had already shown -- and
// the turn ended having changed nothing. In the baseline, 9 of 11 failures
// proposed no edit at all. The repeated-call stall note (agentloop.go) already
// tells it when a read returned the same bytes; it re-read anyway.
//
// So once, at half the turn's model calls, a turn that was asked for a change
// and has neither edited nor run anything is told so, plainly, and told that
// what it read is already in front of it. Guarded so it cannot misfire on a
// question: it needs a change request (or a spec/build turn, which is one by
// definition), it never fires in plan or check mode (they cannot edit), and it
// fires at most once.

// changeVerbs mark a request for a change. A leading question word overrides
// them: "how do I add a flag?" asks for an explanation, not an edit.
var (
	changeVerbs  = regexp.MustCompile(`(?i)\b(fix|add|implement|create|write|rename|change|update|refactor|remove|delete|build|make|move|replace|convert|edit)\b`)
	questionLead = regexp.MustCompile(`(?i)^\s*(how|what|why|where|which|when|who|explain|describe|tell me)\b`)
)

// asksForChange reports whether a user request asks for code or files to change.
func asksForChange(request string) bool {
	return changeVerbs.MatchString(request) && !questionLead.MatchString(request)
}

// actingTools are the calls that count as having done something.
var actingTools = []string{"propose_edit", "propose_ast_edit", "sandbox_exec"}

func hasActed(toolNames []string) bool {
	for _, n := range toolNames {
		for _, a := range actingTools {
			if n == a || strings.HasSuffix(n, "__"+a) {
				return true
			}
		}
	}
	return false
}

// actNudge returns the note to add after this iteration's tool results, or ""
// for none. It marks the turn nudged when it returns one.
func actNudge(turn *agentTurn, bud budget) string {
	if turn.nudgedToAct || isPlanMode(turn.mode) || isCheckMode(turn.mode) {
		return ""
	}
	half := (bud.maxIterations + 1) / 2
	if turn.iteration < half || turn.iteration >= bud.maxIterations || hasActed(turn.toolNames) {
		return ""
	}
	if !isBuildMode(turn.mode) && !isSpecMode(turn.mode) && !asksForChange(turn.request) {
		return ""
	}
	turn.nudgedToAct = true
	return fmt.Sprintf("(A note from Mochiii, not the user.) You have used %d of this turn's %d steps "+
		"and have not changed or run anything yet. Everything you have read is already in this "+
		"conversation -- do not read it again. If the task asks for a change, make it now with "+
		"propose_edit, then run the tests with sandbox_exec. If it needs no change, answer now.",
		turn.iteration, bud.maxIterations)
}
