package main

// Phase narration: the line that tells a user WHICH specialist is working.
//
// FOUND BY RUNNING IT, not by reading it. The daemon emitted a phase marker
// before every phase of an orchestrated turn, and both shipping clients
// discarded it -- narratePhase used ToolPhaseRequested, which toolActivityLine
// and oneShotActivityLine deliberately render as the empty string. The feature
// existed on the wire and did nothing on the screen.
//
// The tests here are about the CLIENT half of that: no daemon-side test could
// have caught it, because the daemon was doing exactly what it meant to.

import (
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
)

// phaseMarker is what daemon/orchestrator.go's narratePhase sends: no CallID,
// because there is no call.
func phaseMarker(display, step string) protocol.ToolActivity {
	return protocol.ToolActivity{Phase: protocol.ToolPhaseStep, Tool: display, Detail: step}
}

func TestPhaseMarkerRendersInTheChatUI(t *testing.T) {
	got := toolActivityLine(phaseMarker("Researcher", "step 2/4"))
	if got == "" {
		t.Fatal("a phase marker rendered as the empty string, so the user is never told " +
			"which specialist is working -- this is the bug that shipped")
	}
	if !strings.Contains(got, "Researcher") || !strings.Contains(got, "step 2/4") {
		t.Errorf("phase line = %q, want both the specialist and its position in the pipeline", got)
	}
	// It must not read as a tool the agent ran.
	if strings.Contains(got, "__") {
		t.Errorf("phase line = %q, but a phase is not a qualified tool name", got)
	}
}

func TestPhaseMarkerRendersInTheOneShotClient(t *testing.T) {
	got := oneShotActivityLine(phaseMarker("Coder", "step 3/4"))
	if got == "" {
		t.Fatal("a phase marker rendered as the empty string in the one-shot client")
	}
	if !strings.Contains(got, "Coder") || !strings.Contains(got, "step 3/4") {
		t.Errorf("phase line = %q", got)
	}
}

// EVERY phase must get its own line. All four markers share an empty CallID,
// and the transcript used to key on CallID unconditionally -- so a four-phase
// turn rewrote one line four times and the user saw only the last specialist.
func TestEveryPhaseGetsItsOwnTranscriptLine(t *testing.T) {
	m := &chatModel{}
	for _, p := range []struct{ display, step string }{
		{"Planner", "step 1/4"},
		{"Researcher", "step 2/4"},
		{"Coder", "step 3/4"},
		{"Tester", "step 4/4"},
	} {
		m.noteToolActivity(phaseMarker(p.display, p.step))
	}

	if len(m.turns) != 4 {
		var lines []string
		for _, tn := range m.turns {
			lines = append(lines, tn.text)
		}
		t.Fatalf("a four-phase turn produced %d transcript line(s), want 4: %q", len(m.turns), lines)
	}
	for i, want := range []string{"Planner", "Researcher", "Coder", "Tester"} {
		if !strings.Contains(m.turns[i].text, want) {
			t.Errorf("transcript line %d = %q, want the %s phase", i, m.turns[i].text, want)
		}
	}
}

// The same empty-CallID collapse hit REAL tool calls: measured live, a provider
// emitted tool calls with no id, and the second refusal overwrote the first.
func TestTwoCallsWithNoIDDoNotOverwriteEachOther(t *testing.T) {
	m := &chatModel{}
	m.noteToolActivity(protocol.ToolActivity{Phase: protocol.ToolPhaseDenied, Tool: "search_code", Detail: "no such tool"})
	m.noteToolActivity(protocol.ToolActivity{Phase: protocol.ToolPhaseDenied, Tool: "list_files", Detail: "no such tool"})

	if len(m.turns) != 2 {
		t.Fatalf("two refusals with no call id produced %d line(s), want 2 -- the user was told about "+
			"fewer refusals than actually happened", len(m.turns))
	}
	if !strings.Contains(m.turns[0].text, "search_code") || !strings.Contains(m.turns[1].text, "list_files") {
		t.Errorf("lines = %q, %q", m.turns[0].text, m.turns[1].text)
	}
}

// A tool named without its server prefix must still read as a name, not as
// "__whatever". The daemon now reports the raw name it was given.
func TestUnqualifiedToolNameRendersWithoutAStrayPrefix(t *testing.T) {
	a := protocol.ToolActivity{CallID: "c1", Tool: "search_code", Phase: protocol.ToolPhaseDenied}
	got := toolActivityLine(a)
	if strings.Contains(got, "__") {
		t.Errorf("line = %q, want no dangling qualifier when the server is unknown", got)
	}
	if !strings.Contains(got, "search_code") {
		t.Errorf("line = %q, want the tool the model actually named", got)
	}
	// A REAL qualified name must be unchanged.
	b := protocol.ToolActivity{CallID: "c2", Server: "builtin", Tool: "read_file", Phase: protocol.ToolPhaseDenied}
	if got := toolActivityLine(b); !strings.Contains(got, "builtin__read_file") {
		t.Errorf("a qualified name rendered as %q, want it unchanged", got)
	}
}

