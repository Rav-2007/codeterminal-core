package main

import (
	"strings"
	"testing"
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
