package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
	"mochiii/protocol"
)

// A LONG TASK is work that does not fit in one turn: a debugging session, a bug
// hunt, a refactor across many files (taskmodes.go). It runs as ONE request made
// of many SEGMENTS, the way a /team pipeline runs its phases inside one request.
//
// EACH SEGMENT STARTS FRESH. It is an ordinary runAgentLoop whose context is the
// system message and history the task began with -- the same bytes in every
// segment, so the provider bills them at its cache price -- and one user
// message: the user's request, fenced, and the task's LEDGER (taskledger.go),
// which carries the goal, the plan, the findings with their evidence, the last
// check and the handoff one segment leaves the next.
//
// WHY NOT ONE LONG LOOP. Within a loop the context only grows: every tool result
// is appended and re-sent on every later call. A hundred-call loop would carry
// the whole of everything it ever read, at a cost that grows with the square of
// its length and a focus that thins with it. Segments keep what each call
// carries bounded; the ledger is what keeps the work continuous across them.
//
// WHAT SPANS THE SEGMENTS: the working copy (one proposalSink for the request,
// so the seventh segment builds on the third's edits and the user reviews one
// diff at the end), the approval grants (the user said "for this turn" and the
// task is one turn), and the budget.
//
// IT STOPS when finish_task is accepted, the run's budget is spent, two
// segments in a row change nothing, the provider fails, or the user stops it.
// The ledger and the working copy's diff are saved after every segment, so a
// stop -- even a closed terminal -- loses at most the segment in flight, and
// /task resume carries on from there.

// taskRun is one run of a long task: one request.
type taskRun struct {
	ledger   *taskLedger
	budget   taskBudget
	start    time.Time
	deadline time.Time
	resumed  bool
	// prior is what earlier runs spent, so the ledger's total stays right.
	prior taskSpend
	// usage is this request's tally: every call of every segment, including
	// retries and the checkpoint call at each segment's end.
	usage *usageTally
	// doneCalls is the model calls of the segments already over, segCalls the
	// running segment's, nested an investigation's inside it, wrapUps the
	// handoffs at segments' ends. COUNTED here as well as billed by the tally:
	// a provider that sends no usage must not make the call budget unlimited.
	doneCalls, segCalls, nested, wrapUps int
	// investigate runs one investigation (investigate.go); set by runLongTask.
	investigate func(ctx context.Context, question string) (string, error)
	// budgetStop is the run's budget verdict once there is one; it sticks.
	budgetStop *protocol.IncompleteInfo
	// judge compares each check with the ones before it (correction.go); nil
	// when mcp.correction.mode is "off".
	judge *correctionJudge
	// talkRetries counts the talk-only segments run again rather than
	// counted toward stuck (runLongTask).
	talkRetries int
	// What the judge's control did (correction.go): cut ends the running
	// segment and says why, stopWhy stops the task. mayRestore is whether it
	// may put code back unasked -- the checkpoint tool's policy is "allow".
	// restores counts the times it did; escalate, when set, moves the next
	// segments to a stronger model and reports whether it did.
	cut, stopWhy string
	mayRestore   bool
	restores     int
	escalate     func() bool
	// rechecks counts the stale checks the daemon ran again (recheck.go),
	// and recheckTime what they took of the run's minutes.
	rechecks    int
	recheckTime time.Duration
	// escalated is the tier the run moved to, once it did; reflections
	// counts the calls that asked the model why a failure keeps coming back.
	escalated   string
	reflections int
}

// spent is what this run has used so far.
func (r *taskRun) spent() taskSpend {
	billed, usd := r.usage.spent()
	return taskSpend{Calls: max(billed, r.doneCalls+r.segCalls+r.nested+r.wrapUps), USD: usd, Seconds: int(time.Since(r.start).Seconds())}
}

