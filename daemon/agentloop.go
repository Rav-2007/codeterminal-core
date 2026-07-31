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

	// grants holds the tools the user answered ApprovalApproveForTurn for,
	// keyed by QUALIFIED NAME ONLY -- not by arguments, because "allow this tool
	// for the rest of the turn" is exactly what the user chose and narrowing it
	// to the arguments they happened to see first would make the option do
	// nothing.
	//
	// It lives here, on one turn's state, and nowhere else. It is never written
	// to disk and never carried to the next turn, so a grant cannot outlive the
	// task it was given for. A durable "always allow" belongs in models.json,
	// where the user writes it themselves and can read it back later.
	grants map[string]bool
}

// grant records an approve-for-turn decision.
func (t *agentTurn) grant(qualified string) {
	if t.grants == nil {
		t.grants = map[string]bool{}
	}
	t.grants[qualified] = true
}

// budget is the resolved set of ceilings for one turn.
type budget struct {
	maxIterations    int
	deadline         time.Time
	maxResultBytes   int
	maxTotalToolByte int
}

func resolveBudget(cfg MCPBudgetConfig, now time.Time) budget {
	return budget{
		maxIterations:    cfg.resolvedMaxIterations(),
		deadline:         now.Add(cfg.resolvedTurnTimeout()),
		maxResultBytes:   cfg.resolvedMaxToolResultBytes(),
		maxTotalToolByte: cfg.resolvedMaxTotalToolBytes(),
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
func (s *Server) runAgentLoop(
	ctx context.Context,
	registry *mcp.Registry,
	model string,
	messages []chatMessage,
	routing providerRouting,
	appr approver,
	onToken func(string) error,
	onActivity func(protocol.ToolActivity),
	onProvider func(string),
	onReasoning func(string),
) (agentResult, error) {
	bud := resolveBudget(s.cfg.MCP.Budget, time.Now())
	turn := &agentTurn{messages: messages}

	tools, listErrs := s.advertisedToolSpecs(ctx, registry)
	for _, err := range listErrs {
		s.logger.Printf("agent: %v", err)
	}

	var full strings.Builder

	for turn.iteration = 1; ; turn.iteration++ {
		if stop := s.budgetStop(turn, bud); stop != nil {
			return agentResult{
				FinalText: full.String(), Incomplete: stop, ToolNames: turn.toolNames,
				ToolSignatures: turn.toolSignatures, Iterations: turn.iteration - 1,
			}, nil
		}

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
			return agentResult{}, err
		}

		// No tool calls means the model is done talking. This is the ONLY
		// normal exit, and it is the model's decision rather than ours.
		if len(calls) == 0 {
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
			result, cancelled := s.dispatchToolCall(ctx, registry, turn, &bud, call, appr, onActivity)
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
) (chatMessage, bool) {
	name := call.Function.Name
	// ONE variable, read once, used for the digest the user approves, the
	// bytes handed to the tool, and the audit record. Re-reading the call for
	// any of the three would be how "what you approved" and "what ran" drift
	// apart.
	arguments := call.Function.Arguments
	server, tool, _ := mcp.SplitQualifiedName(name)

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

	decision := s.resolveExecutable(ctx, registry, turn, *bud, call, appr)

	// Give the turn back the time the human spent deciding.
	// mcp.budget.turn_timeout_seconds bounds how long the MACHINE may work; a
	// user who paused to actually read the arguments must not have their turn
	// killed for taking the care this prompt exists to ask of them.
	bud.deadline = bud.deadline.Add(decision.waited)

	audit := auditFor(turn.iteration, name, decision.tool, decision.policy, arguments)
	audit.Source, audit.DenyCause = decision.source, decision.cause
	audit.WaitedMS = decision.waited.Milliseconds()
	if decision.tool.Server != "" {
		activity.Server, activity.Tool = decision.tool.Server, decision.tool.Name
	}

	if !decision.run {
		s.count(func(c *counters) { c.toolCallsDenied.Add(1) })
		s.logger.Printf("agent: refusing %s: %s", name, decision.reason)
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
	result, err := registry.Call(ctx, name, json.RawMessage(arguments))
	elapsed := time.Since(started)
	s.toolsInFlight.Add(-1)
	audit.DurationMS = elapsed.Milliseconds()

	if err != nil {
		s.count(func(c *counters) { c.toolCallsFailed.Add(1) })
		// Full detail to the local log; a client-safe summary to the model and
		// the user. Same split socketSafeError makes, for the same reason.
		s.logger.Printf("agent: tool %s failed: %v", name, err)
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
	remaining := bud.maxTotalToolByte - turn.toolBytes
	cap := bud.maxResultBytes
	if remaining < cap {
		cap = remaining
	}
	rendered, kinds, emitted := renderToolResult(result.Content, cap, s.noScrub())
	turn.toolBytes += emitted
	turn.toolNames = append(turn.toolNames, name)
	turn.toolSignatures = append(turn.toolSignatures, name+"("+strings.TrimSpace(arguments)+")")

	if len(kinds) > 0 {
		s.logger.Printf("agent: scrub redacted %d suspected secret(s) in %s output: %s",
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
) toolDecision {
	qualified := call.Function.Name
	spec, policy, err := registry.Lookup(ctx, qualified)
	if err != nil {
		// The model named something that does not exist. Told plainly so it can
		// pick a real tool rather than retry the same wrong name.
		return toolDecision{
			policy: mcp.PolicyDeny,
			source: auditDeniedConfig,
			cause:  denyByNoChannel,
			reason: fmt.Sprintf("there is no tool called %q available in this turn", qualified),
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

	// A grant given earlier in THIS turn. It skips the prompt and nothing else:
	// the call is still counted, still narrated, still audited.
	if turn.grants[qualified] {
		return toolDecision{tool: spec, policy: policy, source: auditTurnGrant, run: true}
	}

	arguments := call.Function.Arguments
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
		ReadOnlyHint:    spec.ReadOnlyHint,
		Destructive:     spec.Destructive,
		Iteration:       turn.iteration,
		MaxIterations:   bud.maxIterations,
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
			turn.grant(qualified)
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
func (s *Server) advertisedToolSpecs(ctx context.Context, registry *mcp.Registry) ([]toolSpec, []error) {
	tools, errs := registry.Advertised(ctx)
	specs := make([]toolSpec, 0, len(tools))
	for _, tool := range tools {
		specs = append(specs, toolSpec{
			Type: "function",
			Function: toolSpecFunction{
				Name:        tool.QualifiedName(),
				Description: tool.Description,
				Parameters:  tool.Schema,
			},
		})
	}
	return specs, errs
}

// agentModeEngaged reports whether this turn should run the loop.
//
// THREE conditions, all required, and the capability is the one that makes the
// rest safe to add: a client that never declared CapToolApproval is never sent
// a question it cannot answer, so agent mode simply does not exist for it.
func (s *Server) agentModeEngaged(hs protocol.HandshakeRequest) bool {
	return s.cfg != nil && s.cfg.MCP.Enabled && hs.HasCapability(protocol.CapToolApproval)
}
