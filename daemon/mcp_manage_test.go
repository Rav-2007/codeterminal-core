package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
)

// UPDATE, ENABLE AND DISABLE (FeatureMCPManage): what a client needs to manage a
// server it added without removing it and typing it in again. The VS Code
// extension's server list is built on these.

func manageRequest(action, name string) protocol.MCPRequest {
	return protocol.MCPRequest{MCP: true, Action: action, Name: name}
}

// addedEcho is a daemon with the echo server added and acknowledged.
func addedEcho(t *testing.T, args ...string) *Server {
	t.Helper()
	s := mcpAdminTestServer(t)
	req := addRequest("echo", buildEchoServer(t), args...)
	req.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), req); !resp.Ok {
		t.Fatalf("adding echo: %s %s", resp.Error, resp.Detail)
	}
	return s
}

// registryOffers says whether an agent turn built now would offer the tool.
func registryOffers(t *testing.T, s *Server, tool string) bool {
	t.Helper()
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	defer func() { _ = registry.Close() }()
	if len(errs) != 0 {
		t.Fatalf("building the registry: %v", errs)
	}
	_, _, err := registry.Lookup(context.Background(), tool)
	return err == nil
}

// AN UPDATE IS A NEW GRANT. The program may be a different one, so the first
// answer is the warning and nothing is stored; only the acknowledged request
// replaces what runs.
//
// Neuter check: drop the `if !req.AcknowledgedUnconfined` return in mcpStore --
// the first update stores the new arguments.
func TestAnUpdateAsksAgainAndThenReplacesWhatRuns(t *testing.T) {
	s := addedEcho(t, "--old")

	update := addRequest("echo", buildEchoServer(t), "--new")
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{"ECHO_TOKEN"}
	resp := s.mcpResult(context.Background(), update)
	if resp.Ok || !resp.NeedsAcknowledgement || resp.Error != "" {
		t.Fatalf("ok=%v needs_ack=%v err=%q; an update must ask first", resp.Ok, resp.NeedsAcknowledgement, resp.Error)
	}
	for _, want := range []string{`Changing "echo"`, buildEchoServer(t) + " --new", "YOUR full privileges", "ECHO_TOKEN"} {
		if !strings.Contains(resp.Detail, want) {
			t.Errorf("the warning does not say %q:\n%s", want, resp.Detail)
		}
	}
	if got := storedServers(t, s)["echo"].Args; !slices.Equal(got, []string{"--old"}) {
		t.Fatalf("an unacknowledged update stored args %q", got)
	}

	update.AcknowledgedUnconfined = true
	resp = s.mcpResult(context.Background(), update)
	if !resp.Ok || !strings.HasPrefix(resp.Detail, "Changed echo. It started and offers") {
		t.Fatalf("acknowledged update: ok=%v err=%q detail=%q", resp.Ok, resp.Error, resp.Detail)
	}
	got := storedServers(t, s)["echo"]
	if !slices.Equal(got.Args, []string{"--new"}) || !slices.Equal(got.Env, []string{"ECHO_TOKEN"}) || !got.AcknowledgedUnconfined {
		t.Errorf("stored as %+v after the update", got)
	}
}

// What an update may not touch: a server it did not add, and a name nobody added.
func TestAnUpdateChangesOnlyWhatWasAdded(t *testing.T) {
	s := mcpAdminTestServer(t)
	s.cfg.MCP.Servers = map[string]MCPServerConfig{"fromconfig": plainUserServer("uvx")}
	update := func(name string) protocol.MCPResponse {
		req := addRequest(name, "uvx", "something")
		req.Action, req.AcknowledgedUnconfined = protocol.MCPActionUpdate, true
		return s.mcpResult(context.Background(), req)
	}
	if resp := update("fromconfig"); resp.Ok || !strings.Contains(resp.Error, "/opt/mochiii/models.agent.json") {
		t.Errorf("updating a config-defined server: ok=%v err=%q", resp.Ok, resp.Error)
	}
	if resp := update("nobody"); resp.Ok || !strings.Contains(resp.Error, "no server named nobody") {
		t.Errorf("updating an unknown server: ok=%v err=%q", resp.Ok, resp.Error)
	}
	if got := storedServers(t, s); len(got) != 0 {
		t.Errorf("a refused update stored %v", got)
	}
}

