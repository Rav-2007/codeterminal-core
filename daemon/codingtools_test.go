package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// The coding gaps closed on 2026-10-15: finding a file by name, reading past
// 64 KB in an ordinary turn, a test run longer than 30 s, the turn's deadline
// binding a command, and a non-Go/npm project testing the agent's edits in its
// working copy.

func findFiles(t *testing.T, s *Server, ctx context.Context, args map[string]any) mcp.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := s.builtinFindFiles(ctx, raw)
	if err != nil {
		t.Fatalf("find_files: %v", err)
	}
	return res
}

func TestFindFilesByWordAndByGlob(t *testing.T) {
	s, _ := stageProject(t, map[string]string{
		"src/auth/AuthService.ts":       "x",
		"src/auth/auth.test.ts":         "x",
		"src/user.ts":                   "x",
		"cmd/server/main.go":            "x",
		"main.go":                       "x",
		"node_modules/auth-lib/auth.js": "x", // a dependency folder: not searched
		".git/auth-hook":                "x", // protected: not searched
		"build/auth.gen.ts":             "x", // .gitignored: not searched
		".gitignore":                    "build/\n",
		".env":                          "SECRET=1", // secret-shaped: never named
	})
	ctx := context.Background()
	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"pattern": "auth"}, []string{"src/auth/AuthService.ts", "src/auth/auth.test.ts"}},
		{map[string]any{"pattern": "*.test.ts"}, []string{"src/auth/auth.test.ts"}},
		{map[string]any{"pattern": "**/main.go"}, []string{"cmd/server/main.go", "main.go"}},
		{map[string]any{"pattern": "cmd/*/main.go"}, []string{"cmd/server/main.go"}},
		{map[string]any{"pattern": "*.ts", "path": "src/auth"}, []string{"src/auth/AuthService.ts", "src/auth/auth.test.ts"}},
		{map[string]any{"pattern": "env"}, nil},
	} {
		res := findFiles(t, s, ctx, tc.args)
		var got []string
		for _, line := range strings.Split(strings.TrimSpace(res.Content), "\n")[1:] {
			if line != "" {
				got = append(got, line)
			}
		}
		if tc.want == nil {
			if !res.Empty {
				t.Errorf("%v found %q, want nothing", tc.args, res.Content)
			}
			continue
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%v = %v, want %v", tc.args, got, tc.want)
		}
	}
	for _, bad := range []map[string]any{
		{"pattern": ""}, {"pattern": "x", "path": "/etc"}, {"pattern": "x", "path": "../.."},
		{"pattern": "[abc"}, {"pattern": "x", "path": "src/user.ts"},
	} {
		if res := findFiles(t, s, ctx, bad); !res.IsError {
			t.Errorf("%v was accepted: %q", bad, res.Content)
		}
	}
}

// It searches the working copy, so a file the agent created this turn is
// found -- and the real project does not have it yet.
func TestFindFilesSeesTheWorkingCopy(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.go": "package a\n"})
	sink := stagedSink(t, s)
	if out, isErr := propose(t, s, sink, "newpkg/fresh_handler.go", "", "package newpkg\n"); isErr {
		t.Fatalf("creating a file in the working copy failed: %s", out)
	}
	res := findFiles(t, s, sink.readCtx(context.Background()), map[string]any{"pattern": "fresh"})
	if !strings.Contains(res.Content, "newpkg/fresh_handler.go") {
		t.Errorf("a file created this turn was not found: %q", res.Content)
	}
	if pathExists(filepath.Join(dir, "newpkg", "fresh_handler.go")) {
		t.Error("the file reached the real project before review")
	}
}

