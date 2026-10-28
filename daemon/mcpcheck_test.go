package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
)

// HEALTH, AS `claude mcp list` GIVES IT (mcpcheck.go, mcpstatus.go).

func TestEachServerSaysHowItFared(t *testing.T) {
	s, _ := keyedEchoWithFile(t)
	if got := serverInfo(t, s, "echo").StatusLine; got != "! Needs a key: "+echoKeyName {
		t.Errorf("before a key: %q", got)
	}
	check := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionCheck, Name: "echo"})
	if !check.Ok || !strings.HasPrefix(check.Detail, "echo: ✘ Failed to connect: Error: "+echoKeyName+" environment variable is required") {
		t.Errorf("a check without its key: ok=%v detail=%q err=%q", check.Ok, check.Detail, check.Error)
	}

	s.mcpResult(context.Background(), setEnvRequest(echoKeyName, secretSentinel))
	check = s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionCheck, Name: "echo"})
	// echo, slow, secret, plus the rest the fixture offers: the count is the server's own.
	if !strings.HasPrefix(check.Detail, "echo: ✔ Connected (") || serverInfo(t, s, "echo").ToolCount == 0 {
		t.Errorf("a check with its key: %q (tools %d)", check.Detail, serverInfo(t, s, "echo").ToolCount)
	}
	if strings.Contains(check.Detail, secretSentinel) {
		t.Error("the check repeats the key")
	}

	s.mcpResult(context.Background(), manageRequest(protocol.MCPActionDisable, "echo"))
	if got := serverInfo(t, s, "echo").StatusLine; got != "⊘ Disabled" {
		t.Errorf("paused: %q", got)
	}
}

func TestStatusLines(t *testing.T) {
	for _, c := range []struct {
		info protocol.MCPServerInfo
		want string
	}{
		{protocol.MCPServerInfo{}, "○ Not started yet"},
		{protocol.MCPServerInfo{State: protocol.MCPStateRunning, ToolCount: 1, ToolsListed: true}, "✔ Connected (1 tool)"},
		{protocol.MCPServerInfo{State: protocol.MCPStateRunning, ToolCount: 5, ToolsListed: true}, "✔ Connected (5 tools)"},
		{protocol.MCPServerInfo{State: protocol.MCPStateRunning}, "✔ Connected"},
		{protocol.MCPServerInfo{State: protocol.MCPStateToolsFailed}, "! Connected · tools fetch failed"},
		{protocol.MCPServerInfo{State: protocol.MCPStateFailed, StateDetail: "boom"}, "✘ Failed to connect: boom"},
		{protocol.MCPServerInfo{EnvMissing: []string{"A", "B"}}, "! Needs a key: A, B"},
		{protocol.MCPServerInfo{Disabled: true, State: protocol.MCPStateFailed}, "⊘ Disabled"},
	} {
		if got := mcpStatusLine(c.info); got != c.want {
			t.Errorf("%+v: %q, want %q", c.info, got, c.want)
		}
	}
}

// /mcp-server prints each server's whole count and says the ones a turn leaves
// off its list are reached with find_tools -- without promising how many fit.
// Neuter check: drop the hint from writeServerStatus and the first case fails.
func TestTheServerListSaysTheRestAreReachedWithFindTools(t *testing.T) {
	info := func(i protocol.MCPServerInfo) protocol.MCPServerInfo {
		i.Name, i.Command = "redis", "npx redis-mcp"
		i.StatusLine = mcpStatusLine(i)
		return i
	}
	listed := info(protocol.MCPServerInfo{State: protocol.MCPStateRunning, ToolCount: 53, ToolsListed: true})
	for _, c := range []struct {
		name  string
		infos []protocol.MCPServerInfo
		hint  bool
	}{
		{"running, its tools listed", []protocol.MCPServerInfo{listed}, true},
		{"running, not listed yet", []protocol.MCPServerInfo{info(protocol.MCPServerInfo{State: protocol.MCPStateRunning})}, false},
		{"listed, none offered", []protocol.MCPServerInfo{info(protocol.MCPServerInfo{State: protocol.MCPStateRunning, ToolsListed: true})}, false},
		{"disabled", []protocol.MCPServerInfo{info(protocol.MCPServerInfo{Disabled: true, State: protocol.MCPStateRunning, ToolCount: 53, ToolsListed: true})}, false},
		{"failed", []protocol.MCPServerInfo{info(protocol.MCPServerInfo{State: protocol.MCPStateFailed})}, false},
	} {
		var b strings.Builder
		writeServerStatus(&b, c.infos)
		out := b.String()
		if got := strings.Contains(out, "find_tools"); got != c.hint {
			t.Errorf("%s: find_tools hint = %v, want %v:\n%s", c.name, got, c.hint, out)
		}
		if c.hint && !strings.Contains(out, "✔ Connected (53 tools)") {
			t.Errorf("%s: the whole count is not shown:\n%s", c.name, out)
		}
		for _, n := range []string{"up to", "places", "24"} {
			if strings.Contains(out, n) {
				t.Errorf("%s: says %q, a number of places no list outside a turn can know:\n%s", c.name, n, out)
			}
		}
	}
}

