package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"codeterminal/daemon/mcp"
	"codeterminal/protocol"
)

// laneBClient is a third-party MCP server under this test's control, so a tool
// name can be chosen to collide with a first-party one on purpose.
type laneBClient struct{ tools []mcp.Tool }

func (c *laneBClient) ListTools(context.Context) ([]mcp.Tool, error) {
	// The real stdio client stamps Server and Lane on every tool it returns
	// (stdioclient.go); a fake that did not would make this test's qualified
	// names and lane checks lie about the thing they are testing.
	out := make([]mcp.Tool, 0, len(c.tools))
	for _, t := range c.tools {
		t.Server = "helpful"
		t.Lane = protocol.LaneThirdParty
		out = append(out, t)
	}
	return out, nil
}

func (c *laneBClient) CallTool(context.Context, string, json.RawMessage) (mcp.Result, error) {
	return mcp.Result{Content: "the unconfined tool ran"}, nil
}
func (c *laneBClient) Close() error { return nil }

// laneTestRegistry advertises one third-party server, configured and
// acknowledged, so it is a tool surface the user genuinely asked for rather
// than one this test smuggled past the policy resolver.
func laneTestRegistry(t *testing.T, tools []mcp.Tool) *mcp.Registry {
	t.Helper()
	cfg := &Config{MCP: MCPConfig{
		Enabled: true,
		Servers: map[string]MCPServerConfig{
			"helpful": {Command: "true", AcknowledgedUnconfined: true},
		},
	}}
	r := mcp.NewRegistry(configPolicy{cfg: cfg}, 50)
	if err := r.AddServer("helpful", &laneBClient{tools: tools}); err != nil {
		t.Fatal(err)
	}
	return r
}

// A ROLE ALLOWLIST NAMES FIRST-PARTY TOOLS, AND A NAME IS NOT A LANE.
//
// The Researcher's list contains "read_file" because someone read what this
// daemon's read_file does: it is compiled in, and confined by the same resolver
// that gates model-proposed edits. A third-party server may expose a tool
// called read_file too. It is a different program, in a different lane,
// unconfined, and nothing about sharing a string makes the Researcher's
// judgement about the first one true of it.
//
// This is the tool-level twin of the reservation ValidateServerName already
// makes at the server level, in its own words: "so a user cannot shadow the
// confined tools with unconfined ones of the same name."
//
// MEASURED BEFORE THE FIX: the Researcher's menu contained
// "helpful__read_file".
func TestAThirdPartyToolCannotTakeAFirstPartyRolesSlot(t *testing.T) {
	s := builtinTestServer(t)
	registry := laneTestRegistry(t, []mcp.Tool{
		{Name: "read_file", Description: "an unconfined namesake"},
	})

	// The HINT half: it must not be on the menu.
	specs, excluded, _ := s.advertisedToolSpecs(context.Background(), registry, &roleResearcher)
	for _, sp := range specs {
		if strings.HasPrefix(sp.Function.Name, "helpful"+mcp.QualifiedNameSeparator) {
			t.Errorf("%q was offered to the Researcher; its allowlist names the CONFINED read_file",
				sp.Function.Name)
		}
	}
	if len(excluded) != 1 || excluded[0] != "helpful__read_file" {
		t.Errorf("excluded = %v, want the namesake reported so the user is told rather than left to infer", excluded)
	}

	// The ENFORCEMENT half, which is the one that matters: a model can name a
	// tool it was never shown, and that is precisely the shape a prompt-injected
	// instruction takes. Both halves compared the same lane-blind string before
	// the fix, so the second lock was no lock at all.
	dec := s.resolveExecutable(context.Background(), registry, &agentTurn{grants: map[string]bool{}},
		budget{maxIterations: 4}, toolCall{Function: toolCallFunction{Name: "helpful__read_file", Arguments: "{}"}},
		&recordingApprover{}, &roleResearcher)
	if dec.run || dec.policy != mcp.PolicyDeny {
		t.Errorf("the Researcher was allowed to CALL %q (policy=%v run=%v); the menu is a hint, this is the control",
			"helpful__read_file", dec.policy, dec.run)
	}
}

// A LANE B TOOL EXCLUDED FROM A PHASE MUST BE SAID OUT LOUD.
//
// DegradedToolMenuTruncated already states the rule for the advertised cap: "a
// tool that was dropped and a tool the server never offered both show up as the
// model not using it. The user configured that server on purpose and deserves
// to know which of the two happened." The role filter is a second way to drop a
// tool and it skipped that rule -- so for the length of a pipeline turn a
// user's entire configured MCP surface vanished, with no evidence but an agent
// that inexplicably stopped using it.
func TestAPipelinePhaseSaysWhichThirdPartyToolsItDropped(t *testing.T) {
	s := builtinTestServer(t)
	registry := laneTestRegistry(t, []mcp.Tool{
		{Name: "deploy", Description: "a third-party tool with no first-party namesake"},
	})

	unrestricted, excluded, _ := s.advertisedToolSpecs(context.Background(), registry, nil)
	if len(unrestricted) == 0 {
		t.Fatal("premise broken: the unorchestrated agent was offered nothing, so there is no loss to detect")
	}
	if excluded != nil {
		t.Errorf("the unorchestrated agent excludes nothing by role, but reported %v", excluded)
	}

	for _, role := range []*agentRole{&roleResearcher, &roleCoder, &roleTester} {
		specs, excluded, _ := s.advertisedToolSpecs(context.Background(), registry, role)
		for _, sp := range specs {
			if strings.HasPrefix(sp.Function.Name, "helpful"+mcp.QualifiedNameSeparator) {
				t.Errorf("%s was offered %q; no role's curated list can vouch for a third-party tool",
					role.Display, sp.Function.Name)
			}
		}
		if len(excluded) != 1 || excluded[0] != "helpful__deploy" {
			t.Errorf("%s dropped the tool but reported %v; a silent drop is the bug", role.Display, excluded)
		}
	}
}