// AN `allow` WRITTEN FOR ONE PROGRAM DOES NOT PASS TO THE NEXT. A policy names a
// tool, and after an update the tool of that name may belong to a different
// program; it asks again, and the answer says why.
//
// Neuter check: copy old.Tools into srv in mcpStore.
func TestAnUpdateDropsToolPoliciesWrittenForTheOldProgram(t *testing.T) {
	s := mcpAdminTestServer(t)
	old := plainUserServer(buildEchoServer(t))
	old.Tools = map[string]string{"echo": PolicyAllow}
	if err := saveUserMCPServers(s.userServersPath, map[string]MCPServerConfig{"echo": old}); err != nil {
		t.Fatal(err)
	}

	update := addRequest("echo", buildEchoServer(t), "--new")
	update.Action, update.AcknowledgedUnconfined = protocol.MCPActionUpdate, true
	resp := s.mcpResult(context.Background(), update)
	if !resp.Ok {
		t.Fatalf("update: %s %s", resp.Error, resp.Detail)
	}
	if got := storedServers(t, s)["echo"].Tools; len(got) != 0 {
		t.Errorf("the old program's policies survived the update: %v", got)
	}
	if !slices.ContainsFunc(resp.Notes, func(n string) bool { return strings.Contains(n, "per-tool policies") }) {
		t.Errorf("the answer does not say the policies were dropped: %q", resp.Notes)
	}
}

// PAUSED, NOT FORGOTTEN. A disabled server stays stored, acknowledged, and in
// the list -- and no agent turn starts it until it is enabled again, when it is
// tried at once like an add.
//
// Neuter check: drop `srv.Disabled ||` from buildRegistry's connect loop -- the
// paused server's tool is offered.
func TestADisabledServerIsKeptButNotStarted(t *testing.T) {
	s := addedEcho(t)
	if !registryOffers(t, s, "echo__echo") {
		t.Fatal("the added server is not offered before it is disabled")
	}

	resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionDisable, "echo"))
	if !resp.Ok || !strings.HasPrefix(resp.Detail, "Disabled echo.") {
		t.Fatalf("disable: ok=%v err=%q detail=%q", resp.Ok, resp.Error, resp.Detail)
	}
	stored := storedServers(t, s)["echo"]
	if !stored.Disabled || !stored.AcknowledgedUnconfined || stored.Command != buildEchoServer(t) {
		t.Errorf("stored as %+v after disable; want it kept, acknowledged and paused", stored)
	}
	if len(resp.Servers) != 1 || !resp.Servers[0].Disabled {
		t.Errorf("the answer lists %+v, want echo marked disabled", resp.Servers)
	}
	if registryOffers(t, s, "echo__echo") {
		t.Error("an agent turn would start the disabled server")
	}
	if again := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionDisable, "echo")); !again.Ok ||
		again.Detail != "echo is already disabled." {
		t.Errorf("disabling twice: ok=%v detail=%q", again.Ok, again.Detail)
	}

	resp = s.mcpResult(context.Background(), manageRequest(protocol.MCPActionEnable, "echo"))
	if !resp.Ok || !strings.HasPrefix(resp.Detail, "Enabled echo. It started and offers") {
		t.Fatalf("enable: ok=%v err=%q detail=%q", resp.Ok, resp.Error, resp.Detail)
	}
	if storedServers(t, s)["echo"].Disabled {
		t.Error("still stored as disabled after enable")
	}
	if !registryOffers(t, s, "echo__echo") {
		t.Error("the enabled server is not offered to the next turn")
	}
}

// An update to a paused server keeps it paused, and does not start it to try it.
func TestAnUpdateKeepsAPausedServerPaused(t *testing.T) {
	s := addedEcho(t)
	if resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionDisable, "echo")); !resp.Ok {
		t.Fatalf("disable: %s", resp.Error)
	}
	update := addRequest("echo", buildEchoServer(t), "--new")
	update.Action, update.AcknowledgedUnconfined = protocol.MCPActionUpdate, true
	resp := s.mcpResult(context.Background(), update)
	if !resp.Ok || !strings.Contains(resp.Detail, "It is disabled, so it was not started") {
		t.Fatalf("update of a paused server: ok=%v err=%q detail=%q", resp.Ok, resp.Error, resp.Detail)
	}
	if !storedServers(t, s)["echo"].Disabled {
		t.Error("the update resumed a paused server")
	}
}

// Enable and disable reach only what the user added, and say where the rest lives.
func TestEnableAndDisableReachOnlyAddedServers(t *testing.T) {
	s := mcpAdminTestServer(t)
	s.cfg.MCP.Servers = map[string]MCPServerConfig{"fromconfig": plainUserServer("uvx")}
	for _, action := range []string{protocol.MCPActionEnable, protocol.MCPActionDisable} {
		if resp := s.mcpResult(context.Background(), manageRequest(action, "fromconfig")); resp.Ok ||
			!strings.Contains(resp.Error, `"disabled"`) || !strings.Contains(resp.Error, "/opt/mochiii/models.agent.json") {
			t.Errorf("%s a config-defined server: ok=%v err=%q", action, resp.Ok, resp.Error)
		}
		if resp := s.mcpResult(context.Background(), manageRequest(action, "nobody")); resp.Ok ||
			!strings.Contains(resp.Error, "no server named nobody") {
			t.Errorf("%s an unknown server: ok=%v err=%q", action, resp.Ok, resp.Error)
		}
		if resp := s.mcpResult(context.Background(), manageRequest(action, "")); resp.Ok || resp.Error == "" {
			t.Errorf("%s with no name was accepted", action)
		}
	}
}

