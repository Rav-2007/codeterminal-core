package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// What counts as a call that taught the model nothing, and what never does.
// The end-to-end half -- the real daemon stopping a scripted model that
// wanders -- is in agentwaste_e2e_test.go.

func builtinTool(name string) mcp.Tool {
	return mcp.Tool{Server: mcp.BuiltinServerName, Name: name}
}

var (
	failed = mcp.Result{Content: "cannot read x: no such file", IsError: true}
	empty  = mcp.Result{Content: "No line matches", Empty: true}
)

// weigh runs one call through the turn and reports the text the model gets.
func weigh(t *agentTurn, tool, sig string, r mcp.Result) string {
	return t.weighResult(builtinTool(tool), sig, r, r.Content, false)
}

func TestFiveFailedLookUpsInARowEndTheTurnAndTheThirdSaysSo(t *testing.T) {
	turn := &agentTurn{}
	for i := 1; i <= 5; i++ {
		got := weigh(turn, "read_file", "read_file(guess"+string(rune('0'+i))+")", failed)
		steered := strings.HasSuffix(got, stallSteerNote)
		if steered != (i == 3) {
			t.Errorf("call %d: steering note present = %v, want it on the third failure and only there", i, steered)
		}
		if stop := turn.stall >= stallStopAt; stop != (i == 5) {
			t.Errorf("call %d: would stop = %v, want the stop at the fifth failure and not before", i, stop)
		}
	}
}

// A refused edit changes nothing, so it is the same as a failed read: the
// second case reproduced on 2026-10-08, seventeen model calls of it.
func TestRefusedEditsCountAsFailedLookUpsDo(t *testing.T) {
	turn := &agentTurn{}
	refused := mcp.Result{Content: "that edit cannot be applied: search text not found in main.go", IsError: true}
	for i := 0; i < 5; i++ {
		weigh(turn, "propose_edit", "propose_edit(v"+string(rune('0'+i))+")", refused)
	}
	if turn.stall < stallStopAt {
		t.Errorf("five refused edits in a row left stall at %d, under the stop at %d", turn.stall, stallStopAt)
	}
}

// "Nothing matches" is sometimes the thing being checked -- "is this name used
// anywhere?" six times over is an audit, not a loop. Half weight: ten in a row
// before the stop, six before a word is said.
func TestASearchThatFindsNothingCountsForHalf(t *testing.T) {
	turn := &agentTurn{}
	for i := 1; i <= 9; i++ {
		got := weigh(turn, "grep", "grep(name"+string(rune('0'+i))+")", empty)
		if steered := strings.HasSuffix(got, stallSteerNote); steered != (i == 6) {
			t.Errorf("empty search %d: steering note present = %v, want it on the sixth", i, steered)
		}
		if turn.stall >= stallStopAt {
			t.Fatalf("stopped after %d empty searches; six lookups that find nothing are an audit, not a loop", i)
		}
	}
	weigh(turn, "grep", "grep(name10)", empty)
	if turn.stall < stallStopAt {
		t.Error("ten empty searches in a row did not reach the stop")
	}
}

// ANYTHING NEW STARTS THE COUNT AGAIN. Four misses, a hit, four more misses is
// a model finding its way, and it must not be stopped at the fifth miss.
func TestNewsResetsTheCount(t *testing.T) {
	turn := &agentTurn{}
	for i := 0; i < 4; i++ {
		weigh(turn, "read_file", "read_file(miss"+string(rune('0'+i))+")", failed)
	}
	if got := weigh(turn, "read_file", "read_file(main.go)", mcp.Result{Content: "package main\n"}); got != "package main\n" {
		t.Errorf("a successful read was altered: %q", got)
	}
	if turn.stall != 0 {
		t.Fatalf("a successful read left stall at %d, want 0", turn.stall)
	}
	for i := 0; i < 4; i++ {
		weigh(turn, "read_file", "read_file(again"+string(rune('0'+i))+")", failed)
	}
	if turn.stall >= stallStopAt {
		t.Error("four misses after a hit reached the stop; the hit did not reset the count")
	}
	// An edit that was taken is progress too, and so is a steering note's
	// being heeded: the note can come again on a later run of misses.
	weigh(turn, "propose_edit", "propose_edit(a)", mcp.Result{Content: "staged"})
	if turn.stall != 0 {
		t.Errorf("a taken edit left stall at %d", turn.stall)
	}
	for i := 1; i <= 3; i++ {
		got := weigh(turn, "read_file", "read_file(third"+string(rune('0'+i))+")", failed)
		if i == 3 && !strings.HasSuffix(got, stallSteerNote) {
			t.Error("a second run of misses was not told so")
		}
	}
}

