package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mochiii/protocol"
)

// THE PROVIDER'S OWN WORDS, copied from a real daemon log (2026-10-23 and
// 2026-10-24, Groq's free tier; the organisation id is replaced). The
// end-to-end tests in sizerefusal_e2e_test.go answer with the same bodies.
const (
	// 2026-10-24 15:01:20, the second call of a fresh chat's first turn.
	refusal413ITPMBody = `{"error":{"message":"Request too large for model ` + "`qwen/qwen3.8-27b`" + ` in organization ` +
		"`org_test`" + ` service tier ` + "`on_demand`" + ` on input tokens per minute (ITPM): Limit 7000, Requested 7941, ` +
		`please reduce your message size and try again. Need more tokens? Upgrade to Dev Tier today at ` +
		`https://console.groq.com/settings/billing","type":"tokens","code":"rate_limit_exceeded"}}`
	// 2026-10-23 23:41:11, the first call of a turn carrying six turns of chat.
	refusal413TPMBody = `{"error":{"message":"Request too large for model ` + "`openai/gpt-oss-120b`" + ` in organization ` +
		"`org_test`" + ` service tier ` + "`on_demand`" + ` on tokens per minute (TPM): Limit 8000, Requested 9191, ` +
		`please reduce your message size and try again. Need more tokens? Upgrade to Dev Tier today at ` +
		`https://console.groq.com/settings/billing","type":"tokens","code":"rate_limit_exceeded"}}`
	// 2026-10-23 23:34:15: a throttle, not a size refusal -- this request fits,
	// the minute it was sent in did not.
	refusal429TPMBody = `{"error":{"message":"Rate limit reached for model ` + "`openai/gpt-oss-120b`" + ` in organization ` +
		"`org_test`" + ` service tier ` + "`on_demand`" + ` on tokens per minute (TPM): Limit 8000, Used 3065, Requested 6340. ` +
		`Please try again in 10.5375s. Need more tokens? Upgrade to Dev Tier today at ` +
		`https://console.groq.com/settings/billing","type":"tokens","code":"rate_limit_exceeded"}}`
	// 2026-10-24 15:02:16: the OUTPUT allowance, which no shorter input changes.
	refusal429OTPMBody = `{"error":{"message":"Request too large for model ` + "`qwen/qwen3.8-27b`" + ` in organization ` +
		"`org_test`" + ` service tier ` + "`on_demand`" + ` on output tokens per minute (OTPM): Limit 1000, Requested 1448. ` +
		`The request's expected output tokens exceed the enforced limit; reduce max_tokens (or the request's expected ` +
		`output) and try again. Need more tokens? Upgrade to Dev Tier today at https://console.groq.com/settings/billing",` +
		`"type":"tokens","code":"rate_limit_exceeded"}}`
	// A context-window refusal in OpenAI's words (code context_length_exceeded).
	// Not from this project's logs: no provider in them has refused one yet.
	refusalContextWindowBody = `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your ` +
		`messages resulted in 9191 tokens. Please reduce the length of the messages.","type":"invalid_request_error",` +
		`"param":"messages","code":"context_length_exceeded"}}`
)

func TestSizeLimitFromReadsEachRealShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want sizeLimit
	}{
		{"413 ITPM", refusal413ITPMBody, sizeLimit{limit: 7000, requested: 7941, per: "minute"}},
		{"413 TPM", refusal413TPMBody, sizeLimit{limit: 8000, requested: 9191, per: "minute"}},
		{"429 OTPM", refusal429OTPMBody, sizeLimit{limit: 1000, requested: 1448, per: "minute", output: true}},
		// A throttle: the request fits, the minute it was sent in did not.
		{"429 TPM throttle", refusal429TPMBody, sizeLimit{}},
		{"context window (OpenAI)", refusalContextWindowBody, sizeLimit{limit: 8192, requested: 9191}},
		{"context window (OpenRouter)", `{"error":{"message":"This endpoint's maximum context length is 131072 tokens. ` +
			`However, you requested about 140211 tokens (140211 of text input).","code":400}}`, sizeLimit{limit: 131072, requested: 140211}},
		{"no figures", `{"error":{"message":"context_length_exceeded"}}`, sizeLimit{}},
	} {
		if got := sizeLimitFrom(tc.body); got != tc.want {
			t.Errorf("%s: sizeLimitFrom = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestSizeLimitMessageNamesTheNumbersAndTheWayOut(t *testing.T) {
	for _, tc := range []struct {
		name    string
		s       sizeLimit
		want    []string
		notWant []string
	}{
		{"a minute held by the step before", sizeLimit{limit: 7000, requested: 7941, per: "minute", used: 6345},
			[]string{"7,941 tokens", "7,000 a minute", "6,345 of the same minute", "/model", "/connect"}, []string{"/compact the chat"}},
		{"too much that cannot be left out", sizeLimit{limit: 5000, requested: 6345, per: "minute", carried: 5900},
			[]string{"6,345 tokens", "5,000 a minute", "5,900 of them could not be left out", "/model", "/connect"}, []string{"/compact the chat"}},
		{"nothing that can be left out", sizeLimit{limit: 5000, requested: 6345, per: "minute", carried: 6345},
			[]string{"6,345 tokens", "5,000 a minute", "none of it could be left out", "/model", "/connect"}, []string{"/compact the chat"}},
		{"output", sizeLimit{limit: 1000, requested: 1448, per: "minute", output: true},
			[]string{"1,448 tokens", "1,000 output tokens a minute", "shorter chat does not change", "/model"}, []string{"/compact"}},
		{"context window", sizeLimit{limit: 8192, requested: 9191},
			[]string{"9,191 tokens", "at most 8,192", "/compact the chat", "larger context window"}, []string{"/connect"}},
	} {
		msg := tc.s.message()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: %q does not say %q", tc.name, msg, w)
			}
		}
		for _, w := range tc.notWant {
			if strings.Contains(msg, w) {
				t.Errorf("%s: %q says %q", tc.name, msg, w)
			}
		}
	}
	if got := thousands(1234567); got != "1,234,567" {
		t.Errorf("thousands(1234567) = %q", got)
	}
}