// The list carries each server's parts, so a client can offer to change one
// without splitting the one-line Command back up -- and Env stays names only.
func TestTheListCarriesEachServersParts(t *testing.T) {
	s := mcpAdminTestServer(t)
	req := addRequest("echo", buildEchoServer(t), "--flag", "two words")
	req.Env, req.Cwd, req.AcknowledgedUnconfined = []string{"ECHO_TOKEN"}, protocol.MCPCwdWorkspace, true
	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok || len(resp.Servers) != 1 {
		t.Fatalf("add: ok=%v err=%q servers=%+v", resp.Ok, resp.Error, resp.Servers)
	}
	got := resp.Servers[0]
	if got.Program != buildEchoServer(t) || !slices.Equal(got.Args, []string{"--flag", "two words"}) ||
		!slices.Equal(got.Env, []string{"ECHO_TOKEN"}) || got.Cwd != protocol.MCPCwdWorkspace {
		t.Errorf("listed as %+v", got)
	}
}

// `mochiii-daemon mcp disable|enable <name>` reach the same store, without
// asking for the name to be typed: neither changes what the server runs.
func TestTheDisableAndEnableCommandsReachTheStore(t *testing.T) {
	home := t.TempDir()
	origCred := credentialsPath
	t.Cleanup(func() { credentialsPath = origCred })
	credentialsPath = func() (string, error) { return filepath.Join(home, ".mochiii", "credentials.json"), nil }
	userPath := filepath.Join(home, ".mochiii", userMCPFileName)
	if err := saveUserMCPServers(userPath, map[string]MCPServerConfig{"echo": plainUserServer(buildEchoServer(t))}); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cfgPath, []byte(`{"config_version":1,"default_tier":"p","tiers":{"p":{"slug":"a/b","active":true}},
		"mcp":{"enabled":true,"builtin":{"disabled":true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	origOut := mcpOut
	t.Cleanup(func() { mcpOut = origOut })
	logger := log.New(&bytes.Buffer{}, "", 0)
	run := func(sub string) string {
		var out bytes.Buffer
		mcpOut = &out
		if err := runMCPCommand([]string{sub, "echo", "--config", cfgPath}, logger); err != nil {
			t.Fatalf("mcp %s: %v", sub, err)
		}
		return out.String()
	}

	if out := run("disable"); !strings.Contains(out, "Disabled echo.") {
		t.Errorf("disable said:\n%s", out)
	}
	if got, _, _ := loadUserMCPServers(userPath); !got["echo"].Disabled {
		t.Fatalf("stored %+v after disable", got)
	}
	if out := run("enable"); !strings.Contains(out, "Enabled echo. It started and offers") {
		t.Errorf("enable said:\n%s", out)
	}
	if got, _, _ := loadUserMCPServers(userPath); got["echo"].Disabled {
		t.Fatalf("stored %+v after enable", got)
	}
	if err := runMCPCommand([]string{"disable"}, logger); err == nil || !strings.Contains(err.Error(), "needs the name") {
		t.Errorf("disable with no name: %v", err)
	}
}

// THE SERVERS ACTION STARTS NOTHING. A sidebar redraws whenever it is shown;
// list would launch every configured third-party program each time to ask what
// it offers. Starting one makes its private folder (serverStartDir), so an empty
// run directory after the call is the proof -- and list, asked the same thing,
// is the control that shows the proof would see a start.
//
// Neuter check: answer MCPActionServers with s.mcpList(ctx).
func TestTheServersActionListsWithoutStartingAnything(t *testing.T) {
	s := mcpAdminTestServer(t)
	if err := saveUserMCPServers(s.userServersPath, map[string]MCPServerConfig{"echo": plainUserServer(buildEchoServer(t))}); err != nil {
		t.Fatal(err)
	}
	started := func() int {
		entries, err := os.ReadDir(s.mcpRunDir)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}

	resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionServers, ""))
	if !resp.Ok || !resp.AgentMode || len(resp.Servers) != 1 || resp.Servers[0].Name != "echo" || resp.Report != "" {
		t.Fatalf("servers: ok=%v agent_mode=%v servers=%+v report=%q err=%q", resp.Ok, resp.AgentMode, resp.Servers, resp.Report, resp.Error)
	}
	if n := started(); n != 0 {
		t.Fatalf("the servers action started %d server(s)", n)
	}

	if list := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true}); !list.Ok {
		t.Fatalf("list: %s", list.Error)
	}
	if started() == 0 {
		t.Fatal("list started nothing either, so this test cannot tell a start from none")
	}
}

// A client offers editing and pausing only when the daemon says it can.
func TestTheHandshakeSaysServersCanBeManaged(t *testing.T) {
	s := mcpAdminTestServer(t)
	if !slices.Contains(s.features(), protocol.FeatureMCPManage) {
		t.Errorf("features %q do not include %q", s.features(), protocol.FeatureMCPManage)
	}
}