// The same bytes under another spelling of the call: read_file a.go and then
// ./a.go. The second teaches nothing and is not sent again.
func TestTheSameContentFromADifferentCallIsNotSentTwice(t *testing.T) {
	turn := &agentTurn{}
	body := mcp.Result{Content: "package a\n\nfunc A() {}\n"}
	if got := weigh(turn, "read_file", `read_file({"path":"a.go"})`, body); got != body.Content {
		t.Fatalf("the first read was altered: %q", got)
	}
	got := weigh(turn, "read_file", `read_file({"path":"./a.go"})`, body)
	if strings.Contains(got, "func A()") {
		t.Errorf("the same file was sent a second time: %q", got)
	}
	if !strings.Contains(got, `read_file({"path":"a.go"})`) {
		t.Errorf("the pointer does not say which earlier call returned this: %q", got)
	}
	if turn.stall != stallWeightFailed {
		t.Errorf("stall = %d after one repeat of earlier content, want %d", turn.stall, stallWeightFailed)
	}
	// The SAME call again is the older rule's to judge (it has its own note
	// and its own count); this one must not call it a different call's bytes.
	if got := weigh(turn, "read_file", `read_file({"path":"a.go"})`, body); got != body.Content {
		t.Errorf("the same call with the same result was rewritten here: %q", got)
	}
}

// A COMMAND'S FAILURE IS NEWS: a failing test prints why. So is every tool
// this file does not know -- a task tool that answers "recorded" each time, a
// third-party server -- for which the only stall is the same call returning
// the same bytes, and the loop already counts that.
func TestACommandsFailureAndAnUnknownToolAreNeverCounted(t *testing.T) {
	turn := &agentTurn{}
	testFailed := mcp.Result{Content: "--- FAIL: TestX\nexit status 1", IsError: true}
	recorded := mcp.Result{Content: "recorded"}
	for i := 0; i < 8; i++ {
		weigh(turn, "sandbox_exec", "sandbox_exec(go test)", testFailed)
		weigh(turn, "update_tasks", "update_tasks(x)", recorded)
		weigh(turn, "record_finding", "record_finding(y)", recorded)
		turn.weighResult(mcp.Tool{Server: "github", Name: "read_file"}, "github__read_file(z)", failed, failed.Content, false)
	}
	if turn.stall != 0 {
		t.Errorf("stall = %d after commands and unknown tools only, want 0", turn.stall)
	}
	// Except the one thing that is a stall for any tool: the loop saying this
	// is the same call with the same result.
	turn.weighResult(builtinTool("sandbox_exec"), "sandbox_exec(go test)", testFailed, "same call, same result", true)
	if turn.stall != stallWeightFailed {
		t.Errorf("an identical repeat of a command did not count: stall = %d", turn.stall)
	}
}

// Every built-in the loop can offer is sorted on purpose, not by falling
// through to "other". A new tool that reads or edits and is not listed here
// would never be counted, with nothing to say so.
func TestEveryBuiltinToolIsSortedOnPurpose(t *testing.T) {
	deliberatelyOther := map[string]bool{
		// Commands: their failure is news.
		"sandbox_exec": true,
		// A task's own record: saying the same thing back is their job.
		"record_criterion": true, "update_tasks": true, "record_finding": true,
		"finish_task": true, "checkpoint": true,
		// Changes many files, or runs a model of its own.
		"rename_symbol": true, "investigate": true,
	}
	s := builtinTestServer(t)
	for _, mode := range []string{"", modePlan, modeSpec, modeBuild, modeCheck, modeDebug} {
		for _, b := range s.builtinTools(&proposalSink{}, mode) {
			_, sorted := stallKinds[b.Tool.Name]
			if !sorted && !deliberatelyOther[b.Tool.Name] {
				t.Errorf("built-in %q (mode %q) is neither in stallKinds nor listed here as deliberately uncounted", b.Tool.Name, mode)
			}
		}
	}
}

