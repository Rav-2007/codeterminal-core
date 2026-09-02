package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"codeterminal/daemon/mcp"
	"codeterminal/protocol"
)

// The agentic loop.
//
// BACKLOG.md deferred this for three stated reasons, and the shape here is the
// answer to them rather than a way around them:
//
//   - "removes the human-in-the-loop property the whole architecture rests on".
//     It does not. Lane A tools cannot write -- the edit tool proposes into the
//     existing diff review -- and Lane B tools require consent per call. The
//     loop can read and think; it cannot change anything without the same human
//     step that governed every edit before it existed.
//   - "highest token-burn feature". Bounded here, explicitly, by four ceilings
//     that are all config-visible, and every termination says which one bit.
//   - "must live in the daemon, not the client". It does.
//
// The loop ASKS. Policy "ask" -- which is what every tool the user has not
// written a policy for resolves to -- suspends the turn and puts the exact call
// in front of a human (see toolapproval.go). Nothing here can run a tool on the
// strength of the model wanting it to: a call runs because config says allow,
// or because a person said yes to those exact argument bytes.

// agentTurn is one loop's mutable state.
type agentTurn struct {
	messages []chatMessage
	// toolBytes is the cumulative post-scrub, post-truncation size of every
	// tool result fed back this turn -- i.e. how much extra has left the
	// machine because of tools. Budgeted because that is the quantity a
	// privacy-positioned product should be able to bound and report.
	toolBytes int
	toolNames []string
	// toolSignatures records name+arguments per executed call. Only the loop
	// eval reads it, and it earns its place there: repeating an identical call
	// is a loop's second-most-characteristic failure after not stopping, and it
	// is invisible in a name-only list (three reads of three different files
	// look the same as three reads of one).
	toolSignatures []string
	iteration      int

	// resultDigests maps an executed call's signature to the rendered bytes it
	// produced, so a later identical call can be recognised as having changed
	// nothing. Keyed on name+arguments and compared on the POST-scrub,
	// POST-truncation text -- the same bytes the model would have been sent, so
	// two calls "match" exactly when re-sending would teach the model nothing.
	//
	// One turn's state, like grants: a repeat is only a stall within the task
	// that is stalling, and carrying digests across turns would suppress a
	// legitimate re-read in the next one.
	// priorIterations is model calls made by EARLIER phases of this turn. Zero
	// for a standalone loop, so the whole-turn ceiling reduces to the per-phase
	// one and the unorchestrated path is untouched.
	priorIterations int
	// calls counts model calls this loop actually STARTED.
	//
	// Deriving it from turn.iteration does not work and the attempt undercounted
	// by one per phase: the loop counter is one ahead at a budget stop (the check
	// runs before the call) but exact at the normal exit (the call was made), so
	// the two exits disagree about what iteration means. A ledger built on the
	// wrong one let a 4-phase pipeline make 11 calls against a ceiling of 7.
	calls         int
	resultDigests map[string]string
	// repeats counts identical-call-identical-result events this turn. Reported
	// so "it went in circles" is answerable after the fact rather than a
	// suspicion.
	repeats int

	// liveQuestion records that this turn's question looked like one whose
	// answer changes over time.
	//
	// CLASSIFIED ONCE, AT THE TOP, and kept -- not recomputed at the exit. By
	// the time the loop ends, turn.messages has grown assistant turns and tool
	// results, so lastUserQuestion would be reading a different conversation
	// than the one that was classified.
	liveQuestion bool

	// nudgedForGrounding records that the loop already asked this turn's model
	// to look something up instead of hedging about it.
	//
	// ITS ONLY JOB IS TO MAKE THE NUDGE FIRE AT MOST ONCE. Without it a model
	// that hedges, is nudged, and hedges again in the same words has built a
	// loop whose exit condition is the model changing its mind -- which is the
	// one thing this heuristic cannot make it do. One try, then the answer
	// stands as the model wrote it.
	nudgedForGrounding bool

	// grants holds the tools the user answered ApprovalApproveForTurn for.
	//
	// KEYED BY QUALIFIED NAME for an ordinary tool -- not by arguments, because
	// "allow this tool for the rest of the turn" is exactly what the user chose
	// for read_file, and narrowing it to the arguments they happened to see
	// first would make the option do nothing.
	//
	// AND BY THE ARGUMENTS TOO for a tool that executes code, because there the
	// same reasoning inverts. Approving `go test ./...` for the turn is
	// reasonable -- a fix-test loop needs to re-run tests repeatedly -- but
	// under a name-only key it also authorised `make <anything>`, unseen, up to
	// max_iterations times. Anything that can steer the model steers it through
	// that grant: a hostile file read into context, a poisoned Lane B tool
	// description, crafted build output.
	//
	// Binding to the argument digest keeps the legitimate workflow whole (the
	// IDENTICAL command is still covered, so re-running the same tests never
	// re-prompts) and closes the escalation (a DIFFERENT command is a different
	// key and asks again). See grantKey.
	//
	// It lives here, on one turn's state, and nowhere else. It is never written
	// to disk and never carried to the next turn, so a grant cannot outlive the
	// task it was given for. A durable "always allow" belongs in models.json,
	// where the user writes it themselves and can read it back later.
	grants map[string]bool
	mode   string
}

// grantKey is what an approve-for-turn decision is remembered under.
//
// ONE FUNCTION FOR BOTH THE WRITE AND THE READ. A grant stored under one key and
// looked up under another is either a prompt that never stops asking or, far
// worse, one that stops asking for something the user never saw -- and those two
// bugs look identical from the code if the key is spelled out twice.
//
// The digest is over the EXACT argument bytes, the same value
// ToolApprovalRequest.ArgumentsSHA256 binds what was shown to what runs. So the
// grant covers precisely the command the user read, and nothing else.
func grantKey(spec mcp.Tool, qualified, arguments string) string {
	if !spec.ExecutesCode {
		return qualified
	}
	return qualified + "\x00" + argumentsDigest(arguments)
}

// grant records an approve-for-turn decision.
func (t *agentTurn) grant(key string) {
	if t.grants == nil {
		t.grants = map[string]bool{}
	}
	t.grants[key] = true
}

