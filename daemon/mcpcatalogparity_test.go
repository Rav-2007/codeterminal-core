package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
)

// THE MARKETPLACE CATALOG AND THE DAEMON AGREE (2026-10-26). mochiii.in's
// catalog build (scripts/mcp/catalog-rules.mjs) was probed against the
// extension's checks and this daemon's, and tightened until it refused
// whatever either would. These keep the daemon's side of that agreement.

// An env name is a variable name by the catalog's rule, which is set-key's
// own: letters, digits and _, not starting with a digit, at most 128.
//
// Neuter check: drop the mcpEnvNamePattern check in validateUserServer -- a
// 129-character name and "MY-VAR" are stored, though no value could ever be
// given for either, and the extension drops such an entry from the catalog.
func TestAnEnvNameNoValueCouldBeGivenForIsRefused(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "_X", "x1", strings.Repeat("A", 128)} {
		if err := validateUserServer("srv", MCPServerConfig{Command: "npx", Env: []string{name}}); err != nil {
			t.Errorf("env name %.20q… refused: %v", name, err)
		}
	}
	for _, name := range []string{strings.Repeat("A", 129), "MY-VAR", "1ST", "A.B", "A B", ""} {
		err := validateUserServer("srv", MCPServerConfig{Command: "npx", Env: []string{name}})
		if err == nil {
			t.Errorf("env name %.20q… accepted", name)
		} else if len(name) > 40 && strings.Contains(err.Error(), name) {
			t.Errorf("the refusal repeats the whole name: %v", err)
		}
	}
}

// A variable the server runs without -- a catalog's required: false -- is
// stored as optional, and its absence is not reported as a missing key: not
// in env_missing, not in the status line, not in the note an add answers
// with. A variable the server needs still is.
//
// Neuter check: make keysMissing return s.envMissing(server, srv.Env) -- the
// optional variable is listed missing again and the status says "Needs a key".
func TestAnOptionalVariableIsNotAMissingKey(t *testing.T) {
	const optional = "MOCHIII_TEST_OPTIONAL_SETTING"
	t.Setenv(optional, "")
	t.Setenv(echoKeyName, "")
	s := mcpAdminTestServer(t)
	req := addRequest("echo", buildEchoServer(t))
	req.Env = []string{optional}
	req.EnvOptional = []string{optional}
	req.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok {
		t.Fatalf("adding echo: %s", resp.Error)
	}
	for _, note := range resp.Notes {
		if strings.Contains(note, optional) {
			t.Errorf("the add says the optional variable is missing: %s", note)
		}
	}
	info := serverInfo(t, s, "echo")
	if len(info.EnvMissing) != 0 || strings.Contains(info.StatusLine, "Needs a key") {
		t.Errorf("optional variable reported missing: env_missing %v, status %q", info.EnvMissing, info.StatusLine)
	}
	if !slices.Equal(info.EnvOptional, []string{optional}) {
		t.Errorf("env_optional = %v, want [%s]", info.EnvOptional, optional)
	}
	if got := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionGet, "echo")).Detail; !strings.Contains(got, optional+" (optional, no value)") {
		t.Errorf("mcp get does not say the variable is optional and unset:\n%s", got)
	}

	// A needed variable beside it is still reported.
	update := addRequest("echo", buildEchoServer(t))
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{optional, echoKeyName}
	update.EnvOptional = []string{optional}
	update.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), update); !resp.Ok {
		t.Fatalf("updating echo: %s", resp.Error)
	}
	info = serverInfo(t, s, "echo")
	if !slices.Equal(info.EnvMissing, []string{echoKeyName}) || !strings.Contains(info.StatusLine, "Needs a key: "+echoKeyName) {
		t.Errorf("env_missing %v, status %q; want only %s", info.EnvMissing, info.StatusLine, echoKeyName)
	}
}

