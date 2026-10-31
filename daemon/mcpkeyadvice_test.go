package main

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// THE ADVICE WHEN A SERVER FAILS FOR A MISSING KEY (setKeyAdvice). FOUND
// 2026-10-31: a server that exited for want of its key was answered "fix the
// command (/mcp-server remove ..., then add it again)" -- its command was fine.

// addFailingForItsKey adds the echo server, which exits without echoKeyName,
// declaring that variable and giving it no value.
func addFailingForItsKey(t *testing.T, cli bool) string {
	t.Helper()
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	s.mcpFromCLI = cli
	t.Setenv(echoKeyName, "")
	req := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	req.Env = []string{echoKeyName}
	req.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok || !strings.Contains(resp.Detail, "did NOT start when tried") {
		t.Fatalf("the add did not report a failed start: ok=%v err=%q\n%s", resp.Ok, resp.Error, resp.Detail)
	}
	return resp.Detail
}

// Through the socket (the chat, VS Code) the advice is /mcp-server key, and
// Set key; through `mochiii mcp add` it is `mochiii mcp set-key`. Neither says
// to fix the command.
//
// Neuter check: drop the keysMissing branch in tryStoredServer -- both answers
// go back to "fix the command".
func TestAFailedStartWithAMissingKeySaysToSetIt(t *testing.T) {
	for _, c := range []struct {
		cli        bool
		want, not  string
		alsoWanted string
	}{
		{false, "`/mcp-server key echo " + echoKeyName + "`", "mochiii mcp set-key", "in VS Code, Set key on the server"},
		{true, "`mochiii mcp set-key echo " + echoKeyName + "`", "/mcp-server key", "its next start uses it"},
	} {
		detail := addFailingForItsKey(t, c.cli)
		for _, want := range []string{"Mochiii has no value for " + echoKeyName + ", which it reads", c.want, c.alsoWanted} {
			if !strings.Contains(detail, want) {
				t.Errorf("cli=%v: the answer does not say %q:\n%s", c.cli, want, detail)
			}
		}
		for _, not := range []string{"fix the command", "then add it again", c.not} {
			if strings.Contains(detail, not) {
				t.Errorf("cli=%v: the answer says %q:\n%s", c.cli, not, detail)
			}
		}
	}
}

// The CLI's own command, end to end, with no terminal (so nothing is asked).
func TestTheAddCommandSaysToSetAMissingKey(t *testing.T) {
	cfgPath, _, _ := addCommandHome(t)
	out, _ := addKeyedEchoCommand(t, cfgPath, strings.NewReader("echo\n"))
	if !strings.Contains(out, "did NOT start when tried") ||
		!strings.Contains(out, "Set it with `mochiii mcp set-key echo "+echoKeyName+"`") ||
		strings.Contains(out, "fix the command") {
		t.Errorf("mochiii mcp add did not give the set-key advice:\n%s", out)
	}
}

// Every key it reads has a value and it still fails: the command is the
// suspect, and the advice stays. The value is never in the answer.
func TestAFailedStartWithEveryKeyPresentStillSaysFixTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no `true` to stand in for a program that is not an MCP server")
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` to stand in for a program that is not an MCP server")
	}
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	t.Setenv(echoKeyName, "")
	req := addRequest("notmcp", truePath)
	req.Env = []string{echoKeyName}
	req.EnvValues = map[string]string{echoKeyName: secretSentinel}
	req.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok || !strings.Contains(resp.Detail, "did NOT start when tried") ||
		!strings.Contains(resp.Detail, "fix the command (`/mcp-server remove notmcp`, then add it again)") {
		t.Fatalf("ok=%v err=%q\n%s", resp.Ok, resp.Error, resp.Detail)
	}
	for _, not := range []string{"no value for", "/mcp-server key", "set-key", secretSentinel} {
		if strings.Contains(resp.Detail, not) || strings.Contains(strings.Join(resp.Notes, " "), not) {
			t.Errorf("the answer says %q:\n%s\n%v", not, resp.Detail, resp.Notes)
		}
	}
}

// A server that started is answered as before.
func TestAServerThatStartsIsAnsweredAsBefore(t *testing.T) {
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	t.Setenv(echoKeyName, "")
	req := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	req.Env = []string{echoKeyName}
	req.EnvValues = map[string]string{echoKeyName: secretSentinel}
	req.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok || !strings.HasPrefix(resp.Detail, "Added echo. It started and offers") {
		t.Fatalf("ok=%v err=%q\n%s", resp.Ok, resp.Error, resp.Detail)
	}
	for _, not := range []string{"fix the command", "no value for", "/mcp-server key", "set-key", secretSentinel} {
		if strings.Contains(resp.Detail, not) || strings.Contains(strings.Join(resp.Notes, " "), not) {
			t.Errorf("the answer says %q:\n%s\n%v", not, resp.Detail, resp.Notes)
		}
	}
}
