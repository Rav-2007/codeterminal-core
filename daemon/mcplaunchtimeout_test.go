package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
)

// THE FIRST RUN OF A LAUNCHER (flaw F6). A server started through npx, uvx or
// docker downloads its package the first time, and a cold npx took 21.8 s to
// answer against a 20 s default: the first run of an ordinary server failed and
// the second, with the package cached, worked. A launcher is given the longer
// bound unless someone has said otherwise.

func TestConnectTimeoutFitsALauncherUnlessSomeoneSaidOtherwise(t *testing.T) {
	commands := []string{"npx", "/usr/local/bin/npx", "NPX.EXE", "npx.cmd", "uvx", "bunx", "pnpx", "docker"}
	if runtime.GOOS == "windows" {
		// filepath.Base splits on a backslash only where the daemon is running on Windows.
		commands = append(commands, `C:\Program Files\nodejs\npx.cmd`)
	}
	for _, command := range commands {
		srv := MCPServerConfig{Command: command}
		if got := srv.connectTimeout(MCPBudgetConfig{}); got != launcherConnectTimeout {
			t.Errorf("%q: %s, want the launcher bound %s", command, got, launcherConnectTimeout)
		}
	}
	if launcherConnectTimeout <= 22*time.Second {
		t.Errorf("the launcher bound %s does not cover the cold npx start measured at 21.8 s", launcherConnectTimeout)
	}
	if launcherConnectTimeout > time.Duration(maxConnectTimeoutSeconds)*time.Second {
		t.Errorf("the launcher bound %s is past the %ds ceiling", launcherConnectTimeout, maxConnectTimeoutSeconds)
	}
}

func TestConnectTimeoutLeavesEverythingElseAtTheDefault(t *testing.T) {
	for name, srv := range map[string]MCPServerConfig{
		"a plain program": {Command: "/usr/bin/my-server"},
		"a remote server": {URL: "https://mcp.example.com/mcp"},
		"python itself":   {Command: "python3", Args: []string{"-m", "server"}},
		"no command":      {},
	} {
		if got := srv.connectTimeout(MCPBudgetConfig{}); got != 0 {
			t.Errorf("%s: %s, want 0 (the caller's 20 s default)", name, got)
		}
		if got := handshakeBound(srv, MCPBudgetConfig{}); got != mcp.DefaultConnectTimeout {
			t.Errorf("%s: handshake bound %s, want %s", name, got, mcp.DefaultConnectTimeout)
		}
	}
}

func TestConnectTimeoutPrecedence(t *testing.T) {
	npx := MCPServerConfig{Command: "npx"}

	// The budget's setting wins over the launcher default.
	if got := npx.connectTimeout(MCPBudgetConfig{ConnectTimeoutSeconds: 45}); got != 45*time.Second {
		t.Errorf("budget: %s, want 45s", got)
	}
	// The server's own wins over the budget's and over the default.
	own := MCPServerConfig{Command: "npx", ConnectTimeoutSeconds: 30}
	if got := own.connectTimeout(MCPBudgetConfig{ConnectTimeoutSeconds: 45}); got != 30*time.Second {
		t.Errorf("own: %s, want 30s", got)
	}
	// ... up to the ceiling.
	huge := MCPServerConfig{Command: "npx", ConnectTimeoutSeconds: 100000}
	if got := huge.connectTimeout(MCPBudgetConfig{}); got != time.Duration(maxConnectTimeoutSeconds)*time.Second {
		t.Errorf("ceiling: %s, want %ds", got, maxConnectTimeoutSeconds)
	}
	// A server that is not a launcher still gets the budget's.
	plain := MCPServerConfig{Command: "/usr/bin/my-server"}
	if got := plain.connectTimeout(MCPBudgetConfig{ConnectTimeoutSeconds: 45}); got != 45*time.Second {
		t.Errorf("plain with a budget: %s, want 45s", got)
	}
}

// The check, the listing deadline and a real turn must agree, or a server can
// pass one and fail another. All three resolve through connectTimeout.
func TestEveryPathAgreesOnALaunchersBound(t *testing.T) {
	srv := MCPServerConfig{Command: "npx", Args: []string{"-y", "some-server"}}
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{"s": srv}

	if got := handshakeBound(srv, cfg.MCP.Budget); got != launcherConnectTimeout {
		t.Errorf("the check's handshake bound is %s", got)
	}
	if got := listDeadline(cfg); got != launcherConnectTimeout+mcpListGrace {
		t.Errorf("the list/trial deadline is %s, want the launcher bound plus the grace %s", got, mcpListGrace)
	}
	launch := laneBLaunchConfig("s", srv, cfg, t.TempDir(), nil, nil)
	if launch.ConnectTimeout != launcherConnectTimeout {
		t.Errorf("a real turn starts it with %s", launch.ConnectTimeout)
	}

	plain := &Config{}
	plain.MCP.Servers = map[string]MCPServerConfig{"p": {Command: "/usr/bin/my-server"}}
	if got := listDeadline(plain); got != mcp.DefaultConnectTimeout+mcpListGrace {
		t.Errorf("a plain server's list deadline is %s", got)
	}
}

func TestTimeoutHintNamesTheBoundThatRanOut(t *testing.T) {
	hint := timeoutHint(MCPServerConfig{Command: "npx"}, context.DeadlineExceeded)
	for _, want := range []string{"downloads the package", "1m30s", "Check again", "connect_timeout_seconds"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q does not say %q", hint, want)
		}
	}
	if strings.Contains(hint, "in VS Code") {
		t.Errorf("the hint points VS Code's Check button at a --timeout it cannot send: %q", hint)
	}
}

// ADDING A LAUNCHER WHOSE PACKAGE IS STILL DOWNLOADING is not a wrong command,
// and "fix the command, remove it and add it again" was sent to users whose
// server worked on the next try. A fake `npx` that never answers stands in for
// a download that is not done.
func TestAnAddedLauncherThatIsStillDownloadingSaysSoNotFixTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in launcher is a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "npx")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil { //nolint:gosec // a test's stand-in program
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := mcpAdminTestServer(t)
	s.cfg.MCP.Budget.ConnectTimeoutSeconds = 1 // the budget's bound wins over the launcher default
	req := addRequest("slowpkg", "npx", "-y", "some-server")
	req.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), req)

	if !resp.Ok {
		t.Fatalf("the add was refused: %+v", resp)
	}
	if !strings.Contains(resp.Detail, "still starting when the wait ran out") || !strings.Contains(resp.Detail, "downloads the package") {
		t.Errorf("a launcher that ran out of time must say it is still starting:\n%s", resp.Detail)
	}
	if strings.Contains(resp.Detail, "fix the command") {
		t.Errorf("a slow download is not a wrong command:\n%s", resp.Detail)
	}
	if _, ok := storedServers(t, s)["slowpkg"]; !ok {
		t.Error("the server is saved either way")
	}
}
