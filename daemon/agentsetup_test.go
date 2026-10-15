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
	got := agentSetup("1.2.3", "", "", "/work/p", nil, setupTools{}, false)
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
	got := s.unavailableServerDetail(errs[0])
	for _, want := range []string{"MCP server echo did not start", "is required", "It needs " + echoKeyName, "Customize > MCP"} {
		if !strings.Contains(got, want) {
			t.Errorf("the notice does not say %q: %s", want, got)
		}
	}
}
