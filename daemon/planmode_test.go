package main

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"codeterminal/daemon/mcp"
	"codeterminal/protocol"
)

// TestPlanModeOffersNoToolThatExecutesCode is the security assertion this file
// exists for.
//
// WHAT IT CAUGHT. builtinTools took a `mode` argument and used it exactly once,
// to remove propose_edit. sandbox_exec -- the only built-in that runs arbitrary
// code -- was registered unconditionally, so "plan" mode withdrew the REVIEWED
// write path and kept the unreviewed execution path. A user selecting a mode
// called plan has said they want thinking rather than doing; what the mode
// actually did was remove the safe half.
//
// ASSERTED OVER THE WHOLE MENU, not against the name "sandbox_exec". The point
// of mcp.Tool.ExecutesCode is that confinement is a property rather than a
// spelling, and a test naming today's one tool would pass unchanged on the day
// a second is added -- which is the same shape as the bug it is guarding.
func TestPlanModeOffersNoToolThatExecutesCode(t *testing.T) {
	s := builtinTestServer(t)

	var offending []string
	for _, b := range s.builtinTools(&proposalSink{}, "plan") {
		if b.Tool.ExecutesCode {
			offending = append(offending, b.Tool.Name)
		}
	}
	if len(offending) > 0 {
		t.Errorf("plan mode offers %v, which execute arbitrary code. Plan mode already "+
			"withholds propose_edit -- the REVIEWED write path -- so leaving these registered "+
			"means the only way the model can still change this workspace is the one that "+
			"skips edit review entirely", offending)
	}

	// ANTI-VACUITY. An implementation that returned no tools at all in plan mode
	// would satisfy the loop above and break the feature. Plan mode must still
	// be able to read the workspace it is planning against.
	got := map[string]bool{}
	for _, b := range s.builtinTools(&proposalSink{}, "plan") {
		got[b.Tool.Name] = true
	}
	for _, want := range []string{"read_file", "search_code", "list_directory"} {
		if !got[want] {
			t.Errorf("plan mode does not offer %s, so it cannot explore the workspace it is "+
				"asked to plan against. The filter is too wide", want)
		}
	}

	// And the tool it deliberately withholds is still withheld: this change must
	// not quietly restore propose_edit while fixing the other half.
	if got["propose_edit"] {
		t.Error("plan mode offers propose_edit; the directive tells the model not to propose " +
			"edits, and the menu should agree with the instruction")
	}
}

// TestNonPlanModeIsUnchanged pins the blast radius. Every other mode must see
// exactly the menu it saw before plan mode learned to filter.
func TestNonPlanModeIsUnchanged(t *testing.T) {
	s := builtinTestServer(t)

	executes := false
	for _, b := range s.builtinTools(&proposalSink{}, "") {
		if b.Tool.ExecutesCode {
			executes = true
		}
	}
	if !executes {
		t.Error("the default mode no longer offers any code-executing tool. The plan-mode " +
			"filter has leaked into every turn, which removes sandbox_exec from agent mode " +
			"entirely -- a capability regression wearing a security fix's clothes")
	}
}

// TestPlanDirectiveNamesOnlyToolsThatExist is the guard that would have caught
// the shipped defect on the day it was written.
//
// The directive told the model it "may use view_file and grep_search". Neither
// has ever existed here. A prompt string is compiled against nothing, so two
// wrong tool names sat in every plan-mode request, costing tokens and pointing
// the model at a menu it did not have, with no test able to notice.
func TestPlanDirectiveNamesOnlyToolsThatExist(t *testing.T) {
	s := builtinTestServer(t)

	available := map[string]bool{}
	for _, b := range s.builtinTools(&proposalSink{}, "plan") {
		available[b.Tool.Name] = true
	}

	if len(planModeToolNames) == 0 {
		t.Fatal("planModeToolNames is empty, so this check proves nothing")
	}
	for _, name := range planModeToolNames {
		if !available[name] {
			t.Errorf("the plan directive names %q, which plan mode does not offer. Either the "+
				"tool was renamed and the prompt was not, or the filter removed something the "+
				"prompt still promises", name)
		}
		// The name must also actually appear in the sentence the model reads --
		// otherwise this list could drift into being a decorative variable that
		// agrees with the menu while the prompt says something else.
		if !strings.Contains(planModeDirective, name) {
			t.Errorf("%q is in planModeToolNames but not in the directive text, so this test "+
				"is checking a list nothing sends to the model", name)
		}
	}
}