// turnLedger is the state that must span the PHASES of one turn.
//
// agentTurn is per-loop, and the orchestrator runs one loop per specialist, so
// anything tracked only on agentTurn silently resets at every phase boundary.
// For two fields that reset is a bug rather than a detail:
//
//   - toolBytes is the ceiling on how much workspace content LEAVES THE MACHINE
//     because of tools. Resetting it means an N-phase turn sends up to N times
//     the max_total_tool_bytes the user configured -- a privacy bound quietly
//     multiplied by an implementation detail the user never chose. MEASURED
//     before this existed: a 2-phase turn sent 1563 bytes against a ceiling of
//     1500.
//   - grants is the user answering "allow this tool for the rest of the turn".
//     A pipeline IS one turn, so re-asking at each phase both contradicts what
//     they chose and multiplies the approval prompts that are this design's
//     most likely reason to get switched off.
//
// A nil ledger means a standalone loop that owns its own accounting -- the
// unorchestrated path, unchanged.
type turnLedger struct {
	// toolBytes is post-scrub, post-truncation bytes already sent this turn.
	toolBytes int
	// grants is shared BY REFERENCE across phases, so it must be non-nil before
	// the first phase runs -- agentTurn.grant() allocates a fresh map when it
	// finds nil, and that fresh map would not be the ledger's.
	grants map[string]bool
	// iterations is model calls already made this turn, by earlier phases.
	// max_iterations is per phase by design, so without this the product
	// N x max_iterations is what a pipeline can actually spend and nothing
	// bounds it. See MCPBudgetConfig.MaxTurnIterations.
	iterations int

	// toolByteCap lets the ORCHESTRATOR reserve part of the turn's tool-byte
	// budget for the phase that will actually answer. Zero means "no phase cap"
	// -- the configured turn budget is the only bound, which is what an
	// unorchestrated turn always gets.
	//
	// IT MAY ONLY EVER TIGHTEN. runAgentLoop takes the minimum of this and the
	// configured ceiling, so a caller cannot use it to spend past a budget the
	// user set. That direction is the whole reason it is safe to let the
	// orchestrator write here at all.
	//
	// WHY IT EXISTS. max_total_tool_bytes is a privacy bound on the TURN, and a
	// pipeline is one turn, so every phase draws on one 128 KiB pool. MEASURED
	// live: on a cross-file question the Researcher consumed the pool and the
	// pipeline stopped at phase 2 of 4, handing the user raw partial research
	// instead of an answer. The phase whose prose IS the answer must not be the
	// one left with nothing to read.
	toolByteCap int

	// deadlineCap is the wall-clock twin of toolByteCap, and it exists because
	// the fix that produced toolByteCap was applied to ONE of the three
	// turn-wide bounds.
	//
	// max_total_tool_bytes, max_turn_iterations and turn_timeout_seconds all
	// bound the TURN, and a pipeline is one turn. Iterations were already
	// reserved by accident: max_iterations is per phase, so a pre-answer phase
	// can spend at most its own ceiling and the rest is still there. Bytes are
	// reserved on purpose, by toolByteCap. TIME WAS RESERVED BY NOTHING -- every
	// phase saw the same turnStart+turn_timeout deadline, so a slow first phase
	// could consume the entire turn and leave the answering phase a deadline
	// already in the past. That is the identical starvation the byte
	// reservation exists to prevent, in the bound most likely to bite: §14
	// measured a four-phase turn at six to seven minutes against a ten-minute
	// default ceiling.
	//
	// Zero means no phase cap. IT MAY ONLY EVER TIGHTEN -- runAgentLoop takes it
	// only when it is EARLIER than the configured deadline, for the same reason
	// the byte cap is taken as a minimum.
	deadlineCap time.Time
}

func newTurnLedger() *turnLedger {
	return &turnLedger{grants: map[string]bool{}}
}

// budget is the resolved set of ceilings for one turn.
type budget struct {
	maxIterations int
	// maxTurnIterations bounds every phase together; maxIterations bounds one.
	maxTurnIterations int
	deadline          time.Time
	// deadlineIsPhaseShare records that deadline came from a phase reservation
	// rather than from turn_timeout_seconds, so budgetStop can avoid telling the
	// user to raise a setting that is not what stopped them.
	deadlineIsPhaseShare bool
	maxResultBytes       int
	maxTotalToolByte     int
}

func resolveBudget(cfg MCPBudgetConfig, now time.Time) budget {
	return budget{
		maxIterations:     cfg.resolvedMaxIterations(),
		maxTurnIterations: cfg.resolvedMaxTurnIterations(),
		deadline:          now.Add(cfg.resolvedTurnTimeout()),
		maxResultBytes:    cfg.resolvedMaxToolResultBytes(),
		maxTotalToolByte:  cfg.resolvedMaxTotalToolBytes(),
	}
}

// agentResult is what the loop hands back to serveConn.
type agentResult struct {
	// FinalText is everything the model said across every iteration, which is
	// what gets parsed for edit blocks and persisted.
	FinalText string
	// Incomplete is set when a budget stopped the turn rather than the model
	// finishing. Not an error: the work done so far is real and the user keeps
	// it.
	Incomplete *protocol.IncompleteInfo
	ToolNames  []string
	// ToolSignatures is name+arguments per executed call, in order.
	ToolSignatures []string
	// Iterations is how many model calls the turn actually made.
	Iterations int
}

