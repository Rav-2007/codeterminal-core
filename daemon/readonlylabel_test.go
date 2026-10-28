package main

import (
	"context"
	"testing"

	"mochiii/protocol"
)

// "MARKED DESTRUCTIVE" ON A READ-ONLY TOOL. Live on 2026-10-28 every read tool
// of the Filesystem server -- readOnlyHint: true, destructiveHint left out --
// was shown "marked destructive" when Mochiii asked. The spec says
// destructiveHint is "meaningful only when ReadOnlyHint == false", so the label
// is left off. These go through resolveExecutable against a real stdio MCP
// server (mcp/testdata/echoserver --read-only-hints), which is where the
// approval request is built.

// hintsServer is a daemon with the echo server's three hint shapes, every tool
// "ask", and a registry built the way a turn builds one.
func hintsServer(t *testing.T) (*Server, func(appr approver, turn *agentTurn, tool, args string) toolDecision) {
	t.Helper()
	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{Enabled: true, Servers: map[string]MCPServerConfig{"hints": {
		Command: buildEchoServer(t),
		Args:    []string{"--read-only-hints"},
		Tools: map[string]string{
			"reads": PolicyAsk, "says_nothing": PolicyAsk, "destroys": PolicyAsk, "echo": PolicyAsk,
		},
		AcknowledgedUnconfined: true,
	}}}}
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })
	resolve := func(appr approver, turn *agentTurn, tool, args string) toolDecision {
		call := toolCall{Function: toolCallFunction{Name: "hints__" + tool, Arguments: args}}
		return s.resolveExecutable(context.Background(), registry, turn, budget{maxIterations: 4}, call, appr, nil)
	}
	return s, resolve
}

func TestAReadOnlyToolIsNotLabelledDestructive(t *testing.T) {
	_, resolve := hintsServer(t)
	for _, tc := range []struct {
		tool, why string
		want      bool
	}{
		{"reads", "readOnlyHint true, destructiveHint absent: the Filesystem shape", false},
		{"says_nothing", "an empty annotations block: absent means destructive, as today", true},
		{"destroys", "destructiveHint true", true},
		{"echo", "no annotations block at all: not labelled, as today", false},
	} {
		appr := &recordingApprover{answer: protocol.ApprovalDeny}
		resolve(appr, &agentTurn{grants: map[string]bool{}}, tc.tool, `{"text":"x"}`)
		if len(appr.requests) != 1 {
			t.Fatalf("%s: asked %d time(s), want 1 -- whether Mochiii asks must not change", tc.tool, len(appr.requests))
		}
		if got := appr.requests[0].Destructive; got != tc.want {
			t.Errorf("%s (%s): request says destructive=%v, want %v", tc.tool, tc.why, got, tc.want)
		}
	}
}

// ASKING STAYS EXACTLY AS IT WAS. The label changed; the tool's Destructive did
// not, and it is what narrows a "yes for this task" to the arguments approved
// (grantKey). So after "a" on reads("a"), reads("a") runs without asking and
// reads("b") is asked about -- the same as before the label changed.
func TestTheReadOnlyLabelLeavesTheTurnGrantAlone(t *testing.T) {
	_, resolve := hintsServer(t)
	turn := &agentTurn{grants: map[string]bool{}}

	first := &recordingApprover{answer: protocol.ApprovalApproveForTurn}
	if d := resolve(first, turn, "reads", `{"text":"a"}`); !d.run || len(first.requests) != 1 {
		t.Fatalf("the first call was not asked and approved (asked %d, run %v)", len(first.requests), d.run)
	}

	same := &recordingApprover{answer: protocol.ApprovalDeny}
	if d := resolve(same, turn, "reads", `{"text":"a"}`); !d.run || len(same.requests) != 0 {
		t.Errorf("the same arguments were asked about again (asked %d, run %v); the turn grant should cover them",
			len(same.requests), d.run)
	}

	other := &recordingApprover{answer: protocol.ApprovalDeny}
	resolve(other, turn, "reads", `{"text":"b"}`)
	if len(other.requests) != 1 {
		t.Errorf("different arguments ran on the earlier grant without asking (asked %d); "+
			"the grant's scope changed along with the label", len(other.requests))
	}
}
