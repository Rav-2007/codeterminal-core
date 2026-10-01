package main

import (
	"fmt"
	"strings"
	"time"

	"mochiii/protocol"
)

// LONG-TASK MODES: work that does not fit in one turn -- a debugging session, a
// bug hunt, a refactor across many files. Each runs as a long task
// (longtask.go): many segments inside one request, a durable ledger between
// them, one working copy for the whole task, and a gate on finishing.
//
// A MODE, NOT A PREAMBLE. /debug, /fix and /refactor used to be one line of
// steering text in front of the user's own words, which set no method, no
// budget and no check, and which planmode.go explains is the wrong channel for
// an instruction anyway. Here each is a system-level directive -- the method
// the work follows -- plus the task machinery that lets it run long enough to
// follow it.
const (
	modeTask     = "task"
	modeDebug    = "debug"
	modeFix      = "fix"
	modeRefactor = "refactor"
	modeHunt     = "hunt"
)

// isLongTaskMode reports whether a mode runs as a long task.
func isLongTaskMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case modeTask, modeDebug, modeFix, modeRefactor, modeHunt:
		return true
	}
	return false
}

// longTaskDirective is what a long-task mode adds to the system message: the
// mode's method, then how a long task works. The SAME text in every segment of
// a task -- it sits in the opening every segment shares with the last, which is
// what lets the provider bill that opening at its cache price.
func longTaskDirective(mode string, segmentCalls int) string {
	return modeMethod(mode) + "\n\n" + fmt.Sprintf(longTaskMechanics, segmentCalls)
}

// longTaskMechanics is how every long task works, whatever its mode.
const longTaskMechanics = "THIS IS A LONG TASK, and you work on it in segments. A segment is at most %d " +
	"steps. When one ends you are asked for a short handoff, and the next segment starts fresh from the " +
	"<task_state> block in the user message: the goal, your plan, your findings, the files you have " +
	"changed, the last check and your handoff. That block is your memory. Files you read earlier are not " +
	"kept, so read again whatever you need.\n" +
	"- Keep a short plan with update_tasks: the whole list each time, one task active.\n" +
	"- Record each fact you establish with record_finding, with its evidence: a file:line, or a " +
	"sandbox_exec command you ran in this task and what it showed. A finding without evidence is refused.\n" +
	"- Your edits apply at once to a private working copy that lasts the whole task, so build and test " +
	"there with sandbox_exec as you go. The user reviews every change once, when the task ends.\n" +
	"- When the work is complete, run the command that verifies it, then call finish_task. It is " +
	"accepted only after a passing run that came after your last edit.\n" +
	"- If you cannot go on without the user -- a decision only they can make, or something you cannot " +
	"run here -- call finish_task with status blocked and say exactly what you need."

// modeMethod is each long-task mode's method.
func modeMethod(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case modeDebug:
		return debugMethod
	case modeFix:
		return fixMethod
	case modeRefactor:
		return refactorMethod
	case modeHunt:
		return huntMethod
	}
	return taskMethod
}

const debugMethod = "THE USER WANTS A BUG FOUND AND FIXED AT ITS ROOT.\n" +
	"1. Reproduce: find or write the smallest command or test that shows the failure, run it, and record " +
	"it.\n" +
	"2. Localize: form one specific hypothesis at a time and test it, against the code and with runs (a " +
	"focused test, a temporary print). Record what each test ruled in or out, and move on only with " +
	"evidence.\n" +
	"3. Fix the cause, not the symptom, with the smallest change that does it.\n" +
	"4. Verify: the reproduction passes and the surrounding tests still pass. Remove anything temporary " +
	"you added.\n" +
	"Finish with the root cause, the fix, and the commands that prove both."

const fixMethod = "THE USER WANTS THIS FIXED. Reproduce the problem with a command or a test first. Fix it " +
	"with the smallest correct change, then run the reproduction and the related tests until they pass. " +
	"Finish with what was wrong, what you changed, and what you ran."

