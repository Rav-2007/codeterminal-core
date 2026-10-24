package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// Tests for localguard.go: a local model's turn stops offering tools after
// unusable calls in a row.

// runGuardedLoop is runLocalLoop, keeping the degradations the turn reported.
func runGuardedLoop(t *testing.T, base string) (agentResult, []protocol.Degradation, map[string]int64) {
	t.Helper()
	s := localLoopServer(t, base)
	registry, counts := localTools(t, s)
	var degraded []protocol.Degradation
	ctx := withClientCaps(context.Background(), protocol.HandshakeRequest{Capabilities: []string{protocol.CapToolApproval}})
	result, err := s.runAgentLoop(ctx, time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "what does greet.go do?"}}, providerRouting{},
		&capturingApprover{decision: protocol.ApprovalDeny},
		func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
		func(d protocol.Degradation) { degraded = append(degraded, d) }, nil, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	ran := map[string]int64{}
	for name, n := range counts {
		ran[name] = n.Load()
	}
	return result, degraded, ran
}

// lastMessage is the content of the last message a request sent.
func lastMessage(req map[string]any) string {
	messages, _ := req["messages"].([]any)
	if len(messages) == 0 {
		return ""
	}
	last, _ := messages[len(messages)-1].(map[string]any)
	return fmt.Sprint(last["content"])
}

// A LOCAL MODEL'S TURN STOPS OFFERING TOOLS AFTER TWO UNUSABLE CALLS IN A ROW,
// and answers from what it has. Before, a model that named tools that do not
// exist went on naming them, a round trip each, until the step limit ended the
// turn with no answer.
//
// Neuter check: make step never set stopped, and the third request still
// offers tools, with no note, and nothing tells the person.
func TestALocalModelsTurnStopsOfferingToolsAfterTwoUnusableCalls(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"tools"}, 8192, func(n int, _ map[string]any) (int, []string) {
		switch n {
		case 1:
			return 0, toolCallSSE("c1", "greetingFor", `{"name": "visitor"}`) // the function asked about, not a tool
		case 2:
			return 0, toolCallSSE("c2", "builtin__read_file", `"greet.go"`) // arguments that are not an object
		}
		return 0, textSSE("greetingFor welcomes a visitor")
	})
	result, degraded, ran := runGuardedLoop(t, local.base())

	reqs := local.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3: two unusable calls, then the answer", len(reqs))
	}
	if names := offeredNames(reqs[1]); len(names) == 0 {
		t.Error("the second request already had no tools: one unusable call must not stop them")
	}
	if names := offeredNames(reqs[2]); len(names) != 0 {
		t.Errorf("the request after two unusable calls still offered %v", names)
	}
	if got := lastMessage(reqs[2]); got != toolsStoppedNote {
		t.Errorf("the request after two unusable calls ended with %q, want the note telling the model to answer", got)
	}
	if result.FinalText != "greetingFor welcomes a visitor" {
		t.Errorf("answer = %q", result.FinalText)
	}
	if ran["read_file"] != 1 {
		t.Errorf("read_file ran %d time(s), want 1: an unusable call still gets the tool's own refusal", ran["read_file"])
	}
	stopped := 0
	for _, d := range degraded {
		if d.Component == protocol.DegradedLocalToolsStopped {
			stopped++
		}
	}
	if stopped != 1 {
		t.Errorf("the person was told %d time(s), want once: %+v", stopped, degraded)
	}
}

// A CALL THAT CAN RUN RESETS THE COUNT: two unusable calls apart are not in a
// row, and a model that recovers keeps its tools.
//
// Neuter check: drop `g.unusable = 0` from step, and the fourth request has no
// tools.
func TestAUsableCallResetsTheCount(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"tools"}, 8192, func(n int, _ map[string]any) (int, []string) {
		switch n {
		case 1:
			return 0, toolCallSSE("c1", "greetingFor", `{}`)
		case 2:
			return 0, toolCallSSE("c2", "builtin__read_file", `{"path": "greet.go"}`)
		case 3:
			return 0, toolCallSSE("c3", "greetingFor", `{}`)
		}
		return 0, textSSE("greetingFor welcomes a visitor")
	})
	_, degraded, _ := runGuardedLoop(t, local.base())
	reqs := local.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want 4", len(reqs))
	}
	if names := offeredNames(reqs[3]); len(names) == 0 {
		t.Error("tools were stopped after two unusable calls that a usable one came between")
	}
	if len(degraded) != 0 {
		t.Errorf("the person was told %+v, want nothing", degraded)
	}
}

