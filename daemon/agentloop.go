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
// THIS PHASE DELIBERATELY CANNOT ASK. Policy "ask" is treated as a refusal (see
// resolveExecutable) because the approval channel is Phase 5. That means the
// loop is shippable and testable now, while remaining unable to run anything
// the user has not written "allow" against in their own config file.

// agentTurn is one loop's mutable state.
type agentTurn struct {
	messages []chatMessage
	// toolBytes is the cumulative post-scrub, post-truncation size of every
	// tool result fed back this turn -- i.e. how much extra has left the
	// machine because of tools. Budgeted because that is the quantity a
	// privacy-positioned product should be able to bound and report.
	toolBytes int
	toolNames []string
	iteration int
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
}

// runAgentLoop drives model call -> tool dispatch -> model call until the model
// stops asking for tools, a budget bites, or something fails.
//
// onToken streams assistant text to the client exactly as the single-turn path
// does. onActivity reports each tool step so a user watching a multi-second
// loop sees what it is doing rather than a spinner.
func (s *Server) runAgentLoop(
	ctx context.Context,
	registry *mcp.Registry,
	model string,
	messages []chatMessage,
	routing providerRouting,
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
			return agentResult{FinalText: full.String(), Incomplete: stop, ToolNames: turn.toolNames}, nil
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
				FinalText:  full.String(),
				Incomplete: incompleteInfoFor(finishReason),
				ToolNames:  turn.toolNames,
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
			result := s.dispatchToolCall(ctx, registry, turn, bud, call, onActivity)
			turn.messages = append(turn.messages, result)
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

// dispatchToolCall resolves policy, runs the tool if permitted, and renders the
// result for the model. It ALWAYS returns a tool message: a refusal or a
// failure is information the model can act on, whereas silence makes it repeat
// the call.
func (s *Server) dispatchToolCall(
	ctx context.Context,
	registry *mcp.Registry,
	turn *agentTurn,
	bud budget,
	call toolCall,
	onActivity func(protocol.ToolActivity),
) chatMessage {
	name := call.Function.Name
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

	spec, reason, ok := s.resolveExecutable(ctx, registry, name)
	if !ok {
		s.count(func(c *counters) { c.toolCallsDenied.Add(1) })
		s.logger.Printf("agent: refusing %s: %s", name, reason)
		report(protocol.ToolPhaseDenied, reason, 0, 0)
		return toolResultMessage(call, reason)
	}
	activity.Server, activity.Tool = spec.Server, spec.Name

	s.count(func(c *counters) { c.toolCalls.Add(1) })
	report(protocol.ToolPhaseRunning, "", 0, 0)

	// toolsInFlight is what shutdown waits on: unlike a cut prompt, a cut tool
	// call can leave real work half-done. See WaitForDrain.
	s.toolsInFlight.Add(1)
	started := time.Now()
	result, err := registry.Call(ctx, name, json.RawMessage(call.Function.Arguments))
	elapsed := time.Since(started)
	s.toolsInFlight.Add(-1)

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
		return toolResultMessage(call, detail)
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

	if len(kinds) > 0 {
		s.logger.Printf("agent: scrub redacted %d suspected secret(s) in %s output: %s",
			len(kinds), name, strings.Join(kinds, ","))
	}

	phase := protocol.ToolPhaseSucceeded
	if result.IsError {
		phase = protocol.ToolPhaseFailed
		s.count(func(c *counters) { c.toolCallsFailed.Add(1) })
	}
	report(phase, "", emitted, elapsed)
	return toolResultMessage(call, rendered)
}

// resolveExecutable decides whether one requested call may run, returning a
// client-safe reason when it may not.
//
// "ask" is a REFUSAL in this phase. The approval channel is Phase 5, and the
// alternative -- treating an un-approvable ask as an allow -- is precisely the
// bug this whole design exists to prevent. The refusal text says so plainly, so
// a user who configured "ask" and saw nothing happen learns why.
func (s *Server) resolveExecutable(ctx context.Context, registry *mcp.Registry, qualified string) (mcp.Tool, string, bool) {
	spec, policy, err := registry.Lookup(ctx, qualified)
	if err != nil {
		// The model named something that does not exist. Told plainly so it can
		// pick a real tool rather than retry the same wrong name.
		return mcp.Tool{}, fmt.Sprintf("there is no tool called %q available in this turn", qualified), false
	}

	switch policy {
	case mcp.PolicyAllow:
		return spec, "", true
	case mcp.PolicyAsk:
		return mcp.Tool{}, fmt.Sprintf("%q needs the user's per-call approval, which this build cannot request yet. "+
			"Tell the user they can set it to \"allow\" in models.json if they want it to run without asking.", qualified), false
	default:
		return mcp.Tool{}, fmt.Sprintf("%q is not permitted by this user's configuration", qualified), false
	}
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
