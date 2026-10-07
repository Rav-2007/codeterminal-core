package mcp

import (
	"strings"
	"testing"
)

// A COMMAND SAYS, BEFORE IT RUNS, WHETHER IT WOULD RUN IN THE REAL PROJECT
// (Builtin.InPlace, added 2026-10-07).
//
// The prompt for a command that will write straight into the user's project
// must not read like the prompt for one confined to a private copy, and only
// the tool knows which this call is. So the tool says, and registration keeps
// the claim to tools that run code: on a tool that runs nothing it would be a
// flag nobody reads, on its way to being a flag somebody trusts.
//
// Neuter check: delete the check in RegisterBuiltin and the first case below
// registers cleanly.
func TestOnlyAToolThatRunsCodeMaySayWhereItRuns(t *testing.T) {
	inPlace := func() bool { return true }

	err := NewRegistry(allowAll{}, 10).RegisterBuiltin(Builtin{
		Tool: Tool{Name: "read_x"}, Handler: noopHandler, InPlace: inPlace,
	})
	if err == nil || !strings.Contains(err.Error(), "does not declare ExecutesCode") {
		t.Fatalf("RegisterBuiltin = %v, want a refusal: the tool runs no code", err)
	}

	// And the honest pairing registers, or sandbox_exec could not exist.
	if err := NewRegistry(allowAll{}, 10).RegisterBuiltin(Builtin{
		Tool: Tool{Name: "exec_x", ExecutesCode: true}, Handler: noopHandler, InPlace: inPlace,
	}); err != nil {
		t.Fatalf("a tool that runs code WITH its probe was refused: %v", err)
	}
}

// Registry.RunsInPlace answers only for a built-in that says, and asks the
// tool's own probe each time: where a command runs is a fact about the moment
// (a working copy that could not be made), not about the tool.
func TestRunsInPlaceComesFromTheBuiltinsOwnProbe(t *testing.T) {
	r := NewRegistry(allowAll{}, 10)
	answer, asked := true, 0
	if err := r.RegisterBuiltin(Builtin{
		Tool:    Tool{Name: "exec_z", ExecutesCode: true},
		Handler: noopHandler,
		InPlace: func() bool { asked++; return answer },
	}); err != nil {
		t.Fatal(err)
	}
	// A tool that runs code and says nothing, and one that runs none.
	if err := r.RegisterBuiltin(Builtin{Tool: Tool{Name: "exec_quiet", ExecutesCode: true}, Handler: noopHandler}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBuiltin(Builtin{Tool: Tool{Name: "read_z"}, Handler: noopHandler}); err != nil {
		t.Fatal(err)
	}

	if !r.RunsInPlace("builtin__exec_z") {
		t.Error("RunsInPlace = false, and the tool's probe says it would run in the real project")
	}
	answer = false
	if r.RunsInPlace("builtin__exec_z") {
		t.Error("RunsInPlace = true after the probe's answer changed: it was not asked again")
	}
	if asked != 2 {
		t.Errorf("the probe was asked %d time(s) for 2 questions", asked)
	}
	for _, q := range []string{"builtin__exec_quiet", "builtin__read_z", "builtin__no_such_tool", "someserver__exec_z", "not qualified"} {
		if r.RunsInPlace(q) {
			t.Errorf("RunsInPlace(%q) = true, want false: nothing there says so", q)
		}
	}
}