func TestATimedOutLauncherIsToldToWaitLonger(t *testing.T) {
	err := fmt.Errorf("server x: %w", context.DeadlineExceeded)
	if hint := timeoutHint(MCPServerConfig{Command: "npx"}, err); !strings.Contains(hint, "downloads the package") {
		t.Errorf("npx timeout: %q", hint)
	}
	if hint := timeoutHint(MCPServerConfig{Command: "/usr/bin/my-server"}, err); hint != "" {
		t.Errorf("a non-launcher got the hint: %q", hint)
	}
	if hint := timeoutHint(MCPServerConfig{Command: "npx"}, errors.New("exit status 1")); hint != "" {
		t.Errorf("a failure that is not a timeout got the hint: %q", hint)
	}
}

// The list's deadline covers a server's own handshake; it used to be the
// budget's (0 when unset) plus 15 seconds, under the 20-second default.
func TestTheListWaitsAsLongAsTheSlowestServerMay(t *testing.T) {
	cfg := &Config{MCP: MCPConfig{Servers: map[string]MCPServerConfig{"slow": {Command: "x", ConnectTimeoutSeconds: 90}}}}
	if got := listDeadline(cfg); got != 90*1e9+mcpListGrace {
		t.Errorf("deadline %v", got)
	}
	if got := listDeadline(&Config{}); got <= mcpListGrace {
		t.Errorf("with nothing set the deadline %v does not cover the default handshake", got)
	}
}

func TestGetDescribesOneServer(t *testing.T) {
	s, _ := keyedEchoWithFile(t)
	resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionGet, "echo"))
	for _, want := range []string{"echo:", "Status: ! Needs a key", "Command: ", echoKeyName + " (no value)", "Starts in: a private folder"} {
		if !strings.Contains(resp.Detail, want) {
			t.Errorf("get does not say %q:\n%s", want, resp.Detail)
		}
	}
}

// ${VAR} TEMPLATES (mcpexpand.go).

func TestTemplatesExpand(t *testing.T) {
	lookup := func(n string) (string, bool) { return map[string]string{"A": "1"}[n], n == "A" }
	got, missing := expandTemplate("x${A}y${B:-dflt}z${C}", lookup)
	if got != "x1ydfltz" || !slices.Equal(missing, []string{"C"}) {
		t.Errorf("got %q missing %v", got, missing)
	}
}

func TestATemplateCannotNameThisProductsKey(t *testing.T) {
	for _, srv := range []MCPServerConfig{
		{Command: "x", Args: []string{"--key", "${MOCHIII_API_KEY}"}},
		{Command: "x", EnvMap: map[string]string{"K": "${openrouter_api_key}"}},
		{Command: "x", EnvMap: map[string]string{"MOCHIII_PROXY_KEY": "v"}},
	} {
		if err := validateServerBlock(srv); err == nil {
			t.Errorf("accepted %+v", srv)
		}
	}
}

