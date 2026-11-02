package main

import (
	"context"
	"strings"
	"testing"

	"mochiii/protocol"
)

// The model is told which servers it has and how each fared this turn
// (agentsetup.go). Asked "which mcp server have you connected", it used to say
// none while two were configured -- and both were failing.
func TestTheAgentIsToldEachServersStateThisTurn(t *testing.T) {
	s := keyedEcho(t)
	update := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{echoKeyName}
	update.AcknowledgedUnconfined = true
	s.mcpResult(context.Background(), update)

	system := func() string {
		t.Helper()
		registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
		defer func() { _ = registry.Close() }()
		msgs := s.withAgentSetup(context.Background(), registry, "", "test/model-1", []chatMessage{{Role: "system", Content: "base"}})
		return msgs[0].Content
	}

	got := system()
	for _, want := range []string{
		"ABOUT THIS MOCHIII", "Model: test/model-1",
		"echo: FAILED TO START", echoKeyName + " environment variable is required",
		"It has no value for " + echoKeyName, "Set key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the setup section does not say %q:\n%s", want, got)
		}
	}

	s.mcpResult(context.Background(), setEnvRequest(echoKeyName, "sk-test-0123456789"))
	got = system()
	if !strings.Contains(got, "echo: running; its tools: ") || !strings.Contains(got, "secret") {
		t.Errorf("with its key the server is not described as running with its tools:\n%s", got)
	}
	if strings.Contains(got, "sk-test") {
		t.Errorf("the setup section holds the key:\n%s", got)
	}

	s.mcpResult(context.Background(), manageRequest(protocol.MCPActionDisable, "echo"))
	if got = system(); !strings.Contains(got, "echo: paused by the user") {
		t.Errorf("a paused server is not described as paused:\n%s", got)
	}
}

func TestTheSetupSectionSaysWhereToAddAServerWhenThereIsNone(t *testing.T) {
	got := agentSetup("1.2.3", "", "", "/work/p", nil, setupTools{}, false, mcpPlaces{})
	for _, want := range []string{"Mochiii version 1.2.3", "/work/p", "none added", "Customize > MCP"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// The notice a user sees names the server, its reason and where its key goes.
func TestTheUnavailableNoticeNamesTheServerAndWhy(t *testing.T) {
	s := keyedEcho(t)
	update := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{echoKeyName}
	update.AcknowledgedUnconfined = true
	s.mcpResult(context.Background(), update)
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	_ = registry.Close()
	if len(errs) != 1 {
		t.Fatalf("errs %v", errs)
	}
	got := s.unavailableServerDetail(context.Background(), errs[0])
	for _, want := range []string{"MCP server echo did not start", "is required", "It needs " + echoKeyName, "Customize > MCP"} {
		if !strings.Contains(got, want) {
			t.Errorf("the notice does not say %q: %s", want, got)
		}
	}
}

// FOUND 2026-11-01: the terminal app's model told a user who pasted Figma's
// "mcpServers" entry to add it in Customize > MCP, a page only the VS Code
// client has. In the terminal every place it names is a /mcp-server command.
func TestTheTerminalIsToldItsOwnCommandsNotTheCustomizePage(t *testing.T) {
	terminal := mcpPlacesFor(withClientName(context.Background(), protocol.HandshakeRequest{ClientName: terminalClientName}))
	if !terminal.terminal {
		t.Fatal("the terminal app's client name was not carried into the turn")
	}
	for _, other := range []string{"mochiii-vscode", "agentbench", ""} {
		if mcpPlacesFor(withClientName(context.Background(), protocol.HandshakeRequest{ClientName: other})).terminal {
			t.Errorf("client %q was given the terminal's commands", other)
		}
	}

	none := agentSetup("1.2.3", "", "", "/work/p", nil, setupTools{}, false, terminal)
	servers := []protocol.MCPServerInfo{
		{Name: "proposed", Approval: protocol.MCPApprovalPending},
		{Name: "figma", URL: "https://mcp.figma.com/mcp", State: protocol.MCPStateNeedsAuth},
		{Name: "github", State: protocol.MCPStateFailed, StateDetail: "no token", EnvMissing: []string{"GITHUB_TOKEN", "GITHUB_HOST"}},
		{Name: "linear", URL: "https://mcp.linear.app/mcp", HeadersMissing: []string{"Authorization"}},
	}
	some := agentSetup("1.2.3", "", "", "/work/p", servers, setupTools{}, false, terminal)
	for _, c := range []struct {
		got  string
		want []string
	}{
		{none, []string{"with /account, /model and /mcp", "none added", "in /mcp: it opens a panel",
			"/mcp-server add <name> --transport http <url>", "open /mcp and paste it there",
			"You cannot add, change or remove an MCP server", "Never put a key's or token's value in a command line"}},
		{some, []string{"MCP servers the user added (/mcp, a panel in the terminal app)", "/mcp-server approve proposed",
			"/mcp-server sign-in figma", "/mcp-server key github GITHUB_TOKEN, then /mcp-server key github GITHUB_HOST",
			"/mcp-server header linear Authorization", "You cannot add, change or remove an MCP server"}},
	} {
		for _, want := range c.want {
			if !strings.Contains(c.got, want) {
				t.Errorf("missing %q:\n%s", want, c.got)
			}
		}
		if strings.Contains(c.got, "Customize") {
			t.Errorf("the terminal was sent to a page it does not have:\n%s", c.got)
		}
	}

	// The VS Code client keeps its pages, and is not given the terminal's rule.
	gui := agentSetup("1.2.3", "", "", "/work/p", servers, setupTools{}, false, mcpPlaces{})
	if !strings.Contains(gui, "Customize > MCP") || strings.Contains(gui, "/mcp-server") {
		t.Errorf("the GUI's setup section changed:\n%s", gui)
	}
}

// The notice the terminal shows when a server did not start names the command
// that gives it its key.
func TestTheTerminalsUnavailableNoticeNamesTheKeyCommand(t *testing.T) {
	s := keyedEcho(t)
	update := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{echoKeyName}
	update.AcknowledgedUnconfined = true
	s.mcpResult(context.Background(), update)
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	_ = registry.Close()
	if len(errs) != 1 {
		t.Fatalf("errs %v", errs)
	}
	ctx := withClientName(context.Background(), protocol.HandshakeRequest{ClientName: terminalClientName})
	got := s.unavailableServerDetail(ctx, errs[0])
	if !strings.Contains(got, "/mcp-server key echo "+echoKeyName) || strings.Contains(got, "Customize") {
		t.Errorf("the terminal's notice: %s", got)
	}
}
