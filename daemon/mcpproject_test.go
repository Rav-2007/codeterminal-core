package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// A PROJECT MAY PROPOSE A SERVER; IT CAN NEVER GRANT ONE (mcpproject.go).

// projectWithServer is a daemon whose project's .mcp.json proposes "proj",
// an echo server that writes a marker file when it starts.
func projectWithServer(t *testing.T, extraArgs ...string) (*Server, string) {
	t.Helper()
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	marker := filepath.Join(t.TempDir(), "started")
	args := `"--report-cwd"`
	for _, a := range extraArgs {
		args += `,"` + a + `"`
	}
	writeMCPJSON(t, s.workspace, `{"mcpServers":{"proj":{"command":"`+jsonEscape(buildEchoServer(t))+`","args":[`+args+`],`+
		`"env":{"MARKER_FILE":"`+jsonEscape(marker)+`"}}}}`)
	return s, marker
}

func writeMCPJSON(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, projectMCPFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func approve(t *testing.T, s *Server, name string) protocol.MCPResponse {
	t.Helper()
	first := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionApprove, Name: name})
	if !first.NeedsAcknowledgement || first.Digest == "" {
		t.Fatalf("approve did not ask first: %+v", first)
	}
	return s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionApprove, Name: name,
		AcknowledgedUnconfined: true, Digest: first.Digest})
}

// Proposed is pending, and pending is never started: not by a list, a check
// or a turn.
//
// Neuter check: set AcknowledgedUnconfined = true in withProjectServers -- the
// registry starts the server before anyone approved it.
func TestAProposedServerIsPendingAndNeverStarted(t *testing.T) {
	s, _ := projectWithServer(t)
	info := serverInfo(t, s, "proj")
	if info.Source != protocol.MCPSourceProject || info.Approval != protocol.MCPApprovalPending ||
		info.StatusLine != "⏸ Pending approval" || !strings.HasSuffix(info.ProjectFile, projectMCPFileName) {
		t.Fatalf("info %+v", info)
	}
	s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionCheck})
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	defer func() { _ = registry.Close() }()
	if _, _, err := registry.Lookup(context.Background(), "proj__cwd"); err == nil || len(errs) != 0 {
		t.Fatal("a pending server's tools are offered")
	}
	if serverInfo(t, s, "proj").State != "" {
		t.Error("a pending server was started")
	}
}

// Approved -- the warning, the name typed, the digest echoed -- it starts.
// Changing one argument makes it pending again.
//
// Neuter check: drop `rec.Digest == srv.digest` in withProjectServers -- the
// edited definition stays approved.
func TestApprovalIsForOneExactDefinition(t *testing.T) {
	s, _ := projectWithServer(t)
	first := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionApprove, Name: "proj"})
	for _, want := range []string{"Approving \"proj\"", projectMCPFileName, "YOUR full privileges", "asked again"} {
		if !strings.Contains(first.Detail, want) {
			t.Errorf("the warning does not say %q:\n%s", want, first.Detail)
		}
	}
	if bad := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionApprove, Name: "proj",
		AcknowledgedUnconfined: true, Digest: "sha256:0000"}); bad.Ok {
		t.Fatal("an approval of a digest other than the one shown was accepted")
	}
	if done := approve(t, s, "proj"); !done.Ok {
		t.Fatalf("approve: %+v", done)
	}
	if info := serverInfo(t, s, "proj"); info.Approval != protocol.MCPApprovalApproved {
		t.Fatalf("after approving: %+v", info)
	}
	if !registryOffers(t, s, "proj__cwd") {
		t.Fatal("an approved server's tools are not offered")
	}

	// One argument more, as a pull or an edit would make it.
	writeMCPJSON(t, s.workspace, strings.Replace(readFileString(t, filepath.Join(s.workspace, projectMCPFileName)),
		`"--report-cwd"`, `"--report-cwd","--extra"`, 1))
	if info := serverInfo(t, s, "proj"); info.Approval != protocol.MCPApprovalPending {
		t.Errorf("a changed definition is %q, want pending", info.Approval)
	}
}