// check is the run's budget, asked before every step of every segment with
// the running segment's calls so far.
func (r *taskRun) check(segmentCalls int) *protocol.IncompleteInfo {
	r.segCalls = segmentCalls
	// FINISHED MEANS THE NEXT STEP IS THE SUMMARY, whatever the segment's
	// count: without this, a finish_task accepted on a segment's last call was
	// followed by a checkpoint call asking for a handoff nobody would read.
	if r.closed() {
		return &protocol.IncompleteInfo{Reason: protocol.IncompleteAgentBudget, Detail: "the task is " + r.ledger.State}
	}
	if r.budgetStop != nil {
		return r.budgetStop
	}
	// THE JUDGE ENDED THE SEGMENT: the code it put back, or a failure that
	// keeps coming back, is better met from the task state -- which records
	// what failed -- than from the transcript of the attempts that led there.
	if r.cut != "" || r.stopWhy != "" {
		return &protocol.IncompleteInfo{Reason: protocol.IncompleteAgentBudget, Detail: "the judge ended this segment"}
	}
	// THE CALL THAT ENDS A SEGMENT IS ONE OF THE RUN'S N, as a turn's is
	// (agentloop.go budgetStop): once the segment has made a call, a stop is
	// followed by its handoff, so the last call is kept for that. MEASURED
	// 2026-10-16 with a provider that sends no usage: an 18-call run asked 20
	// and 21 times, one uncounted handoff per segment, and said it made 18.
	reserve := 0
	if segmentCalls > 0 {
		reserve = 1
	}
	spent := r.spent()
	var why string
	switch {
	case spent.USD >= r.budget.usd:
		why = fmt.Sprintf("spent $%.2f, its budget of $%.2f", spent.USD, r.budget.usd)
	case spent.Calls+reserve >= r.budget.calls:
		why = fmt.Sprintf("reached its budget of %d model calls", r.budget.calls)
	case time.Now().After(r.deadline):
		why = fmt.Sprintf("ran for its %d minute", r.budget.minutes)
		if r.budget.minutes != 1 {
			why += "s"
		}
	default:
		// THE SESSION'S OR THE DAY'S LIMIT ENDS A RUN AS ITS OWN BUDGET DOES
		// (spend.go): the work is saved and can be resumed once the person has
		// said to go on. A long task is the largest thing a session can spend
		// on, so it is the last thing that should be outside those limits.
		if stop := r.usage.spendReached(); stop != nil {
			r.budgetStop = &protocol.IncompleteInfo{
				Reason: protocol.IncompleteAgentBudget,
				Detail: "this task's run stopped because it reached " + stop.what() + ". Everything it did is " +
					"saved. " + stop.wayOn() + " Then resume it with /task resume.",
			}
			return r.budgetStop
		}
		return nil
	}
	r.budgetStop = &protocol.IncompleteInfo{
		Reason: protocol.IncompleteAgentBudget,
		Detail: "this task's run " + why + ". Everything it did is saved: resume it with /task resume, " +
			"or give it more with /task budget.",
	}
	return r.budgetStop
}

// closed reports that the task has ended: its finish_task was accepted, as
// done or as blocked.
func (r *taskRun) closed() bool {
	st := r.ledger.State
	return st == protocol.TaskStateFinished || st == protocol.TaskStateBlocked
}

// wrapUpNote is what the call at a segment's limit asks for. A FINISHED TASK
// ASKS FOR NOTHING: its summary is already written, in finish_task, and is the
// reply (runLongTask streams it). MEASURED 2026-10-01 on the real binary: the
// call that used to ask for it again was 1 of a small fix's 5 calls, and 21%
// of the bytes it sent.
//
// The call a note asks for is made as soon as it is returned (wrapUpAtLimit),
// so it is counted here, as the tally bills it for a provider that reports
// usage: before, a provider that sent none left every handoff uncounted.
func (r *taskRun) wrapUpNote(*protocol.IncompleteInfo) string {
	note := segmentCheckpointNote
	switch {
	case r.closed(), r.cut != "", r.stopWhy != "":
		// A cut asks for nothing: the judge has said why, and the task
		// state carries it.
		return ""
	case r.budgetStop != nil:
		note = taskBudgetWrapUpNote
	}
	r.wrapUps++
	return note
}

// segmentCheckpointNote asks for the handoff the next segment starts from.
const segmentCheckpointNote = "This segment has reached its step limit and cannot call any more tools. Write " +
	"the handoff for the next segment, which starts fresh with only your task state: what you have " +
	"established and its evidence, what you changed, what is still failing or unknown, and the exact next " +
	"step. Be brief and concrete, and do not restate the plan."

// taskBudgetWrapUpNote asks for the report a run ends on when its budget does.
const taskBudgetWrapUpNote = "This task's run has reached its budget and cannot call any more tools. Write a " +
	"short report for the user: what you established, what you changed, what is left, and the next step " +
	"if they resume the task."

// Segment openings after the first. The user message ends with one of these.
const (
	taskBeginNote      = "Begin the task: make your plan with update_tasks, then start on it."
	taskContinueNote   = "Continue the task from your handoff."
	taskUnfinishedNote = "Your last segment ended without finish_task. If the work is complete, run the command " +
		"that verifies it and call finish_task; if not, continue from your handoff."
	taskResumeNote = "The user resumed this task. Continue from your handoff, and take account of anything " +
		"they added."
	taskCutNote = "The judge ended your last segment (see your handoff). Continue from the code as it is now, " +
		"and do not repeat what your task state shows failed."
)

// segmentMessages is one segment's whole context.
func (r *taskRun) segmentMessages(system string, history []chatMessage, segment int, closing string) []chatMessage {
	var msgs []chatMessage
	if system != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, history...)
	var b strings.Builder
	// The request first: it is the same in every segment, so it joins the
	// opening the provider has cached. The ledger changes every segment.
	b.WriteString(fenceUserRequest(r.ledger.Goal))
	b.WriteString("\n\n")
	b.WriteString(r.ledger.renderTaskState(r.budget, r.spent(), segment, r.correcting()))
	b.WriteString("\n\n" + closing)
	return append(msgs, chatMessage{Role: "user", Content: b.String()})
}

