package main

import (
	"os"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// A TOOL THE CAP LEFT OUT IS FOUND, JOINS THE MENU, AND RUNS.
//
// The whole fix in one turn: a menu of one, find_tools on it, the model asks
// for "read file", read_file is on the NEXT request's menu, and calling it
// works -- through the ordinary policy path, which here allows it.
func TestADroppedToolIsFoundAndThenCallable(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", findToolsQualified, `{"query":"read a file"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Budget:  MCPBudgetConfig{MaxAdvertisedTools: 1},
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})
	if err := os.WriteFile(s.workspace+"/inside.txt", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, activity, err := runLoopWith(t, s, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(*bodies) != 3 {
		t.Fatalf("made %d requests, want 3", len(*bodies))
	}
	first, second := string((*bodies)[0]), string((*bodies)[1])
	if !strings.Contains(first, findToolsQualified) {
		t.Fatal("a trimmed menu did not offer find_tools")
	}
	if strings.Contains(first, `"builtin__read_file"`) {
		t.Fatal("read_file was on the first menu, so this test proves nothing; lower the cap")
	}
	if !strings.Contains(second, `"name":"builtin__read_file"`) {
		t.Error("read_file was found but is not on the next request's menu")
	}
	ran := false
	for _, a := range activity {
		if a.Tool == "read_file" && a.Phase == protocol.ToolPhaseSucceeded {
			ran = true
		}
	}
	if !ran {
		t.Errorf("the found tool did not run; activity %+v, answer %q", activity, res.FinalText)
	}
}

// The cap working as designed is not a fault, and is not reported as one:
// the notice that named a config key and listed internal tool names is gone
// from the user's screen. The daemon log keeps it.
func TestATrimmedToolMenuIsNotReportedToTheUser(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("done"))
	s := loopServer(t, base, MCPConfig{Enabled: true, Budget: MCPBudgetConfig{MaxAdvertisedTools: 2}})
	if _, _, err := runLoopWith(t, s, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	for _, d := range lastDegradations {
		if d.Component == protocol.DegradedToolMenuTruncated {
			t.Errorf("a trimmed menu reached the user as a degradation: %+v", d)
		}
	}
}

// Nothing dropped, nothing to find: find_tools is not offered.
func TestAFullMenuOffersNoLookup(t *testing.T) {
	base, _, bodies := agentUpstream(t, textSSE("done"))
	s := loopServer(t, base, MCPConfig{Enabled: true})
	if _, _, err := runLoopWith(t, s, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if strings.Contains(string((*bodies)[0]), findToolsQualified) {
		t.Error("find_tools was offered with nothing left to find")
	}
}

// What a step's role or a turn's mode withholds is not a candidate. Dispatch
// would refuse it anyway; offering it would only waste the model's step.
func TestALookupOnlySearchesWhatThisStepMayUse(t *testing.T) {
	dropped := []mcp.Tool{
		{Server: "supabase", Name: "list_projects", Description: "Lists projects.", Lane: protocol.LaneThirdParty},
		{Server: "builtin", Name: "propose_edit", Description: "Proposes an edit.", Lane: protocol.LaneFirstParty},
	}
	firstPartyOnly := func(t mcp.Tool) bool { return t.Lane == protocol.LaneFirstParty }
	ts := newToolSearch(dropped, firstPartyOnly)
	if ts == nil {
		t.Fatal("a step with one usable dropped tool got no lookup")
	}
	if _, found, _ := ts.answer(`{"query":"list projects"}`); len(found) != 0 {
		t.Errorf("found a tool this step may not use: %v", found)
	}
	if newToolSearch(dropped, func(mcp.Tool) bool { return false }) != nil {
		t.Error("a lookup was offered with no usable candidate")
	}
}

// One lookup adds at most maxFoundPerCall tools and a turn at most
// maxFoundPerTurn, so lookups cannot rebuild the wide menu the cap avoids. A
// repeated query is reported so the loop can count it as a repeat.
func TestLookupsAreBounded(t *testing.T) {
	var dropped []mcp.Tool
	for i := 0; i < 40; i++ {
		dropped = append(dropped, mcp.Tool{Server: "big", Name: "project_tool_" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Description: "A project tool.", Lane: protocol.LaneThirdParty})
	}
	ts := newToolSearch(dropped, func(mcp.Tool) bool { return true })
	total := 0
	for i := 0; i < 6; i++ {
		_, found, _ := ts.answer(`{"query":"project tool ` + string(rune('a'+i)) + `"}`)
		if len(found) > maxFoundPerCall {
			t.Fatalf("one lookup added %d tools", len(found))
		}
		total += len(found)
	}
	if total != maxFoundPerTurn {
		t.Errorf("a turn added %d tools, want the per-turn ceiling %d", total, maxFoundPerTurn)
	}
	if _, _, repeat := ts.answer(`{"query":"PROJECT TOOL a"}`); !repeat {
		t.Error("the same query asked twice was not reported as a repeat")
	}
	if content, found, _ := ts.answer(`{}`); len(found) != 0 || !strings.Contains(content, "query") {
		t.Errorf("a lookup with no query = %q, %v", content, found)
	}
}

func TestMenuQueryCarriesAShortFollowUpsContext(t *testing.T) {
	msgs := []chatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "which supabase projects do I have"},
		{Role: "assistant", Content: "Shall I list them?"},
		{Role: "user", Content: "yes"},
	}
	if q := menuQuery(msgs); !strings.Contains(q, "supabase") || !strings.Contains(q, "yes") {
		t.Errorf("menuQuery = %q", q)
	}
}
