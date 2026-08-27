package main

import (
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"codeterminal/daemon/mcp"
)

// THE DECISION NOBODY COULD SEE.
//
// mcp.LaunchConfig has a Sandbox field. WrapCommand honours it. bwrap is
// installed on most Linux hosts and this daemon already drives it for
// sandbox_exec. And laneBLaunchConfig does not set it -- so every third-party
// MCP server runs with the user's own privileges.
//
// That is deliberate and written down (SECURITY_MODEL.md: "Lane B is
// unconfined... accepted deliberately, mitigated by consent and audit;
// sandboxing is a non-goal for v1, not an oversight"), and read inline it looks
// exactly like the oversight it is not: the plumbing sits right there, unused.
// An audit read it as "partial" for the wrong reason, which is the mild version
// of the failure; the severe versions are a future edit "fixing" the omission
// and changing the product's security posture with nobody reviewing that as a
// change, or a future reader assuming containment and misjudging what a Lane B
// approval is worth.
//
// So the posture is pinned here rather than asserted in prose. If this test
// fails, nothing is broken -- someone has changed what the product promises,
// and they are being sent to the document that says why it promised the other
// thing.
func TestLaneBLaunchesUnconfinedOnPurpose(t *testing.T) {
	cfg := &Config{MCP: MCPConfig{Enabled: true}}
	srv := MCPServerConfig{
		Command:                "some-third-party-server",
		Args:                   []string{"--stdio"},
		AcknowledgedUnconfined: true,
	}

	lc := laneBLaunchConfig("thirdparty", srv, cfg, io.Discard, log.New(io.Discard, "", 0).Printf)

	// THE EFFECTIVE ANSWER, not the field. Asserting Sandbox == zero would pass
	// just as happily if ResolveMode later decided a zero config meant "confine
	// it", and the property that matters is what the process actually gets.
	if mcp.Confines(lc.Sandbox) {
		t.Error("a Lane B server is now confined. That is a CHANGE IN WHAT THIS PRODUCT PROMISES, " +
			"not a bug fix: SECURITY_MODEL.md tells users Lane B is unconfined and that consent, " +
			"not containment, is what stands in front of it. Update that document and the approval " +
			"copy in the clients together with this test, or revert.")
	}
	if lc.Sandbox.Mode != "" || lc.Sandbox.WorkspaceRoot != "" {
		t.Errorf("laneBLaunchConfig now asks for confinement (%+v); see above", lc.Sandbox)
	}

	// THE BOUNDS THAT ARE CLAIMED MUST SURVIVE, and "unset" must never be able
	// to mean "unbounded". An unconfigured budget passes zero here on purpose --
	// mcp.Connect reads zero as its own default (see LaunchConfig: "there is no
	// way to ask for no bound") -- so the property to pin is that those defaults
	// are real numbers, not that this struct carries them.
	if mcp.DefaultMaxMessageBytes <= 0 || mcp.DefaultConnectTimeout <= 0 {
		t.Fatal("the package defaults are no longer bounds, so an unconfigured server is unbounded")
	}

	// And a budget the user DID set has to reach the process, or the setting is
	// decorative.
	configured := &Config{MCP: MCPConfig{Enabled: true, Budget: MCPBudgetConfig{
		MaxMessageBytes:       4096,
		ConnectTimeoutSeconds: 7,
	}}}
	got := laneBLaunchConfig("thirdparty", srv, configured, io.Discard, log.New(io.Discard, "", 0).Printf)
	if got.MaxMessageBytes != 4096 {
		t.Errorf("MaxMessageBytes = %d, want the configured 4096", got.MaxMessageBytes)
	}
	if got.ConnectTimeout != 7*time.Second {
		t.Errorf("ConnectTimeout = %s, want the configured 7s", got.ConnectTimeout)
	}
	if mcp.Confines(got.Sandbox) {
		t.Error("configuring a budget somehow turned confinement on")
	}
	// CREDENTIALS NEVER TRAVEL TO A THIRD PARTY, sandbox or no sandbox -- and
	// this is the half of the posture that makes the other half defensible.
	//
	// The first version of this check ranged over lc.EnvAllow looking for a name
	// containing "KEY". The fixture above sets no Env, so it ranged over nil,
	// asserted nothing, and passed: a vacuous assertion in the security test
	// whose entire purpose is to pin a security decision. It was also aimed at
	// the wrong thing -- EnvAllow is the USER's list, so the daemon adding a
	// credential to it was never the risk. The risk is a Lane B process being
	// handed this daemon's inference credentials, and the gate against that is
	// ValidateEnvAllowList, which Connect runs before the launch.
	//
	// So: hand laneBLaunchConfig a server configured to receive exactly that,
	// and require the gate in front of the launch to refuse it.
	credentialed := srv
	credentialed.Env = []string{"HOME", "CODETERMINAL_API_KEY"}
	withCred := laneBLaunchConfig("thirdparty", credentialed, cfg, io.Discard, log.New(io.Discard, "", 0).Printf)
	if len(withCred.EnvAllow) != 2 {
		t.Fatalf("the configured env list did not reach the launch config (%v), so the gate below "+
			"is being asked about something the process would never have seen", withCred.EnvAllow)
	}
	if err := mcp.ValidateEnvAllowList(withCred.EnvAllow); err == nil {
		t.Error("a Lane B server configured to receive this daemon's own inference credentials " +
			"is not refused. Unconfined AND holding the user's API key is not the posture " +
			"SECURITY_MODEL.md describes.")
	}
	// ANTI-VACUITY for the gate itself: it must not be refusing everything.
	if err := mcp.ValidateEnvAllowList([]string{"HOME", "PATH"}); err != nil {
		t.Errorf("the env gate refuses ordinary variables too (%v), so the refusal above proves nothing", err)
	}
}

// mcp.ForbiddenEnvNames is a hand-written list, and the thing it is a list OF
// lives in another file: the credentials daemon/main.go reads out of the
// environment. Nothing connected the two, so adding a credential meant
// remembering to add it here as well -- and forgetting would not fail a test,
// it would quietly let a third-party MCP server be configured to receive the
// new secret. That is a gap that only ever opens in the dangerous direction.
//
// This walks the daemon's own non-test sources for credential-shaped
// os.Getenv calls and requires each one to be refused.
func TestEveryCredentialTheDaemonReadsIsRefusedToMCPServers(t *testing.T) {
	getenv := regexp.MustCompile(`os\.Getenv\("([A-Z0-9_]+)"\)`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range getenv.FindAllStringSubmatch(string(src), -1) {
			v := m[1]
			if !strings.Contains(v, "KEY") && !strings.Contains(v, "TOKEN") && !strings.Contains(v, "SECRET") {
				continue
			}
			found++
			if !mcp.IsForbiddenEnvName(v) {
				t.Errorf("%s reads the credential %q, and an MCP server can be configured to receive it: "+
					"add it to mcp.ForbiddenEnvNames", name, v)
			}
		}
	}
	// ANTI-VACUITY: a scan that matched nothing would pass this test forever,
	// including after someone renamed the daemon's credential variables.
	if found == 0 {
		t.Fatal("the scan found no credential env reads in the daemon at all, so it is checking nothing")
	}
}

// The other half of the posture -- that a Lane B tool never claims to be
// confined -- is asserted where the guarantee is actually kept, against a real
// subprocess: TestLaneBToolsAreNeverConfined in daemon/mcp. Being unconfined is
// only defensible because the user is told, so the two halves are one decision
// and neither test is complete without the other.