// runAgentLoop drives model call -> tool dispatch -> model call until the model
// stops asking for tools, a budget bites, or something fails.
//
// onToken streams assistant text to the client exactly as the single-turn path
// does. onActivity reports each tool step so a user watching a multi-second
// loop sees what it is doing rather than a spinner.
//
// appr is how the loop asks a human about a call whose policy is "ask". A nil
// approver means there is nobody to ask, and every such call is refused -- the
// safe direction, and the one the eval harness runs in.
//
// turnStart is WHEN THE TURN BEGAN, not when this function was called, and the
// difference is the whole reason it is a parameter. MCP servers are connected
// by the caller before the loop exists, and connecting can block for up to one
// connect_timeout_seconds against a server that starts and never answers
// initialize. Resolving the budget from time.Now() here meant that wait fell
// outside turn_timeout_seconds entirely: a user who set a 60 s turn budget
// could sit through 20 s of connect and then a full 60 s of loop. The deadline
// now covers the turn the user actually experienced.
func (s *Server) runAgentLoop(
	ctx context.Context,
	turnStart time.Time,
	registry *mcp.Registry,
	model string,
	mode string,
	messages []chatMessage,
	routing providerRouting,
	appr approver,
	onToken func(string) error,
	onActivity func(protocol.ToolActivity),
	onProvider func(string),
	onReasoning func(string),
	onDegraded func(protocol.Degradation),
	role *agentRole,
	ledger *turnLedger,
) (agentResult, error) {
	bud := resolveBudget(s.cfg.MCP.Budget, turnStart)
	// A role may tighten its own iteration ceiling but never loosen the turn's:
	// a Planner that cannot call tools has no reason to loop, and a role config
	// that could RAISE the ceiling would be a way to spend past a budget the
	// user set.
	if role != nil && role.MaxIterations > 0 && role.MaxIterations < bud.maxIterations {
		bud.maxIterations = role.MaxIterations
	}
	if ledger == nil {
		ledger = newTurnLedger()
	}
	// A phase reservation, like a role ceiling, may only TIGHTEN. Written as a
	// minimum rather than an assignment so that neither a caller bug nor a
	// future edit can turn the reservation into a way to raise the user's
	// privacy bound.
	if ledger.toolByteCap > 0 && ledger.toolByteCap < bud.maxTotalToolByte {
		bud.maxTotalToolByte = ledger.toolByteCap
	}
	// Same rule, same direction, for the turn's wall-clock. Taken only when it
	// is EARLIER than the configured deadline, so a reservation can shorten a
	// phase and can never extend the turn past what the user allowed.
	if !ledger.deadlineCap.IsZero() && ledger.deadlineCap.Before(bud.deadline) {
		bud.deadline = ledger.deadlineCap
		bud.deadlineIsPhaseShare = true
	}
	// Seeded FROM the ledger and written back TO it, so the two cross-phase
	// quantities accumulate over the whole turn instead of restarting here.
	// grants is shared by reference; toolBytes is copied and flushed on the way
	// out, which the defer does on every return path including the budget stops.
	turn := &agentTurn{
		messages:        messages,
		mode:            mode,
		toolBytes:       ledger.toolBytes,
		grants:          ledger.grants,
		priorIterations: ledger.iterations,
	}
	// turn.iteration is the loop counter and is one AHEAD of the calls actually
	// completed at every exit point (the loop increments before the budget check
	// that returns), which is why every return path reports iteration-1 or
	// iteration depending on where it left. Recording iteration-1 here is the
	// conservative reading: it never over-counts a call that was not made.
	defer func() {
		ledger.toolBytes = turn.toolBytes
		ledger.iterations = turn.priorIterations + turn.calls
	}()

	tools, excludedThirdParty, listErrs := s.advertisedToolSpecs(ctx, registry, role)
	for _, err := range listErrs {
		s.logger.Printf("agent: %v", err)
	}

	// THE SAME RULE THE ADVERTISED CAP ALREADY FOLLOWS, applied to the one path
	// that skipped it. A specialist phase never admits a Lane B tool -- see
	// agentRole.allowsTool for why the name alone cannot justify admitting one
	// -- and until this notice existed the whole of a user's configured MCP
	// surface simply vanished for the duration of a pipeline turn, with the
	// only evidence being an agent that inexplicably declined to use it.
	//
	// It is deliberately NOT phrased as a failure. The exclusion is correct;
	// what was wrong was doing it silently. A user who needs those tools has an
	// answer in the notice itself: ask without the pipeline.
	if len(excludedThirdParty) > 0 && onDegraded != nil {
		s.logger.Printf("agent: the %s phase excluded %d third-party tool(s): %s",
			role.Display, len(excludedThirdParty), strings.Join(excludedThirdParty, ", "))
		onDegraded(protocol.Degradation{
			Component: protocol.DegradedToolMenuTruncated,
			Detail: fmt.Sprintf("%d third-party tool(s) were not offered to the %s step of this task, "+
				"because a specialist step only uses this daemon's own confined tools. Ask the same "+
				"question without a pipeline to have them available.",
				len(excludedThirdParty), role.Display),
		})
	}

	// A TOOL THAT WAS DROPPED AND A TOOL THAT WAS NEVER OFFERED LOOK IDENTICAL
	// from outside, and only one of them is the user's own configuration
	// quietly not doing what they wrote. Registry.Advertised has always
	// recorded what the cap left out; until now nothing in a live turn read it,
	// so the report existed only in `mcp list`. Lowering the default to the
	// widest measured menu (5) makes this reachable in ordinary use, so it has
	// to be visible in ordinary use.
	if dropped := registry.Dropped(); len(dropped) > 0 && onDegraded != nil {
		s.logger.Printf("agent: max_advertised_tools (%d) left out %d tool(s): %s",
			s.cfg.MCP.Budget.resolvedMaxAdvertisedTools(), len(dropped), strings.Join(dropped, ", "))
		onDegraded(protocol.Degradation{
			Component: protocol.DegradedToolMenuTruncated,
			Detail: fmt.Sprintf("%d configured tool(s) were not offered to the model this turn, "+
				"because mcp.budget.max_advertised_tools is %d. A wider menu measurably makes the "+
				"model choose worse, so the limit is deliberate — raise it if you need these tools.",
				len(dropped), s.cfg.MCP.Budget.resolvedMaxAdvertisedTools()),
		})
	}

	// PRE-FLIGHT GROUNDING. A question whose answer changes over time gets one
	// paragraph of steering added to the system message BEFORE the first call,
	// costing no extra iteration. See daemon/livequestion.go: this is the cheap
	// half of the fix, and the nudge at the loop's exit is the backstop for
	// what it misses.
	// Classified BEFORE the system message is augmented, so the classifier sees
	// the conversation the user actually sent.
	webAvailable := webToolOffered(tools)
	turn.liveQuestion = webAvailable && looksLikeLiveWorldQuestion(lastUserQuestion(turn.messages))

	// The date always; the lookup directive only when the question calls for
	// one. turnStart rather than time.Now() so a turn that waited on an MCP
	// connect is still dated by when the user asked.
	augmented, steered := applyTurnContext(turn.messages, webAvailable, turnStart)
	turn.messages = augmented
	if steered {
		s.logger.Print("agent: this question looks like it depends on current facts; asking for a lookup before the answer")
	}

	var full strings.Builder

	for turn.iteration = 1; ; turn.iteration++ {
		if stop := s.budgetStop(turn, bud); stop != nil {

			return agentResult{
				FinalText: full.String(), Incomplete: stop, ToolNames: turn.toolNames,
				ToolSignatures: turn.toolSignatures, Iterations: turn.iteration - 1,
			}, nil
		}

		// Counted before the call rather than after it, so a call that fails or is
		// abandoned mid-stream is still charged: it cost the provider the same
		// either way, and a ceiling that only counts successes is one a failing
		// loop can spend past.
		turn.calls++

		finishReason := ""
		calls, err := streamWithRetry(ctx, s.apiBase, s.apiKey, model, turn.messages, tools, routing,
			func(token string) error {
				full.WriteString(token)
				return onToken(token)
			},
			onProvider,
			onReasoning,
			func(reason string) { finishReason = reason },
			s.logger,
		)
		if err != nil {
			// A FAILURE PART-WAY THROUGH IS AN INCOMPLETE TURN, NOT A VOID ONE
			// (QA gate 2026-08-01, P1-1).
			//
			// This used to return agentResult{}, throwing away everything the
			// loop had produced. The user had already WATCHED that text arrive
			// -- it streamed through onToken on its way here -- so discarding it
			// meant the edit blocks in it were never offered, the turn never
			// reached conversation memory, and the answer on screen was
			// contradicted by an error. Meanwhile budgetStop, ten lines up, keeps
			// all of it, explicitly, because "the work done so far is real and
			// the user keeps it". Both are mid-turn stops; only one of them was
			// treating the work as real.
			//
			// The first iteration is the exception: nothing has been produced,
			// so there is no work to preserve and the error is the whole story.
			// Reporting a bare failure as an "incomplete answer" would be worse
			// than reporting it as a failure.
			if full.Len() == 0 {
				return agentResult{}, err
			}
			s.logger.Printf("agent: stopping at iteration %d after a provider failure, keeping %d byte(s) of answer: %v",
				turn.iteration, full.Len(), asModelError(err).Detail())
			return agentResult{
				FinalText: full.String(),
				Incomplete: &protocol.IncompleteInfo{
					Reason: protocol.IncompleteProviderError,
					Detail: "this task stopped part-way because the model provider failed — what you " +
						"see above is everything that was done, and it is yours to keep. Ask again to continue.",
				},
				ToolNames:      turn.toolNames,
				ToolSignatures: turn.toolSignatures,
				Iterations:     turn.iteration,
			}, nil
		}

		// No tool calls means the model is done talking. This is the ONLY
		// normal exit, and it is the model's decision rather than ours.
		if len(calls) == 0 {
			// ONE EXCEPTION, AND IT IS NARROW. If the model just answered a
			// live-fact question out of a frozen memory and said so, while a
			// web tool sat unused on its menu, that is not a finished turn --
			// it is the specific failure prompts/system.txt now forbids and
			// that models produce anyway, because "I don't have real-time
			// access" is a very well-rewarded sentence. See
			// daemon/groundingnudge.go for why this is checked rather than
			// merely asked for.
			//
			// GUARDED FIVE WAYS so it can neither loop nor misfire expensively:
			// once per turn, only when a web tool was actually offered, only
			// when none was called, only on the hedge signature, and the
			// injected message itself gives the model permission to decline.
			// TWO TRIGGERS, AND THE SECOND ONE IS THE IMPORTANT ONE.
			//
			// A hedged answer says out loud that it might be stale. An
			// UNHEDGED answer to a live-fact question says nothing at all --
			// it just states something that stopped being true, in the same
			// confident voice as a correct answer, and the user has no way to
			// tell. That was the observed second failure ("who is the current
			// cm of tn" -> a three-month-stale name, no tool call, no
			// caveat), and a hedge detector could never have caught it.
			hedged := looksLikeStalenessHedge(full.String())
			unchecked := turn.liveQuestion
			if !turn.nudgedForGrounding &&
				webAvailable &&
				!webToolUsed(turn.toolNames) &&
				(hedged || unchecked) {

				turn.nudgedForGrounding = true
				if hedged {
					s.logger.Print("agent: the answer hedged about live data with a web tool unused; asking it to look instead")
				} else {
					s.logger.Print("agent: a question about current facts was answered without a lookup; asking it to check")
				}

				// ANNOUNCED TO THE USER. They watched the hedge stream to their
				// terminal a moment ago; text arriving after it with no
				// explanation reads as a glitch. Streamed through the same
				// onToken every other token uses, so it lands in order.
				if err := onToken(nudgeNotice); err != nil {
					return agentResult{}, err
				}
				full.WriteString(nudgeNotice)

				turn.messages = append(turn.messages,
					assistantToolCallMessage(full.String(), nil),
					chatMessage{Role: "user", Content: nudgeTextFor(hedged)},
				)
				continue
			}

			return agentResult{
				FinalText:      full.String(),
				Incomplete:     incompleteInfoFor(finishReason),
				ToolNames:      turn.toolNames,
				ToolSignatures: turn.toolSignatures,
				Iterations:     turn.iteration,
			}, nil
		}

		// The provider requires the assistant's own request to precede the
		// results; without it the tool messages have nothing to pair with.
		turn.messages = append(turn.messages, assistantToolCallMessage(full.String(), calls))

		for _, call := range calls {
			// Checked between every call, not just between iterations: a
			// shutdown arriving mid-batch must not start the next tool.
			if err := ctx.Err(); err != nil {
				return agentResult{}, err
			}
			result, cancelled := s.dispatchToolCall(ctx, registry, turn, &bud, call, appr, onActivity, role)
			turn.messages = append(turn.messages, result)
			if cancelled {
				// The user chose to stop at an approval prompt. Everything
				// already streamed is theirs, exactly as with a budget stop --
				// but it is reported as the deliberate act it was, never as a
				// ceiling they did not hit.
				s.logger.Print("agent: the user cancelled the turn at an approval prompt")
				return agentResult{
					FinalText: full.String(),
					Incomplete: &protocol.IncompleteInfo{
						Reason: protocol.IncompleteUserCancelled,
						Detail: "you stopped this task at a tool approval — what you see above is " +
							"everything that was done, and nothing further ran.",
					},
					ToolNames:      turn.toolNames,
					ToolSignatures: turn.toolSignatures,
					Iterations:     turn.iteration,
				}, nil
			}
		}

	}
}

