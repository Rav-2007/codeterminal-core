package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mochiii/protocol"
)

func TestParseSlash(t *testing.T) {
	cases := []struct {
		raw            string
		passthrough    bool
		usageOnly      bool
		wantName       string
		wantArgs       string
		wantKind       slashKind
		wantPromptKind string
	}{
		{raw: "hello", passthrough: true},
		{raw: "/", passthrough: true},
		{raw: "/model", passthrough: true},
		{raw: "/model list", passthrough: true},
		{raw: "/unknown thing", passthrough: true},
		{raw: "/help", wantName: "help", wantKind: slashLocal},
		{raw: "/mcp-server", wantName: "mcp-server", wantKind: slashLocal},
		{raw: "/clear", wantName: "clear", wantKind: slashLocal},
		{raw: "/exit", wantName: "exit", wantKind: slashLocal},
		{raw: "/search", usageOnly: true, wantName: "search"},
		{raw: "/search foo", wantName: "search", wantArgs: "foo", wantKind: slashLocal},
		{raw: "/explain", usageOnly: true, wantName: "explain"},
		{raw: "/explain the router", wantName: "explain", wantArgs: "the router", wantKind: slashSteered},
		{raw: "/fix the bug", wantName: "fix", wantArgs: "the bug", wantKind: slashSteered},
		{raw: "/reason deep", wantName: "reason", wantArgs: "deep", wantKind: slashSteered, wantPromptKind: promptKindReason},
		// No reasoning-tier escalation: /refactor runs as a long task on one model.
		{raw: "/refactor clean", wantName: "refactor", wantArgs: "clean", wantKind: slashSteered},
		{raw: "/REASON case", wantName: "reason", wantArgs: "case", wantKind: slashSteered, wantPromptKind: promptKindReason},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			sp := parseSlash(tc.raw)
			if sp.RawPassthrough != tc.passthrough {
				t.Fatalf("RawPassthrough = %v, want %v", sp.RawPassthrough, tc.passthrough)
			}
			if tc.passthrough {
				return
			}
			if sp.Def == nil || sp.Def.Name != tc.wantName {
				t.Fatalf("name = %v, want %q", sp.Def, tc.wantName)
			}
			if sp.UsageOnly != tc.usageOnly {
				t.Fatalf("UsageOnly = %v, want %v", sp.UsageOnly, tc.usageOnly)
			}
			if tc.usageOnly {
				return
			}
			if sp.Args != tc.wantArgs {
				t.Errorf("Args = %q, want %q", sp.Args, tc.wantArgs)
			}
			if sp.Def.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", sp.Def.Kind, tc.wantKind)
			}
			if sp.Def.PromptKind != tc.wantPromptKind {
				t.Errorf("PromptKind = %q, want %q", sp.Def.PromptKind, tc.wantPromptKind)
			}
		})
	}
}

func TestSteeredPromptIncludesPreamble(t *testing.T) {
	d := lookupSlash("explain")
	if d == nil {
		t.Fatal("missing explain")
	}
	got := steeredPrompt(d, "foo")
	if !strings.HasPrefix(got, d.Preamble) || !strings.HasSuffix(got, "foo") {
		t.Fatalf("steeredPrompt = %q", got)
	}
}

func TestFormatSlashHelpListsMCPServer(t *testing.T) {
	help := formatSlashHelp()
	for _, name := range []string{"/explain", "/mcp-server", "/model", "/help", "/plan"} {
		if !strings.Contains(help, name) {
			t.Errorf("help missing %s:\n%s", name, help)
		}
	}
}

func TestRunGitStatus(t *testing.T) {
	out := runGitStatus(".")
	if out == "" {
		t.Fatal("expected non-empty output from runGitStatus")
	}
}

func TestFormatInitChecklist(t *testing.T) {
	out := formatInitChecklist("/tmp/workspace")
	if !strings.Contains(out, "/tmp/workspace") {
		t.Fatalf("expected workspace path in output, got: %s", out)
	}
}

// THE HIJACK, AND WHY IT CANNOT COME BACK. /mcp-server used to find a daemon
// binary and run it -- first through "daemon/mochiii-daemon" relative to the
// working directory, which is the repository being worked on, so a repository
// that shipped an executable at that path got it EXECUTED with the user's full
// environment the moment they typed the command (d56e425). The search was then
// anchored to trusted places, and a year of guards followed for the config path
// it passed along.
//
// Since 2026-10-11 the command runs NOTHING: it asks the running daemon over
// its socket (mcpserver.go). So this plants a binary everywhere one was ever
// looked for -- the working directory, a nested one, PATH, and the old
// MOCHIII_DAEMON_BIN override -- and asserts none of them is started, while the
// answer shown is the daemon's own.
//
// Neuter check: make handleMCPServerCommand exec.Command("mochiii-daemon",
// "mcp", "list") again and this fails with the stolen key in its message.
func TestMCPServer_AsksTheDaemonAndRunsNoProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the payload is a POSIX shell script; NOT RUN on Windows")
	}
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	payload := "#!/bin/sh\necho \"key=$MOCHIII_API_KEY\" > " + marker + "\necho fake\n"
	plant := func(dir string) string {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "mochiii-daemon")
		if err := os.WriteFile(p, []byte(payload), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	plant(filepath.Join(repo, "daemon"))
	plant(repo)
	onPath := plant(t.TempDir())
	nested := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCHIII_API_KEY", "sk-CANARY")
	t.Setenv("MOCHIII_DAEMON_BIN", onPath)
	t.Setenv("PATH", filepath.Dir(onPath)+string(os.PathListSeparator)+os.Getenv("PATH")+string(os.PathListSeparator))

	daemon := startRequestRecorder(t, []string{protocol.FeatureMCPAdmin}, func(json.RawMessage) any {
		return protocol.MCPResponse{Ok: true, AgentMode: true,
			Report: "This daemon's config: /opt/models.agent.json\n\nAgent mode is ON. 2 tool(s) would be offered to the model.\n"}
	})

	for _, cwd := range []string{repo, nested} {
		t.Chdir(cwd)
		m := newChatModel("test", repo, repo, nil)
		next, cmd := m.handleLocalSlash("mcp-server", "")
		answer, ok := findMsg[mcpResultMsg](cmd)
		if !ok {
			t.Fatalf("cwd=%s: /mcp-server asked the daemon nothing", cwd)
		}
		updated, _ := next.(chatModel).Update(answer)
		if last := lastTurn(t, updated); !strings.Contains(last, "Agent mode is ON") || !strings.Contains(last, "/opt/models.agent.json") {
			t.Errorf("cwd=%s: the daemon's own answer is not what was shown:\n%s", cwd, last)
		}
		if b, err := os.ReadFile(marker); err == nil {
			t.Fatalf("HIJACK from cwd=%s: /mcp-server executed a program; it saw %s", cwd, strings.TrimSpace(string(b)))
		}
	}
	if n := len(daemon.requests()); n != 2 {
		t.Errorf("the daemon was asked %d time(s), want once per /mcp-server", n)
	}
}
