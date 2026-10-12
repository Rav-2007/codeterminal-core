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
// both.
func TestMCPListSeparatesNetworkToolsFromUnconfinedSubprocesses(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "read_file", Confined: true},
		{Name: "web_search", ReachesNetwork: true},
		{Name: "web_fetch", ReachesNetwork: true},
		{Name: "third_party", Lane: protocol.LaneThirdParty, Confined: false},
		// This daemon's own, and not confined because it can start a language
		// server. It is not "an external MCP server", and was being counted as
		// one (seen in the real app, 2026-10-11).
		{Name: "query_compiler_definition", Lane: protocol.LaneFirstParty, LaunchesSubprocess: true},
	}

	if got := countOwnUnconfined(tools); got != 1 {
		t.Errorf("countOwnUnconfined = %d, want 1", got)
	}
	if got := countExternal(tools); got != 1 {
		t.Errorf("countExternal = %d, want 1 — a network tool was counted as a subprocess "+
			"and the user would be told web_search runs with their full privileges", got)
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