// budgetStop reports which ceiling, if any, ends the turn now.
//
// Hitting a budget is NOT an error. The assistant text so far is real work the
// user watched arrive, and throwing it away to report a limit would be the
// worse outcome. Each says which ceiling bit, so "it stopped early" is always
// answerable.
func (s *Server) budgetStop(turn *agentTurn, bud budget) *protocol.IncompleteInfo {
	// Checked before the per-phase ceiling: when a pipeline runs out of turn
	// budget the honest message names the turn, not the phase. Reporting "this
	// step reached its limit" would send the user to raise max_iterations, which
	// is not the setting that stopped them.
	if turn.priorIterations+turn.calls >= bud.maxTurnIterations {
		s.logger.Printf("agent: stopping after %d model call(s) across this turn (mcp.budget.max_turn_iterations)",
			bud.maxTurnIterations)
		return &protocol.IncompleteInfo{
			Reason: protocol.IncompleteAgentBudget,
			Detail: fmt.Sprintf("this task reached its limit of %d steps for the whole turn before "+
				"finishing — what you see above is everything that was done. Ask for a narrower "+
				"step, or raise mcp.budget.max_turn_iterations.", bud.maxTurnIterations),
		}
	}
	if turn.iteration > bud.maxIterations {
		s.logger.Printf("agent: stopping after %d iterations (mcp.budget.max_iterations)", bud.maxIterations)
		return &protocol.IncompleteInfo{
			Reason: protocol.IncompleteAgentBudget,
			Detail: fmt.Sprintf("this task reached its limit of %d steps before finishing — "+
				"what you see above is everything that was done. Ask for a narrower step, "+
				"or raise mcp.budget.max_iterations.", bud.maxIterations),
		}
	}
	if time.Now().After(bud.deadline) {
		// Which deadline bit changes what the user should be told. A phase that
		// spent the slice reserved for it has not run the turn out of time --
		// the orchestrator will carry on to the next phase and this Incomplete
		// is swallowed -- so pointing at turn_timeout_seconds here would send
		// somebody to raise a setting that was never reached.
		if bud.deadlineIsPhaseShare {
			s.logger.Print("agent: stopping this step on its reserved share of the turn's time")
			return &protocol.IncompleteInfo{
				Reason: protocol.IncompleteAgentBudget,
				Detail: "this step used the time reserved for it before finishing.",
			}
		}
		s.logger.Print("agent: stopping on the turn deadline (mcp.budget.turn_timeout_seconds)")
		return &protocol.IncompleteInfo{
			Reason: protocol.IncompleteAgentBudget,
			Detail: "this task ran out of time before finishing — what you see above is everything " +
				"that was done. Ask for a narrower step, or raise mcp.budget.turn_timeout_seconds.",
		}
	}
	if turn.toolBytes >= bud.maxTotalToolByte {
		s.logger.Printf("agent: stopping after %d bytes of tool output (mcp.budget.max_total_tool_bytes)", turn.toolBytes)
		return &protocol.IncompleteInfo{
			Reason: protocol.IncompleteAgentBudget,
			Detail: "this task read as much as it is allowed to send to the model in one go — " +
				"what you see above is everything that was done. Ask for a narrower step, " +
				"or raise mcp.budget.max_total_tool_bytes.",
		}
	}
	return nil
}

