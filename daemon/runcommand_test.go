package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
)

// THE REAL TERMINAL (runcommand.go).

func runCmd(t *testing.T, s *Server, args map[string]any) mcp.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := s.builtinRunCommand(context.Background(), raw)
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	return res
}

func terminalServer(t *testing.T) *Server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("these commands are POSIX shell")
	}
	t.Setenv("SHELL", "/bin/sh")
	return &Server{cfg: &Config{}, workspace: t.TempDir(), logger: quietLogger()}
}

func TestARealCommandRunsInTheProjectAndReportsItsExit(t *testing.T) {
	s := terminalServer(t)
	_ = os.WriteFile(filepath.Join(s.workspace, "here.txt"), nil, 0o644)
	res := runCmd(t, s, map[string]any{"command": "ls && echo made > new.txt"})
	if res.IsError || !strings.HasPrefix(res.Content, "Exit code 0") || !strings.Contains(res.Content, "here.txt") {
		t.Fatalf("ls: %+v", res)
	}
	if !pathExists(filepath.Join(s.workspace, "new.txt")) {
		t.Error("the command did not run in the real project")
	}
	if res := runCmd(t, s, map[string]any{"command": "echo oops >&2; exit 3"}); !strings.HasPrefix(res.Content, "Exit code 3") ||
		!strings.Contains(res.Content, "oops") {
		t.Errorf("a failing command: %+v", res)
	}
}

// This product's keys are not in a command's environment; nothing can be
// typed into it, so a prompt reads end-of-file instead of hanging.
func TestACommandGetsNoKeyAndNoInput(t *testing.T) {
	s := terminalServer(t)
	t.Setenv("MOCHIII_API_KEY", "sk-should-not-leak-1234567890")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-should-not-leak-123456")
	res := runCmd(t, s, map[string]any{"command": `echo "k=${MOCHIII_API_KEY:-none} o=${OPENROUTER_API_KEY:-none}"; read answer; echo "got=[$answer]"`})
	if !strings.Contains(res.Content, "k=none o=none") || !strings.Contains(res.Content, "got=[]") {
		t.Fatalf("output: %q", res.Content)
	}
}

// The time limit ends the command and everything it started.
func TestACommandOverItsTimeIsStoppedWithItsChildren(t *testing.T) {
	s := terminalServer(t)
	marker := filepath.Join(s.workspace, "late")
	start := time.Now()
	res := runCmd(t, s, map[string]any{"command": "(sleep 3; touch late) & sleep 30", "timeout_seconds": 1})
	// One second of its own, then at most the drain for pipes a child held.
	if elapsed := time.Since(start); elapsed > 2*runCommandDrain {
		t.Errorf("a 1-second command took %v to stop", elapsed)
	}
	if !strings.Contains(res.Content, "stopped after") {
		t.Errorf("result: %q", res.Content)
	}
	time.Sleep(runCommandDrain)
	if pathExists(marker) {
		t.Error("a child the command started outlived it")
	}
}

func TestACommandRunsOnlyInAFolderOfTheProject(t *testing.T) {
	s := terminalServer(t)
	_ = os.MkdirAll(filepath.Join(s.workspace, "sub"), 0o755)
	_ = os.MkdirAll(filepath.Join(s.workspace, ".git"), 0o755)
	if res := runCmd(t, s, map[string]any{"command": "pwd", "cwd": "sub"}); !strings.Contains(res.Content, filepath.Join(s.workspace, "sub")) {
		t.Errorf("cwd sub: %q", res.Content)
	}
	for _, cwd := range []string{"..", ".git", "/tmp", "missing"} {
		if res := runCmd(t, s, map[string]any{"command": "pwd", "cwd": cwd}); !res.IsError {
			t.Errorf("cwd %q was allowed: %q", cwd, res.Content)
		}
	}
}

func TestRunCommandAsksEvenWhenAllowed(t *testing.T) {
	cfg := &Config{MCP: MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{runCommandToolName: PolicyAllow}}}}
	if got := (configPolicy{cfg: cfg}).PolicyFor(mcp.BuiltinServerName, runCommandToolName); got != mcp.PolicyAsk {
		t.Errorf("run_command under allow resolves to %v, want ask", got)
	}
	s := builtinTestServer(t)
	for _, mode := range []string{modePlan, modeSpec, modeCheck, modeDebug} {
		for _, b := range s.builtinTools(&proposalSink{}, mode) {
			if b.Tool.Name == runCommandToolName {
				t.Errorf("mode %q offers run_command", mode)
			}
		}
	}
}

// The prompt says it runs in the real project, and does not call it confined.
func TestRunCommandsPromptSaysWhereItRuns(t *testing.T) {
	s := builtinTestServer(t)
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	defer func() { _ = registry.Close() }()
	spec, _, err := registry.Lookup(context.Background(), "builtin__"+runCommandToolName)
	if err != nil {
		t.Fatalf("run_command is not registered: %v", err)
	}
	if spec.Confined || !spec.ExecutesCode || !registry.RunsInPlace("builtin__"+runCommandToolName) {
		t.Errorf("confined=%v executes=%v in_place=%v", spec.Confined, spec.ExecutesCode, registry.RunsInPlace("builtin__"+runCommandToolName))
	}
}