// TestPlanDirectiveGoesToTheSystemRoleNotTheUsersText is the injection half.
//
// The directive used to be appended to the user's own message behind a literal
// "[SYSTEM]: " prefix. A real system role exists -- buildChatMessages takes one
// -- so the marker was forging what the architecture already offered honestly.
//
// The reason it matters beyond tidiness: retrieved chunks, tool output and web
// results all reach the model as USER-role text, and any of them can contain
// the string "[SYSTEM]:". Teaching the model that such a marker carries
// authority inside a user turn builds the exact primitive an indirect prompt
// injection needs, in a codebase that already runs a delimiter defence on
// retrieved chunks for precisely that threat.
func TestPlanDirectiveGoesToTheSystemRoleNotTheUsersText(t *testing.T) {
	const base = "BASE SYSTEM PROMPT"
	const userPrompt = "how does retrieval work"

	messages := buildChatMessages(planModeSystemPrompt(base, "plan"), nil, userPrompt)

	var system, user string
	for _, m := range messages {
		switch m.Role {
		case "system":
			system = m.Content
		case "user":
			user = m.Content
		}
	}

	if !strings.Contains(system, planModeDirective) {
		t.Errorf("the plan directive is not in the system message: %q", system)
	}
	if !strings.Contains(system, base) {
		t.Error("the base system prompt was replaced rather than appended to")
	}
	if user != userPrompt {
		t.Errorf("the user message was modified. It must carry what the user typed and nothing "+
			"else; got %q", user)
	}
	for _, m := range messages {
		if m.Role != "system" && strings.Contains(m.Content, "[SYSTEM]") {
			t.Errorf("a %s-role message contains a forged [SYSTEM] marker: %q", m.Role, m.Content)
		}
	}

	// Every other mode is untouched, including the empty one.
	for _, mode := range []string{"", "chat", "agent"} {
		if got := planModeSystemPrompt(base, mode); got != base {
			t.Errorf("mode %q changed the system prompt to %q; only plan mode may", mode, got)
		}
	}
}

// planModeRegistryTools builds the registry the way a real turn does and
// returns what it advertises.
//
// THROUGH buildRegistry, NOT builtinTools, and that distinction is the whole
// point of the tests below.
//
// Every plan-mode test in this file used to call s.builtinTools(..., "plan")
// directly. That function is one of TWO things buildRegistry does, and the
// other one -- connecting third-party servers -- never saw `mode` at all. So a
// suite that looked thorough asserted over the half that was fixed and could
// not observe the half that was broken. Worse, all nineteen buildRegistry call
// sites in this package passed "", so the single line that carries the client's
// mode into the filter (agentturn.go) could have been reverted to a constant
// and every test here would still have passed.
func planModeRegistryTools(t *testing.T, s *Server, mode string) ([]mcp.Tool, []error) {
	t.Helper()
	registry, errs := s.buildRegistry(context.Background(), log.New(io.Discard, "", 0), &proposalSink{}, mode)
	t.Cleanup(func() { _ = registry.Close() })
	tools, _ := registry.Advertised(context.Background())
	return tools, errs
}

