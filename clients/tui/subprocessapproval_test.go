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

// REGISTER ITEM 32, as the user sees it. The daemon now sets LaunchesSubprocess
// only when approving THIS call starts the program, and names it in Program.
// The two prompts a user meets for one language server must therefore read
// differently: the first says it starts gopls, every later one says gopls is
// already running and nothing new starts.
func TestTheLaunchPromptNamesTheProgramItStarts(t *testing.T) {
	panel := renderApprovalPanel(protocol.ToolApprovalRequest{
		Tool: "query_compiler_definition", Lane: protocol.LaneFirstParty,
		LaunchesSubprocess: true, Program: "gopls", Iteration: 1, MaxIterations: 8,
	})
	if !strings.Contains(panel, "STARTS gopls") {
		t.Errorf("the launch prompt does not say it starts gopls by name:\n%s", panel)
	}
	if !strings.Contains(panel, "until the daemon exits") {
		t.Errorf("the launch prompt does not say gopls keeps running afterwards:\n%s", panel)
	}
	if strings.Contains(panel, "ALREADY RUNNING") {
		t.Errorf("the launch prompt claims gopls is already running:\n%s", panel)
	}
}

// Neuter check: delete the Program case. A running-server prompt (Confined
// false) then falls to the default and prints the third-party-server sentence,
// which this test forbids. Moving the case below `default` would prove nothing:
// in Go the default runs only when no case matches, wherever it sits.
func TestARunningServerPromptSaysNothingNewStarts(t *testing.T) {
	panel := renderApprovalPanel(protocol.ToolApprovalRequest{
		Tool: "query_compiler_definition", Lane: protocol.LaneFirstParty, Confined: false,
		LaunchesSubprocess: false, Program: "gopls", Iteration: 2, MaxIterations: 8,
	})
	if !strings.Contains(panel, "ASKS gopls, WHICH IS ALREADY RUNNING") {
		t.Fatalf("the prompt for a running server does not say so:\n%s", panel)
	}
	if !strings.Contains(panel, "starts nothing new") {
		t.Errorf("the prompt does not say this call starts nothing:\n%s", panel)
	}
	for _, forbidden := range []string{
		"STARTS gopls", "STARTS ANOTHER PROGRAM", // item 32's false sentence
		"NOT SANDBOXED: this is a separate program",        // reads as a third-party server
		"anything it changes goes through the same review", // this tool is not confined
	} {
		if strings.Contains(panel, forbidden) {
			t.Errorf("the prompt for an already-running server says %q:\n%s", forbidden, panel)
		}
	}
}

// Program arrives from the daemon and is printed on the one screen where a
// repainted line is forged consent, so it is filtered like every other field
// there. A clear-screen sequence must not survive into the panel.
func TestTheProgramNameCannotRepaintTheConsentScreen(t *testing.T) {
	for _, launches := range []bool{true, false} {
		panel := renderApprovalPanel(protocol.ToolApprovalRequest{
			Tool: "query_compiler_definition", Lane: protocol.LaneFirstParty,
			LaunchesSubprocess: launches, Program: "gopls\x1b[2J\x1b[H", Iteration: 1, MaxIterations: 8,
		})
		if strings.Contains(panel, "\x1b[2J") || strings.Contains(panel, "\x1b[H") {
			t.Errorf("launches=%t: a control sequence in Program reached the consent screen", launches)
		}
	}
}