// dispatchToolCall resolves consent, runs the tool if permitted, renders the
// result for the model, and records the decision in the audit log. It ALWAYS
// returns a tool message: a refusal or a failure is information the model can
// act on, whereas silence makes it repeat the call.
//
// The second return value reports that the user asked to abandon the whole
// turn, which is the caller's business rather than this function's.
func (s *Server) dispatchToolCall(
	ctx context.Context,
	registry *mcp.Registry,
	turn *agentTurn,
	bud *budget,
	call toolCall,
	appr approver,
	onActivity func(protocol.ToolActivity),
	role *agentRole,
) (chatMessage, bool) {
	name := call.Function.Name
	// ONE variable, read once, used for the digest the user approves, the
	// bytes handed to the tool, and the audit record. Re-reading the call for
	// any of the three would be how "what you approved" and "what ran" drift
	// apart.
	arguments := call.Function.Arguments
	server, tool, splitErr := mcp.SplitQualifiedName(name)
	if splitErr != nil {
		// The model named a tool WITHOUT the server prefix. MEASURED against a
		// live model: twice in one turn it asked for "search_code" rather than
		// "builtin__search_code". The call is refused either way -- Lookup does
		// not know the bare name -- but the split failure left Tool empty, so
		// the user was shown a refusal of nothing at all and could not tell
		// which tool the model had reached for.
		//
		// The raw name is what the model actually said, so it is what the user
		// is told was refused. It is model-supplied and NOT trusted: bounded
		// here so a pathological name cannot become an unbounded client-facing
		// string. (decision.reason already carries the same name into Detail on
		// the refusal path below, where it is not bounded -- a pre-existing
		// exposure noted rather than widened.)
		tool = truncateForClient(name, maxReportedToolName)
	}

	activity := protocol.ToolActivity{CallID: call.ID, Server: server, Tool: tool}
	report := func(phase, detail string, resultBytes int, dur time.Duration) {
		if onActivity == nil {
			return
		}
		a := activity
		a.Phase, a.Detail, a.ResultBytes = phase, detail, resultBytes
		a.DurationMS = dur.Milliseconds()
		onActivity(a)
	}
	report(protocol.ToolPhaseRequested, "", 0, 0)

	decision := s.resolveExecutable(ctx, registry, turn, *bud, call, appr, role)

	// Give the turn back the time the human spent deciding.
	// mcp.budget.turn_timeout_seconds bounds how long the MACHINE may work; a
	// user who paused to actually read the arguments must not have their turn
	// killed for taking the care this prompt exists to ask of them.
	bud.deadline = bud.deadline.Add(decision.waited)

	audit := auditFor(turn.iteration, turn.mode, name, decision.tool, decision.policy, arguments)
	audit.Source, audit.DenyCause = decision.source, decision.cause
	audit.WaitedMS = decision.waited.Milliseconds()
	if decision.tool.Server != "" {
		activity.Server, activity.Tool = decision.tool.Server, decision.tool.Name
	}

	if !decision.run {
		s.count(func(c *counters) { c.toolCallsDenied.Add(1) })
		// %q, not %s, on every model-supplied name below. The model can name a
		// tool that does not exist, so this string reaches the log without any
		// server having had to offer it -- and the log is read in a terminal.
		s.logger.Printf("agent: refusing %q: %s", name, decision.reason)
		report(protocol.ToolPhaseDenied, decision.reason, 0, 0)
		audit.Outcome = auditOutcomeRefused
		if decision.cancel {
			audit.Outcome = auditOutcomeCancel
		}
		s.toolAudit.record(audit)
		return toolResultMessage(call, decision.reason), decision.cancel
	}

	s.count(func(c *counters) { c.toolCalls.Add(1) })
	// "Approved" means a person said so. A config-allow call is permitted, not
	// approved, and conflating the two would let the counter and the activity
	// stream both overstate how much a human actually saw.
	if decision.source != auditConfigAllow {
		s.count(func(c *counters) { c.toolCallsApproved.Add(1) })
		report(protocol.ToolPhaseApproved, "", 0, 0)
	}
	report(protocol.ToolPhaseRunning, "", 0, 0)

	// toolsInFlight is what shutdown waits on: unlike a cut prompt, a cut tool
	// call can leave real work half-done. See WaitForDrain.
	s.toolsInFlight.Add(1)
	started := time.Now()
	// DISPATCH ON THE RESOLVED NAME, not on what the model typed. decision.tool
	// is what Lookup returned and what every permission check above was decided
	// against, so its qualified name is the only spelling that is guaranteed to
	// mean the same thing as the approval that was just granted.
	//
	// Using the raw name here was a real bug, caught by the test that a bare
	// built-in name actually RUNS: the call resolved for policy, was approved,
	// reported "running", and then failed in dispatch with "not a qualified
	// tool name". It failed closed, but it failed.
	result, err := registry.Call(ctx, decision.tool.QualifiedName(), json.RawMessage(arguments))
	elapsed := time.Since(started)
	s.toolsInFlight.Add(-1)
	audit.DurationMS = elapsed.Milliseconds()

	if err != nil {
		s.count(func(c *counters) { c.toolCallsFailed.Add(1) })
		// Full detail to the local log; a client-safe summary to the model and
		// the user. Same split socketSafeError makes, for the same reason.
		s.logger.Printf("agent: tool %q failed: %v", decision.tool.QualifiedName(), err)
		detail := "the tool failed to run"
		if errors.Is(err, mcp.ErrServerUnavailable) {
			detail = "the tool's server is unavailable"
		}
		report(protocol.ToolPhaseFailed, detail, 0, elapsed)
		audit.Outcome = auditOutcomeError
		s.toolAudit.record(audit)
		return toolResultMessage(call, detail), false
	}

	// THE EGRESS BOUNDARY. Everything below this line has been scrubbed and
	// size-capped; nothing above it has.
	// THE CAP MUST NOT INVERT INTO "NO LIMIT" WHEN THE BUDGET RUNS OUT.
	//
	// This was `remaining := maxTotalToolByte - toolBytes; cap = min(cap,
	// remaining)`, with no floor. Once the turn's cumulative budget was spent
	// `remaining` went NEGATIVE, and renderToolResult only truncates when
	// maxBytes > 0 -- so at the exact moment the ceiling was reached, truncation
	// switched OFF and the full untruncated result went to the model.
	//
	// MEASURED, six 1 MB results in one iteration on shipped defaults
	// (max_total_tool_bytes=131072, max_tool_result_bytes=32768):
	//
	//	call 4: cap=32567     emitted=32634
	//	call 5: cap=-67       emitted=1000000   <== cap bypassed
	//	call 6: cap=-1000067  emitted=1000000   <== cap bypassed
	//	total sent this single iteration: 2,131,139 against a budget of 131,072
	//
	// A 16x overrun of the quantity toolresult.go's own header calls the one
	// "a privacy-positioned product should be able to bound and report".
	//
	// budgetStop cannot cover this: it runs BETWEEN iterations, and one
	// iteration may contain many tool calls -- the overrun happens inside the
	// batch.
	//
	// TWO CONVENTIONS, INDIVIDUALLY REASONABLE AND JOINTLY UNSAFE: a clamp
	// written as min() without a floor, and a sentinel where <=0 means
	// "unlimited". The same collision, in a different field, is why
	// turnLedger.toolByteCap is floored (see orchestrator.go). Fixed here by
	// flooring the remainder at zero and treating an exhausted budget as its own
	// case rather than as a cap value.
	remaining := bud.maxTotalToolByte - turn.toolBytes
	if remaining < 0 {
		remaining = 0
	}
	cap := bud.maxResultBytes
	if remaining < cap {
		cap = remaining
	}
	if cap <= 0 {
		// EXHAUSTED, so nothing of this result may be sent -- but the model is
		// told, because a tool that silently returns nothing reads as a broken
		// tool and gets retried. The note is the same shape budgetStop uses:
		// name the ceiling that bit so "it stopped early" is answerable.
		s.logger.Printf("agent: %s ran, but the turn's tool-output budget is spent; its output was not sent", name)
		report(protocol.ToolPhaseSucceeded, "", 0, elapsed)
		audit.Outcome = auditOutcomeOK
		s.toolAudit.record(audit)
		return toolResultMessage(call, "this task has already sent as much tool output as it is "+
			"allowed to send to the model in one turn, so this result was withheld. Answer from what "+
			"you have, or ask for a narrower step."), false
	}
	// THE THIRD-PARTY ENVELOPE, AND ITS COST TAKEN OUT OF THE CAP FIRST.
	//
	// A Lane B result is wrapped in <lane_b_output server="..."> after
	// rendering (see frameLaneBOutput for why after, and not before). Those
	// wrapper bytes are model-facing text like any other, so they are reserved
	// from the cap BEFORE the render rather than added to the total after it.
	//
	// Adding them afterwards would have been three characters shorter and would
	// have put the turn a few dozen bytes over max_total_tool_bytes on every
	// third-party call. This file already carries the postmortem of an egress
	// cap that stopped binding (the 16x overrun above); a new, small, permanent
	// overrun in the same budget is not a rounding error, it is the same bug
	// with a smaller constant.
	frameOverhead := 0
	if decision.tool.Lane == protocol.LaneThirdParty {
		frameOverhead = len(frameLaneBOutput(decision.tool.Server, ""))
	}
	renderCap := cap - frameOverhead
	if renderCap < 0 {
		renderCap = 0
	}
	rendered, kinds, emitted := renderToolResult(result.Content, renderCap, s.noScrub(), result.PreNeutralized)
	if decision.tool.Lane == protocol.LaneThirdParty {
		rendered = frameLaneBOutput(decision.tool.Server, rendered)
		emitted = len(rendered)
	}

	// THE REPEATED-CALL STALL.
	//
	// A loop that asks for the same tool with the same arguments and gets the
	// same bytes back is stuck, and this file already said so: toolSignatures'
	// comment calls it "a loop's second-most-characteristic failure after not
	// stopping". It was recorded and never acted on, so a stalled turn paid full
	// price for every repeat -- an iteration off max_iterations, and the whole
	// result off max_total_tool_bytes, to learn nothing.
	//
	// WHAT THIS DOES NOT DO: skip the call. The tool still runs, so side effects
	// still happen and semantics are unchanged. Re-reading a file after an edit
	// is a legitimate repeat and must keep working; only a repeat that produced
	// BYTE-IDENTICAL output is treated as a stall, which is a fact about the
	// result rather than a guess about intent.
	//
	// What it saves is the expensive half: feeding the same payload to the model
	// a second time. The note below is a few dozen bytes instead of a few
	// thousand, and it tells the model plainly that it is repeating itself --
	// which is the thing that actually breaks the stall.
	sig := name + "(" + strings.TrimSpace(arguments) + ")"
	if prior, seen := turn.resultDigests[sig]; seen && prior == rendered {
		turn.repeats++
		s.logger.Printf("agent: %q repeated with identical arguments and identical output; "+
			"feeding a stall note instead of %d byte(s) (repeat %d this turn)", name, emitted, turn.repeats)
		rendered = fmt.Sprintf("This is the same call you already made this turn (%s), and it "+
			"returned exactly the same result. Nothing has changed. Use what you already have, "+
			"or do something different.", name)
		emitted = len(rendered)
	} else {
		if turn.resultDigests == nil {
			turn.resultDigests = map[string]string{}
		}
		turn.resultDigests[sig] = rendered
	}

	turn.toolBytes += emitted
	turn.toolNames = append(turn.toolNames, name)
	turn.toolSignatures = append(turn.toolSignatures, sig)

	if len(kinds) > 0 {
		s.logger.Printf("agent: scrub redacted %d suspected secret(s) in %q output: %s",
			len(kinds), name, strings.Join(kinds, ","))
	}

	phase := protocol.ToolPhaseSucceeded
	audit.Outcome = auditOutcomeOK
	if result.IsError {
		phase = protocol.ToolPhaseFailed
		audit.Outcome = auditOutcomeError
		s.count(func(c *counters) { c.toolCallsFailed.Add(1) })
	}
	report(phase, "", emitted, elapsed)
	audit.ResultBytes = emitted
	s.toolAudit.record(audit)
	return toolResultMessage(call, rendered), false
}