// The shape router's decision reaches the screen through the same step marker
// the phases use. A user who watches one question take two steps and the next
// take one has to be able to see that as a decision rather than as the daemon
// being inconsistent -- so the REASON has to survive to the terminal, not just
// to the daemon's log.
func TestTheRoutingDecisionReachesTheScreen(t *testing.T) {
	line := toolActivityLine(protocol.ToolActivity{
		Phase:  protocol.ToolPhaseStep,
		Tool:   "Routing",
		Detail: "2 specialists: evidence spans 4 files across 5 chunks",
	})
	if line == "" {
		t.Fatal("the routing decision rendered as nothing at all")
	}
	for _, want := range []string{"Routing", "4 files"} {
		if !strings.Contains(line, want) {
			t.Errorf("the rendered line %q dropped %q", line, want)
		}
	}
}

// "/team " asks for ONE turn of specialists without editing a config file.
//
// The parse contract is deliberately identical to parsePromptKind's, including
// the load-bearing trailing space: startTurn trims before parsing, so a bare
// "/team" can never carry one and falls through as ordinary text.
func TestTheTeamCommandSelectsTheMeasuredShape(t *testing.T) {
	for _, tc := range []struct {
		raw        string
		wantOK     bool
		wantPrompt string
	}{
		{"/team how do the two locks interact", true, "how do the two locks interact"},
		{"/team    padded question   ", true, "padded question"},
		// Everything below must pass through untouched, byte for byte.
		{"/team", false, "/team"},
		{"/teamwork on this", false, "/teamwork on this"},
		{"/", false, "/"},
		{"what does /team do", false, "what does /team do"},
		{"ordinary question", false, "ordinary question"},
	} {
		pipeline, prompt, ok := parseTeamCommand(tc.raw)
		if ok != tc.wantOK {
			t.Errorf("%q: ok=%v, want %v", tc.raw, ok, tc.wantOK)
			continue
		}
		if prompt != tc.wantPrompt {
			t.Errorf("%q: prompt=%q, want %q", tc.raw, prompt, tc.wantPrompt)
		}
		if !tc.wantOK && pipeline != nil {
			t.Errorf("%q: a non-command asked for a pipeline: %v", tc.raw, pipeline)
		}
	}
}

// §14 put researcher-then-coder ahead 2-1 and the four-phase shape behind 1-2
// at 3.5x the wall-clock. A command that offers "more specialists" and hands
// over the measured loser is worse than no command.
func TestTheTeamCommandDoesNotShipTheMeasuredLoser(t *testing.T) {
	pipeline, _, ok := parseTeamCommand("/team q")
	if !ok {
		t.Fatal("/team did not parse")
	}
	if len(pipeline) != 2 || pipeline[0] != "researcher" || pipeline[1] != "coder" {
		t.Fatalf("got %v, want the two-phase shape that won the A/B", pipeline)
	}
	for _, name := range pipeline {
		if name == "planner" || name == "tester" {
			t.Errorf("%q is in the default command's shape", name)
		}
	}
}

// THE PLANNER AND TESTER HAVE EXISTED, FULLY IMPLEMENTED, SINCE THE PIPELINE
// WAS WRITTEN -- and until "/team:" there was no way to reach either from the
// TUI. The wire field carried an arbitrary list, the daemon resolved an
// arbitrary list, and the only client sent one hardcoded pair.
func TestTeamShapeNamesThePhasesExplicitly(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		pipeline []string
		prompt   string
	}{
		{"/team:planner,coder fix the parser", []string{"planner", "coder"}, "fix the parser"},
		{"/team:tester run the tests", []string{"tester"}, "run the tests"},
		// Spaces after commas are what a person actually types.
		{"/team:researcher, coder why", []string{"researcher", "coder"}, "why"},
		// Not validated here: what roles exist is the daemon's fact, and it
		// answers with a notice the user can read. See parseTeamCommand.
		{"/team:nosuchrole hello", []string{"nosuchrole"}, "hello"},
	} {
		pipeline, prompt, ok := parseTeamCommand(tc.raw)
		if !ok {
			t.Errorf("%q was not recognised as a team command", tc.raw)
			continue
		}
		if !slices.Equal(pipeline, tc.pipeline) {
			t.Errorf("%q asked for %v, want %v", tc.raw, pipeline, tc.pipeline)
		}
		if prompt != tc.prompt {
			t.Errorf("%q left prompt %q, want %q", tc.raw, prompt, tc.prompt)
		}
	}
}

// A colon with nothing usable after it must fall through as text, never quietly
// choose a shape on the user's behalf -- they typed the colon precisely because
// they wanted to choose.
func TestAnIncompleteTeamShapeIsOrdinaryText(t *testing.T) {
	for _, raw := range []string{"/team:", "/team:coder", "/team: ", "/team: question", "/team:coder "} {
		pipeline, prompt, ok := parseTeamCommand(raw)
		if ok {
			t.Errorf("%q was treated as a team command asking for %v", raw, pipeline)
		}
		if prompt != raw {
			t.Errorf("%q was rewritten to %q; passthrough must be byte-identical", raw, prompt)
		}
	}
}

// The bare form still runs the measured winner, unchanged.
func TestBareTeamStillRunsTheMeasuredWinner(t *testing.T) {
	pipeline, prompt, ok := parseTeamCommand("/team why is this slow")
	if !ok || !slices.Equal(pipeline, []string{"researcher", "coder"}) || prompt != "why is this slow" {
		t.Fatalf("got %v %q ok=%v, want the two-phase default", pipeline, prompt, ok)
	}
}