// progressMark is what a segment that made progress changes: a plan status, a
// finding, an edit, a check's verdict.
func (r *taskRun) progressMark(p *proposalSink) string {
	var b strings.Builder
	for _, t := range r.ledger.Plan {
		b.WriteString(t.ID + "=" + t.Status + ";")
	}
	if r.correcting() && p.stage != nil {
		fmt.Fprintf(&b, "|f%d%s", len(r.ledger.Findings), r.judge.progress(p.stage.stateHash()))
		if c := p.lastCheck(); c != nil {
			fmt.Fprintf(&b, "|c%s:%v", c.command, c.passed)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "|f%d|e%d", len(r.ledger.Findings), p.editCount())
	if c := p.lastCheck(); c != nil {
		fmt.Fprintf(&b, "|c%s:%v:%d", c.command, c.passed, c.edits)
	}
	return b.String()
}

// absorbSegment copies what the daemon knows first-hand into the ledger.
//
// THE CHECK'S OUTPUT IS CLEANED HERE, because nothing upstream of this did it.
// The sink keeps a command's output as the tool returned it, before the scrub
// every tool result gets on its way to the model -- and the ledger sends it to
// the model again in every later segment. A test that prints a token would
// otherwise reach the model through the one path that skipped the scrub.
// clean is cleanForLedger: the scrub AND the daemon's own credentials, which
// a tool result always has removed and which the scrub, switched off with
// --no-scrub, would not catch.
func (r *taskRun) absorbSegment(p *proposalSink, clean func(string) string) {
	if p.tasks != nil {
		r.ledger.Plan = append([]protocol.TaskItem(nil), p.tasks...)
	}
	if p.stage != nil {
		r.ledger.FilesChanged = changedFiles(p.stage)
	}
	if c := p.lastCheck(); c != nil {
		output := clean(p.checkOutput)
		r.ledger.LastCheck = &taskCheck{
			Command:       c.command,
			Passed:        c.passed,
			AfterLastEdit: c.edits == p.editCount(),
			Output:        truncateRunes(output, maxTaskOutputRunes),
		}
	}
}

// cleanForLedger is what a command's output, or a model's words about it, goes
// through before a ledger may hold it: the scrub every tool result gets, and
// the daemon's own live credentials, removed whether or not the scrub is on.
func (s *Server) cleanForLedger(text string) string {
	cleaned, _ := scrub(text, s.noScrub())
	cleaned, _ = s.credRedact(cleaned)
	return cleaned
}

// save writes the ledger and the working copy's diff. A failure is logged, not
// fatal: the task can go on, it just cannot be resumed from this point.
func (r *taskRun) save(p *proposalSink, logf func(string, ...any)) {
	spent := r.spent()
	r.ledger.Spent = taskSpend{
		Calls:   r.prior.Calls + spent.Calls,
		USD:     r.prior.USD + spent.USD,
		Seconds: r.prior.Seconds + spent.Seconds,
	}
	r.ledger.Updated = time.Now()
	var blocks []editapply.EditBlock
	if p.stage != nil {
		blocks, _ = p.stage.netChanges()
		if blocks == nil {
			blocks = []editapply.EditBlock{} // "no changes" is saved too
		}
	}
	if err := r.ledger.save(blocks); err != nil {
		logf("task %s: saving its state failed: %v", r.ledger.ID, err)
	}
}

// incomplete is how a run that did not finish is reported on the Done.
func (r *taskRun) incomplete() *protocol.IncompleteInfo {
	switch r.ledger.State {
	case protocol.TaskStateFinished, protocol.TaskStateBlocked:
		return nil
	case protocol.TaskStateFailed:
		return &protocol.IncompleteInfo{Reason: protocol.IncompleteProviderError, Detail: r.ledger.Detail}
	case protocol.TaskStateStopped:
		return &protocol.IncompleteInfo{Reason: protocol.IncompleteUserCancelled, Detail: r.ledger.Detail}
	}
	return &protocol.IncompleteInfo{Reason: protocol.IncompleteAgentBudget, Detail: r.ledger.Detail}
}

// persistText is what conversation memory keeps of the run: where it stands,
// not every segment's narration.
func (r *taskRun) persistText() string {
	body := r.ledger.Summary
	if body == "" {
		body = r.ledger.Handoff
	}
	return fmt.Sprintf("[Long task %s (%s), %s after %d segment(s).] %s",
		r.ledger.ID, r.ledger.Mode, r.ledger.State, r.ledger.Segments, body)
}

// runLongTask runs a long task's segments until one of its stops.
func (s *Server) runLongTask(
	ctx context.Context,
	turnStart time.Time,
	registry *mcp.Registry,
	model string,
	messages []chatMessage,
	routing providerRouting,
	appr approver,
	proposals *proposalSink,
	onToken func(string) error,
	onActivity func(protocol.ToolActivity),
	onProvider func(string),
	onReasoning func(string),
	onDegraded func(protocol.Degradation),
	onStatus func(protocol.TaskStatus),
) (agentResult, error) {
	run := proposals.task
	run.usage = usageTallyFrom(ctx)
	if mode := s.cfg.MCP.Correction.resolvedMode(); mode != correctionOff && run.judge == nil {
		run.judge = newCorrectionJudge(mode)
		run.judge.attempts = s.cfg.MCP.Correction.resolvedAttempts()
	}
	run.mayRestore = s.cfg.MCP.Builtin.policyFor("checkpoint") == PolicyAllow
	// Released the moment the runner is done, not when the request is: the
	// ledger and diff are saved by then, and a /task review sent the instant
	// the Done arrives must not find the task still claimed.
	defer s.releaseTask(run)
	system, history, _ := splitMessages(messages)
	// ONE approval map for the whole task, shared by reference with every
	// segment: "allow for this turn" was said about the task.
	grants := map[string]bool{}
	run.investigate = s.newInvestigator(run, turnStart, registry, model, system, routing, appr, grants,
		onActivity, onProvider)
	// A STRONGER MODEL, WHEN THE USER NAMED ONE (mcp.correction.escalate_to):
	// the judge's ladder moves the next segments to it when a failure keeps
	// coming back, and a provider that keeps failing moves the task to it
	// rather than ending it. Off unless named; once per run; the same
	// provider and key, because a tier is a model of the provider in use.
	if tier := s.cfg.MCP.Correction.EscalateTo; tier != "" && run.correcting() {
		if strong, strongRouting, why := s.escalationTarget(tier, model); why != "" {
			s.logger.Printf("task %s: mcp.correction.escalate_to %q is not used: %s", run.ledger.ID, tier, why)
		} else {
			run.escalate = func() bool {
				if run.escalated != "" {
					return false
				}
				model, routing, run.escalated = strong, strongRouting, tier
				run.investigate = s.newInvestigator(run, turnStart, registry, model, system, routing, appr, grants,
					onActivity, onProvider)
				s.logger.Printf("task %s: the next segments run on %s (%s)", run.ledger.ID, tier, strong)
				return true
			}
		}
	}

	closing := taskBeginNote
	if run.resumed {
		closing = taskResumeNote
		if notes := s.reapplyTaskDiff(run, proposals); len(notes) > 0 {
			detail := "some of this task's saved changes no longer apply to the project: " + strings.Join(notes, "; ")
			if onDegraded != nil {
				onDegraded(protocol.Degradation{Component: protocol.DegradedWorkingCopy, Detail: detail})
			}
			closing += " " + truncateRunes(neutralizeDelimiters(detail), 1500) + ". Check those files first."
		}
		// The plan goes on from where it stood, on screen as in the model's copy.
		if len(run.ledger.Plan) > 0 {
			proposals.tasks = append([]protocol.TaskItem(nil), run.ledger.Plan...)
			if proposals.onTasks != nil {
				proposals.onTasks(proposals.tasks)
			}
		}
	}

	var (
		combined agentResult
		text     strings.Builder
		quiet    int
		// talkOnly counts the segments in a row that ended on text alone, and
		// servedBy is the host that answered the running segment's last call:
		// together they tell a dropped tool call from a task going in circles.
		talkOnly int
		servedBy string
	)
	segmentProvider := func(provider string) {
		servedBy = provider
		if onProvider != nil {
			onProvider(provider)
		}
	}
	finish := func(state, detail string) {
		run.ledger.State, run.ledger.Detail = state, detail
	}
	// THE USER STOPPED IT (or the daemon is stopping). Nobody is left to send
	// a Done to, and that is exactly when the saved state matters most: it is
	// what /task resume carries on from.
	stopped := func(err error) (agentResult, error) {
		finish(protocol.TaskStateStopped, "you stopped the task; it is saved, and /task resume carries on")
		run.save(proposals, s.logger.Printf)
		return agentResult{FinalText: text.String(), Iterations: combined.Iterations}, err
	}

	for segment := run.ledger.Segments + 1; ; segment++ {
		// Before starting a segment, not only inside one: a stop that landed
		// while the last segment was ending must not open another.
		if err := ctx.Err(); err != nil {
			return stopped(err)
		}
		if stop := run.check(0); stop != nil {
			finish(protocol.TaskStateBudget, stop.Detail)
			break
		}
		run.ledger.Segments = segment
		onStatus(run.status(segment))
		before := run.progressMark(proposals)
		s.logger.Printf("task %s: segment %d (%s)", run.ledger.ID, segment, run.ledger.Mode)

		ledger := &turnLedger{grants: grants, segment: &segmentBudget{
			calls: run.budget.segmentCalls, deadline: run.deadline, check: run.check, wrapUp: run.wrapUpNote,
			closed: run.closed,
			// Time at an approval prompt is the user's, not the run's.
			waited: func(d time.Duration) { run.deadline = run.deadline.Add(d) },
			// What the model says before its next call, after a failed
			// check, is its own word on why the check failed (correction.go).
			said: func(text string) {
				if run.correcting() {
					run.judge.said(text)
				}
			},
		}}
		servedBy = ""
		if run.judge != nil {
			run.judge.segment = segment
		}
		res, err := s.runAgentLoop(ctx, turnStart, registry, model, run.ledger.Mode,
			s.withRulesBesideTheQuestion(run.segmentMessages(system, history, segment, closing)), routing, appr,
			onToken, onActivity, segmentProvider, onReasoning, onDegraded, nil, ledger)

		combined.ToolNames = append(combined.ToolNames, res.ToolNames...)
		combined.ToolSignatures = append(combined.ToolSignatures, res.ToolSignatures...)
		combined.Iterations += res.Iterations
		run.doneCalls, run.segCalls = run.doneCalls+res.Iterations, 0
		if res.FinalText != "" {
			if text.Len() > 0 {
				text.WriteString("\n\n")
			}
			text.WriteString(res.FinalText)
		}
		// THE BOUNDARY IS MEASURED: code edited after the last check is
		// checked again before the next segment opens on it (recheck.go).
		if err == nil && run.budgetStop == nil && (res.Incomplete == nil || res.Incomplete.Reason == protocol.IncompleteAgentBudget) {
			s.recheck(ctx, run, proposals, registry, grants)
		}
		run.absorbSegment(proposals, s.cleanForLedger)
		for _, ev := range run.flushCorrection(s.cleanForLedger) {
			s.logger.Printf("task %s: correction (%s): segment %d, check %d `%s`: %s %s",
				run.ledger.ID, run.judge.mode, ev.Segment, ev.Check, ev.Command, ev.Verdict, ev.Detail)
		}
		if err == nil {
			s.reflect(ctx, run, system, history, segment, model, routing)
		}

		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return stopped(err)
			}
			// A PROVIDER THAT KEEPS FAILING is not the end of a task that has a
			// stronger model to go to: its retries are spent, the next segment
			// runs there instead.
			if run.escalate != nil && providerTrouble(err) && run.escalate() {
				s.logger.Printf("task %s: the model provider failed (%s); the task goes on", run.ledger.ID, asModelError(err).Class)
				closing = taskContinueNote
				run.save(proposals, s.logger.Printf)
				continue
			}
			finish(protocol.TaskStateFailed, "the model provider failed: "+asModelError(err).Detail()+
				". The task is saved; /task resume carries on from here.")
			if text.Len() == 0 {
				run.save(proposals, s.logger.Printf)
				return agentResult{}, err
			}
			break
		}

		talked := false
		switch {
		case run.closed():
			// finish_task was accepted during this segment, and its summary IS
			// the reply: nothing calls the model to say it again. A client that
			// has gone misses the words, not the task -- it is saved below.
			if sum := run.ledger.Summary; sum != "" {
				if text.Len() > 0 {
					sum = "\n\n" + sum
				}
				text.WriteString(sum)
				_ = onToken(sum)
			}
		case res.Incomplete != nil && res.Incomplete.Reason == protocol.IncompleteUserCancelled:
			finish(protocol.TaskStateStopped, res.Incomplete.Detail)
		case res.Incomplete != nil && res.Incomplete.Reason == protocol.IncompleteProviderError:
			finish(protocol.TaskStateFailed, res.Incomplete.Detail)
		case run.stopWhy != "":
			finish(protocol.TaskStateStuck, run.stopWhy+". The task is saved: resume it with a hint, "+
				"for example /task resume look at the parser first.")
		case run.cut != "":
			run.ledger.setHandoff("The judge ended this segment: " + run.cut + ". " + res.FinalText)
			closing = taskCutNote
			run.cut = ""
		case run.budgetStop != nil:
			run.ledger.setHandoff(res.FinalText)
			finish(protocol.TaskStateBudget, run.budgetStop.Detail)
		case res.Incomplete != nil:
			// The segment's own limit -- its calls, a repeated step, its bytes.
			// Its last words are the checkpoint the next segment starts from.
			run.ledger.setHandoff(res.FinalText)
			closing = taskContinueNote
		default:
			// The model stopped talking without finish_task. Not the end: the
			// finish is a gate, and ending a reply is not passing it.
			run.ledger.setHandoff(res.FinalText)
			closing = taskUnfinishedNote
			talked = ledger.asked == 0
		}
		if run.ledger.State != protocol.TaskStateRunning {
			break
		}

		// A SEGMENT THAT WAS ONLY TALK MAY BE A DROPPED TOOL CALL, NOT A TASK
		// GOING IN CIRCLES. MEASURED in Round 4 (2026-10-04): two /debug and
		// /refactor trials ended stuck after two calls and no tool call at all;
		// each reply was a sentence of intent ("I'll start by understanding the
		// project structure...") and nothing else, every one from the
		// DigitalOcean host -- the host that sends the text before a tool call
		// and drops the call. The empty-reply retry in runAgentLoop never saw
		// them, because they were not empty. So the first such segment is not
		// counted and runs again where it was (the drop is intermittent, and
		// that host's cache holds the prompt -- 114397d); a second in a row
		// moves the REST OF THE TASK to another host, not counted either; a
		// third counts like any other, so a model that only talks still stops.
		retried := false
		if talked {
			talkOnly++
			switch talkOnly {
			case 1:
				retried = true
				run.talkRetries++
				s.logger.Printf("task %s: segment %d ended on text alone (served by %q); running the next on the same host",
					run.ledger.ID, segment, servedBy)
			case 2:
				retried = true
				run.talkRetries++
				routing = avoidProvider(routing, servedBy)
				run.investigate = s.newInvestigator(run, turnStart, registry, model, system, routing, appr, grants,
					onActivity, onProvider)
				s.logger.Printf("task %s: segment %d ended on text alone again (served by %q); the task moves to another host",
					run.ledger.ID, segment, servedBy)
			}
		} else {
			talkOnly = 0
		}

		// TWO SEGMENTS IN A ROW THAT CHANGE NOTHING -- no plan step moved, no
		// finding, no edit, no new verdict from a check -- is a task going in
		// circles. It stops and says so rather than spending the rest of the
		// budget on the same circle.
		switch {
		case retried:
		case run.progressMark(proposals) == before:
			quiet++
		default:
			quiet = 0
		}
		if quiet >= 2 {
			finish(protocol.TaskStateStuck, "two segments in a row moved nothing forward -- no plan step, "+
				"finding, edit or check result changed. The task is saved: resume it with a hint, "+
				"for example /task resume look at the parser first.")
			break
		}
		run.save(proposals, s.logger.Printf)
	}

	run.save(proposals, s.logger.Printf)
	onStatus(run.status(run.ledger.Segments))
	s.logger.Printf("task %s: %s after %d segment(s): %s", run.ledger.ID, run.ledger.State, run.ledger.Segments, run.ledger.Detail)

	combined.FinalText = text.String()
	combined.Incomplete = run.incomplete()
	combined.Persist = run.persistText()
	return combined, nil
}

