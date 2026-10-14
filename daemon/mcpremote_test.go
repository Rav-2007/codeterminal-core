package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"mochiii/protocol"
)

// A REMOTE SERVER, as this daemon keeps one (mcpremote.go).

type remoteNoArgs struct{}

// remoteMCP is a streamable-HTTP MCP server that counts requests and records
// the Authorization header.
func remoteMCP(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Int32) {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "remote", Version: "v0"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "ping", Description: "Answer pong."},
		func(_ context.Context, _ *sdk.CallToolRequest, _ remoteNoArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil, nil
		})
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	var auth atomic.Value
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, &auth, &hits
}

func addRemote(t *testing.T, s *Server, name, url string, headers map[string]string) protocol.MCPResponse {
	t.Helper()
	req := protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAdd, Name: name, Transport: "http", URL: url, Headers: headers}
	first := s.mcpResult(context.Background(), req)
	if !first.NeedsAcknowledgement {
		t.Fatalf("a remote add did not ask first: %+v", first)
	}
	if !strings.Contains(first.Detail, "SENDS its arguments to") {
		t.Errorf("the warning does not say where the arguments go:\n%s", first.Detail)
	}
	req.AcknowledgedUnconfined = true
	return s.mcpResult(context.Background(), req)
}

// A token given with a remote add is kept as a key -- not in the server list --
// and sent to the server; the server's tools are offered and run.
func TestARemoteServerIsAddedWithItsTokenKeptAsAKey(t *testing.T) {
	ts, auth, _ := remoteMCP(t)
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	done := addRemote(t, s, "web", ts.URL, map[string]string{"Authorization": "Bearer " + secretSentinel})
	if !done.Ok || !strings.Contains(done.Detail, "It started and offers 1 tool(s): ping") {
		t.Fatalf("ok=%v err=%q detail=%q", done.Ok, done.Error, done.Detail)
	}
	stored := storedServers(t, s)["web"]
	if stored.URL != ts.URL || stored.Headers["Authorization"] != "" {
		t.Errorf("stored as %+v", stored)
	}
	if strings.Contains(readFileString(t, s.userServersPath), secretSentinel) {
		t.Fatal("the token was written into the server list")
	}
	if got, _ := auth.Load().(string); got != "Bearer "+secretSentinel {
		t.Errorf("the server was sent Authorization %q", got)
	}
	if got, _ := s.credRedact("echoed: " + secretSentinel); strings.Contains(got, secretSentinel) {
		t.Error("the token is not scrubbed")
	}
	if !registryOffers(t, s, "web__ping") {
		t.Error("the remote tool is not offered")
	}
	if line := serverInfo(t, s, "web").StatusLine; !strings.HasPrefix(line, "✔ Connected (1 tool)") {
		t.Errorf("status %q", line)
	}
}

// A header kept as a key but never given says so, and set_env with a header
// name gives it.
func TestARemoteServerWithoutItsTokenNeedsAKey(t *testing.T) {
	ts, auth, _ := remoteMCP(t)
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	addRemote(t, s, "web", ts.URL, map[string]string{"Authorization": ""})
	info := serverInfo(t, s, "web")
	if info.StatusLine != "! Needs a key: Authorization header" || info.Transport != "http" {
		t.Errorf("info %+v", info)
	}
	resp := s.mcpResult(context.Background(), protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionSetEnv, Name: "web", HeaderName: "Authorization", EnvValue: "Bearer tok-abcdef123",
	})
	if !resp.Ok {
		t.Fatalf("set header: %s", resp.Error)
	}
	if len(serverInfo(t, s, "web").HeadersMissing) != 0 {
		t.Error("still missing after it was set")
	}
	registryOffers(t, s, "web__ping")
	if got, _ := auth.Load().(string); got != "Bearer tok-abcdef123" {
		t.Errorf("the server was sent Authorization %q", got)
	}
}

// A header's ${VAR} is filled in from a stored key, and the block keeps the
// template as written.
func TestARemoteHeaderTemplateIsFilledIn(t *testing.T) {
	ts, auth, _ := remoteMCP(t)
	s := mcpAdminTestServer(t)
	path := withKeyFile(t, s)
	if err := newMCPSecretStore(path).setVar("", "MOCHIII_TEST_TOKEN", "tok-from-var-1"); err != nil {
		t.Fatal(err)
	}
	addRemote(t, s, "web", ts.URL, map[string]string{"Authorization": "Bearer ${MOCHIII_TEST_TOKEN}"})
	if got := storedServers(t, s)["web"].Headers["Authorization"]; got != "Bearer ${MOCHIII_TEST_TOKEN}" {
		t.Errorf("stored %q", got)
	}
	if got, _ := auth.Load().(string); got != "Bearer tok-from-var-1" {
		t.Errorf("sent %q", got)
	}
}

// Plan mode connects no third-party server, remote included: connecting is
// itself an action, and plan mode promises none.
func TestPlanModeDoesNotReachARemoteServer(t *testing.T) {
	ts, _, hits := remoteMCP(t)
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	addRemote(t, s, "web", ts.URL, nil)
	before := hits.Load()
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "plan")
	_ = registry.Close()
	if hits.Load() != before {
		t.Errorf("plan mode reached the remote server (%d request(s))", hits.Load()-before)
	}
}

func TestARemoteBlockIsRefusedWhenItIsNotOne(t *testing.T) {
	for _, srv := range []MCPServerConfig{
		{URL: "https://x.example/mcp"},                                   // no type
		{Type: "http", URL: "http://x.example/mcp"},                      // http off this machine
		{Type: "http", URL: "https://x.example/mcp", Command: "npx"},     // both
		{Type: "http", URL: "https://x.example/mcp?key=${K}"},            // a key in the URL
		{Type: "http", URL: "https://x.example/mcp", Env: []string{"K"}}, // env on a remote
		{Type: "http", URL: "https://x.example/mcp", Headers: map[string]string{"Authorization": "Bearer ${MOCHIII_API_KEY}"}},
		{Type: "ws", URL: "wss://x.example/mcp"},
	} {
		if err := validateServerBlock(srv); err == nil {
			t.Errorf("accepted %+v", srv)
		}
	}
}
