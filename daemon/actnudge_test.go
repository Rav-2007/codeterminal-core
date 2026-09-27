package main

import (
	"strings"
	"testing"
)

func TestWhatCountsAsAChangeRequest(t *testing.T) {
	for req, want := range map[string]bool{
		"The tests are failing. Find the bug and fix it.":       true,
		"Add a function IsPalindrome to strutil":                true,
		"Rename CalcTotal to Total everywhere":                  true,
		"Implement ParseSize so the tests pass":                 true,
		"how do I add a flag?":                                  false,
		"What does resolveExecutable do?":                       false,
		"explain why the build is slow":                         false,
		"Summarise this repository":                             false,
		"Users report Average returns NaN. Fix it, with a test": true,
	} {
		if got := asksForChange(req); got != want {
			t.Errorf("asksForChange(%q) = %v, want %v", req, got, want)
		}
	}
}

func TestTheActNudgeFiresOnceAtHalfTheBudget(t *testing.T) {
	bud := budget{maxIterations: 8}
	turn := &agentTurn{mode: "auto", request: "fix the failing test"}
	for turn.iteration = 1; turn.iteration < 4; turn.iteration++ {
		if actNudge(turn, bud) != "" {
			t.Fatalf("nudged at step %d of 8", turn.iteration)
		}
	}
	turn.iteration = 4
	if note := actNudge(turn, bud); !strings.Contains(note, "4 of this turn's 8 steps") {
		t.Fatalf("no nudge at half the budget: %q", note)
	}
	turn.iteration = 5
	if actNudge(turn, bud) != "" {
		t.Error("nudged twice in one turn")
	}

	for name, tn := range map[string]*agentTurn{
		"a question":            {mode: "auto", request: "what does this function do?"},
		"a turn that edited":    {mode: "auto", request: "fix it", toolNames: []string{"builtin__propose_edit"}},
		"a turn that ran tests": {mode: "auto", request: "fix it", toolNames: []string{"builtin__sandbox_exec"}},
		"plan mode":             {mode: "plan", request: "fix it"},
		"check mode":            {mode: "check", request: "check it"},
	} {
		tn.iteration = 4
		if actNudge(tn, bud) != "" {
			t.Errorf("%s was nudged to act", name)
		}
	}
	build := &agentTurn{mode: "build", request: "Build what the spec specs/x.md describes.", iteration: 12}
	if actNudge(build, budget{maxIterations: 24}) == "" {
		t.Error("a build that has done nothing by step 12 of 24 was not nudged")
	}
}

// On a real loop: the note reaches the model on the call after half the
// budget, once, and never on a question.
func TestTheActNudgeReachesTheModel(t *testing.T) {
	reads := func() [][]string {
		var r [][]string
		for i := 0; i < 6; i++ {
			r = append(r, toolCallSSE("c"+string(rune('a'+i)), "builtin__list_directory", `{"path":"."}`))
		}
		return append(r, textSSE("done"))
	}
	for _, tc := range []struct {
		prompt string
		want   int
	}{{"fix the failing test", 1}, {"what is in this folder?", 0}} {
		base, _, bodies := agentUpstream(t, reads()...)
		s := loopServer(t, base, MCPConfig{Enabled: true, Budget: MCPBudgetConfig{MaxIterations: 8},
			Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": "allow"}}})
		if _, _, err := runLoopPrompt(t, s, nil, tc.prompt); err != nil {
			t.Fatal(err)
		}
		seen, firstAt := 0, -1
		for i, b := range *bodies {
			if strings.Contains(string(b), "A note from Mochiii") {
				if firstAt < 0 {
					firstAt = i
				}
				seen++
			}
		}
		// Every later request re-sends the history, so the note appears in
		// each one after it was added; what matters is that it was added once
		// and first reached the model on the 5th call (after step 4).
		if tc.want == 0 && seen != 0 {
			t.Errorf("%q: a question was nudged to act", tc.prompt)
		}
		if tc.want == 1 && (firstAt != 4 || strings.Count(string((*bodies)[len(*bodies)-1]), "A note from Mochiii") != 1) {
			t.Errorf("%q: the note first reached the model on call %d (want 5, index 4) and appears %d time(s) in the last request",
				tc.prompt, firstAt+1, strings.Count(string((*bodies)[len(*bodies)-1]), "A note from Mochiii"))
		}
	}
}
