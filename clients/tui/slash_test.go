package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
		{raw: "/refactor clean", wantName: "refactor", wantArgs: "clean", wantKind: slashSteered, wantPromptKind: promptKindRefactor},
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

func TestRunMCPServerList(t *testing.T) {
	out := runMCPServerList("")
	if out == "" {
		t.Fatal("expected non-empty output from runMCPServerList")
	}
}

func TestFormatInitChecklist(t *testing.T) {
	out := formatInitChecklist("/tmp/workspace")
	if !strings.Contains(out, "/tmp/workspace") {
		t.Fatalf("expected workspace path in output, got: %s", out)
	}
}

// THE HIJACK. runMCPServerList used to resolve its binary through
// "daemon/codeterminal-daemon" and "../../daemon/codeterminal-daemon", both
// relative to the working directory — and the TUI's only mode of use is to run
// it from inside the repository being worked on.
//
// So a repository that ships an executable at that path got it EXECUTED, with
// the user's full environment, the moment they typed /mcp-server. No approval
// prompt stands in front of a slash command.
//
// Neuter check: restore either CWD-relative candidate and this test fails with
// the stolen key in its message.
func TestRunMCPServerList_DoesNotExecuteABinaryFromTheWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the payload is a POSIX shell script; NOT RUN on Windows")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, "executed")
	payload := "#!/bin/sh\necho \"key=$CODETERMINAL_API_KEY\" > " + marker + "\necho fake\n"
	if err := os.WriteFile(filepath.Join(repo, "daemon", "codeterminal-daemon"), []byte(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	// The second historical candidate, reached from a nested working directory.
	nested := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODETERMINAL_API_KEY", "sk-CANARY")
	t.Setenv(daemonBinEnvVar, "") // no override; exercise the search itself

	for _, cwd := range []string{repo, nested} {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		t.Chdir(cwd)
		_ = runMCPServerList("")

		if b, err := os.ReadFile(marker); err == nil {
			t.Fatalf("HIJACK from cwd=%s: the working directory's binary was executed; it saw %s",
				cwd, strings.TrimSpace(string(b)))
		}
	}
}

// The explicit override must still work — a gate that refuses everything is not
// a gate, and developers need a way to point at a daemon built elsewhere.
func TestResolveDaemonBin_HonoursTheExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "my-daemon")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(daemonBinEnvVar, bin)
	if got := resolveDaemonBin(); got != bin {
		t.Errorf("resolveDaemonBin() = %q, want the override %q", got, bin)
	}
}
