package main

import (
	"context"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// THE LINK NEUTERING FOUND MISSING.
//
// LaunchesSubprocess drove the Confined stamp and plan-mode denial from the day
// it was added, and reached no client, so the consent screen could say "not
// sandboxed" about a go-to-definition call with no way to say why. Adding the
// wire field and the two renderers is not enough on its own: deleting the one
// line in agentloop.go that copies spec.LaunchesSubprocess into the request
// still compiled, still passed the protocol test (which marshals a hand-built
// struct) and still passed both client tests (which render a hand-built
// struct). Every piece was green and the flag was false in production.
//
// This is the assertion over the join: what the registry knows about a tool
// must be what the human is asked about it.
func TestApprovalRequestCarriesTheToolsCapabilities(t *testing.T) {
	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{Enabled: true}}

	for _, name := range []string{"query_compiler_definition", "query_compiler_references"} {
		spec := builtinSpec(t, s, name)
		if !spec.LaunchesSubprocess {
			t.Fatalf("%s no longer declares LaunchesSubprocess; this test is guarding nothing "+
				"and the fixture must be changed to a tool that does", name)
		}

		registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
		t.Cleanup(func() { _ = registry.Close() })

		appr := &recordingApprover{answer: protocol.ApprovalDeny}
		call := toolCall{
			Function: toolCallFunction{
				Name:      mcp.BuiltinServerName + "__" + name,
				Arguments: `{"symbol":"x"}`,
			},
		}
		s.resolveExecutable(context.Background(), registry,
			&agentTurn{grants: map[string]bool{}}, budget{maxIterations: 4}, call, appr, nil)

		if len(appr.requests) == 0 {
			t.Fatalf("%s was resolved without asking anyone; policy must be \"ask\" for a tool "+
				"that starts an unconfined program", name)
		}
		req := appr.requests[len(appr.requests)-1]

		if !req.LaunchesSubprocess {
			t.Errorf("the approval request for %s does not carry LaunchesSubprocess.\n"+
				"  The registry knows this tool starts a language server; the human being asked "+
				"to approve it is not told.", name)
		}
		// The pairing that makes the flag worth having: not confined, and the
		// request says why.
		if req.Confined {
			t.Errorf("%s is advertised as confined while declaring LaunchesSubprocess", name)
		}
	}
}

// ANTI-VACUITY. A tool that starts nothing must not carry the flag, or the
// assertion above would pass against a daemon that sets it on everything.
func TestOrdinaryBuiltinsDoNotClaimToStartPrograms(t *testing.T) {
	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{Enabled: true}}

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	appr := &recordingApprover{answer: protocol.ApprovalDeny}
	call := toolCall{Function: toolCallFunction{
		Name:      mcp.BuiltinServerName + "__repo_map",
		Arguments: `{}`,
	}}
	s.resolveExecutable(context.Background(), registry,
		&agentTurn{grants: map[string]bool{}}, budget{maxIterations: 4}, call, appr, nil)

	for _, req := range appr.requests {
		if req.LaunchesSubprocess {
			t.Errorf("repo_map claims to start a program; it reads the index: %+v", req)
		}
	}
}