// toolDecision is the outcome of resolving one requested call: against config
// policy first, and then -- where policy says ask -- against the user.
type toolDecision struct {
	tool   mcp.Tool
	policy mcp.Policy
	source string // an audit* constant: how this call came to run, or not
	cause  string // a denyBy* constant, for the audit; empty when it runs
	reason string // client-safe refusal text, fed to the model; empty when it runs
	run    bool
	cancel bool          // the user abandoned the whole turn
	waited time.Duration // human deliberation, excluded from the turn deadline
}

// resolveExecutable decides whether one requested call may run.
//
// Three ways a call can be permitted and they are deliberately distinguishable
// afterwards: the user's config said allow, the user said yes to these exact
// argument bytes, or the user said yes to this tool earlier in this same turn.
// Everything else is a refusal, including every failure of the asking machinery
// itself -- see toolapproval.go for why "we could not ask" resolves to "no".
func (s *Server) resolveExecutable(
	ctx context.Context,
	registry *mcp.Registry,
	turn *agentTurn,
	bud budget,
	call toolCall,
	appr approver,
	role *agentRole,
) toolDecision {
	// ONE VARIABLE carries the resolved name into all four things that decide
	// whether this call may run: Lookup, the role allowlist, the policy, and the
	// per-turn grant ledger. That is deliberate and load-bearing. Accepting a
	// bare name and then checking permission against the RAW string is how a
	// naming convenience turns into a policy bypass, and keeping the resolved
	// name in a single variable is what makes it structurally impossible here.
	//
	// The human is unaffected by design: the approval prompt below is built from
	// spec.Server and spec.Name, so it has always shown the canonical name. What
	// is approved and what is dispatched stay the same string.
	qualified, aliased := registry.Canonicalize(call.Function.Name)
	if aliased {
		// Logged rather than silent. The model asked for one string and another
		// ran; that is exactly the kind of rewrite a reader of this log needs to
		// see, even though it is safe.
		s.logger.Printf("agent: resolving unqualified %q to the built-in %q", call.Function.Name, qualified)
		s.count(func(c *counters) { c.toolNamesCanonicalized.Add(1) })
	}

	spec, policy, err := registry.Lookup(ctx, qualified)
	if err != nil {
		// The model named something that does not exist. Told plainly so it can
		// pick a real tool rather than retry the same wrong name.
		reason := fmt.Sprintf("there is no tool called %q available in this turn", qualified)
		if !strings.Contains(qualified, mcp.QualifiedNameSeparator) {
			// A bare name that Canonicalize did not resolve is not a built-in,
			// so it must be some server's tool and the model has to say which.
			// Told HERE rather than left to a second wasted iteration -- this
			// is the residue of the same tax Canonicalize exists to remove.
			reason += fmt.Sprintf(", and %q is not one of this daemon's built-in tools. "+
				"Tools on other servers must be named %s%s%s.",
				qualified, "<server>", mcp.QualifiedNameSeparator, "<tool>")
		}
		return toolDecision{
			policy: mcp.PolicyDeny,
			source: auditDeniedConfig,
			cause:  denyByNoChannel,
			reason: reason,
		}
	}

	// THE ENFORCEMENT HALF of plan mode, added for the same reason the role
	// check below has one, and absent until now.
	//
	// Plan mode had exactly one enforcement point: builtinTools declined to put
	// the tool in the registry, so Lookup failed and the call died there. For
	// the BUILT-INS that is structurally sufficient -- there is no second way to
	// obtain one. It is not sufficient as a design, and the asymmetry was the
	// tell: role scoping filters the menu AND checks at dispatch, on the
	// reasoning written three lines below, while plan mode filtered the menu and
	// stopped. Any future path that assembles a registry without consulting
	// mode -- which is precisely the bug being fixed in this batch, where Lane B
	// did exactly that -- reopens the hole with nothing behind it.
	//
	// Checking `turn.mode` here also gives that field a job. It existed already
	// and was read in exactly one place, to stamp the audit record: the daemon
	// recorded which mode a tool ran under without ever letting the mode decide
	// whether it could.
	if isPlanMode(turn.mode) && (planModeDenies(spec) || spec.Lane != protocol.LaneFirstParty) {
		return toolDecision{
			tool:   spec,
			policy: mcp.PolicyDeny,
			source: auditDeniedConfig,
			cause:  denyByNoChannel,
			reason: fmt.Sprintf("%q is not available in plan mode", qualified),
		}
	}

	// THE ENFORCEMENT HALF of role scoping. The menu this phase was shown
	// already excluded the tool, so reaching here means the model named
	// something it was not offered -- which is precisely the shape a
	// prompt-injected instruction takes, and precisely why a filtered menu
	// alone would not be a control.
	if !role.allowsTool(spec) {
		return toolDecision{
			tool:   spec,
			policy: mcp.PolicyDeny,
			source: auditDeniedConfig,
			cause:  denyByNoChannel,
			reason: fmt.Sprintf("%q is not available to the %s step of this task", qualified, role.Display),
		}
	}

	if policy == mcp.PolicyAllow {
		return toolDecision{tool: spec, policy: policy, source: auditConfigAllow, run: true}
	}
	if policy != mcp.PolicyAsk {
		return toolDecision{
			tool:   spec,
			policy: mcp.PolicyDeny,
			source: auditDeniedConfig,
			cause:  denyByNoChannel,
			reason: fmt.Sprintf("%q is not permitted by this user's configuration", qualified),
		}
	}

	// Hoisted above the grant check: for a tool that executes code the grant is
	// keyed on these bytes, so they have to exist before it is consulted.
	arguments := call.Function.Arguments

	// A grant given earlier in THIS turn. It skips the prompt and nothing else:
	// the call is still counted, still narrated, still audited.
	if turn.grants[grantKey(spec, qualified, arguments)] {
		return toolDecision{tool: spec, policy: policy, source: auditTurnGrant, run: true}
	}

	answer := askApproval(ctx, appr, protocol.ToolApprovalRequest{
		CallID:    call.ID,
		Server:    spec.Server,
		Tool:      spec.Name,
		Arguments: arguments,
		// The binding between what is shown and what runs. Re-checked against
		// the client's echo before anything is dispatched.
		ArgumentsSHA256: argumentsDigest(arguments),
		Lane:            spec.Lane,
		Confined:        spec.Confined,
		ReachesNetwork:  spec.ReachesNetwork,
		ReadOnlyHint:    spec.ReadOnlyHint,
		Destructive:     spec.Destructive,
		Iteration:       turn.iteration,
		MaxIterations:   bud.maxIterations,
		// THE TOOL'S OWN DESCRIPTION, at the moment of consent.
		//
		// It was written and never shown. sandbox_exec's description is the one
		// place the truth about it is stated -- "Confined by bwrap or docker
		// WHEN ONE IS INSTALLED; otherwise it runs with your full privileges",
		// and that approving a call approves whatever the project's build files
		// do -- and ToolApprovalRequest carried no field for it, so the honest
		// sentence was unreachable exactly where it mattered. A Lane B server's
		// description arrives here too; it is untrusted text and the clients
		// render it as the server's claim, which is what Detail already means.
		Detail: spec.Description,
	})

	source, _ := auditSourceFor(answer)
	decision := toolDecision{
		tool:   spec,
		policy: policy,
		source: source,
		cause:  answer.Cause,
		waited: answer.Waited,
	}

	switch {
	case answer.Decision == protocol.ApprovalCancelTurn:
		decision.cancel = true
		decision.reason = "the user stopped this task rather than approving the call"
	case answer.approved():
		decision.run = true
		if answer.Decision == protocol.ApprovalApproveForTurn {
			turn.grant(grantKey(spec, qualified, arguments))
		}
	case answer.Cause == denyByUser:
		decision.reason = fmt.Sprintf("the user declined to run %q. Do not ask again for the same thing — "+
			"explain what you would have done, or take a different route.", qualified)
	default:
		// Timeout, a garbled answer, an answer to a different question, or no
		// approval channel at all. The model is told the truth: nobody said yes.
		decision.reason = fmt.Sprintf("%q needs the user's approval and no valid answer came back, "+
			"so it was not run.", qualified)
	}
	return decision
}