// A variable given through env_map reaches the server with its template filled
// in from a stored key.
func TestAnEnvMapTemplateIsFilledInFromAStoredKey(t *testing.T) {
	s, _ := keyedEchoWithFile(t)
	// Only the server under test starts: echo itself has no key here.
	s.mcpResult(context.Background(), manageRequest(protocol.MCPActionDisable, "echo"))
	resp := s.mcpResult(context.Background(), protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionAddJSON, Name: "mapped", AcknowledgedUnconfined: true,
		JSON: []byte(`{"command":"` + jsonEscape(buildEchoServer(t)) + `","args":["--require-env","` + echoKeyName + `"],` +
			`"env":{"` + echoKeyName + `":"${MOCHIII_TEST_SOURCE_KEY}"}}`),
	})
	if !resp.Ok {
		t.Fatalf("add_json: %s", resp.Error)
	}
	info := serverInfo(t, s, "mapped")
	if len(info.Warnings) != 1 || !strings.Contains(info.Warnings[0], "${MOCHIII_TEST_SOURCE_KEY} has no value") {
		t.Errorf("warnings %v", info.Warnings)
	}
	set := s.mcpResult(context.Background(), protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionSetEnv, Name: "mapped", EnvName: "MOCHIII_TEST_SOURCE_KEY", EnvValue: secretSentinel,
	})
	if !set.Ok {
		t.Fatalf("set_env for a template's variable: %s", set.Error)
	}
	if !registryOffers(t, s, "mapped__secret") {
		t.Fatal("the server did not get the value its template names")
	}
}

func jsonEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
}

// ADD FROM ANOTHER CLIENT'S JSON (mcpjson.go).

func TestAnotherClientsEntryBecomesABlockAndKeys(t *testing.T) {
	entry, name, notes, err := parseClientServerJSON([]byte(`{"mcpServers":{"perplexity":{"command":"npx",` +
		`"args":["-y","@perplexity-ai/mcp-server"],"env":{"PERPLEXITY_API_KEY":"pplx-abc","PASS":"${PASS}",` +
		`"MAPPED":"Bearer ${TOKEN}"},"disabled":false}}}`))
	if err != nil || name != "perplexity" || len(notes) != 1 {
		t.Fatalf("parse: name %q notes %v err %v", name, notes, err)
	}
	srv, values, _, _, err := blockFromClientEntry(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(srv.Env, []string{"PASS", "PERPLEXITY_API_KEY"}) || srv.EnvMap["MAPPED"] != "Bearer ${TOKEN}" ||
		values["PERPLEXITY_API_KEY"] != "pplx-abc" || len(values) != 1 {
		t.Errorf("env %v env_map %v values %v", srv.Env, srv.EnvMap, values)
	}
}

func TestAnEntryThatCannotBeAddedSaysWhyWithoutQuotingIt(t *testing.T) {
	for _, c := range []struct{ json, want string }{
		{`{"url":"https://x.example/mcp","headers":{"Authorization":"Bearer ` + secretSentinel + `"}}`, `has a "url" but no "type"`},
		{`{"type":"ws","url":"wss://x.example/mcp"}`, "WebSocket"},
		{`{"args":["x"]}`, `no "command"`},
		{`not json ` + secretSentinel, "not a JSON object"},
		{`{"mcpServers":{"a":{"command":"x"},"b":{"command":"y"}}}`, "one at a time"},
	} {
		entry, _, _, err := parseClientServerJSON([]byte(c.json))
		if err == nil {
			_, _, _, _, err = blockFromClientEntry(entry)
		}
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), secretSentinel) {
			t.Errorf("%s: %v", c.json, err)
		}
	}
}

// add-json keeps an entry's literal key out of the server list.
func TestAddJSONKeepsTheKeyOutOfTheServerList(t *testing.T) {
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	t.Setenv(echoKeyName, "")
	req := protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAddJSON, Name: "echo",
		JSON: []byte(`{"command":"` + jsonEscape(buildEchoServer(t)) + `","args":["--require-env","` + echoKeyName + `"],` +
			`"env":{"` + echoKeyName + `":"` + secretSentinel + `"}}`)}
	first := s.mcpResult(context.Background(), req)
	if !first.NeedsAcknowledgement {
		t.Fatalf("add_json did not ask first: %+v", first)
	}
	req.AcknowledgedUnconfined = true
	done := s.mcpResult(context.Background(), req)
	if !done.Ok || !strings.Contains(done.Detail, "It started and offers") {
		t.Fatalf("ok=%v err=%q detail=%q", done.Ok, done.Error, done.Detail)
	}
	if got := storedServers(t, s)["echo"]; !slices.Equal(got.Env, []string{echoKeyName}) {
		t.Errorf("stored env %v", got.Env)
	}
	if data := readFileString(t, s.userServersPath); strings.Contains(data, secretSentinel) {
		t.Fatal("the key was written into the server list")
	}
}