// TestPlanModeRegistryWithholdsEveryActingTool is the assertion at the boundary
// a real turn crosses.
func TestPlanModeRegistryWithholdsEveryActingTool(t *testing.T) {
	s := builtinTestServer(t)
	// MCP.Enabled matters: configPolicy denies every tool when it is false, so
	// Advertised returns an empty list and the assertions below would all hold
	// for a registry that offers nothing. The anti-vacuity check catches that,
	// but the config is the honest fix.
	s.cfg = &Config{MCP: MCPConfig{Enabled: true}}

	tools, errs := planModeRegistryTools(t, s, "plan")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.Name] = true
		if tool.ExecutesCode {
			t.Errorf("plan mode advertises %q, which executes code", tool.Name)
		}
		if tool.ReachesNetwork {
			t.Errorf("plan mode advertises %q, which reaches the network. The filter tested "+
				"ExecutesCode alone while calling itself capability-keyed, and mcp.Tool "+
				"declares two such flags", tool.Name)
		}
		if tool.LaunchesSubprocess {
			t.Errorf("plan mode advertises %q, which starts a program. Lane B servers are "+
				"withheld from plan mode precisely because connecting spawns a subprocess; a "+
				"first-party built-in doing the same thing was not", tool.Name)
		}
	}

	// ANTI-VACUITY, and it has to be here: a buildRegistry that returned an
	// empty registry in plan mode would satisfy every assertion above.
	if len(tools) == 0 {
		t.Fatal("plan mode advertises no tools at all, so the checks above prove nothing")
	}
	for _, want := range []string{"read_file", "search_code", "list_directory", "repo_map"} {
		if !got[want] {
			t.Errorf("plan mode does not offer %q, so it cannot explore the workspace it is "+
				"asked to plan against", want)
		}
	}
	// repo_map specifically: it is ReadOnlyHint and was withheld only because it
	// had been typed inside the `mode != "plan"` block next to the propose_*
	// tools. Nothing about it warrants removal, and plan mode is the mode that
	// most needs it.
	if !got["repo_map"] {
		t.Error("repo_map is read-only and was filtered as collateral")
	}
	// query_compiler_* named explicitly as well as covered by the flag loop
	// above: they are the pair that shipped through three controls with no
	// capability declared, and a regression that silently drops the flag would
	// otherwise only be caught by a loop that reads it.
	for _, unwanted := range []string{
		"sandbox_exec", "web_search", "web_fetch", "propose_edit", "propose_ast_edit",
		"query_compiler_definition", "query_compiler_references",
	} {
		if got[unwanted] {
			t.Errorf("plan mode advertises %q", unwanted)
		}
	}
}

// TestPlanModeNeverConnectsLaneBServers is the hole this batch was opened for.
//
// The built-in filter was recorded as closing the plan-mode issue. It could not
// have: third-party servers are connected and registered further down
// buildRegistry, in a loop that never saw `mode`. Plan mode therefore withdrew
// the REVIEWED, confined, first-party write path and kept the UNREVIEWED,
// unconfined, third-party one -- the same inversion the built-in fix was
// written to correct, one lane over.
//
// HOW THIS DETECTS IT. The configured command does not exist, so a connection
// ATTEMPT is observable as an error: non-plan mode reports exactly one. Plan
// mode must report zero -- not "an error that was tolerated", but no attempt at
// all. Asserting on the error count rather than the tool list is what makes
// this test able to tell "never launched" apart from "launched, then not
// advertised", and the difference matters: mcp.Connect starts the server's
// command as a subprocess with the user's privileges, which is an effect a
// turn promising not to act must not cause.
func TestPlanModeNeverConnectsLaneBServers(t *testing.T) {
	cfg := func() *Config {
		return &Config{MCP: MCPConfig{
			Enabled: true,
			Servers: map[string]MCPServerConfig{
				"broken": {Command: "/definitely/not/a/binary", AcknowledgedUnconfined: true},
			},
		}}
	}

	// Control: outside plan mode the server IS attempted, so the probe works.
	s := builtinTestServer(t)
	s.cfg = cfg()
	if _, errs := planModeRegistryTools(t, s, ""); len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly one; without a connection attempt here the plan-mode "+
			"assertion below would pass for the wrong reason", errs)
	}

	s = builtinTestServer(t)
	s.cfg = cfg()
	tools, errs := planModeRegistryTools(t, s, "plan")
	if len(errs) != 0 {
		t.Errorf("plan mode attempted to connect a third-party server (errs = %v). Connecting "+
			"launches the server's command as a subprocess with the user's privileges, which is "+
			"an effect plan mode must not cause", errs)
	}
	for _, tool := range tools {
		if tool.Lane != protocol.LaneFirstParty {
			t.Errorf("plan mode advertises %q from lane %v -- an unconfined third-party tool in "+
				"the mode whose whole promise is that nothing is touched", tool.Name, tool.Lane)
		}
	}
}

