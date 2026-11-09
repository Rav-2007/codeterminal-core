package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// WHAT THE LIST SAYS ABOUT A SERVER MATCHES WHAT HAPPENED TO IT (UI report,
// 2026-11-08): after Enable the answer said "It started and offers 3 tool(s)"
// while the list said "Not started"; a server whose program does not exist was
// "Not started" -- like a healthy one -- until someone pressed Check.
//
// NEUTER CHECKS (2026-11-09):
//   - the forget deferred again in mcpResult: TestEnableListsWhatTheTrialFound
//     fails (the list says "○ Not started yet" after "It started").
//   - resp.Servers built before tryStoredServer again: the same test fails on
//     the answer's own list.
//   - programMissing returning "": TestAProgramThatIsNotThereIsFailedWithoutACheck fails.

func listLine(t *testing.T, servers []protocol.MCPServerInfo, name string) string {
	t.Helper()
	for _, s := range servers {
		if s.Name == name {
			return s.StatusLine
		}
	}
	t.Fatalf("%s is not in the list", name)
	return ""
}

func TestEnableListsWhatTheTrialFound(t *testing.T) {
	s := mcpAdminTestServer(t)
	add := addRequest("probe", buildEchoServer(t))
	add.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), add); !resp.Ok {
		t.Fatalf("add: %s", resp.Error)
	}
	if resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionDisable, "probe")); !resp.Ok {
		t.Fatalf("disable: %s", resp.Error)
	}
	resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionEnable, "probe"))
	if !resp.Ok || !strings.Contains(resp.Detail, "It started and offers") {
		t.Fatalf("enable did not start it: ok=%v detail=%q err=%q", resp.Ok, resp.Detail, resp.Error)
	}
	if got := listLine(t, resp.Servers, "probe"); !strings.HasPrefix(got, "✔ Connected") {
		t.Errorf("the enable answer's own list: %q, under %q", got, resp.Detail)
	}
	if got := serverInfo(t, s, "probe").StatusLine; !strings.HasPrefix(got, "✔ Connected") {
		t.Errorf("the list after enable: %q, though the answer said %q", got, resp.Detail)
	}
}

func TestAProgramThatIsNotThereIsFailedWithoutACheck(t *testing.T) {
	s := mcpAdminTestServer(t)
	gone := filepath.Join(t.TempDir(), "nonexistent-mcp-server")
	servers := map[string]MCPServerConfig{
		"broken":  {Command: gone, AcknowledgedUnconfined: true},
		"nowhere": {Command: "mochiii-no-such-program-0001", AcknowledgedUnconfined: true},
		"paused":  {Command: gone, AcknowledgedUnconfined: true, Disabled: true},
		"probe":   {Command: buildEchoServer(t), AcknowledgedUnconfined: true},
	}
	if err := saveUserMCPServers(s.userServersPath, servers); err != nil {
		t.Fatal(err)
	}
	list := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionServers})
	if got := listLine(t, list.Servers, "broken"); !strings.HasPrefix(got, "✘ Failed to connect: its program ") || !strings.Contains(got, "does not exist") {
		t.Errorf("a missing absolute program: %q", got)
	}
	if got := listLine(t, list.Servers, "nowhere"); !strings.HasPrefix(got, "✘ Failed to connect: ") || !strings.Contains(got, "not found on this daemon's PATH") {
		t.Errorf("a program not on PATH: %q", got)
	}
	if got := listLine(t, list.Servers, "paused"); got != "⊘ Disabled" {
		t.Errorf("a paused server is described by its pause: %q", got)
	}
	if got := listLine(t, list.Servers, "probe"); got != "○ Not started yet" {
		t.Errorf("a healthy server that has not started: %q", got)
	}
}
