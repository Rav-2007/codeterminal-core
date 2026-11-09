package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
)

// A KEY ON A SERVER'S COMMAND LINE (mcpargkeys.go). FOUND 2026-11-08: a dummy
// key in args was accepted, stored as written, printed in the add log line and
// shown wherever the server is listed.
//
// NEUTER CHECKS:
//   - return nil at the top of refuseArgKeys: TestAKeyInArgsIsRefusedOnEveryWayIn fails (stored, logged);
//   - return args unchanged from protocol.MaskArgKeys: TestAServerStoredWithAKeyInArgsStillRunsButIsShownMasked fails;
//   - drop argKeyWarning from serverInfos: the same test fails on the warning.

const dummyArgKey = "DUMMY-ARGKEY-7731"

func TestCommandLineShowsAKeyMasked(t *testing.T) {
	args := []string{"-y", "pkg", "--api-key", dummyArgKey, "PROBE_TOKEN=" + dummyArgKey}
	line := commandLine(MCPServerConfig{Command: "npx", Args: args})
	if strings.Contains(line, dummyArgKey) || !strings.Contains(line, "--api-key *** PROBE_TOKEN=***") {
		t.Errorf("commandLine does not show the key masked: %s", line)
	}
	if args[3] != dummyArgKey {
		t.Error("masking changed the stored arguments")
	}
}

// Every way a server reaches the daemon as a request -- add (asked and
// acknowledged), another client's JSON, and a change -- is refused with the
// way to do it instead, and the key is in no answer, no log line, no file.
func TestAKeyInArgsIsRefusedOnEveryWayIn(t *testing.T) {
	s := mcpAdminTestServer(t)
	var logged bytes.Buffer
	s.logger = log.New(&logged, "", 0)
	echo := buildEchoServer(t)

	existing := addRequest("argsrv", echo)
	existing.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), existing); !resp.Ok {
		t.Fatalf("adding a plain server first: %s", resp.Error)
	}

	update := addRequest("argsrv", echo, "--api-key", dummyArgKey)
	update.Action = protocol.MCPActionUpdate
	entry, _ := json.Marshal(map[string]any{"command": echo, "args": []string{"PROBE_TOKEN=" + dummyArgKey}})
	jsonReq := protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAddJSON, Name: "fromjson", JSON: entry}
	cases := map[string]protocol.MCPRequest{
		"add, --api-key VALUE": addRequest("newsrv", echo, "--api-key", dummyArgKey),
		"add, --token=VALUE":   addRequest("newsrv", echo, "--token="+dummyArgKey),
		"add, NAME=value":      addRequest("newsrv", echo, "PROBE_TOKEN="+dummyArgKey),
		"add_json, NAME=value": jsonReq,
		"update, --api-key":    update,
	}
	for what, req := range cases {
		for _, ack := range []bool{false, true} {
			req.AcknowledgedUnconfined = ack
			resp := s.mcpResult(context.Background(), req)
			if resp.Ok || resp.NeedsAcknowledgement || resp.Error == "" {
				t.Errorf("%s (ack=%v): ok=%v needs_ack=%v err=%q; want a refusal", what, ack, resp.Ok, resp.NeedsAcknowledgement, resp.Error)
				continue
			}
			body, _ := json.Marshal(resp)
			if bytes.Contains(body, []byte(dummyArgKey)) {
				t.Errorf("%s: the answer repeats the key: %s", what, body)
			}
			if !strings.Contains(resp.Error, "/mcp-server key "+req.Name+" ") || !strings.Contains(resp.Error, "${") {
				t.Errorf("%s: the refusal does not say where the key goes instead: %q", what, resp.Error)
			}
		}
	}
	stored := storedServers(t, s)
	if len(stored) != 1 || len(stored["argsrv"].Args) != 0 {
		t.Errorf("a refused request changed what is stored: %+v", stored)
	}
	if strings.Contains(logged.String(), dummyArgKey) {
		t.Errorf("the daemon log holds the key:\n%s", logged.String())
	}
}