// The edit form does not know which variables are optional, so an update that
// does not say keeps what the server was added with -- for the names it still
// reads.
//
// Neuter check: drop the EnvOptional carry-over in mcpStoreBlockWith -- an edit
// of a catalog server brings "Needs a key" back for a variable it runs without.
func TestAnEditKeepsWhichVariablesAreOptional(t *testing.T) {
	const optional, other = "MOCHIII_TEST_OPTIONAL_SETTING", "MOCHIII_TEST_OTHER_SETTING"
	t.Setenv(optional, "")
	t.Setenv(other, "")
	s := mcpAdminTestServer(t)
	req := addRequest("echo", buildEchoServer(t))
	req.Env = []string{optional, other}
	req.EnvOptional = []string{optional, other}
	req.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), req); !resp.Ok {
		t.Fatalf("adding echo: %s", resp.Error)
	}

	edit := addRequest("echo", buildEchoServer(t), "--some-flag")
	edit.Action = protocol.MCPActionUpdate
	edit.Env = []string{optional} // the edit dropped `other`, and says nothing about optional
	edit.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), edit); !resp.Ok {
		t.Fatalf("updating echo: %s", resp.Error)
	}
	if got := storedServers(t, s)["echo"].EnvOptional; !slices.Equal(got, []string{optional}) {
		t.Errorf("after the edit env_optional = %v, want [%s]", got, optional)
	}
	if info := serverInfo(t, s, "echo"); len(info.EnvMissing) != 0 {
		t.Errorf("after the edit env_missing = %v", info.EnvMissing)
	}
}

// An optional variable is one of the server's own: naming one env does not
// list is refused, in a server added by hand or in models.json.
func TestAnOptionalVariableMustBeOneTheServerReads(t *testing.T) {
	err := validateUserServer("srv", MCPServerConfig{Command: "npx", Env: []string{"A"}, EnvOptional: []string{"B"}})
	if err == nil || !strings.Contains(err.Error(), "env_optional") {
		t.Errorf("an optional variable outside env: err %v", err)
	}
	if err := validateUserServer("srv", MCPServerConfig{Command: "npx", Env: []string{"A", "B"}, EnvOptional: []string{"B"}}); err != nil {
		t.Errorf("an optional variable inside env refused: %v", err)
	}
}

// THE ICON IS A SLUG, NEVER A URL OR A PATH (2026-10-28). These lists are the
// agreement with mochiii_website scripts/mcp/catalog.test.mjs and the extension's
// mcpMarketplace.test.ts, which carry the same strings: change one list and the
// other two have to change with it, or a catalog entry the site publishes is
// dropped by the extension with no reason shown.
//
// "" is not in the refused list: a Go string cannot tell an absent icon from an
// empty one, so it means "no icon" here. The catalog's validators refuse it.
//
// Neuter check: drop the validateIconSlug call from validateUserServer, or make
// validateIconSlug return nil -- every slug in refused is stored, and a URL gets
// into a file a client trusts to hold a name.
func TestAnIconIsASlugAndNeverAURLOrAPath(t *testing.T) {
	accepted := []string{"github", "chrome-devtools", "a", "x1", "0", "a--b", strings.Repeat("x", 32), ""}
	refused := []string{
		strings.Repeat("x", 33),
		"GitHub",
		"git_hub",
		"git hub",
		"https://example.com/github.svg",
		"//evil.example/a",
		"../github",
		"a/b",
		"github.svg",
		"a\n",
		"%2e%2e",
		"ｇithub", // a full-width letter
	}
	if err := validateUserServer("srv", MCPServerConfig{Command: "npx", Icon: "github"}); err != nil {
		t.Fatalf("positive control: a good slug refused: %v", err)
	}
	for _, icon := range accepted {
		if err := validateUserServer("srv", MCPServerConfig{Command: "npx", Icon: icon}); err != nil {
			t.Errorf("icon %q refused: %v", icon, err)
		}
	}
	for _, icon := range refused {
		err := validateUserServer("srv", MCPServerConfig{Command: "npx", Icon: icon})
		if err == nil {
			t.Errorf("icon %q accepted", icon)
		} else if !strings.Contains(err.Error(), "icon") {
			t.Errorf("icon %q refused, but not for being an icon: %v", icon, err)
		}
	}
}
