package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"mochiii/protocol"
)

var (
	echoServerOnce sync.Once
	echoServerPath string
	echoServerErr  error
)

// buildEchoServer compiles daemon/mcp/testdata/echoserver -- a real MCP server
// over stdio -- once per package run.
func buildEchoServer(t *testing.T) string {
	t.Helper()
	echoServerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "echoserver")
		if err != nil {
			echoServerErr = err
			return
		}
		bin := filepath.Join(dir, exeName("echoserver"))
		if out, err := exec.Command("go", "build", "-o", bin, "./mcp/testdata/echoserver").CombinedOutput(); err != nil {
			echoServerErr = &buildError{out: string(out)}
			return
		}
		echoServerPath = bin
	})
	if echoServerErr != nil {
		t.Fatalf("building the echo MCP server: %v", echoServerErr)
	}
	return echoServerPath
}

// mcpAdminTestServer is a daemon with agent mode on, its own tools off (so a
// report shows only what a test put there), and somewhere to keep added servers.
func mcpAdminTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		cfg:          &Config{MCP: MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Disabled: true}}},
		workspace:    t.TempDir(),
		logger:       log.New(&bytes.Buffer{}, "", 0),
		serverExtras: serverExtras{configPath: "/opt/mochiii/models.agent.json", userServersPath: userServersFile(t)},
	}
}

func addRequest(name, command string, args ...string) protocol.MCPRequest {
	return protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAdd, Name: name, Command: command, Args: args}
}

