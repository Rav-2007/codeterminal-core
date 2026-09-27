package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// Above the review, the user is told whether the change was ever built or
// tested, and what the agent is not offering.
func TestTheWorkingCopyReportSaysWhatWasChecked(t *testing.T) {
	cases := []struct {
		info *protocol.WorkingCopyInfo
		want []string
	}{
		{&protocol.WorkingCopyInfo{Checked: "go test ./...", Passed: true}, []string{"✓ checked: go test ./... passed"}},
		{&protocol.WorkingCopyInfo{Checked: "go test ./...", Output: "--- FAIL: TestX\nFAIL"},
			[]string{"✗ checked: go test ./... FAILED", "  --- FAIL: TestX"}},
		{&protocol.WorkingCopyInfo{}, []string{"not checked: the agent did not build or test these changes"}},
		{&protocol.WorkingCopyInfo{Checked: "make", Passed: true, NotOffered: []string{"out.bin: created by a command, not offered"}},
			[]string{"not offered: out.bin: created by a command"}},
	}
	for _, c := range cases {
		got := workingCopyText(c.info)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("workingCopyText(%+v) =\n%s\nwant it to contain %q", c.info, got, w)
			}
		}
	}
	if workingCopyText(nil) != "" {
		t.Error("no working copy should say nothing")
	}
	// Model-chosen text is sanitized: an escape sequence cannot repaint the line.
	if got := workingCopyText(&protocol.WorkingCopyInfo{Checked: "go test \x1b[2J./..."}); strings.Contains(got, "\x1b") {
		t.Errorf("escape sequence reached the screen: %q", got)
	}
}
