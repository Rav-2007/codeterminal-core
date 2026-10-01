package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// AN INVESTIGATION RETURNS ITS ANSWER, NOT ITS READING. The sub-run greps in
// its own context; the main segment receives fifteen lines and none of what was
// read to find them. The sub-run's menu is read tools only -- no edit, no
// command, no investigate -- its text never streams to the user, and its calls
// count against the task's budget.
//
// Neuter checks: pass the main segment's messages to the sub-run instead of a
// fresh pair; or drop &roleInvestigator from the sub-run's runAgentLoop call,
// and its menu offers the edit tools and investigate itself.
func TestAnInvestigationReturnsItsAnswerNotItsReading(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("m1", "builtin__investigate", `{"question":"where is the retry budget set?"}`),
		toolCallSSE("n1", "builtin__grep", `{"pattern":"RETRY_BUDGET"}`),
		textSSE("ANSWER-7: the retry budget is set at budget.go:2."),
		toolCallSSE("m2", "builtin__record_finding", `{"claim":"the budget is set once","evidence":"budget.go:2"}`),
		toolCallSSE("m3", "builtin__finish_task", `{"status":"done","summary":"Found it."}`),
	)
	cfg := taskPolicies()
	cfg.Builtin.Tools["investigate"] = PolicyAllow
	cfg.Builtin.Tools["grep"] = PolicyAllow
	sockAddr, _, srv := agentSocketServer(t, base, cfg)
	if err := os.WriteFile(filepath.Join(srv.workspace, "budget.go"),
		[]byte("package retry\nconst RETRY_BUDGET = 45 // SECRET_READING_MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs := taskRequest(t, sockAddr, protocol.PromptRequest{Prompt: "find the retry budget", Mode: modeTask})
	if st := lastTaskStatus(t, msgs); st.State != protocol.TaskStateFinished {
		t.Fatalf("state = %q, want finished", st.State)
	}
	if len(*bodies) != 5 {
		t.Fatalf("model calls = %d, want 5 (main, two in the sub-run, two main; the finish's summary is the reply)",
			len(*bodies))
	}

	sub := sentMessages(t, (*bodies)[1])
	if len(sub) != 2 || !strings.Contains(sub[0].Content, investigatorDirective) || !strings.Contains(sub[1].Content, "retry budget") {
		t.Errorf("the sub-run did not start from its own two messages: %+v", sub)
	}
	subTools := strings.Join(offeredTools(t, (*bodies)[1]), " ")
	for _, banned := range []string{"propose_edit", "sandbox_exec", "investigate", "finish_task", "checkpoint"} {
		if strings.Contains(subTools, banned) {
			t.Errorf("the sub-run was offered %s: %s", banned, subTools)
		}
	}
	if !strings.Contains(string((*bodies)[2]), "SECRET_READING_MARKER") {
		t.Fatal("the sub-run never saw what it read; the test proves nothing")
	}

	after := string((*bodies)[3])
	if !strings.Contains(after, "ANSWER-7") {
		t.Error("the main segment did not receive the answer")
	}
	if strings.Contains(after, "SECRET_READING_MARKER") {
		t.Error("the sub-run's reading leaked into the main segment's context")
	}
	if strings.Contains(strings.Join(tokensOf(msgs), ""), "ANSWER-7") {
		t.Error("the sub-run's text was streamed to the user")
	}
}

// AN INVESTIGATION SPENDS THE TASK'S BUDGET. With two calls allowed, the main
// call that asked and the investigation's first grep are the two: the sub-run
// stops there and answers from what it has, and the main segment reports --
// rather than the sub-run grepping on past the budget.
//
// Neuter check: leave run.nested out of taskRun.spent, and the sub-run runs
// all its greps (6 calls, not 4).
func TestAnInvestigationSpendsTheTasksBudget(t *testing.T) {
	base, calls, bodies := agentUpstream(t,
		toolCallSSE("m1", "builtin__investigate", `{"question":"where is it?"}`),
		toolCallSSE("n1", "builtin__grep", `{"pattern":"x"}`),
		toolCallSSE("n2", "builtin__grep", `{"pattern":"y"}`),
		toolCallSSE("n3", "builtin__grep", `{"pattern":"z"}`),
		textSSE("ANSWER: nowhere yet."),
		textSSE("Report: out of budget."),
	)
	cfg := taskPolicies()
	cfg.Builtin.Tools["investigate"] = PolicyAllow
	cfg.Builtin.Tools["grep"] = PolicyAllow
	sockAddr, _, _ := agentSocketServer(t, base, cfg)
	msgs := taskRequest(t, sockAddr, protocol.PromptRequest{Prompt: "go", Mode: modeTask,
		TaskBudget: &protocol.TaskBudget{Calls: 2}})
	if st := lastTaskStatus(t, msgs); st.State != protocol.TaskStateBudget {
		t.Errorf("state = %q, want budget", st.State)
	}
	if calls.Load() != 4 {
		t.Errorf("model calls = %d, want 4: two counted, and each loop's answer at its limit", calls.Load())
	}
	// The sub-run's second request is its answer at the limit, not a third grep.
	if n := len(*bodies); n >= 3 {
		sub := sentMessages(t, (*bodies)[2])
		if last := sub[len(sub)-1].Content; last != investigatorWrapUpNote {
			t.Errorf("the sub-run's second request was not its answer at the budget: %q", last)
		}
	}
}
