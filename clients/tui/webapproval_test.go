package main

import (
	"bufio"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// THE CONSENT PROMPT IS WHERE THE PRIVACY CLAIM IS EITHER KEPT OR BROKEN.
//
// A web tool changes nothing on disk, so every "is this safe" field on the
// request says "harmless" and the panel's reassuring branch is the one that
// would fire. The user standing at that prompt is not asking what the tool
// changes. They are asking where their words are about to go, and this product
// is sold on the answer.
func TestTheApprovalPanelSaysPlainlyWhenACallLeavesTheMachine(t *testing.T) {
	req := protocol.ToolApprovalRequest{
		CallID:         "c1",
		Server:         "builtin",
		Tool:           "web_search",
		Arguments:      `{"query":"who is the pm of india"}`,
		Lane:           protocol.LaneFirstParty,
		Confined:       false,
		ReachesNetwork: true,
		ReadOnlyHint:   true,
		Iteration:      1,
		MaxIterations:  8,
	}

	panel := renderApprovalPanel(req)

	if !strings.Contains(panel, "LEAVES YOUR MACHINE") {
		t.Fatalf("the panel does not say the call leaves the machine:\n%s", panel)
	}
	// The exact sentence that must NOT appear. It is written for a confined
	// local tool and is technically true of a search -- a search changes
	// nothing -- which is what makes printing it here a way of answering a
	// question nobody asked while the real one goes unanswered.
	if strings.Contains(panel, "anything it changes goes through the same review") {
		t.Error("the panel printed the local-confinement reassurance for a network call")
	}
	// The arguments are what actually leaves, so they must be visible in full.
	if !strings.Contains(panel, "who is the pm of india") {
		t.Error("the panel hid the text that is about to be transmitted")
	}
	// What the daemon does and does not promise, both stated.
	lower := strings.ToLower(panel)
	if !strings.Contains(lower, "untrusted") {
		t.Error("the panel does not say returned content is treated as untrusted data")
	}
	if !strings.Contains(lower, "cannot vouch") {
		t.Error("the panel overstates the guarantee; it must say what it cannot promise about the far end")
	}
}

// The other two branches must be unchanged. This is the regression guard for
// the switch that replaced the original if/else.
func TestTheOtherApprovalBranchesAreUnchanged(t *testing.T) {
	local := protocol.ToolApprovalRequest{
		Server: "builtin", Tool: "read_file", Lane: protocol.LaneFirstParty, Confined: true,
	}
	if panel := renderApprovalPanel(local); !strings.Contains(panel, "anything it changes goes through the same review") {
		t.Errorf("a confined built-in lost its reassurance line:\n%s", panel)
	}

	laneB := protocol.ToolApprovalRequest{
		Server: "someserver", Tool: "do_thing", Lane: protocol.LaneThirdParty, Confined: false,
	}
	panel := renderApprovalPanel(laneB)
	if !strings.Contains(panel, "NOT SANDBOXED") {
		t.Errorf("a Lane B tool lost its warning:\n%s", panel)
	}
	if strings.Contains(panel, "LEAVES YOUR MACHINE") {
		t.Error("a subprocess tool was described as a network call")
	}
}

// THE ONE-SHOT CLIENT IS A SECOND, INDEPENDENT RENDERER of the same consent,
// and it was left behind by the first pass of this change -- which is exactly
// how a second renderer fails. Its unconfined branch says "this is a separate
// program running with your full access", and for web_search that is not a
// softened truth, it is FALSE: nothing is spawned and nothing local is
// touched. Overstating a risk is not the safe error here. It is how a prompt
// gets trained out of being read, and the sentence needs to keep working for
// the real subprocesses it was written about.
func TestTheOneShotPromptIsAccurateForANetworkCall(t *testing.T) {
	var out strings.Builder
	req := protocol.ToolApprovalRequest{
		Server: "builtin", Tool: "web_search",
		Arguments:      `{"query":"current go version"}`,
		Lane:           protocol.LaneFirstParty,
		Confined:       false,
		ReachesNetwork: true,
		Iteration:      1, MaxIterations: 8,
	}
	askOneShotApproval(req, bufio.NewReader(strings.NewReader("n\n")), &out)
	got := out.String()

	if !strings.Contains(got, "LEAVES YOUR MACHINE") {
		t.Fatalf("the one-shot prompt does not say the call leaves the machine:\n%s", got)
	}
	if strings.Contains(got, "separate program running with your full access") {
		t.Error("the one-shot prompt called a network call a subprocess with full access; that is false, not cautious")
	}
	if strings.Contains(got, "anything it changes goes through the same review") {
		t.Error("the one-shot prompt printed the local-confinement reassurance for a network call")
	}
	if !strings.Contains(got, "current go version") {
		t.Error("the one-shot prompt hid the text about to be transmitted")
	}
}

// Both other branches, unchanged.
func TestTheOneShotPromptKeepsItsOtherTwoBranches(t *testing.T) {
	var confined, laneB strings.Builder
	askOneShotApproval(
		protocol.ToolApprovalRequest{Server: "builtin", Tool: "read_file", Confined: true},
		bufio.NewReader(strings.NewReader("n\n")), &confined)
	if !strings.Contains(confined.String(), "anything it changes goes through the same review") {
		t.Errorf("a confined built-in lost its line:\n%s", confined.String())
	}

	askOneShotApproval(
		protocol.ToolApprovalRequest{Server: "srv", Tool: "t", Lane: protocol.LaneThirdParty},
		bufio.NewReader(strings.NewReader("n\n")), &laneB)
	if !strings.Contains(laneB.String(), "NOT SANDBOXED") {
		t.Errorf("a Lane B tool lost its warning:\n%s", laneB.String())
	}
}