func storedServers(t *testing.T, s *Server) map[string]MCPServerConfig {
	t.Helper()
	got, _, err := loadUserMCPServers(s.userServersPath)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// THE REPORT THIS ANSWERS (OWNER, 2026-10-11): "MCP is not working in the CLI".
// /mcp-server was answered by a second copy of the daemon binary, which loaded
// ./models.json for itself -- agent mode off -- while the daemon that was
// running had its tools on. The running daemon now answers for itself, and what
// a models.json in the working directory says is not consulted at all.
//
// Neuter check: make mcpList load resolveConfigPath() instead of using s.cfg.
func TestTheRunningDaemonReportsItsOwnToolsNotAConfigInTheWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	// The trap: a models.json where a second process would look, with no tools.
	if err := os.WriteFile(filepath.Join(cwd, "models.json"),
		[]byte(`{"config_version":1,"default_tier":"p","tiers":{"p":{"slug":"a/b","active":true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	s := mcpAdminTestServer(t)
	s.cfg.MCP.Builtin.Disabled = false
	resp := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true})
	if !resp.Ok || !resp.AgentMode {
		t.Fatalf("ok=%v agent_mode=%v (%s); a daemon with tools on must say so", resp.Ok, resp.AgentMode, resp.Error)
	}
	for _, want := range []string{"Agent mode is ON", "This daemon's config: /opt/mochiii/models.agent.json", "read_file", "first_party"} {
		if !strings.Contains(resp.Report, want) {
			t.Errorf("the report does not say %q:\n%s", want, resp.Report)
		}
	}
	if strings.Contains(resp.Report, "Agent mode is OFF") {
		t.Errorf("a daemon with its tools on was reported as off:\n%s", resp.Report)
	}
	// NO EXTERNAL SERVER IS CONFIGURED, so nothing may be said to run in one.
	// The daemon's own tools that can start a program get their own sentence.
	if strings.Contains(resp.Report, "external MCP servers") {
		t.Errorf("a daemon with no external server reports tools running in one:\n%s", resp.Report)
	}
	if !strings.Contains(resp.Report, "of this daemon's own tools are marked NO as well") {
		t.Errorf("the daemon's own unconfined tools are not accounted for:\n%s", resp.Report)
	}

	// And a daemon that really has no tools says that, naming ITS config.
	off := mcpAdminTestServer(t)
	off.cfg.MCP.Enabled = false
	resp = off.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionList})
	if !resp.Ok || resp.AgentMode {
		t.Fatalf("ok=%v agent_mode=%v for a daemon with agent mode off", resp.Ok, resp.AgentMode)
	}
	if !strings.Contains(resp.Report, "Agent mode is OFF (mcp.enabled is not true in /opt/mochiii/models.agent.json)") {
		t.Errorf("the off report does not name the daemon's own config:\n%s", resp.Report)
	}
}

// TWO STEPS, AND THE FIRST STORES NOTHING. An unconfined program is not added
// by a single request: the first answer is what is being agreed to.
//
// Neuter check: drop the `if !req.AcknowledgedUnconfined` return in mcpAdd.
func TestAddingAServerAsksFirstAndStoresNothingUntilAcknowledged(t *testing.T) {
	s := mcpAdminTestServer(t)
	req := addRequest("echo", buildEchoServer(t), "--flag")
	req.Env = []string{"ECHO_TOKEN"}

	resp := s.mcpResult(context.Background(), req)
	if resp.Ok || !resp.NeedsAcknowledgement || resp.Error != "" {
		t.Fatalf("ok=%v needs_ack=%v err=%q; want a refusal that asks", resp.Ok, resp.NeedsAcknowledgement, resp.Error)
	}
	for _, want := range []string{`"echo"`, buildEchoServer(t) + " --flag", "YOUR full privileges", "cannot confine", "asks before it runs", "ECHO_TOKEN"} {
		if !strings.Contains(resp.Detail, want) {
			t.Errorf("the warning does not say %q:\n%s", want, resp.Detail)
		}
	}
	if got := storedServers(t, s); len(got) != 0 {
		t.Fatalf("%d server(s) were stored before anything was acknowledged", len(got))
	}
	if _, err := os.Stat(s.userServersPath); !os.IsNotExist(err) {
		t.Error("a file was written before anything was acknowledged")
	}
}

// ADDED, TRIED, AND LIVE WITHOUT A RESTART: an acknowledged add stores the
// server, starts it once to learn what it offers, and the very next list -- and
// so the very next agent turn, which builds its tools the same way -- has them.
func TestAnAcknowledgedServerIsStoredTriedAndOfferedAtOnce(t *testing.T) {
	s := mcpAdminTestServer(t)
	req := addRequest("echo", buildEchoServer(t))
	req.AcknowledgedUnconfined = true

	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok || resp.Error != "" {
		t.Fatalf("add failed: %s %s", resp.Error, resp.Detail)
	}
	if !strings.Contains(resp.Detail, "It started and offers") || !strings.Contains(resp.Detail, "echo") ||
		!strings.Contains(resp.Detail, "each asks before it runs") {
		t.Errorf("the answer does not say the server works and what it offers:\n%s", resp.Detail)
	}
	stored := storedServers(t, s)
	if got := stored["echo"]; got.Command != buildEchoServer(t) || !got.AcknowledgedUnconfined {
		t.Fatalf("stored as %+v", got)
	}
	if len(resp.Servers) != 1 || resp.Servers[0].Name != "echo" || resp.Servers[0].Source != protocol.MCPSourceUser {
		t.Errorf("the answer lists %+v, want echo marked as the user's", resp.Servers)
	}

	list := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true})
	for _, want := range []string{"echo__echo", "third_party", "NO", "ask", "external MCP servers, which this product CANNOT confine"} {
		if !strings.Contains(list.Report, want) {
			t.Errorf("the tool report does not show %q:\n%s", want, list.Report)
		}
	}
	// The policy is ASK: an added server's tool never runs unasked.
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	defer func() { _ = registry.Close() }()
	if len(errs) != 0 {
		t.Fatalf("an agent turn could not start the added server: %v", errs)
	}
	_, policy, err := registry.Lookup(context.Background(), "echo__echo")
	if err != nil || string(policy) != PolicyAsk {
		t.Errorf("echo__echo resolves to policy %q (%v), want ask", policy, err)
	}

	// Removed: gone from the file, gone from the next turn.
	removed := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionRemove, Name: "echo"})
	if !removed.Ok || !strings.Contains(removed.Detail, "Removed echo") {
		t.Fatalf("remove: %s %s", removed.Error, removed.Detail)
	}
	if got := storedServers(t, s); len(got) != 0 {
		t.Errorf("%d server(s) still stored after remove", len(got))
	}
	if cfg, _ := s.agentConfig(); len(cfg.MCP.Servers) != 0 {
		t.Error("the next turn would still start the removed server")
	}
}

// EVERYTHING THAT MUST NOT BE STORED IS REFUSED BEFORE THE QUESTION IS ASKED,
// so nobody types a confirmation for something that then fails.
func TestWhatMayNotBeAdded(t *testing.T) {
	s := mcpAdminTestServer(t)
	s.cfg.MCP.Servers = map[string]MCPServerConfig{"fromconfig": plainUserServer("uvx")}
	echo := buildEchoServer(t)

	withEnv := func(env ...string) protocol.MCPRequest {
		r := addRequest("ok", echo)
		r.Env = env
		return r
	}
	for name, req := range map[string]protocol.MCPRequest{
		"no name":                addRequest("", echo),
		"the built-ins' name":    addRequest("builtin", echo),
		"a name with a space":    addRequest("my server", echo),
		"a name with an escape":  addRequest("x\x1b[2J", echo),
		"no command":             addRequest("ok", ""),
		"a relative command":     addRequest("ok", "./server"),
		"a NAME=VALUE env entry": withEnv("GITHUB_TOKEN=ghp_secretsecretsecret"),
		"this product's own key": withEnv("MOCHIII_API_KEY"),
		"a name the config uses": addRequest("fromconfig", echo),
	} {
		for _, ack := range []bool{false, true} {
			req.AcknowledgedUnconfined = ack
			resp := s.mcpResult(context.Background(), req)
			if resp.Ok || resp.Error == "" || resp.NeedsAcknowledgement {
				t.Errorf("%s (acknowledged=%v): ok=%v needs_ack=%v err=%q; want a plain refusal", name, ack, resp.Ok, resp.NeedsAcknowledgement, resp.Error)
			}
			if strings.Contains(resp.Error, "ghp_secret") {
				t.Errorf("%s: the refusal repeats the secret it was refusing", name)
			}
		}
	}
	if got := storedServers(t, s); len(got) != 0 {
		t.Errorf("a refused add stored %v", sortedServerNames(got))
	}

	// Twice under one name: the second is refused, the first is untouched.
	first := addRequest("ok", echo)
	first.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), first); !resp.Ok {
		t.Fatalf("first add: %s", resp.Error)
	}
	again := addRequest("ok", "npx")
	again.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), again); resp.Ok || !strings.Contains(resp.Error, "already added") {
		t.Errorf("a second add under the same name: ok=%v err=%q", resp.Ok, resp.Error)
	}
	if got := storedServers(t, s)["ok"].Command; got != echo {
		t.Errorf("the second add replaced the first (%q)", got)
	}
}

// "ADDED" IS NOT "WORKS", and the answer never blurs them: a program that is not
// there, and a program that starts and is not an MCP server, are each saved and
// each said -- the user finds out now, not in the middle of their next question.
func TestAnAddedServerThatDoesNotStartSaysSo(t *testing.T) {
	s := mcpAdminTestServer(t)
	missing := addRequest("ghost", "no-such-program-mochiii-test")
	missing.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), missing)
	if !resp.Ok || !strings.Contains(resp.Detail, "was not found on this daemon's PATH") || strings.Contains(resp.Detail, "It started") {
		t.Errorf("a program that is not installed: ok=%v\n%s", resp.Ok, resp.Detail)
	}

	if runtime.GOOS == "windows" {
		return
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` to stand in for a program that is not an MCP server")
	}
	notMCP := addRequest("notmcp", truePath)
	notMCP.AcknowledgedUnconfined = true
	resp = s.mcpResult(context.Background(), notMCP)
	if !resp.Ok || !strings.Contains(resp.Detail, "did NOT start when tried") || strings.Contains(resp.Detail, "It started and offers") {
		t.Errorf("a program that is not an MCP server: ok=%v\n%s", resp.Ok, resp.Detail)
	}
	if got := storedServers(t, s); len(got) != 2 {
		t.Errorf("%d stored; both are kept, like a key that could not be verified", len(got))
	}
}

// What an add leaves the user to do is said with it: a variable the daemon does
// not have, and agent mode being off.
func TestAnAddSaysWhatElseIsNeeded(t *testing.T) {
	s := mcpAdminTestServer(t)
	s.cfg.MCP.Enabled = false
	t.Setenv("MOCHIII_TEST_SET_VARIABLE", "x")
	req := addRequest("echo", buildEchoServer(t))
	req.Env = []string{"MOCHIII_TEST_SET_VARIABLE", "MOCHIII_TEST_UNSET_VARIABLE"}
	req.AcknowledgedUnconfined = true

	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok {
		t.Fatalf("add: %s", resp.Error)
	}
	notes := strings.Join(resp.Notes, "\n")
	if !strings.Contains(notes, "MOCHIII_TEST_UNSET_VARIABLE is not set") || strings.Contains(notes, "MOCHIII_TEST_SET_VARIABLE is not set") {
		t.Errorf("the notes are wrong about which variable is missing:\n%s", notes)
	}
	if !strings.Contains(notes, "agent mode is off in /opt/mochiii/models.agent.json") {
		t.Errorf("nothing says agent mode is off, so the server will not run:\n%s", notes)
	}
	list := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true})
	if !strings.Contains(list.Report, "1 MCP server(s) are configured and will not run until agent mode is on") {
		t.Errorf("the off report hides the configured server:\n%s", list.Report)
	}
}