// ONCE THE TOOLS ARE STOPPED, A CALL THE MODEL MAKES ANYWAY DOES NOT RUN. A
// server may still return a call it was not offered; it is dropped, and the
// reply's text is the answer.
//
// Neuter check: make step return calls when stopped, and read_file runs.
func TestACallAfterTheToolsStoppedDoesNotRun(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"tools"}, 8192, func(n int, _ map[string]any) (int, []string) {
		if n <= 2 {
			return 0, toolCallSSE(fmt.Sprintf("c%d", n), "greetingFor", `{}`)
		}
		return 0, []string{
			`data: {"choices":[{"delta":{"content":"greetingFor welcomes a visitor"}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c3","type":"function","function":{"name":"builtin__read_file","arguments":"{\"path\":\"greet.go\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		}
	})
	result, _, ran := runGuardedLoop(t, local.base())
	if ran["read_file"] != 0 {
		t.Errorf("read_file ran %d time(s) after the tools were stopped", ran["read_file"])
	}
	if len(local.requests()) != 3 {
		t.Errorf("%d requests, want 3: the reply with text ends the turn", len(local.requests()))
	}
	if result.FinalText != "greetingFor welcomes a visitor" {
		t.Errorf("answer = %q", result.FinalText)
	}
}

// ONLY A SERVER ON THIS MACHINE. A cloud model's calls are counted by nothing
// here: its turn is exactly as before.
func TestTheGuardLeavesEveryOtherProviderAlone(t *testing.T) {
	s := localLoopServer(t, "http://127.0.0.1:1/v1")
	var g localCallGuard
	unusable := []toolCall{{ID: "c1", Type: "function", Function: toolCallFunction{Name: "greetingFor", Arguments: "{}"}}}
	var full strings.Builder
	for i := 0; i < 3; i++ {
		if got := g.step(s, "https://openrouter.ai/api/v1", nil, unusable, &full, 0, nil); len(got) != 1 {
			t.Fatalf("a cloud model's call was dropped: %+v", got)
		}
	}
	if g.stopped || g.menu([]toolSpec{{}}) == nil {
		t.Error("a cloud model's tools were stopped")
	}
}

func TestUsableCall(t *testing.T) {
	menu := []toolSpec{
		{Type: "function", Function: toolSpecFunction{Name: "builtin__read_file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}},
		{Type: "function", Function: toolSpecFunction{Name: "builtin__list_directory", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	for _, tc := range []struct {
		name, tool, args string
		usable           bool
	}{
		{"a call as the menu names it", "builtin__read_file", `{"path":"a.go"}`, true},
		{"a built-in's bare name", "read_file", `{"path":"a.go"}`, true},
		{"no required arguments, none given", "list_directory", `{}`, true},
		{"a tool not on the menu", "repo_map", `{}`, false},
		{"a function from the question", "greetingFor", `{"name":"x"}`, false},
		{"arguments that are a string", "read_file", `"a.go"`, false},
		{"arguments that are a list", "read_file", `["a.go"]`, false},
		{"arguments that are null", "list_directory", `null`, false},
		{"a required argument left out", "read_file", `{"file":"a.go"}`, false},
	} {
		call := toolCall{Function: toolCallFunction{Name: tc.tool, Arguments: tc.args}}
		if got := usableCall(call, menu); got != tc.usable {
			t.Errorf("%s: usableCall = %v, want %v", tc.name, got, tc.usable)
		}
	}
}

// screenOfLoop runs one local turn and returns what reached the screen.
func screenOfLoop(t *testing.T, base string) (string, agentResult) {
	t.Helper()
	s := localLoopServer(t, base)
	registry, _ := localTools(t, s)
	var screen strings.Builder
	ctx := withClientCaps(context.Background(), protocol.HandshakeRequest{Capabilities: []string{protocol.CapToolApproval}})
	result, err := s.runAgentLoop(ctx, time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "what does greet.go do?"}}, providerRouting{},
		&capturingApprover{decision: protocol.ApprovalDeny},
		func(token string) error { screen.WriteString(token); return nil }, func(protocol.ToolActivity) {}, nil, nil,
		func(protocol.Degradation) {}, nil, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	return screen.String(), result
}

// A CALL WRITTEN AS TEXT NEVER REACHES THE SCREEN, nor a result the model made
// up after it. Measured 2026-10-24 in the real app: qwen2.5-coder:7b's reply
// to "run ls" was the call as JSON and then three files that do not exist,
// all of it on screen before the real ls listed two others.
//
// Neuter check: make show return onToken(token) at once, and the JSON and the
// made-up files are on the screen.
func TestACallWrittenAsTextIsNotShown(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"tools"}, 8192, func(n int, _ map[string]any) (int, []string) {
		if n == 1 {
			return 0, textSSE(`{"name": "builtin__run_command", "arguments": {"command": "ls"}}` + "\n.go files: main.go, utils.go")
		}
		return 0, textSSE("ls lists greet.go and quokka.go")
	})
	screen, result := screenOfLoop(t, local.base())
	if screen != "ls lists greet.go and quokka.go" {
		t.Errorf("the screen showed %q, want only the answer", screen)
	}
	if result.FinalText != screen {
		t.Errorf("the answer kept is %q, the screen %q", result.FinalText, screen)
	}
}

// A STEP THAT ONLY LOOKED LIKE A CALL IS SHOWN WHOLE when it ends.
//
// Neuter check: drop the onToken call from endStep, and the answer never
// reaches the screen.
func TestAnAnswerThatOnlyLookedLikeACallIsShown(t *testing.T) {
	forgetLocalModels(t)
	const answer = "```go\nfunc greetingFor(name string, visits int) string\n```\nIt welcomes a visitor."
	local := newScriptedLocal(t, []string{"tools"}, 8192, func(int, map[string]any) (int, []string) {
		return 0, textSSE(answer)
	})
	if screen, _ := screenOfLoop(t, local.base()); screen != answer {
		t.Errorf("the screen showed %q, want the whole answer", screen)
	}
}

// WHAT THE MODEL SAYS BEFORE A CALL IS SHOWN AND KEPT; the call and what
// follows it are neither. Measured 2026-10-24 in the real app: the 7B wrote
// "I'll start by listing the contents of the directory." and then the call.
//
// Neuter check: make splitAtCall find no call, and the JSON is on the screen.
func TestTheWordsBeforeACallWrittenAsTextAreShownAndKept(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"tools"}, 8192, func(n int, _ map[string]any) (int, []string) {
		if n == 1 {
			return 0, []string{
				`data: {"choices":[{"delta":{"content":"First I'll read the file. {\"na"}}]}`,
				`data: {"choices":[{"delta":{"content":"me\": \"builtin__read_file\", \"arguments\": {\"path\": \"greet.go\"}}\nIt says hello."}}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`,
			}
		}
		return 0, textSSE("greetingFor welcomes a visitor.")
	})
	screen, result := screenOfLoop(t, local.base())
	const want = "First I'll read the file. greetingFor welcomes a visitor."
	if screen != want {
		t.Errorf("the screen showed %q, want %q", screen, want)
	}
	if result.FinalText != want {
		t.Errorf("the answer kept is %q, want %q", result.FinalText, want)
	}
}

func TestShowHoldsOnlyWhatMayOpenACall(t *testing.T) {
	menu := []toolSpec{{Type: "function", Function: toolSpecFunction{Name: "builtin__read_file"}}}
	feed := func(base string, tools []toolSpec, ran bool, tokens ...string) string {
		var g localCallGuard
		var shown strings.Builder
		for _, tok := range tokens {
			_ = g.show(base, tools, tok, func(s string) error { shown.WriteString(s); return nil })
		}
		before := shown.String()
		g.endStep(ran)
		return before + "|" + strings.TrimPrefix(shown.String(), before)
	}
	const local, cloud = "http://127.0.0.1:11434/v1", "https://openrouter.ai/api/v1"
	for _, tc := range []struct {
		name   string
		base   string
		tools  []toolSpec
		ran    bool
		tokens []string
		want   string // shown while streaming | shown when the step ended
	}{
		{"prose streams as it arrives", local, menu, false, []string{"It ", "welcomes"}, "It welcomes|"},
		{"code is shown as soon as it is not a call", local, menu, false,
			[]string{"func f() {", " return 1 }"}, "func f() { return 1 }|"},
		{"a fence of code is shown as soon as it is not a call", local, menu, false,
			[]string{"```", "go\nfunc f()", " {}"}, "```go\nfunc f() {}|"},
		{"a call alone is held, and dropped once it ran", local, menu, true,
			[]string{`{"name": "read_file", `, `"arguments": {}}`}, "|"},
		{"a call in a fence after words: the words are shown", local, menu, true,
			[]string{"Reading it.\n```json\n", `{"name": "read_file"}` + "\n```"}, "Reading it.\n|"},
		{"a call that did not run is shown when the step ends", local, menu, false,
			[]string{`{"name": "Ada", "age": 36}`}, `|{"name": "Ada", "age": 36}`},
		{"a brace at the end waits for the next word", local, menu, false, []string{"set {", "x}"}, "set {x}|"},
		{"no tools on the menu: nothing is held", local, nil, true, []string{`{"name"`, `: 1}`}, `{"name": 1}|`},
		{"a cloud provider: nothing is held", cloud, menu, true, []string{`{"name"`, `: 1}`}, `{"name": 1}|`},
	} {
		if got := feed(tc.base, tc.tools, tc.ran, tc.tokens...); got != tc.want {
			t.Errorf("%s: shown %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCallOpening(t *testing.T) {
	for s, want := range map[string]int{
		`{"name": "x"`: openingCall, `{ "arguments"`: openingCall, "```json\n{\"parameters\"": openingCall,
		"```{\"name\"": openingCall, `{"na`: openingMaybe, `{`: openingMaybe, "```": openingMaybe, "```js": openingMaybe,
		"``": openingMaybe, `{"path": "a"}`: openingNone, `{x}`: openingNone, "```go\nfunc": openingNone, "`code`": openingNone,
	} {
		if got := callOpening(s); got != want {
			t.Errorf("callOpening(%q) = %d, want %d", s, got, want)
		}
	}
}
