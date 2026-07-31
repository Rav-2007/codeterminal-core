package main

import (
	"context"
	"encoding/json"
	"strings"

	"codeterminal/protocol"
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
	hsReq protocol.HandshakeRequest,
	promptReq protocol.PromptRequest,
	model string,
	messages []chatMessage,
	routing providerRouting,
	full *strings.Builder,
) {
	s.count(func(c *counters) { c.agentTurns.Add(1) })

	// One sink per turn: propose_edit files its validated edits here, and they
	// join whatever the assistant text itself produced on the Done message.
	proposals := &proposalSink{}

	registry, connectErrs := s.buildRegistry(ctx, s.logger, proposals)
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

	result, err := s.runAgentLoop(ctx, registry, model, messages, routing,
		func(token string) error {
			full.WriteString(token)
			return enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: token})
		},
		func(a protocol.ToolActivity) {
			// Optional observability: a write error here must not fail a turn
			// the token stream is otherwise completing, exactly like the
			// provider and reasoning notices on the single-turn path.
			activity := a
			enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, ToolActivity: &activity})
		},
		func(provider string) {
			s.logger.Printf("agent: model API served by provider=%q", provider)
			enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Provider: provider})
		},
		func(reasoning string) {
			enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Reasoning: reasoning})
		},
	)
	if err != nil {
		modelErr := asModelError(err)
		s.logger.Printf("agent: turn failed: %s", modelErr.Detail())
		enc.Encode(protocol.TokenResponse{
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

	// Two sources, one review. Blocks the model wrote as text (the pre-agent
	// format, still supported) and edits it filed through propose_edit both
	// arrive as ordinary five-gate proposals -- there is no path by which an
	// agent turn changes a file without the user seeing a diff first.
	blocks := append(s.parseAndLogEditBlocks(result.FinalText), proposals.blocks...)
	enc.Encode(protocol.TokenResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Done:            true,
		EditProposals:   editProposalsFromBlocks(blocks),
		Incomplete:      result.Incomplete,
	})
	s.logger.Printf("agent: turn complete after %d tool call(s)", len(result.ToolNames))

	// Persisted as ONE user + ONE assistant turn, with a compact note of which
	// tools ran rather than what they returned. Tool output must not re-enter
	// future requests through the history path (D11), and validTurn would
	// reject a tool role anyway.
	s.persistTurn(promptReq.Prompt, result.FinalText+summariseToolActivity(result.ToolNames))
	_ = hsReq
}
