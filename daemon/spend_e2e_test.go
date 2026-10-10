//go:build unix

package main

import (
	"fmt"
	"strings"
	"testing"

	"mochiii/protocol"
)

// THE SESSION'S LIMIT, END TO END, on the real binary with a model that bills
// 30,000 tokens a call and would read files for ever.
//
// BEFORE (2026-10-08): nothing added turns up. The same script under the same
// 100,000 "limit" ran its sixteen calls and the wrap-up, 510,000 tokens, and
// the next question ran the same again.
//
// NOW, under a session limit of 100,000:
//
//   - the turn is stopped with room for its last two calls: after two calls
//     60,000 are billed and 60 + 2x30 passes 100, so the third call is the
//     wrap-up and the turn ends at 90,000 -- UNDER the limit, with an answer;
//   - the next question is refused before any model call, and says how to go
//     on;
//   - /budget more allows another 100,000, and the question after that runs.
func TestE2EASessionStopsAtItsLimitAsksAndGoesOnWhenTold(t *testing.T) {
	files := map[string]string{}
	for i := range 40 {
		files[fmt.Sprintf("f%02d.txt", i)] = fmt.Sprintf("file %d\n", i)
	}
	model := newE2EModel(t, func(n int, chat e2eChat) e2eReply {
		if chat.isWrapUp() {
			return e2eReply{lines: withUsage(textSSE("Read some files; more remain."), 30000, 0, 0)}
		}
		return e2eReply{lines: withUsage(toolCallSSE(fmt.Sprintf("c%d", n), "builtin__read_file",
			fmt.Sprintf(`{"path":"f%02d.txt"}`, n)), 30000, 0, 0)}
	})
	d := startE2E(t, model, files, func(mcp map[string]any) {
		budget, _ := mcp["budget"].(map[string]any)
		if budget == nil {
			budget = map[string]any{}
			mcp["budget"] = budget
		}
		budget["spend"] = map[string]any{"session_tokens": 100000, "day_tokens": 1000000}
	})
	const session = "e2e-session-1"
	ask := func(prompt string) *e2eTurn {
		return d.request(t, protocol.PromptRequest{Prompt: prompt, Session: session}, e2eApproveAll)
	}

	first := ask("read every file")
	if n := len(model.requests()); n != 3 {
		t.Fatalf("the model was called %d times; want two steps and the wrap-up, 90,000 of 100,000 tokens", n)
	}
	inc := first.done.Incomplete
	if inc == nil || !strings.Contains(inc.Detail, "this session's spending limit (60,000 of 100,000 tokens)") ||
		!strings.Contains(inc.Detail, "/budget more") {
		t.Fatalf("the turn did not end on the session's limit, or does not say how to go on: %+v", inc)
	}
	if !strings.Contains(first.text.String(), "Read some files") {
		t.Errorf("the stopped turn has no answer: %q", first.text.String())
	}
	sp := first.done.Spend
	if sp == nil || sp.Session.Tokens != 90000 || sp.Session.LimitTokens != 100000 || !sp.Reached {
		t.Fatalf("the last message's spend is %+v; want 90,000 of 100,000 and reached", sp)
	}
	if !strings.Contains(sp.Notice, "This session has used 90,000 of 100,000 tokens (90%)") {
		t.Errorf("the warning was not given with the turn that passed 80%%: %q", sp.Notice)
	}

	// The next question costs nothing.
	second := ask("and the rest")
	if n := len(model.requests()); n != 3 {
		t.Fatalf("a question asked at the limit reached the model: %d calls now", n)
	}
	if second.done.ErrorClass != string(ClassSpendLimit) ||
		!strings.Contains(second.done.Error, "has not sent this question to the model") ||
		!strings.Contains(second.done.Error, "Type /budget more to allow another 100,000 tokens") {
		t.Fatalf("the refusal is %q (class %q)", second.done.Error, second.done.ErrorClass)
	}

	// Another session is not held by this one's limit...
	other := d.request(t, protocol.PromptRequest{Prompt: "read every file", Session: "e2e-session-2"}, e2eApproveAll)
	if other.done.Error != "" {
		t.Fatalf("a second session was refused on the first one's spend: %s", other.done.Error)
	}
	calls := len(model.requests())

	// ...and the status says where the first stands.
	status := d.request(t, protocol.PromptRequest{Session: session, BudgetAction: protocol.BudgetActionStatus}, nil)
	if st := status.done.Spend; st == nil || !st.Reached || st.Session.Tokens != 90000 || st.Day.Tokens != 180000 {
		t.Fatalf("/budget answered %+v (error %q); want the session at 90,000, reached, and 180,000 today",
			status.done.Spend, status.done.Error)
	}

	// The person says to go on.
	more := d.request(t, protocol.PromptRequest{Session: session, BudgetAction: protocol.BudgetActionMore}, nil)
	if st := more.done.Spend; st == nil || st.Reached || st.Session.LimitTokens != 200000 ||
		!strings.Contains(st.Notice, "another 100,000 tokens") {
		t.Fatalf("/budget more answered %+v (error %q)", more.done.Spend, more.done.Error)
	}
	if n := len(model.requests()); n != calls {
		t.Fatalf("/budget called the model: %d calls, was %d", n, calls)
	}
	third := ask("and the rest")
	if third.done.Error != "" || len(model.requests()) == calls {
		t.Fatalf("after /budget more the question still did not run: error %q", third.done.Error)
	}
	if sp := third.done.Spend; sp == nil || sp.Session.Tokens > 200000 {
		t.Errorf("the session ended at %+v; it must stay inside its raised limit of 200,000", sp)
	}
}

