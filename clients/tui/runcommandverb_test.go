package tui

import (
	"slices"
	"testing"

	"mochiii/protocol"
)

// run_command's question says where THIS call runs: the turn's working copy,
// or -- git, an installer, a turn with no copy -- the real project, with the
// warning that goes with it.
func TestRunCommandsQuestionSaysWhereItRuns(t *testing.T) {
	req := protocol.ToolApprovalRequest{Server: "builtin", Tool: "run_command", Arguments: `{"command":"make test"}`}
	if q := approvalQuestion(req); q != "Run in this turn's working copy: make test ?" {
		t.Errorf("in the working copy, asked %q", q)
	}
	if slices.Contains(approvalRisks(req), inPlaceRisk) {
		t.Error("a command in the working copy is warned about as one in the real project")
	}
	req.InPlace = true
	if q := approvalQuestion(req); q != "Run in your terminal: make test ?" {
		t.Errorf("in the real project, asked %q", q)
	}
	if !slices.Contains(approvalRisks(req), inPlaceRisk) {
		t.Error("a command in the real project is not warned about as one")
	}
}