const refactorMethod = "THE USER WANTS A REFACTOR, AND BEHAVIOUR MUST NOT CHANGE.\n" +
	"1. Baseline: before editing, build and run the tests, and record the result. A failure that was " +
	"there before you started is not yours, but say so.\n" +
	"2. Plan the change as small steps with update_tasks.\n" +
	"3. After every step, build and run the tests, and fix what broke before the next step.\n" +
	"4. Keep public names and signatures unless the user asked you to change them, and list every one " +
	"you did change.\n" +
	"Finish only when the same build and tests pass as at the baseline."

const huntMethod = "THE USER WANTS REAL BUGS FOUND, WITH PROOF -- NOT A STYLE REVIEW.\n" +
	"1. Map the code (repo_map) and put the riskiest areas in update_tasks: input parsing and " +
	"validation, error paths, boundaries and off-by-one, concurrency, resource cleanup, and anything the " +
	"tests do not cover.\n" +
	"2. For each area, read the code, form a specific suspicion, and try to prove it -- best with a small " +
	"failing test you write in the working copy and run.\n" +
	"3. Record each CONFIRMED bug with record_finding: the file:line, and the command that shows it " +
	"failing. Never record a guess.\n" +
	"4. Do not fix anything unless the user asked you to.\n" +
	"Finish with the confirmed bugs ranked by severity, and the suspicions you could not confirm."

const taskMethod = "THE USER WANTS THIS TASK DONE END TO END. Plan it with update_tasks, carry out the steps " +
	"in order, and check each one with sandbox_exec before moving on. Finish when the plan is done and " +
	"the checks pass."

// taskBudget is one run's resolved limits.
type taskBudget struct {
	minutes      int
	usd          float64
	calls        int
	segmentCalls int
}

func (b taskBudget) duration() time.Duration { return time.Duration(b.minutes) * time.Minute }

// resolveTaskBudget applies the defaults, then the request's own numbers,
// never past the ceilings. A request may ask for more than the configured
// default: the user typing /task budget in their own terminal is choosing to
// spend it. It may never ask for more than the ceiling, which is what stops a
// client bug from turning a run into a bill.
func resolveTaskBudget(set *MCPTaskBudgetConfig, req *protocol.TaskBudget) taskBudget {
	var cfg MCPTaskBudgetConfig
	if set != nil {
		cfg = *set
	}
	b := taskBudget{
		minutes:      orDefault(cfg.MaxMinutes, defaultTaskMinutes),
		usd:          cfg.MaxUSD,
		calls:        orDefault(cfg.MaxCalls, defaultTaskCalls),
		segmentCalls: orDefault(cfg.SegmentCalls, defaultTaskSegmentCalls),
	}
	if b.usd <= 0 {
		b.usd = defaultTaskUSD
	}
	if req != nil {
		if req.Minutes > 0 {
			b.minutes = min(req.Minutes, maxTaskMinutes)
		}
		if req.USD > 0 {
			b.usd = min(req.USD, maxTaskUSD)
		}
		if req.Calls > 0 {
			b.calls = min(req.Calls, maxTaskCalls)
		}
	}
	// A segment can never be longer than the whole run.
	b.segmentCalls = min(b.segmentCalls, b.calls)
	return b
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// nonVerifyingSubcommands are commands that pass without testing anything.
// finish_task is accepted only after a passing run that checks the work, and
// `go version` passes whatever the code does.
var nonVerifyingSubcommands = map[string]bool{
	"version": true, "env": true, "help": true, "doc": true, "list": true, "--version": true, "-v": true,
}

// isVerifyingCommand reports whether a command could have checked the work: a
// build, a test, a vet or lint, a make target. The verdict is only as good as
// the command, and the user sees which one it was.
func isVerifyingCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		// A bare "make" runs the default target, which is usually the build.
		return len(fields) == 1 && fields[0] == "make"
	}
	return !nonVerifyingSubcommands[fields[1]]
}