// askApproval is the nil-safe front door to the approver. A nil approver is not
// an error condition -- it is the ordinary state of every caller that has no
// client on the other end, such as the loop eval -- and it means no.
func askApproval(ctx context.Context, appr approver, req protocol.ToolApprovalRequest) approvalDecision {
	if appr == nil {
		return denied(denyByNoChannel, 0)
	}
	return appr.Ask(ctx, req)
}

// advertisedToolSpecs converts the registry's tools into the provider's wire
// shape.
// role, when non-nil, narrows the menu to that specialist's tools. This is the
// HINT half of the two-part scoping -- resolveExecutable enforces the same
// allowlist on the way in, because a menu the model chose not to read is not a
// control. See agentRole.Tools.
//
// excludedThirdParty names the Lane B tools the role filter removed, because a
// pipeline phase never admits one (agentRole.allowsTool). The caller reports
// them, for the reason DegradedToolMenuTruncated already states about the
// advertised cap: a tool that was dropped and a tool that was never offered are
// indistinguishable from outside, and only one of them is the user's own
// configuration quietly not applying. It is nil for the unorchestrated agent,
// which excludes nothing.
func (s *Server) advertisedToolSpecs(ctx context.Context, registry *mcp.Registry, role *agentRole) (specs []toolSpec, excludedThirdParty []string, errs []error) {
	tools, errs := registry.Advertised(ctx)
	specs = make([]toolSpec, 0, len(tools))
	for _, tool := range tools {
		if !role.allowsTool(tool) {
			if role != nil && tool.Lane != protocol.LaneFirstParty {
				excludedThirdParty = append(excludedThirdParty, tool.QualifiedName())
			}
			continue
		}
		specs = append(specs, toolSpec{
			Type: "function",
			Function: toolSpecFunction{
				Name:        tool.QualifiedName(),
				Description: tool.Description,
				Parameters:  tool.Schema,
			},
		})
	}
	return specs, excludedThirdParty, errs
}

// agentModeEngaged reports whether this turn should run the loop.
//
// THREE conditions, all required, and the capability is the one that makes the
// rest safe to add: a client that never declared CapToolApproval is never sent
// a question it cannot answer, so agent mode simply does not exist for it.
func (s *Server) agentModeEngaged(hs protocol.HandshakeRequest) bool {
	return s.cfg != nil && s.cfg.MCP.Enabled && hs.HasCapability(protocol.CapToolApproval)
}

// maxReportedToolName bounds a model-supplied tool name that is echoed back to
// the client. Real qualified names are far shorter; the cap exists so an
// unusable name cannot also be an unbounded one.
const maxReportedToolName = 128

// truncateForClient bounds a model-supplied string that is about to be shown to
// a user, marking the cut rather than silently shortening it.
func truncateForClient(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