// An approvals file inside the project -- shipped with the repository -- is not
// read: the project cannot approve itself.
func TestAProjectCannotApproveItself(t *testing.T) {
	s, _ := projectWithServer(t)
	srv := serverInfo(t, s, "proj")
	planted := `{"version":1,"projects":{"` + jsonEscape(s.workspace) + `":{"proj":{"digest":"` + srv.Digest + `","decision":"approved"}}}}`
	for _, p := range []string{filepath.Join(s.workspace, mcpApprovalsFileName), filepath.Join(s.workspace, ".mochiii", mcpApprovalsFileName)} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(planted), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if info := serverInfo(t, s, "proj"); info.Approval != protocol.MCPApprovalPending {
		t.Fatalf("a planted approval was honoured: %+v", info)
	}
}

func TestARejectedServerStaysOff(t *testing.T) {
	s, _ := projectWithServer(t)
	resp := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionReject, Name: "proj"})
	if !resp.Ok || serverInfo(t, s, "proj").StatusLine != "✘ Rejected" {
		t.Fatalf("reject: %+v", resp)
	}
}

func TestAProjectFileThatCannotBeTrustedIsNotRead(t *testing.T) {
	s := mcpAdminTestServer(t)
	cases := []struct{ body, want string }{
		{`{"mcpServers":{"k":{"command":"npx","env":{"API_KEY":"sk-proj-8f2c1e9a7b3d5f60a4c2e8b1"}}}}`, "looks like a key"},
		{`{"mcpServers":{"k":{"type":"http","url":"https://x.example/mcp","headers":{"Authorization":"Bearer abc"}}}}`, "looks like a key"},
		{`{"mcpServers":{"Bad Name":{"command":"npx"}}}`, "not usable"},
		{`not json`, "not valid JSON"},
	}
	for _, c := range cases {
		writeMCPJSON(t, s.workspace, c.body)
		_, problems := s.agentConfig()
		if !strings.Contains(strings.Join(problems, "\n"), c.want) {
			t.Errorf("%s: problems %v", c.body, problems)
		}
		if strings.Contains(strings.Join(problems, "\n"), "sk-proj-8f2c") {
			t.Error("a problem repeats the key")
		}
	}
	// A ${VAR} is fine, and a project cannot set a tool to allow.
	writeMCPJSON(t, s.workspace, `{"mcpServers":{"k":{"command":"npx","env":{"API_KEY":"${MY_KEY}"},"tools":{"x":"allow"}}}}`)
	cfg, _ := s.agentConfig()
	if srv := cfg.MCP.Servers["k"]; srv.EnvMap["API_KEY"] != "${MY_KEY}" || len(srv.Tools) != 0 {
		t.Errorf("block %+v", srv)
	}

	// A link is not followed.
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	_ = os.WriteFile(target, []byte(`{"mcpServers":{"k":{"command":"npx"}}}`), 0o644)
	_ = os.Remove(filepath.Join(s.workspace, projectMCPFileName))
	if err := os.Symlink(target, filepath.Join(s.workspace, projectMCPFileName)); err == nil {
		if _, problems := s.agentConfig(); !strings.Contains(strings.Join(problems, "\n"), "not a regular file") {
			t.Errorf("a linked .mcp.json was read: %v", problems)
		}
	}
}

// A name the user already uses keeps the user's server.
func TestAProjectServerDoesNotShadowYours(t *testing.T) {
	s, _ := projectWithServer(t)
	add := addRequest("proj", buildEchoServer(t))
	add.AcknowledgedUnconfined = true
	s.mcpResult(context.Background(), add)
	cfg, problems := s.agentConfig()
	if cfg.MCP.Servers["proj"].project != "" || !strings.Contains(strings.Join(problems, "\n"), "already have one by that name") {
		t.Errorf("project server shadowed the user's: %v", problems)
	}
}