// The token ceiling keeps back room for one more step and the wrap-up, each at
// least as large as the last request. What that means in numbers.
func TestTheTokenCeilingKeepsRoomForTheLastTwoCalls(t *testing.T) {
	for _, c := range []struct {
		total, last, ceiling int
		spent                bool
	}{
		{0, 0, 600_000, false},            // nothing billed yet: the first call is always made
		{100_000, 50_000, 240_000, false}, // 100 + 2x50 = 200: room
		{150_000, 50_000, 240_000, true},  // 150 + 2x50 = 250: no room for both
		{140_000, 50_000, 240_000, false}, // exactly at the ceiling fits
		{139_999, 50_001, 240_000, true},
		{5_000_000, 100_000, 0, false}, // no ceiling
		{10, 4_000, 600_000, false},
	} {
		if got := tokensSpent(c.total, c.last, c.ceiling); got != c.spent {
			t.Errorf("tokensSpent(%d, %d, %d) = %v, want %v", c.total, c.last, c.ceiling, got, c.spent)
		}
	}
	// A provider that reports no usage: nothing to count, so no stop from this
	// ceiling -- the ceiling in calls still binds.
	var none *usageTally
	if total, last := none.tokens(); total != 0 || last != 0 {
		t.Errorf("a missing tally reports %d, %d", total, last)
	}
	if groupThousands(376000) != "376,000" || groupThousands(999) != "999" || groupThousands(1000) != "1,000" || groupThousands(-12345) != "-12,345" {
		t.Error("groupThousands does not group in threes")
	}
}

// A TOOL THAT WORKED AND FOUND NOTHING SAYS SO TO THE LOOP (mcp.Result.Empty).
// The stall count reads that flag and nothing else: a search with no match is
// not an error, so without the flag it looks like news. Each of the three
// tools that can come back with nothing is checked, with a result that has
// something beside it so the flag cannot simply be always on.
func TestALookUpThatFoundNothingIsMarkedEmpty(t *testing.T) {
	s := builtinTestServer(t)
	writeFiles(t, s.workspace, map[string]string{"main.go": "package main\n\nfunc Run() {}\n"})
	if err := os.MkdirAll(filepath.Join(s.workspace, "hollow"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for name, tc := range map[string]struct {
		handler      mcp.Handler
		empty, found string
	}{
		"grep":           {s.builtinGrep, `{"pattern":"func NoSuchThing"}`, `{"pattern":"func Run"}`},
		"list_directory": {s.builtinListDirectory, `{"path":"hollow"}`, `{"path":"."}`},
	} {
		if res, err := tc.handler(ctx, json.RawMessage(tc.empty)); err != nil || res.IsError || !res.Empty {
			t.Errorf("%s found nothing and did not say so: empty=%t error=%t %v\n%s", name, res.Empty, res.IsError, err, res.Content)
		}
		if res, err := tc.handler(ctx, json.RawMessage(tc.found)); err != nil || res.IsError || res.Empty {
			t.Errorf("%s found something and is marked empty: %v\n%s", name, err, res.Content)
		}
	}

	// search_code, over an index that holds nothing for the query.
	s.embedder, s.store = &fakeEmbedder{dim: embedDim}, fixedStore{}
	s.retrievalTopK, s.contextBudgetChars, s.rerankDisabled = 5, 1<<20, true
	res, err := s.builtinSearchCode(ctx, json.RawMessage(`{"query":"where is the retry limit"}`))
	if err != nil || res.IsError || !res.Empty {
		t.Errorf("search_code found nothing and did not say so: empty=%t error=%t %v\n%s", res.Empty, res.IsError, err, res.Content)
	}
	if strings.Contains(res.Content, "cannot search") {
		t.Errorf("a search that ran and found nothing is reported as a search that could not run: %q", res.Content)
	}
	// One that could not run is still an error, and says why.
	s.embedder, s.store, s.retrievalDisabledReason = nil, nil, "no index for this project yet"
	res, err = s.builtinSearchCode(ctx, json.RawMessage(`{"query":"where is the retry limit"}`))
	if err != nil || !res.IsError || res.Empty || !strings.Contains(res.Content, "cannot search") {
		t.Errorf("search_code without an index answered: empty=%t error=%t %v\n%s", res.Empty, res.IsError, err, res.Content)
	}
}