// reapplyTaskDiff rebuilds a resumed task's working copy: a fresh copy of the
// project, with the task's saved changes applied again through the ordinary
// edit gates. It returns a note for each change that no longer applies.
//
// A change already in the project -- the user accepted it at the end of an
// earlier run -- is skipped, not applied twice.
func (s *Server) reapplyTaskDiff(run *taskRun, p *proposalSink) []string {
	blocks, err := loadTaskDiff(run.ledger.Workspace, run.ledger.ID)
	if err != nil {
		return []string{err.Error()}
	}
	if len(blocks) == 0 {
		return nil
	}
	st, err := p.workingCopy()
	if err != nil || st == nil {
		return []string{"this run has no working copy, so the saved changes were not re-applied"}
	}
	var notes []string
	for _, b := range blocks {
		rel := filepath.FromSlash(b.FilePath)
		if current, exists, err := st.readCopyFile(rel, 0); err == nil && exists {
			have := string(current)
			if (b.Search == "" && have == b.Replace) ||
				(b.Replace != "" && b.Search != "" && strings.Contains(have, b.Replace) && !strings.Contains(have, b.Search)) {
				continue
			}
		}
		if _, err := st.apply(editapply.EditBlock{FilePath: rel, Search: b.Search, Replace: b.Replace}); err != nil {
			notes = append(notes, fmt.Sprintf("%s (%v)", b.FilePath, err))
		}
	}
	return notes
}