// TestBuildRegistryHonoursMode asserts that the mode argument decides the menu
// at the boundary a real turn crosses, for the spellings a real client sends.
//
// WHAT THIS DOES NOT COVER, stated because the batch it belongs to exists to
// stop tests claiming more than they check: it calls buildRegistry directly, so
// it does NOT prove that agentturn.go passes promptReq.Mode rather than a
// constant. That one line is still unasserted here -- driving it needs a live
// socket, model and approver. What stands behind it instead is the dispatch
// check in resolveExecutable, which reads turn.mode independently: both call
// sites would have to regress together for plan mode to stop applying, where
// before this batch either one alone was enough.
func TestBuildRegistryHonoursMode(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		filtered bool
	}{
		{"plan", true},
		{"Plan", true},   // case must not decide whether a security filter runs
		{" plan ", true}, // nor whitespace
		{"", false},
		{"auto", false},
		{"manual", false},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			s := builtinTestServer(t)
			s.cfg = &Config{MCP: MCPConfig{Enabled: true}}
			tools, _ := planModeRegistryTools(t, s, tc.mode)

			if len(tools) == 0 {
				t.Fatal("no tools advertised at all, so this case proves nothing either way")
			}
			acting := false
			for _, tool := range tools {
				if tool.ExecutesCode || tool.ReachesNetwork {
					acting = true
				}
			}
			if tc.filtered && acting {
				t.Errorf("mode %q still offers a code-executing or network-reaching tool", tc.mode)
			}
			if !tc.filtered && !acting {
				t.Errorf("mode %q offers no acting tool -- the plan filter has leaked into "+
					"every mode, which is a capability regression wearing a security fix's "+
					"clothes", tc.mode)
			}
		})
	}
}

