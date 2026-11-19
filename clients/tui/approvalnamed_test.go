package tui

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// A THIRD-PARTY TOOL IS NAMED WITH ITS SERVER, WHATEVER ITS ARGUMENTS ARE CALLED.
//
// FOUND 2026-11-19, live: Cloudflare's docs server has a search tool taking
// {"query": ...}, and its approval read "Search the web for: bind a KV namespace
// to a Worker ?" -- Mochiii's own web-search question, with no server or tool
// named. The short forms were chosen by argument name alone, before the branch
// that names a server's tool, so any server could borrow the wording of a
// built-in: "Open web page" for a "url", and "Run command" -- the sandboxed
// runner's question -- for a "command".
//
// Neuter check: drop the `own &&` from the three short-form cases in
// approvalQuestion (chat.go).
func TestAThirdPartyToolIsNamedWithItsServerWhateverItsArgumentsAreCalled(t *testing.T) {
	for _, tc := range []struct {
		server, tool, args string
		network            bool
		borrowed           string
	}{
		{"cloudflare", "search_cloudflare_documentation", `{"query": "bind a KV namespace to a Worker"}`, true, "Search the web for"},
		{"fetcher", "fetch", `{"url": "https://example.com/page"}`, true, "Open web page"},
		{"shell", "run", `{"command": "rm -rf build"}`, false, "Run command"},
	} {
		q := approvalQuestion(protocol.ToolApprovalRequest{
			Server: tc.server, Tool: tc.tool, Arguments: tc.args,
			Lane: protocol.LaneThirdParty, ReachesNetwork: tc.network,
		})
		if strings.Contains(q, tc.borrowed) {
			t.Errorf("%s/%s is asked as %q: a built-in's wording, not the server's tool", tc.server, tc.tool, q)
		}
		if want := "Allow " + tc.server + "/" + tc.tool + ": "; !strings.HasPrefix(q, want) {
			t.Errorf("question = %q, want it to start %q", q, want)
		}
	}

	// The built-ins keep their short questions.
	for _, tc := range []struct{ tool, args, want string }{
		{"web_search", `{"query": "go 1.26 release notes"}`, "Search the web for: go 1.26 release notes ?"},
		{"web_fetch", `{"url": "https://go.dev/doc"}`, "Open web page: https://go.dev/doc ?"},
		{"sandbox_exec", `{"command": "go test ./..."}`, "Run command: go test ./... ?"},
	} {
		q := approvalQuestion(protocol.ToolApprovalRequest{
			Server: "builtin", Tool: tc.tool, Arguments: tc.args,
			Lane: protocol.LaneFirstParty, ReachesNetwork: tc.tool != "sandbox_exec",
		})
		if q != tc.want {
			t.Errorf("builtin %s: question = %q, want %q", tc.tool, q, tc.want)
		}
	}
}