// ---------------------------------------------------------------------------
// Opening a run, and the task requests that call no model.
// ---------------------------------------------------------------------------

// openTaskRun starts this request's run of a long task: a new task for a
// long-task mode, or the saved one the request names (goal is then the user's
// note to it). It claims the task, so the caller must releaseTask.
func (s *Server) openTaskRun(req protocol.PromptRequest, goal string, now time.Time) (*taskRun, error) {
	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return nil, err
	}
	run := &taskRun{budget: resolveTaskBudget(s.cfg.MCP.Budget.Task, req.TaskBudget), start: now}
	run.deadline = now.Add(run.budget.duration())
	if req.Task == "" {
		run.ledger = newTaskLedger(realRoot, req.Mode, goal, now)
	} else {
		l, err := loadTaskLedger(realRoot, req.Task)
		if err != nil {
			return nil, err
		}
		switch {
		case isLongTaskMode(req.Mode):
			// A hunt resumed as /fix fixes what it found.
			l.Mode = req.Mode
		case req.Mode != "" && req.Mode != modeAuto && req.Mode != modeManual:
			return nil, fmt.Errorf("a %s turn cannot resume a task", req.Mode)
		}
		if note := strings.TrimSpace(goal); note != "" && !strings.EqualFold(note, "continue") {
			l.addNote(note)
		}
		// HOW THE LAST RUN ENDED is kept before it is cleared: a run that
		// resumes a stuck task should know it was stuck, and why.
		if l.State != "" && l.State != protocol.TaskStateRunning {
			l.PriorStop = l.State
			if l.Detail != "" {
				l.PriorStop += ": " + truncateRunes(l.Detail, 300)
			}
		}
		l.State, l.Detail, l.Summary = protocol.TaskStateRunning, "", ""
		run.ledger, run.resumed, run.prior = l, true, l.Spent
	}
	if _, busy := s.runningTasks.LoadOrStore(run.ledger.ID, true); busy {
		return nil, fmt.Errorf("task %s is already running", run.ledger.ID)
	}
	run.ledger.Runs++
	return run, nil
}

