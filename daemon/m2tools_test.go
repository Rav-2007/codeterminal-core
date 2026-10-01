package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// A LONG OUTPUT KEEPS ITS FAILURES AND ITS END. Clipped to its first bytes, a
// long failing test run reached the model as passing tests and no failure.
//
// Neuter check: return output unchanged from commandDigest, and the FAIL line
// in the middle and the verdict at the end are both cut.
func TestALongCommandOutputKeepsItsFailuresAndItsEnd(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		switch i {
		case 2500:
			b.WriteString("--- FAIL: TestTotal (0.00s)\n    total_test.go:12: got 4, want 5\n")
		default:
			fmt.Fprintf(&b, "=== RUN   TestCase%04d\n--- PASS: TestCase%04d (0.00s)\n", i, i)
		}
	}
	b.WriteString("FAIL\nFAIL\texample.com/calc\t0.412s\n")
	out := b.String()
	limit := 31 << 10
	d := commandDigest(out, limit)
	if len(d) > limit {
		t.Errorf("the digest is %d bytes, over its limit of %d", len(d), limit)
	}
	for _, want := range []string{"--- FAIL: TestTotal", "got 4, want 5", "FAIL\texample.com/calc", "=== RUN   TestCase0000", "line(s) left out"} {
		if !strings.Contains(d, want) {
			t.Errorf("the digest lost %q", want)
		}
	}
	if short := "ok  \texample.com/calc\t0.01s\n"; commandDigest(short, limit) != short {
		t.Error("an output that fits was changed")
	}
}

// EXTRA PROGRAMS RUN ONLY WHERE COMMANDS ARE CONFINED. Each is as able to run
// arbitrary code as `make`; the sandbox is what contains that, so without one
// the list does not apply -- and says why. Shells and the like are refused when
// the config loads.
//
// Neuter check: drop the !confined branch in execRefusalWith.
func TestExtraProgramsRunOnlyWhereCommandsAreConfined(t *testing.T) {
	extras := []string{"pytest", "python3"}
	if why := execRefusalWith("pytest", extras, true); why != "" {
		t.Errorf("a configured program was refused on a confined host: %q", why)
	}
	if why := execRefusalWith("pytest", extras, false); !strings.Contains(why, "install bubblewrap") {
		t.Errorf("a configured program ran with nothing to confine it: %q", why)
	}
	if why := execRefusalWith("node", extras, true); !strings.Contains(why, "go, npm, make, cargo, pytest or python3") {
		t.Errorf("an unlisted program's refusal = %q", why)
	}
	if why := execRefusalWith("python3", nil, true); !strings.Contains(why, "not one of go, npm, make or cargo") {
		t.Errorf("with no extras the refusal changed: %q", why)
	}
	// And this host's own answer agrees with the rule.
	s := builtinTestServer(t)
	s.cfg.MCP.Builtin.ExtraPrograms = extras
	if got, want := s.execRefusal("pytest") == "", s.sandboxExecConfined(); got != want {
		t.Errorf("on this host pytest allowed = %v, but commands confined = %v", got, want)
	}
	for _, bad := range []string{"bash", "sh", "sudo", "curl", "/usr/bin/python3", "py test", "-x", "git"} {
		if validateExtraPrograms([]string{bad}) == nil {
			t.Errorf("extra_programs accepted %q", bad)
		}
	}
	if err := validateExtraPrograms([]string{"pytest", "python3", "node", "deno", "bun", "zig"}); err != nil {
		t.Errorf("ordinary test runners were refused: %v", err)
	}
}

// A LONG TASK'S COMMANDS RUN LONGER, AND ITS APPROVAL SAYS SO: the timeout the
// handler applies is the one the approving human reads.
func TestALongTasksCommandsRunLongerAndSaySo(t *testing.T) {
	if got := execTimeoutFor(nil); got != execTimeout {
		t.Errorf("an ordinary turn's timeout = %s", got)
	}
	run := &taskRun{budget: resolveTaskBudget(nil, nil)}
	p := &proposalSink{task: run}
	if got := execTimeoutFor(p); got != 5*time.Minute {
		t.Errorf("a long task's timeout = %s, want 5 min", got)
	}
	s := builtinTestServer(t)
	for _, b := range s.builtinTools(p, modeDebug) {
		if b.Tool.Name == "sandbox_exec" && !strings.Contains(b.Tool.Description, "with a 5 min timeout") {
			t.Errorf("the approval text does not say 5 min: %q", b.Tool.Description)
		}
	}
}