// The way to do it instead works: a template after the flag is allowed, and is
// filled at launch from the value given with set-key, which accepts it.
func TestATemplateAfterACredentialFlagIsAllowedAndFilledFromSetKey(t *testing.T) {
	s := mcpAdminTestServer(t)
	echo := buildEchoServer(t)
	add := addRequest("tmplsrv", echo, "--api-key", "${PROBE_API_KEY}")
	add.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), add); !resp.Ok {
		t.Fatalf("adding with --api-key ${PROBE_API_KEY}: %s", resp.Error)
	}
	set := protocol.MCPRequest{MCP: true, Action: protocol.MCPActionSetEnv, Name: "tmplsrv", EnvName: "PROBE_API_KEY", EnvValue: dummyArgKey}
	if resp := s.mcpResult(context.Background(), set); resp.Error != "" {
		t.Fatalf("set-key for the template's variable: %s", resp.Error)
	}
	args, _, _ := s.expandedLaunch("tmplsrv", storedServers(t, s)["tmplsrv"])
	if !slices.Contains(args, dummyArgKey) {
		t.Errorf("the template was not filled from set-key at launch: %q", args)
	}
}

// A server stored before this rule, with a key in its arguments, is not
// refused at load -- it goes on starting with its key -- but every place that
// shows or logs its command line shows it masked, with a warning on how to
// move the key.
func TestAServerStoredWithAKeyInArgsStillRunsButIsShownMasked(t *testing.T) {
	s := mcpAdminTestServer(t)
	var logged bytes.Buffer
	s.logger = log.New(&logged, "", 0)
	echo := buildEchoServer(t)
	old := MCPServerConfig{Command: echo, Args: []string{"--api-key", dummyArgKey, "PROBE_TOKEN=" + dummyArgKey}, AcknowledgedUnconfined: true}
	if err := saveUserMCPServers(s.userServersPath, map[string]MCPServerConfig{"legacy": old}); err != nil {
		t.Fatal(err)
	}

	loaded, problems, err := loadUserMCPServers(s.userServersPath)
	if err != nil || len(problems) != 0 || loaded["legacy"].Command == "" {
		t.Fatalf("the stored server no longer loads: err=%v problems=%v", err, problems)
	}
	if args, _, _ := s.expandedLaunch("legacy", loaded["legacy"]); !slices.Contains(args, dummyArgKey) {
		t.Errorf("the stored server would no longer start with its key: %q", args)
	}

	resp := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionList})
	var info *protocol.MCPServerInfo
	for i := range resp.Servers {
		if resp.Servers[i].Name == "legacy" {
			info = &resp.Servers[i]
		}
	}
	if info == nil {
		t.Fatalf("the list does not have the stored server: %+v", resp)
	}
	body, _ := json.Marshal(resp)
	if bytes.Contains(body, []byte(dummyArgKey)) {
		t.Errorf("the list shows the key: %s", body)
	}
	if !strings.Contains(info.Command, "--api-key ***") || !slices.Contains(info.Args, "PROBE_TOKEN=***") {
		t.Errorf("the command line is not shown masked: command=%q args=%q", info.Command, info.Args)
	}
	warned := false
	for _, w := range info.Warnings {
		warned = warned || strings.Contains(w, "/mcp-server key legacy API_KEY")
	}
	if !warned {
		t.Errorf("no warning says how to move the key: %q", info.Warnings)
	}
	check := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionCheck, Name: "legacy"})
	if body, _ := json.Marshal(check); bytes.Contains(body, []byte(dummyArgKey)) {
		t.Errorf("check shows the key: %s", body)
	}
	if strings.Contains(logged.String(), dummyArgKey) {
		t.Errorf("the daemon log holds the key:\n%s", logged.String())
	}
}
