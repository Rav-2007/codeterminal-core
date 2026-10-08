package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A limit ends the turn WITH AN ANSWER. MEASURED live before this existed:
// "study Mochiii and recreate it" read five files, hit max_total_tool_bytes,
// and ended on "let me study the core agent loop more closely" -- five tool
// lines and no conclusion from anything that was read.

func TestTheByteLimitEndsWithAnAnswerFromWhatWasRead(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"big.txt"}`),
		textSSE("SUMMARY OF WHAT I READ"), // the wrap-up call
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		Budget:  MCPBudgetConfig{MaxIterations: 10, MaxToolResultBytes: 4096, MaxTotalToolBytes: 4096},
	})
	if err := os.WriteFile(filepath.Join(s.workspace, "big.txt"), []byte(strings.Repeat("x", 8000)), 0o600); err != nil {
		t.Fatal(err)
	}

	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if !strings.Contains(res.FinalText, "SUMMARY OF WHAT I READ") {
		t.Errorf("the turn ended without an answer: %q", res.FinalText)
	}
	if res.Incomplete == nil {
		t.Error("the limit is no longer reported; the user must still be told it was reached")
	}
	if len(*bodies) != 2 || !strings.Contains(string((*bodies)[1]), "cannot call any more tools") {
		t.Errorf("want exactly one wrap-up call carrying the no-tools instruction; got %d request(s)", len(*bodies))
	}
}

func TestTheStepLimitEndsWithAnAnswerAndRunsNoMoreTools(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__list_directory", `{"path":"."}`),
		toolCallSSE("c2", "builtin__list_directory", `{"path":"."}`),
		// The wrap-up reply tries to call a tool anyway: it must not run.
		toolCallSSE("c3", "builtin__list_directory", `{"path":"."}`),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": "allow"}},
		Budget:  MCPBudgetConfig{MaxIterations: 3},
	})
	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	// THE WRAP-UP IS ONE OF THE THREE (2026-10-08). It used to come after the
	// ceiling -- a limit of N billed N+1 calls, and the extra one re-sent the
	// whole conversation -- so this read "MaxIterations: 2 ... want 3".
	if len(*bodies) != 3 {
		t.Errorf("%d model call(s) under a limit of 3, want 2 steps and the wrap-up: 3", len(*bodies))
	}
	if !strings.Contains(string((*bodies)[2]), "cannot call any more tools") {
		t.Error("the third call is not the wrap-up")
	}
	ran := 0
	for _, a := range activity {
		if a.Phase == "succeeded" {
			ran++
		}
	}
	if ran != 2 {
		t.Errorf("%d tool call(s) ran, want 2: the wrap-up reply's tool call must be ignored", ran)
	}
	if res.Incomplete == nil {
		t.Error("the step limit is no longer reported")
	}
}

// A step limit of N is N model calls: the answer that ends a stopped turn is
// counted in it. A limit of ONE cannot hold both a step and an answer; it
// keeps the answer, so it is the one limit that still bills one more.
func TestAStepLimitCountsTheCallThatAnswers(t *testing.T) {
	for _, tc := range []struct{ limit, wantCalls, wantSteps int }{
		{limit: 1, wantCalls: 2, wantSteps: 1},
		{limit: 2, wantCalls: 2, wantSteps: 1},
		{limit: 5, wantCalls: 5, wantSteps: 4},
	} {
		// A different file each step, so nothing but the step limit can end
		// the turn: not the repeated-call rule, not the stall rule.
		replies := make([][]string, 0, 8)
		for i := range 8 {
			replies = append(replies, toolCallSSE(fmt.Sprintf("c%d", i), "builtin__read_file", fmt.Sprintf(`{"path":"f%d.txt"}`, i)))
		}
		base, _, bodies := agentUpstream(t, replies...)
		s := loopServer(t, base, MCPConfig{
			Enabled: true,
			Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
			Budget:  MCPBudgetConfig{MaxIterations: tc.limit},
		})
		for i := range 8 {
			if err := os.WriteFile(filepath.Join(s.workspace, fmt.Sprintf("f%d.txt", i)), fmt.Appendf(nil, "file %d\n", i), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		res, activity, err := runLoop(t, s)
		if err != nil {
			t.Fatalf("limit %d: runAgentLoop: %v", tc.limit, err)
		}
		steps := 0
		for _, a := range activity {
			if a.Phase == "succeeded" {
				steps++
			}
		}
		if got := len(*bodies); got != tc.wantCalls || steps != tc.wantSteps {
			t.Errorf("limit %d: %d model calls and %d tool steps; want %d and %d", tc.limit, got, steps, tc.wantCalls, tc.wantSteps)
		}
		if res.Incomplete == nil || !strings.Contains(res.Incomplete.Detail, "max_iterations") {
			t.Errorf("limit %d: the step limit is not what was reported: %+v", tc.limit, res.Incomplete)
		}
	}
}