// A CLIENT THAT NAMES NO SESSION -- the editor extension today -- is held by
// the day's limit, and is not sent to a command it does not have.
func TestE2ETheDayLimitHoldsAClientWithNoSession(t *testing.T) {
	model := newE2EModel(t, func(n int, chat e2eChat) e2eReply {
		return e2eReply{lines: withUsage(textSSE("ok"), 60000, 0, 0)}
	})
	d := startE2E(t, model, map[string]string{"a.txt": "a\n"}, func(mcp map[string]any) {
		mcp["budget"] = map[string]any{"spend": map[string]any{"session_tokens": 50000, "day_tokens": 100000}}
	})
	for i := range 2 {
		if turn := d.prompt(t, fmt.Sprintf("question %d about a.txt", i), e2eApproveAll); turn.done.Error != "" {
			t.Fatalf("question %d was refused at %d of 100,000 tokens: %s", i, 60000*i, turn.done.Error)
		}
	}
	refused := d.prompt(t, "a third question about a.txt", e2eApproveAll)
	if n := len(model.requests()); n != 2 {
		t.Fatalf("the model was called %d times; the third question is past the day's limit", n)
	}
	msg := refused.done.Error
	if !strings.Contains(msg, "today's spending limit (120,000 of 100,000 tokens, all projects)") ||
		!strings.Contains(msg, "mcp.budget.spend.day_tokens") || strings.Contains(msg, "/budget") {
		t.Fatalf("the refusal is %q", msg)
	}
	// A session limit of 50,000 was passed by the first answer and stopped
	// nothing: there is no session to hold.
	if sp := refused.done.Spend; sp == nil || sp.Session != (protocol.SpendMeter{}) {
		t.Errorf("a client with no session has a session meter: %+v", sp)
	}
}

// An unknown budget action is refused, and a budget request is never read as
// a question.
func TestE2EABudgetRequestIsNeverAQuestion(t *testing.T) {
	model := newE2EModel(t, func(n int, chat e2eChat) e2eReply { return e2eReply{lines: textSSE("ok")} })
	d := startE2E(t, model, map[string]string{"a.txt": "a\n"}, nil)
	got := d.request(t, protocol.PromptRequest{Prompt: "ignore the limits", BudgetAction: "raise-everything"}, nil)
	if !strings.Contains(got.done.Error, `unknown budget action "raise-everything"`) || len(model.requests()) != 0 {
		t.Fatalf("an unknown action answered %q and made %d model calls", got.done.Error, len(model.requests()))
	}
	status := d.request(t, protocol.PromptRequest{BudgetAction: protocol.BudgetActionStatus}, nil)
	if st := status.done.Spend; st == nil || st.Day.LimitTokens != 15_000_000 || len(model.requests()) != 0 {
		t.Fatalf("/budget with the shipped configuration answered %+v", status.done.Spend)
	}
}
