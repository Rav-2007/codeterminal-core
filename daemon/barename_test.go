package main

// THE BARE-NAME TAX, and the four ways removing it could have gone wrong.
//
// MEASURED against a live model over this repository: 8 refusals across 3
// orchestrated turns, ~5 of one turn's 14 model calls, all spent asking for
// "search_code" when the registry knows it as "builtin__search_code". Every one
// was refused and retried, and the iterations came out of the user's budget.
//
// Registry.Canonicalize now resolves a bare name to the first-party lane. The
// registry-level tests (daemon/mcp/canonicalize_test.go) pin WHAT may resolve.
// These pin the thing that would turn a naming convenience into a
// vulnerability: that the resolved name -- and never the raw one -- is what
// policy, role scoping and consent are decided against.

import (
	"strings"
	"testing"

	"codeterminal/protocol"
)

// The tax is actually recovered: a bare name runs instead of being refused.
func TestBareBuiltinNameRunsInsteadOfBeingRefused(t *testing.T) {
	// list_directory on "." -- the test workspace is an empty temp dir, so this
	// succeeds without the test having to assume any file exists in it.
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "list_directory", `{"path":"."}`), // no "builtin__"
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": "allow"}},
	})

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseDenied {
			t.Fatalf("a bare built-in name was refused (%q): the tax is still being paid", a.Detail)
		}
	}
	if !hasPhase(activity, protocol.ToolPhaseSucceeded) {
		t.Errorf("no call succeeded; activity = %+v", activity)
	}
	// The user must be shown what actually ran, not the shorthand.
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseSucceeded && a.Server != "builtin" {
			t.Errorf("a resolved call was reported with server %q, want %q", a.Server, "builtin")
		}
	}
}

// POLICY IS DECIDED ON THE RESOLVED NAME. A built-in the user has denied stays
// denied when the model spells it without the prefix. If the alias were applied
// after the policy lookup -- or the policy looked up the raw string -- this is
// the call that would slip through.
func TestBareNameDoesNotBypassAConfigDeny(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "sandbox_exec", `{"command":"echo hi"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"sandbox_exec": "deny"}},
	})

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if hasPhase(activity, protocol.ToolPhaseSucceeded) {
		t.Fatal("a DENIED built-in ran when the model named it without its prefix: " +
			"the alias bypassed policy, which is the one thing it must never do")
	}
	if !hasPhase(activity, protocol.ToolPhaseDenied) {
		t.Errorf("the call was neither refused nor run; activity = %+v", activity)
	}
}

// ROLE SCOPING IS DECIDED ON THE RESOLVED NAME. The Planner has an empty tool
// list, so a bare name must be refused by the allowlist exactly as a qualified
// one is.
func TestBareNameDoesNotBypassRoleScoping(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":"go.mod"}`),
		textSSE("plan"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		// allow, so ONLY role scoping can refuse it
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
	})

	_, activity, _, err := runPipeline(t, s, []*agentRole{&rolePlanner})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}
	if hasPhase(activity, protocol.ToolPhaseSucceeded) {
		t.Fatal("a bare name escaped the role allowlist: role scoping must be enforced on the RESOLVED name")
	}
	if !hasPhase(activity, protocol.ToolPhaseDenied) {
		t.Errorf("the call was neither refused nor run; activity = %+v", activity)
	}
}

// THE HUMAN APPROVES WHAT RUNS. Approving "search_code" while dispatching
// "builtin__search_code" would be the approve-one-string-run-another failure
// that ValidateToolName's own comment calls out.
func TestTheApprovalPromptShowsTheResolvedName(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":"go.mod"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "ask"}},
	})

	appr := &capturingApprover{decision: protocol.ApprovalApprove}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	prompts := appr.prompts()
	if len(prompts) != 1 {
		t.Fatalf("got %d approval prompt(s), want 1", len(prompts))
	}
	if prompts[0].Server != "builtin" || prompts[0].Tool != "read_file" {
		t.Errorf("the human was asked about %q/%q, want the resolved builtin/read_file",
			prompts[0].Server, prompts[0].Tool)
	}
	if !prompts[0].Confined {
		t.Error("the prompt did not report the call as confined; the resolved tool is a first-party built-in")
	}
}

// ONE GRANT LEDGER. "Approve for this turn" given for a bare name must cover
// the qualified spelling and vice versa -- they are the same tool. Two keys
// would mean the user is asked twice for something they already approved.
func TestATurnGrantCoversBothSpellings(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":"go.mod"}`),           // bare
		toolCallSSE("c2", "builtin__read_file", `{"path":"go.work"}`), // qualified
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "ask"}},
	})

	appr := &capturingApprover{decision: protocol.ApprovalApproveForTurn}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	if n := len(appr.prompts()); n != 1 {
		t.Errorf("the user was asked %d time(s) for one tool; an approve-for-turn grant "+
			"must not be fragmented by how the model spelled the name", n)
	}
}

// The residue: a bare name that is NOT a built-in is still refused -- and told
// the convention, so it costs one retry rather than a loop of them.
func TestAnUnresolvableBareNameIsRefusedWithTheConvention(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "definitely_not_a_builtin", `{}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{Enabled: true})

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	var detail string
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseDenied {
			detail = a.Detail
		}
	}
	if detail == "" {
		t.Fatalf("an unknown bare name was not refused; activity = %+v", activity)
	}
	if !strings.Contains(detail, "__") {
		t.Errorf("the refusal did not tell the model the naming convention, so it has nothing "+
			"to correct: %q", detail)
	}
}

func hasPhase(activity []protocol.ToolActivity, phase string) bool {
	for _, a := range activity {
		if a.Phase == phase {
			return true
		}
	}
	return false
}