func (s *Server) releaseTask(run *taskRun) {
	if run != nil && run.ledger != nil {
		s.runningTasks.Delete(run.ledger.ID)
	}
}

// validTaskRequest refuses a task field the daemon cannot read, fail-closed
// like normalizeMode: an action it does not know is not treated as a default.
func validTaskRequest(req protocol.PromptRequest) error {
	switch req.TaskAction {
	case "", protocol.TaskActionResume, protocol.TaskActionReview, protocol.TaskActionDiscard:
	default:
		return fmt.Errorf("unknown task action %q (want resume, review or discard)", req.TaskAction)
	}
	if req.Task != "" && req.Task != protocol.TaskLatest && !validTaskID(req.Task) {
		return fmt.Errorf("%q is not a task ID", req.Task)
	}
	return nil
}

// serveTaskAction answers the task requests that call no model: review sends
// a saved task's changes through the ordinary edit review, and discard deletes
// the task. It reports whether the request was one of them.
func (s *Server) serveTaskAction(enc *json.Encoder, req protocol.PromptRequest) bool {
	if req.TaskAction != protocol.TaskActionReview && req.TaskAction != protocol.TaskActionDiscard {
		return false
	}
	fail := func(err error) {
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true,
			Error: "task: " + err.Error(), ErrorClass: string(ClassInvalidRequest)})
	}
	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		fail(err)
		return true
	}
	id := req.Task
	if id == "" {
		id = protocol.TaskLatest
	}
	l, err := loadTaskLedger(realRoot, id)
	if err != nil {
		fail(err)
		return true
	}
	if _, running := s.runningTasks.Load(l.ID); running {
		fail(fmt.Errorf("task %s is running; stop it first", l.ID))
		return true
	}
	say := func(text string) {
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: text})
	}
	if req.TaskAction == protocol.TaskActionDiscard {
		if err := discardTask(realRoot, l.ID); err != nil {
			fail(err)
			return true
		}
		say(fmt.Sprintf("Task %s (%s) is discarded.", l.ID, l.Mode))
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
		return true
	}
	blocks, err := loadTaskDiff(realRoot, l.ID)
	if err != nil {
		fail(err)
		return true
	}
	files := map[string]bool{}
	for _, b := range blocks {
		files[b.FilePath] = true
	}
	text := fmt.Sprintf("Task %s (%s) is %s after %d segment(s): %d change(s) to %d file(s).",
		l.ID, l.Mode, l.State, l.Segments, len(blocks), len(files))
	if len(blocks) == 0 {
		text = fmt.Sprintf("Task %s (%s) is %s after %d segment(s), and has no changes to review.",
			l.ID, l.Mode, l.State, l.Segments)
	}
	if l.Summary != "" {
		text += "\n\n" + l.Summary
	}
	say(text)
	// What it last checked, and whether that check says anything about the
	// code as it now stands, above the review -- the line that makes accepting
	// an informed decision.
	var wc *protocol.WorkingCopyInfo
	if c := l.LastCheck; c != nil {
		// A run that passed BEFORE a later edit passed: it is stale, not failed.
		// FOUND 2026-10-01: it was reported as "FAILED", over passing output.
		wc = &protocol.WorkingCopyInfo{Checked: c.Command, Passed: c.Passed, Stale: !c.AfterLastEdit}
		if !c.Passed {
			wc.Output = c.Output
		}
	}
	st := l.status(taskBudget{}, l.Spent, l.Segments)
	_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true,
		EditProposals: editProposalsFromBlocks(blocks), WorkingCopy: wc, TaskStatus: &st})
	return true
}