// What gives way first, and what never does.
func TestShrinkMessagesTakesTheOldestFirstAndNeverTheQuestion(t *testing.T) {
	big := func(c byte) string { return strings.Repeat(string(c), 4000) }
	call := func(id string) chatMessage {
		return chatMessage{Role: "assistant", ToolCalls: []toolCall{{ID: id, Function: toolCallFunction{Name: "read_file", Arguments: `{"path":"a"}`}}}}
	}
	msgs := []chatMessage{
		{Role: "system", Content: big('s')},
		{Role: "user", Content: big('1')}, {Role: "assistant", Content: big('2')},
		{Role: "user", Content: big('3')}, {Role: "assistant", Content: big('4')},
		{Role: "user", Content: "the question"},
		call("c1"), {Role: "tool", ToolCallID: "c1", Content: big('o')},
		call("c2"), {Role: "tool", ToolCallID: "c2", Content: big('n')},
	}
	const prompt = 5
	unchanged := func(t *testing.T, sh shrink) {
		t.Helper()
		if sh.messages[0].Content != msgs[0].Content {
			t.Error("the system message changed")
		}
		q := prompt - sh.dropped
		if sh.messages[q].Content != "the question" || len(sh.messages) != len(msgs)-sh.dropped {
			t.Errorf("the question moved or changed: %+v", sh.messages[q])
		}
		for _, m := range sh.messages {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
				t.Error("a tool call changed")
			}
		}
	}

	t.Run("the older tool result first", func(t *testing.T) {
		sh, ok := shrinkMessages(msgs, prompt, 1000)
		if !ok || sh.results != 1 || sh.dropped != 0 || sh.clipped != 0 || sh.messages[7].Content != shrunkResult {
			t.Fatalf("ok=%v results=%d dropped=%d clipped=%d", ok, sh.results, sh.dropped, sh.clipped)
		}
		if sh.messages[9].Content != big('n') {
			t.Error("the newest result was touched before the chat")
		}
		unchanged(t, sh)
	})
	t.Run("then the chat, a whole exchange at a time", func(t *testing.T) {
		sh, ok := shrinkMessages(msgs, prompt, 5000)
		if !ok || sh.results != 1 || sh.dropped != 2 || sh.clipped != 0 {
			t.Fatalf("ok=%v results=%d dropped=%d clipped=%d", ok, sh.results, sh.dropped, sh.clipped)
		}
		if sh.messages[1].Role != "user" || sh.messages[1].Content != big('3') {
			t.Errorf("what is left of the chat starts with %q", sh.messages[1].Content[:1])
		}
		unchanged(t, sh)
	})
	t.Run("then the newest result, cut short", func(t *testing.T) {
		// 3,890 from the older result and 16,000 from the chat leave 2,110
		// for the newest.
		sh, ok := shrinkMessages(msgs, prompt, 22000)
		if !ok || sh.dropped != 4 || sh.clipped != 1 || sh.saved < 22000 {
			t.Fatalf("ok=%v results=%d dropped=%d clipped=%d saved=%d", ok, sh.results, sh.dropped, sh.clipped, sh.saved)
		}
		last := sh.messages[len(sh.messages)-1].Content
		if !strings.HasSuffix(last, clippedResult) || len(last) < keepOfNewest {
			t.Errorf("the newest result is %d bytes", len(last))
		}
		unchanged(t, sh)
	})
	t.Run("and when that is not enough, nothing", func(t *testing.T) {
		sh, ok := shrinkMessages(msgs, prompt, 1<<20)
		if ok || sh.messages != nil || sh.saved == 0 {
			t.Fatalf("ok=%v saved=%d", ok, sh.saved)
		}
	})
}

// sizeReply is one scripted answer: a refusal (status and body) or SSE lines.
type sizeReply struct {
	status int
	body   string
	lines  []string
}

