package main

import (
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
		Budget:  MCPBudgetConfig{MaxIterations: 2},
	})
	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(*bodies) != 3 {
		t.Errorf("%d model call(s), want 2 steps + 1 wrap-up", len(*bodies))
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
