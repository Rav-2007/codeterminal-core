package main

import (
	"strings"
	"testing"
)

// AN MCP LOOKUP COUNTS AS HAVING CHECKED -- when its server says the tool
// reaches the outside world (openWorldHint: true), and the call succeeded.
//
// Live on 2026-10-28: the Terraform MCP server returned aws 6.70.0 from the
// registry, the model answered from it, and the loop still printed the nudge
// notice and forced one more call (docs/GROUNDING_MCP_PROPOSAL_2026-10-28.md).
// These drive the same shape with a real stdio MCP server
// (mcp/testdata/echoserver --open-world) standing in for the registry, so they
// need no network and no live model.
const reproQuestion = "Using the Terraform server, look up the latest version of the hashicorp/aws provider in the Terraform registry."

const reproAnswer = "The latest version of the hashicorp/aws provider in the Terraform registry is **6.70.0**."

// runMCPLookupTurn runs the live question through one call to registry__<tool>
// and then reqAnswer, and reports how many model calls the turn made and its
// final text. A third scripted reply is there for the nudge to consume.
func runMCPLookupTurn(t *testing.T, tool string) (calls int64, final string) {
	t.Helper()
	if !looksLikeLiveWorldQuestion(reproQuestion) {
		t.Fatal("fixture is wrong: the live-question classifier must fire on this request, as it did live")
	}
	base, n, _ := agentUpstream(t,
		toolCallSSE("c1", "registry__"+tool, `{"text":"hashicorp/aws latest version: 6.70.0"}`),
		textSSE(reproAnswer),
		textSSE("I didn't answer from memory — the registry tool returned 6.70.0."),
	)
	cfg := webCfg() // web_search and web_fetch on the menu, as in production
	cfg.Servers = map[string]MCPServerConfig{"registry": {
		Command:                buildEchoServer(t),
		Args:                   []string{"--open-world"},
		Tools:                  map[string]string{tool: PolicyAllow},
		AcknowledgedUnconfined: true,
	}}
	s := loopServer(t, base, cfg)

	res, _, err := runLoopWithPrompt(t, s, reproQuestion)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(res.ToolNames) != 1 || res.ToolNames[0] != "registry__"+tool {
		t.Fatalf("tool calls %v, want exactly registry__%s -- the MCP call did not run", res.ToolNames, tool)
	}
	return n.Load(), res.FinalText
}

func TestAnMCPLookupCountsAsHavingChecked(t *testing.T) {
	calls, final := runMCPLookupTurn(t, "lookup")
	if calls != 2 {
		t.Errorf("made %d model call(s), want 2 -- an answer taken from an MCP lookup was sent back to search", calls)
	}
	if strings.Contains(final, strings.TrimSpace(nudgeNotice)) {
		t.Errorf("the user was told the answer came from memory when it came from an MCP tool: %q", final)
	}
}

// THE BOUNDARIES. Each of these must still be sent back once: the call ran,
// but it is not evidence that the live fact was looked up.
func TestMCPCallsThatAreNotLookupsStillGetTheNudge(t *testing.T) {
	for _, tc := range []struct{ tool, why string }{
		// A Memory-like tool: it reads back what was stored on this machine,
		// which may be exactly the stale fact the check exists to catch.
		{"recall", "openWorldHint false"},
		// Says nothing. The spec's default would be true; nothing said is not
		// a claim (mcp.Tool.OpenWorldHint).
		{"echo", "no openWorldHint"},
		// Reaches the world but failed: nothing came back to answer from.
		{"lookup_fails", "openWorldHint true, but the call failed"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			calls, final := runMCPLookupTurn(t, tc.tool)
			if calls != 3 {
				t.Errorf("%s: made %d model call(s), want 3 -- the nudge did not fire", tc.why, calls)
			}
			if !strings.Contains(final, strings.TrimSpace(nudgeNotice)) {
				t.Errorf("%s: no notice in the final text: %q", tc.why, final)
			}
		})
	}
}