// sizeUpstream answers request n with replies[n] (the last repeats) and keeps
// every request body.
func sizeUpstream(t *testing.T, replies ...sizeReply) (base string, bodies func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(got)
		got = append(got, string(b))
		mu.Unlock()
		rep := replies[min(n, len(replies)-1)]
		if rep.status != 0 {
			w.WriteHeader(rep.status)
			_, _ = w.Write([]byte(rep.body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range rep.lines {
			_, _ = fmt.Fprintf(w, "%s\n\n", line)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// overBy is a 413 a little over an 8,000-token limit: room enough that cutting
// a long tool result short brings the request under.
var overBy = sizeReply{status: http.StatusRequestEntityTooLarge,
	body: strings.Replace(refusal413TPMBody, "Requested 9191", "Requested 9000", 1)}

// ONE SHRINK PER TURN, AND A LONG TASK IS ONE TURN. Segment 1 is refused after
// a long read, cut it short and went on; segment 2 is refused the same way,
// with a long read it could cut too -- and does not: the task ends there.
//
// Neuter check: drop run.shrunk from the segment's ledger (longtask.go), and
// segment 2 cuts its read short and sends again.
func TestALongTaskShrinksOnceForTheWholeRunNotOncePerSegment(t *testing.T) {
	read := sizeReply{lines: toolCallSSE("r", "builtin__read_file", `{"path":"long.txt"}`)}
	base, bodies := sizeUpstream(t,
		read, overBy, // segment 1: a long read, refused
		sizeReply{lines: toolCallSSE("u", "builtin__update_tasks", `{"tasks":[{"id":"1","title":"Read","status":"active"}]}`)},
		sizeReply{lines: textSSE("HANDOFF: read long.txt again.")}, // segment 1's checkpoint
		read, overBy, // segment 2: the same, refused
		sizeReply{lines: textSSE("should not be asked")},
	)
	cfg := taskPolicies()
	cfg.Budget.Task = &MCPTaskBudgetConfig{SegmentCalls: 3}
	s := loopServer(t, base, cfg)
	if err := os.WriteFile(filepath.Join(s.workspace, "long.txt"), []byte(strings.Repeat("line of the long file\n", 600)), 0o644); err != nil {
		t.Fatal(err)
	}
	run, _, runIt := directTaskRun(t, s, modeTask)
	_, _ = runIt(context.Background())

	got := bodies()
	if len(got) != 6 {
		t.Fatalf("the model saw %d requests, want 6: segment 1 refused, cut and sent again, its checkpoint, "+
			"then segment 2 refused and ended (state %q)", len(got), run.ledger.State)
	}
	if !strings.Contains(got[2], "the rest of this output was removed") {
		t.Fatal("segment 1 did not send its request again smaller; the test proves nothing")
	}
	if run.ledger.State != protocol.TaskStateFailed {
		t.Errorf("state %q, want failed on segment 2's refusal", run.ledger.State)
	}
}

// AN INVESTIGATION NEVER SHRINKS: what it streams reaches the model, not the
// user, so a request made smaller there would be made without telling them.
// Refused, the investigation fails, as it did before.
//
// Neuter check: start the investigator's ledger unshrunk (investigate.go), and
// its long read is cut short and sent again, unannounced.
func TestAnInvestigationDoesNotShrinkUnseen(t *testing.T) {
	base, bodies := sizeUpstream(t,
		sizeReply{lines: toolCallSSE("m1", "builtin__investigate", `{"question":"what does long.txt say?"}`)},
		sizeReply{lines: toolCallSSE("n1", "builtin__read_file", `{"path":"long.txt"}`)},
		overBy,
		sizeReply{lines: toolCallSSE("m2", "builtin__finish_task", `{"status":"done","summary":"Could not look."}`)},
	)
	cfg := taskPolicies()
	cfg.Builtin.Tools["investigate"] = PolicyAllow
	sockAddr, _, srv := agentSocketServer(t, base, cfg)
	if err := os.WriteFile(filepath.Join(srv.workspace, "long.txt"), []byte(strings.Repeat("line of the long file\n", 600)), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs := taskRequest(t, sockAddr, protocol.PromptRequest{Prompt: "read long.txt", Mode: modeTask})

	got := bodies()
	// The third request is the investigation's own, refused, holding the read.
	if len(got) < 3 || !strings.Contains(got[2], mustJSON(investigatorDirective)[1:20]) || !strings.Contains(got[2], "line of the long file") {
		t.Fatalf("the refused request was not the investigation's, holding the read (%d requests); the test proves nothing", len(got))
	}
	for i, b := range got {
		if strings.Contains(b, "the rest of this output was removed") {
			t.Errorf("request %d carries a read cut short inside the investigation", i+1)
		}
	}
	if strings.Contains(strings.Join(tokensOf(msgs), ""), shrinkNoticeLead) {
		t.Error("a shrink notice reached the user from inside an investigation")
	}
}

// shrinkNoticeLead begins every shrink notice (shrink.notice).
const shrinkNoticeLead = "\n\n[the request was"

// Nothing removable is all of it, at every request size: the float form gave
// one token short for about one size in seven, among them the 26,162 bytes CI
// sent in TestE2ESizeRefusalWithNothingToRemoveSaysWhatCannotBeLeftOut.
func TestNothingRemovableCarriesEveryToken(t *testing.T) {
	for total := 25000; total < 27500; total++ {
		if got := carriedTokens(6345, total, 0); got != 6345 {
			t.Fatalf("carriedTokens(6345, %d, 0) = %d, want all 6345", total, got)
		}
	}
	if got := carriedTokens(6345, 26162, 13081); got != 3172 {
		t.Errorf("half the bytes removable carries %d tokens, want 3172", got)
	}
	if got := carriedTokens(6345, 0, 0); got != 0 {
		t.Errorf("no bytes at all carries %d tokens, want 0", got)
	}
}
