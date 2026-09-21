package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// A go-to-definition call starts gopls, tsserver or pyright from the user's
// PATH, against the repository they have open, and that program reads
// project-supplied configuration. The daemon has known this since
// LaunchesSubprocess was added; the consent screen could not say it, because
// the flag drove the Confined stamp and never reached the wire.
//
// What the user saw instead was the third-party-server sentence -- "a separate
// program running with your full access" -- which is true and sends them
// looking for an MCP server they never configured.
func TestSubprocessApprovalSaysWhatItStarts(t *testing.T) {
	req := protocol.ToolApprovalRequest{
		CallID:             "c1",
		Tool:               "query_compiler_definition",
		Arguments:          `{"symbol":"buildRegistry"}`,
		Lane:               protocol.LaneFirstParty,
		Confined:           false,
		LaunchesSubprocess: true,
		ReadOnlyHint:       true,
		Iteration:          1,
		MaxIterations:      8,
	}

	panel := renderApprovalPanel(req)

	if !strings.Contains(panel, "STARTS ANOTHER PROGRAM") {
		t.Fatalf("the panel does not say a program is started:\n%s", panel)
	}
	// The two sentences that must NOT be the answer here.
	if strings.Contains(panel, "anything it changes goes through the same review") {
		t.Error("printed the confined-tool reassurance for a call that starts an unconfined program")
	}
	if strings.Contains(panel, "NOT SANDBOXED: this is a separate program") {
		t.Error("printed the third-party-server sentence for a first-party tool; " +
			"it sends the user looking for a server they never configured")
	}
	// Name the actual programs: "a language server" is not actionable, "gopls"
	// is something a user can look for.
	lower := strings.ToLower(panel)
	for _, want := range []string{"gopls", "language server", "configuration"} {
		if !strings.Contains(lower, want) {
			t.Errorf("the panel never mentions %q, so the user cannot tell what is being started:\n%s", want, panel)
		}
	}
}

// The branch must not steal calls that belong to the other two answers.
func TestSubprocessBranchDoesNotShadowTheOthers(t *testing.T) {
	network := protocol.ToolApprovalRequest{
		Tool: "web_search", Lane: protocol.LaneFirstParty, ReachesNetwork: true,
	}
	if p := renderApprovalPanel(network); !strings.Contains(p, "LEAVES YOUR MACHINE") {
		t.Errorf("a network call lost its warning:\n%s", p)
	}

	confined := protocol.ToolApprovalRequest{
		Tool: "read_file", Lane: protocol.LaneFirstParty, Confined: true,
	}
	if p := renderApprovalPanel(confined); !strings.Contains(p, "anything it changes goes through the same review") {
		t.Errorf("a confined call lost its line:\n%s", p)
	}

	laneB := protocol.ToolApprovalRequest{
		Tool: "whatever", Server: "some-server", Lane: protocol.LaneThirdParty,
	}
	if p := renderApprovalPanel(laneB); !strings.Contains(p, "NOT SANDBOXED") {
		t.Errorf("a third-party call lost its warning:\n%s", p)
	}
}