// TestNormalizeModeRejectsWhatItCannotInterpret.
//
// The comparison this replaces was `mode != "plan"`, which is fail-OPEN: any
// string that was not exactly that selected the full menu, including the one
// containing sandbox_exec. "Plan" from a client that capitalises and "planning"
// from one that guesses both resolved to full execution capability, for a user
// who had asked for the mode that runs nothing.
//
// "auto" and "manual" are accepted deliberately and this test says why: the
// wire field is shared with the VS Code approval picker, whose default is
// "auto". An enum of {"", "plan"} would look stricter, pass every test in this
// package, and reject the primary client's ordinary turn.
func TestNormalizeModeRejectsWhatItCannotInterpret(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"plan", modePlan, false},
		{"PLAN", modePlan, false},
		{"  Plan  ", modePlan, false},
		{"", "", false},
		{"auto", modeAuto, false},
		{"manual", modeManual, false},
		{"planning", "", true},
		{"chat", "", true},
		{"agent", "", true},
		{"plan mode", "", true},
	} {
		got, err := normalizeMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeMode(%q) = %q, want an error. Falling through to the full "+
					"menu is how a mode the daemon cannot interpret becomes code execution", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeMode(%q) errored: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("normalizeMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPlanModeDeniesBothCapabilities covers the predicate directly, so a future
// third flag on mcp.Tool has an obvious place to be added.
func TestPlanModeDeniesBothCapabilities(t *testing.T) {
	if !planModeDenies(mcp.Tool{ExecutesCode: true}) {
		t.Error("a code-executing tool is allowed in plan mode")
	}
	if !planModeDenies(mcp.Tool{ReachesNetwork: true}) {
		t.Error("a network-reaching tool is allowed in plan mode. mcp.go says of ReachesNetwork " +
			"that it exists for the same reason ExecutesCode does; the filter honoured one")
	}
	if planModeDenies(mcp.Tool{ReadOnlyHint: true}) {
		t.Error("a read-only tool is withheld in plan mode, which makes the mode useless")
	}
}

// planTurnResponse drives a real prompt turn and returns the terminal Done
// message. Reuses runPromptTurn (response_robustness_test.go), which already
// drives serveConn over a net.Pipe -- the WIRE rather than a function call,
// which is the point: the previous plan-mode tests all called builtinTools
// directly and could not see what actually crossed the socket.
//
// The SINGLE-TURN path (no CapToolApproval) is deliberate. It has no tools at
// all, so it is where plan mode was a prompt string and nothing else.
func planTurnResponse(t *testing.T, apiBase, mode, prompt string) protocol.TokenResponse {
	t.Helper()
	srv := &Server{
		apiBase:       apiBase,
		apiKey:        "k",
		cfg:           &Config{},
		modelOverride: "test/model",
		logger:        discardLogger(),
		workspace:     t.TempDir(),
	}
	_, responses := runPromptTurn(t, srv, protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion, Prompt: prompt, Mode: mode,
	})
	if len(responses) == 0 {
		t.Fatal("daemon wrote nothing to the wire")
	}
	return responses[len(responses)-1]
}

// TestPlanModeWithholdsEditsFromProse is the half of plan mode the tool filter
// never covered.
//
// builtinTools withholds propose_edit, and that closes the path an edit takes
// through a TOOL. The model can also emit a SEARCH/REPLACE block in ordinary
// prose: parseAndLogEditBlocks lifts it out of the reply and it reaches the
// client as an EditProposal like any other. VS Code derives auto-apply from the
// mode PICKER rather than the wire mode, so `/plan` with the picker on Auto sent
// `{mode:"plan", autoApply:true}` and that block was written to disk.
//
// The summary shipped for /plan promises "read-only: no edits". This is the test
// that makes the first third of that sentence true rather than instructed.
func TestPlanModeWithholdsEditsFromProse(t *testing.T) {
	reply := strings.Join([]string{
		"Here is the plan, and here is the change:",
		"",
		"path: main.go",
		"<<<<<<< SEARCH",
		"func main() {",
		"=======",
		"func hello() {}",
		"",
		"func main() {",
		">>>>>>> REPLACE",
	}, "\n")

	t.Run("plan mode withholds and says so", func(t *testing.T) {
		api := sseServerWithFinish(t, reply, "stop")
		defer api.Close()

		got := planTurnResponse(t, api.URL, "plan", "plan a refactor")
		if n := len(got.EditProposals); n != 0 {
			t.Errorf("plan mode returned %d edit proposal(s): %+v\nAn auto-apply client writes these "+
				"to disk, and the command that produced them says \"read-only: no edits\"", n, got.EditProposals)
		}
		// Withheld, not vanished. editapply.BlockError exists because "a refused
		// block is a block the user asked for and did not get, and dropping it
		// silently is the failure mode this type exists to make impossible".
		found := false
		for _, r := range got.EditRejections {
			if strings.Contains(r.Reason, "plan mode") {
				found = true
			}
		}
		if !found {
			t.Errorf("no rejection mentions plan mode; the edit was dropped silently. Rejections: %+v",
				got.EditRejections)
		}
	})

	// ANTI-VACUITY, and it is the whole load-bearing half: without it an
	// implementation that never parses edit blocks at all would satisfy the case
	// above and break editing for every ordinary turn.
	t.Run("normal mode still proposes", func(t *testing.T) {
		api := sseServerWithFinish(t, reply, "stop")
		defer api.Close()

		got := planTurnResponse(t, api.URL, "", "make the change")
		if len(got.EditProposals) == 0 {
			t.Error("a normal turn returned no edit proposals; the plan-mode filter has leaked into " +
				"every mode, which removes editing from the product")
		}
	})
}

// TestUnknownModeIsRefusedOnTheWire covers the rejection path end to end.
//
// normalizeMode was unit-tested from the first commit; the branch in serveConn
// that turns its error into a client-visible refusal was not. The whole 30-line
// block could be deleted and every test stayed green -- the counter, the
// Done:true, the ErrorClass and the canonical writeback all unasserted. That is
// the same shape as the defect this batch exists to correct.
func TestUnknownModeIsRefusedOnTheWire(t *testing.T) {
	// apiBase points at a closed port: if the refusal does NOT happen, the turn
	// reaches the model call and fails with a DIFFERENT error, so this cannot
	// pass by accident.
	got := planTurnResponse(t, "http://127.0.0.1:1", "planning", "hello")

	if !got.Done {
		t.Fatalf("response is not terminal: %+v", got)
	}
	if !strings.Contains(got.Error, "unknown mode") {
		t.Errorf("Error = %q, want it to name the unknown mode. A mode the daemon cannot "+
			"interpret must be refused, not read permissively -- the previous behaviour handed "+
			"back the full menu including sandbox_exec", got.Error)
	}
	if got.ErrorClass != string(ClassInvalidRequest) {
		t.Errorf("ErrorClass = %q, want %q", got.ErrorClass, ClassInvalidRequest)
	}

	// And an accepted mode must NOT be refused, or this test would pass against a
	// daemon that rejects everything.
	ok := planTurnResponse(t, "http://127.0.0.1:1", "auto", "hello")
	if strings.Contains(ok.Error, "unknown mode") {
		t.Errorf("mode \"auto\" was refused: %q. It is the DEFAULT the VS Code picker sends, so "+
			"refusing it breaks every ordinary turn in the primary client", ok.Error)
	}
}

// TestPlanModeDeniesAtDispatch drives the SECOND enforcement point.
//
// docs/OPEN_ITEMS.md item 23 cites "a second enforcement point in
// resolveExecutable" as part of its closure evidence. Until this test that claim
// was backed by nothing executable: no test in the package constructed an
// agentTurn with mode "plan", so the branch could be deleted whole and the suite
// stayed green. Citing an untested control as evidence is the exact defect that
// cell criticises 3f12a02 for, which is why this exists.
//
// The branch is also unreachable through buildRegistry now -- plan mode returns
// before Lane B is registered, so no third-party tool can be in a plan-mode
// registry. That is precisely why it is worth keeping and worth testing: it is
// the backstop for a future path that assembles a registry without consulting
// mode, which is the bug this whole batch was opened for.
func TestPlanModeDeniesAtDispatch(t *testing.T) {
	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{
		Enabled: true,
		Servers: map[string]MCPServerConfig{"helpful": {Command: "true", AcknowledgedUnconfined: true}},
	}}
	registry := laneTestRegistry(t, []mcp.Tool{{Name: "write_file", Description: "d"}})

	call := toolCall{Function: toolCallFunction{Name: "helpful__write_file", Arguments: "{}"}}

	// In plan mode the call is refused even though the registry is holding the
	// tool -- which is the situation a registry built without consulting mode
	// would produce.
	dec := s.resolveExecutable(context.Background(), registry, &agentTurn{mode: "plan", grants: map[string]bool{}},
		budget{maxIterations: 4}, call, &recordingApprover{}, nil)
	if dec.run || dec.policy != mcp.PolicyDeny {
		t.Errorf("plan mode dispatched %q (policy=%v run=%v). A registry assembled without "+
			"consulting mode would put an unconfined third-party tool one call away, and this "+
			"check is what stands behind that", call.Function.Name, dec.policy, dec.run)
	}

	// ANTI-VACUITY: the same call outside plan mode must NOT be denied by THIS
	// check, or the test would pass against a daemon that refuses everything and
	// prove nothing about mode.
	dec = s.resolveExecutable(context.Background(), registry, &agentTurn{mode: "", grants: map[string]bool{}},
		budget{maxIterations: 4}, call, &recordingApprover{}, nil)
	if dec.policy == mcp.PolicyDeny && strings.Contains(dec.reason, "plan mode") {
		t.Errorf("a normal turn was denied by the plan-mode check: %q", dec.reason)
	}
}
