package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mochiii/protocol"
)

// runAgentTurn is serveConn's agent-mode branch: it owns the registry's
// lifetime for one turn, drives the loop, and emits the same terminal messages
// the single-turn path does so a client needs no new handling to see an answer.
//
// SERVERS ARE STARTED HERE, NOT AT DAEMON STARTUP, and torn down when the turn
// ends. That costs a spawn per agent turn, and buys something worth more: a
// user who configures MCP servers but does not use agent mode never has a
// third-party process running on their machine. "Configured" and "running" stay
// different states.
func (s *Server) runAgentTurn(
	ctx context.Context,
	enc *json.Encoder,
	dec *json.Decoder,
	lc *limitedConn,
	promptReq protocol.PromptRequest,
	model string,
	messages []chatMessage,
	routing providerRouting,
	full *strings.Builder,
) {
	s.count(func(c *counters) { c.agentTurns.Add(1) })

	// THE TURN'S OWN CANCELLABLE CONTEXT, and the reason it exists is the stop
	// key the clients now have (interruptTurn in clients/tui/chat.go).
	//
	// An interrupt reaches the daemon as a closed connection -- there is no
	// "cancel" message on this wire, and there could not usefully be one: the
	// daemon is not reading from this connection except while it is asking an
	// approval question. What it has instead is the write side, and a write that
	// fails is proof the client is gone. Until now that proof was thrown away:
	// the token callback returned the error and unwound the current model call,
	// but a turn stopped BETWEEN calls -- during a tool that takes half a minute
	// -- carried on regardless, ran the tool to completion, and paid for another
	// model call to narrate it to nobody. Cancelling here is what turns "this
	// write failed" into "stop the turn", because runAgentLoop already checks
	// ctx.Err() between iterations and between every tool call, and
	// dispatchToolCall passes it to the tool itself.
	ctx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	// clientGone latches the same fact for the LOG, so an interrupt is not
	// reported as a turn that failed. "context canceled" is what shutdown looks
	// like too, and an operator reading these lines should not have to guess
	// which of the two happened.
	clientGone := false

	// writeToClient is the single write path for everything this turn streams.
	// A failure here means the answer has nowhere left to go -- either the peer
	// hung up, or it has not drained a byte for a full idle timeout, and neither
	// is a state to keep working through.
	writeToClient := func(resp protocol.TokenResponse) error {
		if err := enc.Encode(resp); err != nil {
			clientGone = true
			cancelTurn()
			return err
		}
		return nil
	}

	// The turn's clock starts HERE, before any server is spawned, because this
	// is when the user's wait starts. buildRegistry below can block for up to
	// one connect_timeout_seconds against a server that starts and never
	// answers initialize, and that time used to be charged to nothing at all --
	// turn_timeout_seconds was resolved inside runAgentLoop, after connecting.
	turnStart := time.Now()

	// The consent channel for this turn, and only this turn: it reads and writes
	// the very connection the answer is streaming over, so it dies when the
	// connection does. A per-turn grant the user gives inside it cannot outlive
	// it either -- that state lives on agentTurn, not here and not on Server.
	appr := &connApprover{enc: enc, dec: dec, lc: lc, logger: s.logger}

	// One sink per turn: propose_edit files its validated edits here, and they
	// join whatever the assistant text itself produced on the Done message.
	proposals := &proposalSink{}

	registry, connectErrs := s.buildRegistry(ctx, s.logger, proposals, promptReq.Mode)
	defer func() {
		if err := registry.Close(); err != nil {
			s.logger.Printf("agent: shutting down MCP servers: %v", err)
		}
	}()

	// A server that would not start costs the user its tools, not the turn.
	// Reported the same way every other reduced subsystem is, so it reaches the
	// user before the answer rather than never.
	if len(connectErrs) > 0 {
		degraded := make([]protocol.Degradation, 0, len(connectErrs))
		for range connectErrs {
			degraded = append(degraded, protocol.Degradation{
				Component: protocol.DegradedMCPServer,
				Detail: "a configured MCP server is unavailable, so the tools it provides are " +
					"missing from this turn. The answer may decline to do something you know it can do.",
			})
		}
		if err := enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Degraded: degraded}); err != nil {
			s.logger.Printf("agent: degradation notice write error: %v", err)
			return
		}
	}

	// PLAN MODE WITHHOLDING LANE B IS SAID OUT LOUD, for the reason this file
	// already applies to every other reduced tool surface.
	//
	// DegradedToolMenuTruncated states the rule: "a tool that was dropped and a
	// tool the server never offered both show up as the model not using it. The
	// user configured that server on purpose and deserves to know which of the
	// two happened." Withholding for the mode is a third way to drop one, and it
	// began silent -- so a user who typed /plan with a filesystem server
	// configured saw an agent that inexplicably could not read their workspace,
	// and no reason anywhere. The exclusion is correct; doing it quietly is not.
	if withheld := eligibleLaneBServers(s.cfg); isPlanMode(promptReq.Mode) && withheld > 0 {
		if err := enc.Encode(protocol.TokenResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Degraded: []protocol.Degradation{{
				Component: protocol.DegradedMCPServer,
				Detail: "plan mode: this turn does not connect your configured MCP servers, so " +
					"their tools are not available. Re-send without plan mode to use them.",
			}},
		}); err != nil {
			s.logger.Printf("agent: plan-mode notice write error: %v", err)
			return
		}
	}

	// One pipeline resolution per turn, before any phase runs, so a typo in a
	// role name is reported once rather than once per phase.
	phases, unknownRoles, fromRequest := pipelineForTurn(s.cfg.MCP, promptReq.Pipeline)
	source := "mcp.pipeline"
	if fromRequest {
		source = "this request"
		names := make([]string, 0, len(phases))
		for _, p := range phases {
			names = append(names, p.Name)
		}
		s.logger.Printf("agent: %s names the phases for this turn: %v", source, names)
	}
	for _, name := range unknownRoles {
		s.logger.Printf("agent: %s names an unknown role %q; that phase is skipped", source, name)
	}
	for _, w := range pipelineWarnings(phases, source) {
		s.logger.Printf("agent: %s", w)
	}
	// The same facts, addressed to the person who chose the shape rather than to
	// whoever is tailing the log. See pipelineNotices for why a typo is always
	// reported and a deliberate config choice is not.
	if notices := pipelineNotices(phases, unknownRoles, source, fromRequest); len(notices) > 0 {
		if err := enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Degraded: notices}); err != nil {
			s.logger.Printf("agent: pipeline notice write error: %v", err)
			return
		}
	}

	run := func(
		onToken func(string) error,
		onActivity func(protocol.ToolActivity),
		onProvider func(string),
		onReasoning func(string),
		onDegraded func(protocol.Degradation),
	) (agentResult, error) {
		if len(phases) == 0 {
			// Unorchestrated: exactly the call this function has always made,
			// with a nil role meaning "no scoping, no phase prompt".
			return s.runAgentLoop(ctx, turnStart, registry, model, promptReq.Mode, messages, routing, appr,
				onToken, onActivity, onProvider, onReasoning, onDegraded, nil, nil)
		}
		return s.runOrchestrated(ctx, turnStart, registry, model, promptReq.Mode, messages, routing, appr,
			onToken, onActivity, onProvider, onReasoning, onDegraded, phases)
	}

	result, err := run(
		func(token string) error {
			full.WriteString(token)
			return writeToClient(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: token})
		},
		func(a protocol.ToolActivity) {
			// The RETURN VALUE is still dropped -- an optional notice must not
			// fail a turn the token stream is otherwise completing, exactly like
			// the provider and reasoning notices on the single-turn path. What
			// changed is that writeToClient stops the turn when the connection
			// itself is gone, and this callback is where that is worth the most:
			// it fires while tools run, which is the stretch of an agent turn
			// where nothing else writes and an interrupt would otherwise go
			// unnoticed for as long as the tool takes.
			activity := a
			_ = writeToClient(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, ToolActivity: &activity})
		},
		func(provider string) {
			s.logger.Printf("agent: model API served by provider=%q", provider)
			// Dropped for the same reason as the activity notice above: optional
			// observability must not fail a turn the token stream is completing.
			_ = writeToClient(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Provider: provider})
		},
		func(reasoning string) {
			_ = writeToClient(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Reasoning: reasoning})
		},
		func(d protocol.Degradation) {
			// Sent on the same channel and with the same best-effort discipline
			// as the connect-failure notice above: a client that cannot be told
			// its tool menu was trimmed still gets its answer.
			_ = writeToClient(protocol.TokenResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Degraded:        []protocol.Degradation{d},
			})
		},
	)
	if err != nil && clientGone && errors.Is(err, context.Canceled) {
		// THE INTERRUPT PATH, and it ends here deliberately quietly. There is
		// nobody to send a Done to (the write that proved it is why this branch
		// ran at all), and the turn is not persisted: a turn the user stopped is
		// not a turn they asked to remember, and writing a half-finished answer
		// into conversation memory would feed it back to the model on their next
		// prompt as though it had completed. The client keeps what it already
		// received and marks it as stopped itself.
		s.logger.Printf("agent: the client went away mid-turn; stopped after %s", time.Since(turnStart).Round(time.Millisecond))
		return
	}
	if err != nil {
		modelErr := asModelError(err)
		s.logger.Printf("agent: turn failed: %s", modelErr.Detail())
		// The connection is already gone if this fails, and the local log above
		// is the record that survives either way. There is nothing further to
		// try and nobody left to tell.
		_ = enc.Encode(protocol.TokenResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Done:            true,
			Error:           modelErr.Error(),
			ErrorClass:      string(modelErr.Class),
		})
		return
	}

	if result.Incomplete != nil && result.Incomplete.Reason == protocol.IncompleteAgentBudget {
		s.count(func(c *counters) { c.budgetTerminations.Add(1) })
	}
	// A user-cancelled turn is deliberately NOT counted as a budget termination:
	// the status surface would then report a ceiling problem to an operator
	// whose users are simply saying no, and send them to tune the wrong thing.

	// Two sources, one review. Blocks the model wrote as text (the pre-agent
	// format, still supported) and edits it filed through propose_edit both
	// arrive as ordinary five-gate proposals -- there is no path by which an
	// agent turn changes a file without the user seeing a diff first.
	// Rejections come only from the TEXT half. An edit filed through
	// propose_edit was structured when it arrived and never went through the
	// block parser, so it has nothing to be rejected by -- if it is bad, it is
	// bad at a gate, and the gate answers on the ApplyEditResponse.
	textBlocks, rejections := s.parseAndLogEditBlocks(result.FinalText)
	blocks := append(textBlocks, proposals.blocks...)
	// Plan mode withholds the TEXT write path too. The tool filter took away
	// propose_edit; without this, a SEARCH/REPLACE block in the model's prose
	// still reached the client as a proposal, and an auto-apply client still
	// wrote it.
	if isPlanMode(promptReq.Mode) {
		blocks, rejections = planModeWithholdEdits(blocks, rejections)
	}
	// A failed terminal write means the client has gone; the turn's real work
	// (the edit proposals, the persisted history below) is unaffected.
	_ = enc.Encode(protocol.TokenResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Done:            true,
		EditProposals:   editProposalsFromBlocks(blocks),
		EditRejections:  rejections,
		Incomplete:      result.Incomplete,
	})
	s.logger.Printf("agent: turn complete after %d tool call(s)", len(result.ToolNames))

	// Persisted as ONE user + ONE assistant turn, with a compact note of which
	// tools ran rather than what they returned. Tool output must not re-enter
	// future requests through the history path (D11), and validTurn would
	// reject a tool role anyway.
	s.persistTurn(promptReq.Prompt, result.FinalText+summariseToolActivity(result.ToolNames), result.Incomplete)
}