// ---------------------------------------------------------------------------
// The task's own tools: record_finding and finish_task.
// ---------------------------------------------------------------------------

func (s *Server) builtinRecordFinding(ctx context.Context, raw json.RawMessage, p *proposalSink) (mcp.Result, error) {
	var args struct {
		Claim    string `json:"claim"`
		Evidence string `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if p == nil || p.task == nil {
		return toolError("no long task is running")
	}
	l := p.task.ledger
	claim, evidence := strings.TrimSpace(args.Claim), strings.TrimSpace(args.Evidence)
	if claim == "" {
		return toolError("give the claim: the fact you established")
	}
	// A FINDING IS A FACT WITH PROOF. A command counts whatever it returned --
	// a failing test IS the proof a bug exists -- but it must be one this task
	// actually ran.
	if why := s.unverifiedEvidence(ctx, p, evidence, false); why != "" {
		return toolError("not recorded: %s. Run the check with sandbox_exec and cite the exact command, "+
			"or cite a file:line that exists", why)
	}
	if len(l.Findings) >= maxTaskFindings {
		return toolError("the task already holds %d findings, its limit; the task can still finish", maxTaskFindings)
	}
	id := fmt.Sprintf("F%d", len(l.Findings)+1)
	l.Findings = append(l.Findings, taskFinding{
		ID:       id,
		Claim:    truncateRunes(claim, maxTaskClaimRunes),
		Evidence: truncateRunes(evidence, maxTaskEvidenceRunes),
	})
	return mcp.Result{Content: "Recorded " + id + "."}, nil
}

func (s *Server) builtinFinishTask(raw json.RawMessage, p *proposalSink) (mcp.Result, error) {
	var args struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if p == nil || p.task == nil {
		return toolError("no long task is running")
	}
	status := strings.ToLower(strings.TrimSpace(args.Status))
	if status == "" {
		status = "done"
	}
	if status != "done" && status != "blocked" {
		return toolError("status must be done or blocked, not %q", args.Status)
	}
	summary := strings.TrimSpace(args.Summary)
	if summary == "" {
		return toolError("give a summary: what was wrong or what you did, what you changed, and what you ran")
	}
	l := p.task.ledger
	if status == "done" {
		if why := finishRefusal(l.Mode, l, p); why != "" {
			if p.task.judging() {
				p.task.judge.noteTried("finish_task was refused: " + why)
			}
			return toolError("not finished: %s", why)
		}
		l.State, l.Detail = protocol.TaskStateFinished, ""
	} else {
		l.State, l.Detail = protocol.TaskStateBlocked, "the task needs you: "+truncateRunes(summary, 300)
	}
	l.Summary = truncateRunes(summary, maxTaskHandoffRunes)
	return mcp.Result{Content: "The task is marked " + l.State + ". Your summary is the reply the user sees; " +
		"nothing more runs."}, nil
}

// finishRefusal is THE GATE: why finish_task cannot be accepted yet, or "".
//
// "Done" means the code as it now stands was checked, so it is accepted only
// after a passing run of a command that checks something, with no edit since.
// A hunt proves its bugs with FAILING runs, recorded as findings, so it has no
// passing run to show; a debug that changed nothing must at least have recorded
// the cause it found. The user can always see which command it was.
func finishRefusal(mode string, l *taskLedger, p *proposalSink) string {
	if mode == modeHunt {
		return ""
	}
	edits := p.editCount()
	if edits == 0 {
		switch mode {
		case modeFix, modeRefactor:
			return "nothing has been changed yet, and a " + mode + " changes code. If nothing needs changing, " +
				"call finish_task with status blocked and say why"
		case modeDebug:
			if len(l.Findings) == 0 {
				return "nothing is recorded yet: record the root cause with record_finding, with its evidence"
			}
		}
		return ""
	}
	if p.stage == nil {
		// Edits proposed without a working copy (the project is too large to
		// copy): there is nowhere they could have been checked.
		return ""
	}
	c := p.lastCheck()
	switch {
	case c == nil:
		return "you have not run anything since your changes: build or test with sandbox_exec, then call " +
			"finish_task again. If nothing in this project can build or test this change, call finish_task " +
			"with status blocked and say so"
	case c.edits != edits:
		return fmt.Sprintf("you edited after your last check (`%s`): build or test again", c.command)
	case !c.passed:
		return fmt.Sprintf("the last check after your last edit (`%s`) failed: fix what it shows, or call "+
			"finish_task with status blocked", c.command)
	case !isVerifyingCommand(c.command):
		return fmt.Sprintf("`%s` does not check the work: run the build or the tests", c.command)
	}
	return ""
}

// recordFindingTool and finishTaskTool are offered in the long-task modes.
func (s *Server) recordFindingTool(p *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "record_finding",
			Description: "Record a fact you established in this task, with its evidence, so it survives into " +
				"later segments. The evidence must be verifiable: the exact sandbox_exec command you ran in this " +
				"task (a failing run proves a bug) or a file:line that exists. A finding without such evidence " +
				"is refused.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"claim":{"type":"string","description":"What you established, in one or two sentences."},
					"evidence":{"type":"string","description":"The command you ran and what it showed, or a file:line."}
				},
				"required":["claim","evidence"],
				"additionalProperties":false
			}`),
			// It writes nothing but the task's own record.
			ReadOnlyHint: true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinRecordFinding(ctx, raw, p)
		},
	}
}

func (s *Server) finishTaskTool(p *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "finish_task",
			Description: "End the task. status done is accepted only after a sandbox_exec run that passed with no " +
				"edit since (a hunt needs none: its findings are its proof). status blocked ends it when you " +
				"cannot go on without the user; say exactly what you need. Your summary is the reply the user " +
				"reads, and once the finish is accepted nothing else runs.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"status":{"type":"string","enum":["done","blocked"]},
					"summary":{"type":"string","description":"What was wrong or what you did, what you changed, and what you ran -- or, when blocked, what you need."}
				},
				"required":["status","summary"],
				"additionalProperties":false
			}`),
			ReadOnlyHint: true,
		},
		Handler: func(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinFinishTask(raw, p)
		},
	}
}