// RANGED READS: a long task can read lines 2000-2010 of a file far past
// read_file's 64 KB, numbered so a later edit or a finding can cite them.
func TestReadFileReturnsTheLinesAsked(t *testing.T) {
	s := builtinTestServer(t)
	var b strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&b, "line %d of a long file with enough text to pass sixty-four kilobytes\n", i)
	}
	if err := os.WriteFile(filepath.Join(s.workspace, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(args map[string]any) (string, bool) {
		raw, _ := json.Marshal(args)
		res, err := s.builtinReadFile(context.Background(), raw)
		return res.Content, err == nil && !res.IsError
	}
	out, ok := read(map[string]any{"path": "big.txt", "start_line": 4000, "end_line": 4002})
	if !ok || !strings.Contains(out, "4000| line 4000 of") || !strings.Contains(out, "4002| line 4002 of") || strings.Contains(out, "line 4003 of") {
		t.Errorf("lines 4000-4002 = %q", out)
	}
	if out, ok := read(map[string]any{"path": "big.txt", "start_line": 6000}); ok {
		t.Errorf("a start past the end was accepted: %q", out)
	}
	if out, ok := read(map[string]any{"path": "big.txt"}); !ok || !strings.Contains(out, "truncated") || strings.Contains(out, "1| line 1") {
		t.Errorf("a plain read changed: %q", out[:min(len(out), 200)])
	}
}

// WHAT A LONG TASK IS OFFERED IS WHAT IT CAN CALL, as the real registry builds
// it: every one of its tools registers (git_history needs a launch probe to),
// the withheld web tools are not on the menu at all, and nothing is cut by the
// cap. Two defects the real binary found and the unit tests did not: the web
// tools advertised-then-refused, and git_history refused at registration.
//
// Neuter check: drop isLongTaskMode from builtinTools' filter, or the Launch
// probe from gitHistoryTool.
func TestALongTasksRegistryAdvertisesWhatItCanCall(t *testing.T) {
	s := builtinTestServer(t)
	s.cfg.MCP.Enabled = true // agent mode, as the shipped models.agent.json runs it
	p := &proposalSink{task: &taskRun{ledger: &taskLedger{}, budget: resolveTaskBudget(nil, nil)}}
	registry, _ := s.buildRegistry(context.Background(), s.logger, p, modeDebug)
	t.Cleanup(func() { _ = registry.Close() })
	tools, _ := registry.Advertised(context.Background())
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	got := " " + strings.Join(names, " ") + " "
	for _, want := range []string{"git_history", "grep", "record_finding", "finish_task", "sandbox_exec", "propose_edit"} {
		if !strings.Contains(got, " "+want+" ") {
			t.Errorf("a long task's menu lacks %s: %v", want, names)
		}
	}
	for _, absent := range []string{"web_search", "web_fetch"} {
		if strings.Contains(got, " "+absent+" ") {
			t.Errorf("a long task's menu offers %s, which it withholds: %v", absent, names)
		}
	}
	if dropped := registry.Dropped(); len(dropped) > 0 {
		t.Errorf("the cap cut %v from a long task's menu", dropped)
	}
}

// A REPEATABLE LAUNCH IS ASKED ONCE FOR THE TASK: git_history starts git on
// every call, and "yes for this whole task" covers its later launches -- while
// a plain "yes" covers only the call it answered. (A language server's launch
// is not Repeatable: TestATurnGrantNeverCoversARelaunch still holds for it.)
//
// Neuter check: drop Repeatable from gitHistoryTool's probe, and every call asks.
func TestARepeatableLaunchIsAskedOnceForTheTask(t *testing.T) {
	script := func() []([]string) {
		return [][]string{
			toolCallSSE("g1", "builtin__git_history", `{"command":"log"}`),
			toolCallSSE("g2", "builtin__git_history", `{"command":"log","max":3}`),
			toolCallSSE("g3", "builtin__git_history", `{"command":"show"}`),
			toolCallSSE("f1", "builtin__finish_task", `{"status":"done","summary":"looked"}`),
			textSSE("done"),
		}
	}
	asks := func(msgs []protocol.TokenResponse) (n int) {
		for _, m := range msgs {
			if m.ToolApproval != nil {
				n++
			}
		}
		return n
	}
	cfg := taskPolicies()
	cfg.Builtin.Tools["git_history"] = PolicyAsk

	base, _, _ := agentUpstream(t, script()...)
	sockAddr, _, _ := agentSocketServer(t, base, cfg)
	msgs := taskRequest(t, sockAddr, protocol.PromptRequest{Prompt: "look at the history", Mode: modeTask})
	if n := asks(msgs); n != 1 {
		t.Errorf("approve-for-turn on a repeatable launch: asked %d times, want once", n)
	}

	// A plain approve answers one call only.
	base2, _, _ := agentUpstream(t, script()...)
	sockAddr2, _, _ := agentSocketServer(t, base2, cfg)
	msgs2 := taskRequestAnswering(t, sockAddr2, protocol.PromptRequest{Prompt: "look at the history", Mode: modeTask},
		protocol.ApprovalApprove)
	if n := asks(msgs2); n != 3 {
		t.Errorf("a plain approve on a repeatable launch: asked %d times, want once per call (3)", n)
	}
}

// THE LONG-TASK MENU fits its cap with nothing dropped, and an ordinary turn's
// menu is the measured default, unchanged.
func TestALongTasksMenuFitsItsCapAndTheDefaultIsUnchanged(t *testing.T) {
	s := builtinTestServer(t)
	names := func(mode string) []string {
		var out []string
		for _, b := range s.builtinTools(&proposalSink{}, mode) {
			if !modeWithholds(mode, b.Tool) {
				out = append(out, b.Tool.Name)
			}
		}
		return out
	}
	task := names(modeDebug)
	if cap := s.cfg.MCP.Budget.resolvedMaxAdvertisedToolsFor(modeDebug); len(task) > cap {
		t.Errorf("a long task offers %d built-ins against a cap of %d: %v", len(task), cap, task)
	}
	for _, want := range []string{"grep", "git_history", "record_finding", "finish_task", "update_tasks", "sandbox_exec"} {
		if !strings.Contains(strings.Join(task, " "), want) {
			t.Errorf("a long task does not offer %s", want)
		}
	}
	plain := strings.Join(names(""), " ")
	for _, absent := range []string{"grep", "git_history", "record_finding", "finish_task"} {
		if strings.Contains(plain, absent) {
			t.Errorf("an ordinary turn now offers %s; its menu is the measured default", absent)
		}
	}
}
