package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"mochiii/protocol"
)

// A TOOL THAT FAILS SAYS WHY, TO THE USER AS WELL AS THE MODEL.
//
// Found in the first Stage 7 walkthrough (2026-10-13): a two-folder workspace,
// "list the new_file folder", and the panel printed "builtin__list_directory
// failed" -- twice -- with the reason ("no such folder ...") sent only to the
// model. The phase was reported with an empty detail on this path.
func TestAFailedToolReportsItsReason(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__list_directory", `{"path":"new_file"}`),
		textSSE("There is no such folder."),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": PolicyAllow}},
	})

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	var failed *protocol.ToolActivity
	for i := range activity {
		if activity[i].Phase == protocol.ToolPhaseFailed {
			failed = &activity[i]
		}
	}
	if failed == nil {
		t.Fatalf("listing a folder that does not exist reported no failure: %+v", activity)
	}
	if !strings.Contains(failed.Detail, "new_file") {
		t.Errorf("the failure says %q; it should say what failed and why, as the model is told", failed.Detail)
	}
	if strings.Contains(failed.Detail, s.workspace) {
		t.Errorf("the failure reason carries the workspace's absolute path: %q", failed.Detail)
	}
}

func TestToolFailureReasonIsOneBoundedLine(t *testing.T) {
	if got := toolFailureReason("\n  \ncannot list x: no such folder\nmore detail"); got != "cannot list x: no such folder" {
		t.Errorf("first non-empty line: got %q", got)
	}
	if got := toolFailureReason("bad \x1b[31mred\x1b[0m\x07 bell"); strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("control characters survived: %q", got)
	}
	long := strings.Repeat("é", maxFailureReasonRunes+50)
	got := toolFailureReason(long)
	if !utf8.ValidString(got) {
		t.Errorf("the cut split a character: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != maxFailureReasonRunes+1 {
		t.Errorf("kept %d runes, want %d plus the ellipsis", n, maxFailureReasonRunes)
	}
	if got := toolFailureReason(" \n\t\n"); got != "" {
		t.Errorf("an empty result gave %q", got)
	}
}