// Remove says where a server lives when it is not this command's to remove.
func TestRemoveRefusesWhatItDidNotAdd(t *testing.T) {
	s := mcpAdminTestServer(t)
	s.cfg.MCP.Servers = map[string]MCPServerConfig{"fromconfig": plainUserServer("uvx")}
	remove := func(name string) protocol.MCPResponse {
		return s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionRemove, Name: name})
	}
	if resp := remove("fromconfig"); resp.Ok || !strings.Contains(resp.Error, "/opt/mochiii/models.agent.json") {
		t.Errorf("a config-defined server: ok=%v err=%q", resp.Ok, resp.Error)
	}
	if resp := remove("nobody"); resp.Ok || !strings.Contains(resp.Error, "no server named nobody") {
		t.Errorf("an unknown server: ok=%v err=%q", resp.Ok, resp.Error)
	}
	if resp := remove(""); resp.Ok || resp.Error == "" {
		t.Error("a remove with no name was accepted")
	}
	if resp := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: "restart"}); resp.Ok || !strings.Contains(resp.Error, "unknown mcp action") {
		t.Errorf("an unknown action: ok=%v err=%q", resp.Ok, resp.Error)
	}
}

// The request is told apart by its own key, and reaches its handler over the
// socket -- not the prompt path, which would answer "prompt is empty".
func TestAnMCPRequestIsDispatchedToItsHandler(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"mcp":true}`, true},
		{`{"mcp":false,"action":"list"}`, true},
		{`{"mcp":"yes"}`, false},
		{`{"prompt":"what is mcp"}`, false},
		{`{"mcp":true,"connect":true}`, false}, // two discriminators name nothing
	} {
		if got := isMCPRequest([]byte(tc.raw)); got != tc.want {
			t.Errorf("isMCPRequest(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}

	var logbuf bytes.Buffer
	srv := &Server{
		apiBase: "http://127.0.0.1:1", logger: log.New(&logbuf, "", 0), workspace: t.TempDir(),
		cfg: &Config{}, serverExtras: serverExtras{configPath: "/opt/mochiii/models.json"},
	}
	replies := driveOneRequest(t, srv, &logbuf, fmt.Sprintf(`{"protocol_version":%d,"mcp":true}`, protocol.ProtocolVersion))
	joined := strings.Join(replies, " ")
	if !strings.Contains(joined, "Agent mode is OFF") || !strings.Contains(joined, `"agent_mode":false`) {
		t.Errorf("the socket did not answer an mcp request as one: %s", joined)
	}
	if strings.Contains(joined, "prompt is empty") {
		t.Errorf("the request fell through to the prompt path: %s", joined)
	}

	// THE TWO SHARE ONE BRANCH in serveConn (serverextras.go says why), so the
	// other half is checked here too: a connect request is still answered as a
	// connect, and a body that names both is answered as neither.
	connectSrv, _ := connectServer(t)
	connectSrv.logger, connectSrv.workspace = log.New(&logbuf, "", 0), t.TempDir()
	replies = driveOneRequest(t, connectSrv, &logbuf, fmt.Sprintf(`{"protocol_version":%d,"connect":true,"show":true}`, protocol.ProtocolVersion))
	if joined := strings.Join(replies, " "); !strings.Contains(joined, `"outcome":"shown"`) || strings.Contains(joined, "agent_mode") {
		t.Errorf("a connect request sent down the shared branch was not answered as a connect: %s", joined)
	}
	replies = driveOneRequest(t, srv, &logbuf, fmt.Sprintf(`{"protocol_version":%d,"mcp":true,"connect":true}`, protocol.ProtocolVersion))
	if joined := strings.Join(replies, " "); strings.Contains(joined, "agent_mode") || strings.Contains(joined, `"outcome"`) {
		t.Errorf("a body naming both requests was answered as one of them: %s", joined)
	}

	var found bool
	for _, f := range (&Server{}).features() {
		found = found || f == protocol.FeatureMCPAdmin
	}
	if !found {
		t.Error("features() does not name mcp_admin, so a client cannot tell this daemon from an older one")
	}
}

// `mochiii-daemon mcp add` asks for the name to be typed, and adds nothing when
// it is not.
func TestTheAddCommandNeedsTheNameTyped(t *testing.T) {
	home := t.TempDir()
	origCred := credentialsPath
	t.Cleanup(func() { credentialsPath = origCred })
	credentialsPath = func() (string, error) { return filepath.Join(home, ".mochiii", "credentials.json"), nil }
	userPath := filepath.Join(home, ".mochiii", userMCPFileName)

	cfgPath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cfgPath, []byte(`{"config_version":1,"default_tier":"p","tiers":{"p":{"slug":"a/b","active":true}},
		"mcp":{"enabled":true,"builtin":{"disabled":true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	origIn, origOut := mcpIn, mcpOut
	t.Cleanup(func() { mcpIn, mcpOut = origIn, origOut })
	logger := log.New(&bytes.Buffer{}, "", 0)
	echo := buildEchoServer(t)
	run := func(typed string, args ...string) string {
		var out bytes.Buffer
		mcpIn, mcpOut = strings.NewReader(typed), &out
		if err := runMCPCommand(append(args, "--config", cfgPath, "--", echo), logger); err != nil {
			t.Fatalf("mcp %v: %v", args, err)
		}
		return out.String()
	}

	out := run("no\n", "add", "echo")
	if !strings.Contains(out, "YOUR full privileges") || !strings.Contains(out, "Cancelled; nothing was added") {
		t.Errorf("a wrong answer did not cancel:\n%s", out)
	}
	if got, _, _ := loadUserMCPServers(userPath); len(got) != 0 {
		t.Fatal("a server was added without the name being typed")
	}

	out = run("echo\n", "add", "echo")
	if !strings.Contains(out, "Added echo. It started and offers") {
		t.Errorf("typing the name did not add the server:\n%s", out)
	}
	if got, _, _ := loadUserMCPServers(userPath); got["echo"].Command != echo {
		t.Fatalf("stored %+v", got)
	}

	var removed bytes.Buffer
	mcpOut = &removed
	if err := runMCPCommand([]string{"remove", "echo", "--config", cfgPath}, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(removed.String(), "Removed echo") {
		t.Errorf("remove said:\n%s", removed.String())
	}
	if err := runMCPCommand([]string{"restart"}, logger); err == nil || !strings.Contains(err.Error(), "unknown mcp subcommand") {
		t.Errorf("an unknown subcommand: %v", err)
	}
}

// "OFFERS FOUR TOOLS" IS NOT "THE MODEL WILL SEE FOUR". Seen in the real app
// (2026-10-11): the shipped agent config shows the model at most twelve tools,
// the daemon's own took eleven of the places, and a server reported as "offers
// 4 tool(s)" was going to contribute one. The add answers with the menu the next
// turn will actually build, and says how to offer the rest.
//
// Neuter check: report len(offered)+len(leftOut) as offered, without the
// leftOut branch, in tryAddedServer.
func TestAnAddSaysHowManyToolsWillActuallyReachTheModel(t *testing.T) {
	s := mcpAdminTestServer(t)
	s.cfg.MCP.Budget.MaxAdvertisedTools = 1
	req := addRequest("echo", buildEchoServer(t))
	req.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok {
		t.Fatalf("add: %s", resp.Error)
	}
	for _, want := range []string{"offers 4 tool(s), but only 1 of them will reach the model", "at most 1 tools", "Left out:",
		"mcp.budget.max_advertised_tools in /opt/mochiii/models.agent.json", "costs tokens"} {
		if !strings.Contains(resp.Detail, want) {
			t.Errorf("the answer does not say %q:\n%s", want, resp.Detail)
		}
	}
	if strings.Contains(resp.Detail, "They are available from your next question") {
		t.Errorf("the answer claims all its tools are available:\n%s", resp.Detail)
	}

	// The daemon's own tools come first, so they can take every place.
	full := mcpAdminTestServer(t)
	full.cfg.MCP.Builtin.Disabled = false
	full.cfg.MCP.Budget.MaxAdvertisedTools = 2
	resp = full.mcpResult(context.Background(), req)
	if !resp.Ok || !strings.Contains(resp.Detail, "none of them will reach the model") {
		t.Errorf("a menu with no room left: ok=%v\n%s", resp.Ok, resp.Detail)
	}
	// And an added server never pushes the daemon's own tools off the menu.
	registry, _ := full.buildRegistry(context.Background(), full.logger, &proposalSink{}, "")
	defer func() { _ = registry.Close() }()
	tools, _ := registry.Advertised(context.Background())
	for _, tool := range tools {
		if tool.Lane != protocol.LaneFirstParty {
			t.Errorf("%s took a place from the daemon's own tools", tool.QualifiedName())
		}
	}
}
