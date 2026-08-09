package main

import (
	"os"
	"strings"
	"testing"
)

// THE REPEATED-CALL STALL.
//
// agentTurn.toolSignatures' own comment calls a repeated identical call "a
// loop's second-most-characteristic failure after not stopping" -- and until
// now the loop recorded it and did nothing. A stalled turn paid full price for
// every repeat: one iteration off max_iterations, and the entire result off
// max_total_tool_bytes, to learn nothing it did not already have.
//
// The fix does NOT skip the call. The tool still runs, so side effects still
// happen; only the payload fed back to the model is replaced, and only when the
// repeat produced BYTE-IDENTICAL output. That distinction is the whole design:
// it is a fact about the result, not a guess about intent, so a legitimate
// re-read after a change keeps working.
//
// TestARepeatThatReturnsSomethingNewIsNotSuppressed is the guard on that, and it
// is the more important of the two tests here -- an over-eager version of this
// feature would silently break the most ordinary agent workflow there is
// (edit a file, read it back).

func TestAnIdenticalRepeatedCallDoesNotPayFullPriceTwice(t *testing.T) {
	base, requests, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("Done."),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})

	// Big enough that the saving is unmistakable in the byte budget.
	body := strings.Repeat("payload line\n", 200)
	if err := os.WriteFile(s.workspace+"/inside.txt", []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("made %d model calls, want 3", got)
	}

	// The tool still ran both times -- semantics unchanged.
	if len(res.ToolNames) != 2 {
		t.Fatalf("ToolNames = %v, want the tool to have run twice (this must NOT skip the call)", res.ToolNames)
	}

	// The FIRST feedback carries the payload; the SECOND carries the stall note
	// instead. That is the saving.
	second := string((*bodies)[1])
	third := string((*bodies)[2])
	if !strings.Contains(second, "payload line") {
		t.Fatalf("the first tool result did not reach the model:\n%s", truncateForTest(second))
	}
	if strings.Count(third, "payload line") > strings.Count(second, "payload line") {
		t.Errorf("the repeated result was fed back in full a second time; the byte budget " +
			"pays twice to tell the model something it already has")
	}
	if !strings.Contains(third, "same call you already made") {
		t.Errorf("the model was not told it is repeating itself. Breaking the stall is the "+
			"point -- a silent saving just makes the loop spin more cheaply:\n%s", truncateForTest(third))
	}
}

// THE GUARD. An identical call whose result CHANGED is a legitimate re-read --
// the most ordinary agent workflow there is (edit a file, read it back) -- and
// must be fed through in full.
//
// NEUTER CHECK: compare on the signature alone instead of on the rendered
// bytes, and this fails -- the second read returns the edited content and the
// model never sees it.
func TestARepeatThatReturnsSomethingNewIsNotSuppressed(t *testing.T) {
	// The workspace must exist before the upstream stub can mutate it, so the
	// server is built first and the script is wired to it afterwards.
	dir := t.TempDir()
	path := dir + "/inside.txt"
	if err := os.WriteFile(path, []byte("BEFORE-the-change"), 0600); err != nil {
		t.Fatal(err)
	}

	// The file changes BETWEEN the two identical calls -- exactly as it would if
	// the model had edited it and then read it back. Driven from the upstream
	// stub because that is the one place guaranteed to run between the first
	// tool's completion and the second's dispatch, with no production hook and
	// no sleeping.
	var captured [][]byte
	var n int
	base := rawSSEServerFunc(t, func(body []byte) []string {
		captured = append(captured, body)
		n++
		switch n {
		case 1:
			return toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`)
		case 2:
			_ = os.WriteFile(path, []byte("AFTER-the-change"), 0600)
			return toolCallSSE("c2", "builtin__read_file", `{"path":"inside.txt"}`)
		default:
			return textSSE("Done.")
		}
	})

	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})
	// Point the server at the workspace whose file the stub mutates.
	s.workspace = dir

	if _, _, err := runLoop(t, s); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(captured) < 3 {
		t.Fatalf("only %d model calls; the script needs three", len(captured))
	}

	third := string(captured[2])
	if !strings.Contains(third, "AFTER-the-change") {
		t.Fatalf("a re-read that returned NEW content was suppressed as a repeat. Edit-then-verify "+
			"is the most ordinary agent workflow there is, and this would silently break it:\n%s",
			truncateForTest(third))
	}
	if strings.Contains(third, "same call you already made") {
		t.Errorf("a changed result was reported to the model as an unchanged repeat")
	}
}

func truncateForTest(s string) string {
	if len(s) > 900 {
		return s[:900] + "…"
	}
	return s
}