// find_files is on an ordinary turn's menu, allowed by the shipped configs,
// and classed as confined like grep.
func TestFindFilesIsOfferedAndClassed(t *testing.T) {
	s := builtinTestServer(t)
	names := map[string]bool{}
	for _, b := range s.builtinTools(&proposalSink{}, "") {
		names[b.Tool.Name] = true
	}
	if !names["find_files"] || !names["grep"] {
		t.Errorf("an ordinary turn does not offer find_files and grep: %v", names)
	}
	if builtinToolClass("find_files") != classConfined {
		t.Error("find_files is not classed as confined")
	}
	for _, cfgFile := range []string{"../models.json", "../models.agent.json"} {
		data, err := os.ReadFile(cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"find_files"`) {
			t.Errorf("%s does not set a policy for find_files, so it would ask before every search", cfgFile)
		}
	}
}

// An ordinary turn is offered line ranges, and a truncated read says how to
// get the rest.
func TestAnOrdinaryTurnCanReadPast64KB(t *testing.T) {
	s, dir := stageProject(t, nil)
	var schemaText string
	for _, b := range s.builtinTools(&proposalSink{}, "") {
		if b.Tool.Name == "read_file" {
			schemaText = string(b.Tool.Schema)
		}
	}
	if !strings.Contains(schemaText, "start_line") || !strings.Contains(schemaText, "end_line") {
		t.Fatalf("an ordinary turn's read_file offers no range: %s", schemaText)
	}
	var big strings.Builder
	for i := 1; i <= 20000; i++ {
		big.WriteString("line ")
		big.WriteString(strings.Repeat("x", 10))
		big.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := s.builtinReadFile(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if !strings.Contains(res.Content, "Read the rest with start_line and end_line") {
		t.Errorf("a truncated read does not say how to read the rest: ...%s", res.Content[len(res.Content)-200:])
	}
	res, _ = s.builtinReadFile(context.Background(), json.RawMessage(`{"path":"big.txt","start_line":19999,"end_line":20000}`))
	if res.IsError || !strings.Contains(res.Content, "20000") {
		t.Errorf("reading the last lines of a large file: %q", res.Content)
	}
}

func TestSandboxExecTimeoutIsAskedForAndBounded(t *testing.T) {
	if got := execTimeoutArg(0, execTimeout); got != execTimeout {
		t.Errorf("no timeout_seconds gave %v, want the default %v", got, execTimeout)
	}
	if got := execTimeoutArg(300, execTimeout); got != 5*time.Minute {
		t.Errorf("300 s gave %v", got)
	}
	if got := execTimeoutArg(1_000_000, execTimeout); got != maxExecTimeout {
		t.Errorf("an enormous timeout gave %v, want the ceiling %v", got, maxExecTimeout)
	}
	// A long task already allowed more keeps its own budget as the ceiling.
	if got := execTimeoutArg(1_000_000, time.Hour); got != time.Hour {
		t.Errorf("a long task's hour was cut to %v", got)
	}
	s := builtinTestServer(t)
	for _, b := range s.builtinTools(&proposalSink{}, "") {
		if b.Tool.Name == "sandbox_exec" {
			if !strings.Contains(string(b.Tool.Schema), "timeout_seconds") || !strings.Contains(b.Tool.Description, "timeout_seconds") {
				t.Error("sandbox_exec does not offer timeout_seconds")
			}
		}
	}
}

// THE TURN'S DEADLINE ENDS A COMMAND that asked for longer.
func TestTheTurnDeadlineStopsARunCommand(t *testing.T) {
	s := terminalServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	res, err := s.builtinRunCommand(ctx, json.RawMessage(`{"command":"sleep 30","timeout_seconds":600}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*runCommandDrain {
		t.Errorf("a command in a turn with 1 s left ran %v", elapsed)
	}
	if strings.Contains(res.Content, "10 min") {
		t.Errorf("the result names the asked-for timeout, not the one that applied: %q", res.Content)
	}
	if got := withinDeadline(context.Background(), time.Minute); got != time.Minute {
		t.Errorf("no deadline changed the timeout to %v", got)
	}
}

func TestTheProjectsOwnRunnersAreDetected(t *testing.T) {
	for _, tc := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"pyproject.toml": "", "uv.lock": ""}, "pytest,python,python3,uv"},
		{map[string]string{"package.json": "{}", "pnpm-lock.yaml": ""}, "node,npx,pnpm"},
		{map[string]string{"App.csproj": "", "pom.xml": ""}, "dotnet,mvn"},
		{map[string]string{"go.mod": "", "Makefile": ""}, ""},
		{map[string]string{"pyproject.toml/inner.txt": ""}, ""}, // a folder with a manifest's name is not one
	} {
		_, dir := stageProject(t, tc.files)
		if got := strings.Join(projectToolchainPrograms(dir), ","); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.files, got, tc.want)
		}
	}
	// Detected runners are extras: refused where nothing confines them, with
	// the route that does exist named.
	if why := execRefusalWith("python3", []string{"python3"}, false); !strings.Contains(why, "install bubblewrap") ||
		!strings.Contains(why, "run_command") {
		t.Errorf("unconfined refusal: %q", why)
	}
	if got := execProgramsWith([]string{"python3"}, false); len(got) != 4 {
		t.Errorf("an unconfined host offers %v", got)
	}
}

// FIX IT AND RUN THE TESTS, IN A PYTHON PROJECT, IN ONE TURN. The failing
// test fails in the working copy, the edit lands there, the same test passes,
// and the real project is untouched until review. Needs bubblewrap and
// python3; skipped where either is missing.
func TestAPythonProjectTestsItsEditsInTheWorkingCopy(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH")
	}
	s, dir := stageProject(t, map[string]string{
		"pyproject.toml": "[project]\nname = \"calc\"\nversion = \"0\"\n",
		"calc.py":        "def add(a, b):\n    return a - b\n",
		"test_calc.py": "import unittest\nfrom calc import add\n\n" +
			"class T(unittest.TestCase):\n    def test_add(self):\n        self.assertEqual(add(2, 3), 5)\n\n" +
			"if __name__ == '__main__':\n    unittest.main()\n",
	})
	if !s.sandboxExecConfined() {
		t.Skip("this host does not confine sandbox_exec (no bubblewrap), so project runners are not offered")
	}
	sink := stagedSink(t, s)
	run := func() mcp.Result {
		res, err := s.builtinSandboxExecStaged(context.Background(),
			json.RawMessage(`{"command":"python3 -m unittest test_calc","timeout_seconds":60}`), sink)
		if err != nil {
			t.Fatalf("sandbox_exec: %v", err)
		}
		return res
	}
	if res := run(); res.IsError || !strings.HasPrefix(res.Content, "Command exited with error") {
		t.Fatalf("the failing test did not fail (or python3 was refused): %q", res.Content)
	}
	if out, isErr := propose(t, s, sink, "calc.py", "return a - b", "return a + b"); isErr {
		t.Fatalf("the fix did not apply: %s", out)
	}
	if res := run(); res.IsError || strings.HasPrefix(res.Content, "Command exited with error") {
		t.Fatalf("the test still fails after the fix (error=%v):\n%s", res.IsError, res.Content)
	}
	if got := fileText(t, filepath.Join(dir, "calc.py")); !strings.Contains(got, "a - b") {
		t.Error("the real project changed before review")
	}
	blocks, info, _ := sink.finish()
	if len(blocks) != 1 || blocks[0].FilePath != "calc.py" || info == nil || !info.Passed {
		t.Errorf("offered %+v with check %+v; want the calc.py fix, checked and passing", blocks, info)
	}
}

