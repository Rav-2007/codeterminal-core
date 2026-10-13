package main

import (
	"context"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// DOES THE CAPABILITY ACTUALLY REACH THE MODEL, IN THE CONFIG THE USER RUNS?
//
// This repository has been bitten twice by the same shape of failure: work that
// was correct, tested, and completely unreachable in the shipped path -- a lint
// gate that never ran because a binary was off PATH, and a terminal client that
// could not reach its daemon at all for weeks while every unit test passed. A
// web tool that exists in the registry but never appears on the model's menu
// would fail in exactly that way, and it would look identical to the bug it was
// written to fix: the model would keep hedging, and nothing would say why.
//
// So this test asserts the END of the chain, from the config file the launcher
// actually uses.
func TestTheWebToolsReachTheModelInTheShippedAgentConfig(t *testing.T) {
	cfg, err := LoadConfig("../models.agent.json")
	if err != nil {
		t.Fatalf("the shipped agent config must load: %v", err)
	}
	if !cfg.MCP.Enabled {
		t.Fatal("models.agent.json does not enable agent mode; no tool of any kind would be offered")
	}

	s := builtinTestServer(t)
	s.cfg = cfg
	registry := mcp.NewRegistry(configPolicy{cfg: cfg}, cfg.MCP.Budget.resolvedMaxAdvertisedTools())
	for _, b := range s.builtinTools(&proposalSink{}, "") {
		if err := registry.RegisterBuiltin(b); err != nil {
			t.Fatalf("registering %s: %v", b.Tool.Name, err)
		}
	}

	advertised, errs := registry.Advertised(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	names := map[string]mcp.Tool{}
	for _, tool := range advertised {
		names[tool.Name] = tool
	}
	for _, want := range []string{"web_search", "web_fetch"} {
		tool, ok := names[want]
		if !ok {
			t.Errorf("%q is not on the model's menu in the shipped config; the model cannot look anything up", want)
			continue
		}
		if !tool.ReachesNetwork {
			t.Errorf("%q lost its ReachesNetwork flag on the way to the menu", want)
		}
		if tool.Confined {
			t.Errorf("%q reached the menu claiming to be confined", want)
		}
	}

	// THE CAP IS THE THING MOST LIKELY TO BREAK THIS SILENTLY. Two new tools
	// took the built-in count from eight to ten against a default ceiling of
	// twelve; a third pair, or a user lowering max_advertised_tools, drops one
	// off the end -- and Registry.Dropped is the only evidence it happened.
	if dropped := registry.Dropped(); len(dropped) > 0 {
		t.Errorf("the advertised cap dropped %s; a tool nobody is offered cannot be used",
			strings.Join(dropped, ", "))
	}

	// Headroom, stated as a number so the next person adding a tool sees the
	// budget rather than discovering it.
	if got, ceiling := len(advertised), cfg.MCP.Budget.resolvedMaxAdvertisedTools(); got > ceiling {
		t.Errorf("%d tools advertised against a ceiling of %d", got, ceiling)
	} else {
		t.Logf("advertised %d built-in tool(s) against a ceiling of %d (%d slot(s) spare)",
			got, ceiling, ceiling-got)
	}
}

// THE SHIPPED CONFIG REACHES THE NETWORK FOR NOBODY, and since 2026-10-12 that
// is asserted directly instead of being inherited from agent mode being off.
//
// WHAT THIS TEST USED TO SAY: "models.json has no mcp section, so agent mode is
// off and no web tool exists there either." Both halves were true, and the
// second was a consequence of the first rather than a property of its own --
// with agent mode off, PolicyFor returns PolicyDeny for everything before it
// reads any tool name at all.
//
// So the moment the shipped config turned agent mode ON to give the model the
// read-only tools it needs to answer about a user's code (see
// shippedconfig_test.go), this test failed -- correctly, and for the wrong
// reason. Nothing about network reach had changed; the proxy standing in for it
// had. A test that fails when the thing it names is still true is a test that
// will be "fixed" by deleting the assertion, so it now names the thing.
//
// TWO LOCKS, because they fail differently. mcp.web.disabled stops the tools
// being BUILT, so the model is never shown them -- the posture
// TestDisablingWebRemovesTheToolsFromTheMenuEntirely explains, where a tool
// advertised and then refused costs an iteration and a hedge. The explicit
// "deny" policy is the second lock: it is what PolicyFor answers with, and it
// holds even if a future edit turns the menu back on. The capability stays
// opt-in through models.agent.json, chosen by someone who read what they were
// turning on -- never something that appears under an existing user because
// their extension updated.
func TestThePlainConfigGainsNoNetworkCapability(t *testing.T) {
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("the shipped models.json must load: %v", err)
	}

	policy := configPolicy{cfg: cfg}
	for _, tool := range []string{"web_search", "web_fetch"} {
		if got := policy.PolicyFor(mcp.BuiltinServerName, tool); got != mcp.PolicyDeny {
			t.Errorf("the SHIPPED models.json resolves %q to %v, want %v. Agent mode is on in "+
				"this config for the read-only tools; network reach must not have come with it.",
				tool, got, mcp.PolicyDeny)
		}
	}

	if !cfg.MCP.Web.Disabled {
		t.Error("mcp.web.disabled is false in the shipped config, so web_search and web_fetch are " +
			"built and advertised to the model even though calling them is denied -- the exact " +
			"advertise-then-refuse waste that mcp.web.disabled exists to avoid")
	}
}

// mcp.web.disabled must actually remove the tools, not merely deny them: a
// model shown a tool it is then refused burns an iteration finding out.
func TestDisablingWebRemovesTheToolsFromTheMenuEntirely(t *testing.T) {
	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{Enabled: true, Web: MCPWebConfig{Disabled: true}}}
	for _, b := range s.builtinTools(&proposalSink{}, "") {
		if strings.HasPrefix(b.Tool.Name, "web_") {
			t.Fatalf("%q was still built while mcp.web.disabled is set", b.Tool.Name)
		}
	}
}
