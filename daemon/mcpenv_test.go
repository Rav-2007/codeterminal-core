package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
)

// A SERVER'S KEY, GIVEN BY THE CLIENT (mcpenv.go). Found 2026-10-14: a user's
// two servers were listed "Active" while each exited on every start for want
// of a key VS Code had no way to give them.

const echoKeyName = "MOCHIII_TEST_ECHO_KEY"

// keyedEcho is a daemon with the echo server added, needing echoKeyName.
func keyedEcho(t *testing.T) *Server {
	t.Helper()
	t.Setenv(echoKeyName, "")
	return addedEcho(t, "--require-env", echoKeyName)
}

func serverInfo(t *testing.T, s *Server, name string) protocol.MCPServerInfo {
	t.Helper()
	resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionServers, ""))
	for _, info := range resp.Servers {
		if info.Name == name {
			return info
		}
	}
	t.Fatalf("%s is not listed: %+v", name, resp.Servers)
	return protocol.MCPServerInfo{}
}

func setEnvRequest(name, value string) protocol.MCPRequest {
	return protocol.MCPRequest{MCP: true, Action: protocol.MCPActionSetEnv, EnvName: name, EnvValue: value}
}

// The list says which key is missing and, once the server has been started,
// that it failed and the reason the SERVER gave -- not "connection closed".
//
// Neuter check: drop the s.mcpStates.started call in buildRegistry -- State
// stays "" and the list goes on implying the server works.
func TestAServerWithoutItsKeyIsListedAsFailingAndSaysWhy(t *testing.T) {
	s := keyedEcho(t)
	info := serverInfo(t, s, "echo")
	if !slices.Equal(info.EnvMissing, nil) {
		// The echo server was added without Env; add it to see it reported.
		t.Fatalf("env_missing %v for a server that reads nothing", info.EnvMissing)
	}

	update := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{echoKeyName}
	update.AcknowledgedUnconfined = true
	s.mcpResult(context.Background(), update)

	info = serverInfo(t, s, "echo")
	if !slices.Equal(info.EnvMissing, []string{echoKeyName}) {
		t.Errorf("env_missing = %v, want [%s]", info.EnvMissing, echoKeyName)
	}

	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	_ = registry.Close()
	if len(errs) != 1 {
		t.Fatalf("a server without its key started: errs %v", errs)
	}
	info = serverInfo(t, s, "echo")
	if info.State != protocol.MCPStateFailed || !strings.Contains(info.StateDetail, echoKeyName+" environment variable is required") {
		t.Errorf("state %q, detail %q; want failed with the server's own reason", info.State, info.StateDetail)
	}
}

// A key given with set_env reaches the server that names it, from its next
// start, and the list stops calling it missing.
//
// Neuter check: drop `lc.EnvValues = s.mcpEnvValue` in buildRegistry -- the
// server still exits for want of the key.
func TestAGivenKeyReachesTheServerThatReadsIt(t *testing.T) {
	s := keyedEcho(t)
	update := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{echoKeyName}
	update.AcknowledgedUnconfined = true
	s.mcpResult(context.Background(), update)

	resp := s.mcpResult(context.Background(), setEnvRequest(echoKeyName, "  sk-test-0123456789  "))
	if !resp.Ok || !strings.Contains(resp.Detail, "is set for echo") {
		t.Fatalf("set_env: ok=%v err=%q detail=%q", resp.Ok, resp.Error, resp.Detail)
	}
	if strings.Contains(resp.Detail, "sk-test") {
		t.Fatalf("the answer repeats the value: %q", resp.Detail)
	}
	if info := serverInfo(t, s, "echo"); len(info.EnvMissing) != 0 {
		t.Errorf("still listed as missing %v after it was given", info.EnvMissing)
	}
	if !registryOffers(t, s, "echo__secret") {
		t.Fatal("the server did not start with the key it was given")
	}
	if info := serverInfo(t, s, "echo"); info.State != protocol.MCPStateRunning {
		t.Errorf("state %q after a good start, want running", info.State)
	}

	// The value is a credential: the model never sees it.
	if got, n := s.credRedact("the key is sk-test-0123456789"); n == 0 || strings.Contains(got, "sk-test") {
		t.Errorf("a given key is not scrubbed: %q", got)
	}

	// Forgetting it puts things back.
	s.mcpResult(context.Background(), setEnvRequest(echoKeyName, ""))
	if info := serverInfo(t, s, "echo"); !slices.Equal(info.EnvMissing, []string{echoKeyName}) {
		t.Errorf("env_missing %v after the key was forgotten", info.EnvMissing)
	}
}

// A value is refused for a name no server may be given (this product's own
// key), for a malformed name, and when it holds a line break -- and the
// refusal never repeats it.
func TestSetEnvRefusesWhatCannotBeAServersKey(t *testing.T) {
	s := mcpAdminTestServer(t)
	for _, c := range []struct{ name, value, want string }{
		{"MOCHIII_API_KEY", "sk-secret-1", "inference credentials"},
		{"1NOPE", "sk-secret-1", "not a variable name"},
		{"GOOD_NAME", "sk-secret-1\nX=1", "line break"},
		{"GOOD_NAME", strings.Repeat("k", maxMCPEnvValueBytes+1), "longer than"},
	} {
		resp := s.mcpResult(context.Background(), setEnvRequest(c.name, c.value))
		if resp.Ok || !strings.Contains(resp.Error, c.want) {
			t.Errorf("%s: ok=%v err=%q, want an error saying %q", c.name, resp.Ok, resp.Error, c.want)
		}
		if strings.Contains(resp.Error, "sk-secret") {
			t.Errorf("%s: the refusal repeats the value: %q", c.name, resp.Error)
		}
	}
}