// sayYes approves every question, as a user pressing y would, and records
// which tools were asked about.
type sayYes struct{ asked []string }

func (a *sayYes) Ask(_ context.Context, req protocol.ToolApprovalRequest) approvalDecision {
	a.asked = append(a.asked, req.Tool)
	return approvalDecision{Decision: protocol.ApprovalApprove}
}

// "FIND THE BUG, FIX IT, AND RUN THE TESTS" through the real agent loop: the
// model finds the file by name, reads it, fixes it, runs the tests against the
// fix in the working copy, and answers -- five dependent steps, one turn, the
// real project untouched until review, and every step that changes or runs
// something asked about first.
func TestTheLoopFindsFixesAndTestsABugInOneTurn(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH")
	}
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__find_files", `{"pattern":"calc"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"calc.py"}`),
		toolCallSSE("c3", "builtin__propose_edit", `{"path":"calc.py","search":"return a - b","replace":"return a + b"}`),
		toolCallSSE("c4", "builtin__sandbox_exec", `{"command":"python3 -m unittest test_calc","timeout_seconds":60}`),
		textSSE("Fixed add in calc.py; test_calc passes."),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow, "find_files": PolicyAllow}},
	})
	files := map[string]string{
		"pyproject.toml": "[project]\nname = \"calc\"\nversion = \"0\"\n",
		"calc.py":        "def add(a, b):\n    return a - b\n",
		"test_calc.py": "import unittest\nfrom calc import add\n\nclass T(unittest.TestCase):\n" +
			"    def test_add(self):\n        self.assertEqual(add(2, 3), 5)\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(s.workspace, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !s.sandboxExecConfined() {
		t.Skip("this host does not confine sandbox_exec, so python3 is not offered")
	}

	sink, _ := s.newTurnSink("auto", nil, nil)
	t.Cleanup(sink.discard)
	registry, _ := s.buildRegistry(context.Background(), s.logger, sink, "auto")
	t.Cleanup(func() { _ = registry.Close() })
	appr := &sayYes{}
	var activity []protocol.ToolActivity
	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "find the bug in calc, fix it and run the tests"}},
		providerRouting{}, appr, func(string) error { return nil },
		func(a protocol.ToolActivity) { activity = append(activity, a) }, nil, nil, func(protocol.Degradation) {}, nil, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(*bodies) != 5 {
		t.Fatalf("made %d model calls, want 5", len(*bodies))
	}
	succeeded := map[string]bool{}
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseSucceeded {
			succeeded[a.Tool] = true
		}
	}
	for _, tool := range []string{"find_files", "read_file", "propose_edit", "sandbox_exec"} {
		if !succeeded[tool] {
			t.Errorf("%s did not succeed; activity %+v", tool, activity)
		}
	}
	// Each step's real result reached the next request.
	if !strings.Contains(string((*bodies)[1]), "calc.py") || !strings.Contains(string((*bodies)[2]), "return a - b") {
		t.Error("find_files' and read_file's results did not reach the model")
	}
	if last := string((*bodies)[4]); !strings.Contains(last, "OK") || strings.Contains(last, "AssertionError") {
		t.Errorf("the test run the model saw was not the passing one:\n%s", last[max(0, len(last)-600):])
	}
	// Asked about what changes or runs something; not about reads.
	if strings.Join(appr.asked, ",") != "propose_edit,sandbox_exec" {
		t.Errorf("asked about %v, want propose_edit and sandbox_exec only", appr.asked)
	}
	if got := fileText(t, filepath.Join(s.workspace, "calc.py")); !strings.Contains(got, "a - b") {
		t.Error("the real project changed before review")
	}
	blocks, info, _ := sink.finish()
	if len(blocks) != 1 || blocks[0].FilePath != "calc.py" || info == nil || !info.Passed {
		t.Errorf("offered %+v, check %+v; want the calc.py fix, tested and passing", blocks, info)
	}
	if res.Incomplete != nil {
		t.Errorf("the turn ended incomplete: %+v", res.Incomplete)
	}
}
