package main

import (
	"strings"
	"testing"
)

// What a turn with a working copy is told about testing its edits
// (verifyrule.go): the project's own tests, after the last edit, in the copy
// both command tools run in -- and a plain word when they could not be run.
func TestTheWorkingCopySaysHowToTestAnEdit(t *testing.T) {
	for _, want := range []string{
		"PROJECT'S OWN tests",
		"with sandbox_exec or run_command, runs in this working copy and sees your edits",
		"prefer sandbox_exec, which is confined",
		"A program you write yourself is not the project's tests",
		"a file a command creates is not offered for review",
		"one call per step",
		"say so plainly instead of saying they pass",
	} {
		if !strings.Contains(workingCopyDirective, want) {
			t.Errorf("the working copy's directive does not say %q", want)
		}
	}
	// The old warning is gone: it was true of the old place, and now it is not.
	if strings.Contains(workingCopyDirective, "run_command runs on the user's real files") {
		t.Error("the directive still says run_command cannot see the edits")
	}
}
