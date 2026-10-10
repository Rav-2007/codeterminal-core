package main

import (
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// `mcp list` is the other place a confinement claim is printed, and it had the
// same assumption baked in: "not confined" meant "an external subprocess",
// because until now those were the same population. A web tool is not confined
// and is not a subprocess, so one count and one sentence can no longer cover
// both -- and neither can a FIRST-PARTY built-in that happens to be unconfined.
func TestMCPListSeparatesNetworkToolsFromUnconfinedSubprocesses(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "read_file", Lane: protocol.LaneFirstParty, Confined: true},
		{Name: "web_search", Lane: protocol.LaneFirstParty, ReachesNetwork: true},
		{Name: "web_fetch", Lane: protocol.LaneFirstParty, ReachesNetwork: true},
		// First-party AND unconfined -- exactly propose_ast_edit and the
		// compiler queries. It is not somebody else's subprocess, so the
		// "run in external MCP servers" sentence must not count it. The old
		// `!Confined && !ReachesNetwork` test counted it, inflating that number
		// by this daemon's own tools (the regression this case pins).
		{Name: "query_compiler_definition", Lane: protocol.LaneFirstParty, Confined: false},
		{Name: "third_party", Lane: protocol.LaneThirdParty, Confined: false},
	}

	if got := countExternal(tools); got != 1 {
		t.Errorf("countExternal = %d, want 1 — only the third-party server tool runs in an "+
			"external MCP server; a network tool or a first-party unconfined built-in must not "+
			"be counted as a subprocess with the user's full privileges", got)
	}
	if got := countNetworked(tools); got != 2 {
		t.Errorf("countNetworked = %d, want 2", got)
	}
}

func TestTheConfinedColumnHasAnHonestValueForNetworkTools(t *testing.T) {
	cases := []struct {
		tool mcp.Tool
		want string
	}{
		{mcp.Tool{Confined: true}, "yes"},
		{mcp.Tool{Confined: false}, "NO"},
		// A bare "NO" would file web_search alongside an unconfined subprocess,
		// and the reader's next thought after NO is "what can this do to my
		// machine". For this one the answer is "nothing".
		{mcp.Tool{ReachesNetwork: true}, "network"},
		// ReachesNetwork wins even if something set Confined, matching the
		// force in RegisterBuiltin.
		{mcp.Tool{ReachesNetwork: true, Confined: true}, "network"},
	}
	for _, tc := range cases {
		if got := confinedLabel(tc.tool); got != tc.want {
			t.Errorf("confinedLabel(%+v) = %q, want %q", tc.tool, got, tc.want)
		}
	}
}
