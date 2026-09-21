package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// ---------------------------------------------------------------------------
// The config surface.
// ---------------------------------------------------------------------------

// A shape that lost an A/B is one a user is entitled to run and NOT entitled to
// run without being told. Refusing the config would be overreach; saying
// nothing is how a measured loser stays in somebody's config for a year.
func TestPipelineWarningsNameTheMeasuredLosers(t *testing.T) {
	toolless, _ := resolvePipeline([]string{"planner", "coder"})
	if w := pipelineWarnings(toolless, "mcp.pipeline"); len(w) != 1 || !strings.Contains(w[0], "no tools") {
		t.Errorf("a tool-less first phase produced %v", w)
	}

	four, _ := resolvePipeline([]string{"planner", "researcher", "coder", "tester"})
	if w := pipelineWarnings(four, "mcp.pipeline"); len(w) != 2 {
		t.Errorf("the four-phase shape produced %d warnings, want both the tool-less "+
			"and the phase-count one: %v", len(w), w)
	}

	winner, _ := resolvePipeline([]string{"researcher", "coder"})
	if w := pipelineWarnings(winner, "mcp.pipeline"); len(w) != 0 {
		t.Errorf("the shape that WON the A/B was warned about: %v", w)
	}
	if w := pipelineWarnings(nil, "mcp.pipeline"); len(w) != 0 {
		t.Errorf("an unorchestrated turn was warned about: %v", w)
	}
}

// ---------------------------------------------------------------------------
// The per-turn shape: a client may name the phases for one turn.
// ---------------------------------------------------------------------------

func TestARequestedShapeOverridesTheConfiguredOne(t *testing.T) {
	cfg := MCPConfig{Pipeline: []string{"planner", "coder"}}

	phases, _, fromRequest := pipelineForTurn(cfg, nil)
	if fromRequest || len(phases) != 2 || phases[0].Name != roleNamePlanner {
		t.Fatalf("a request that named nothing did not get the configured shape: %v", phases)
	}

	phases, _, fromRequest = pipelineForTurn(cfg, []string{"researcher", "coder"})
	if !fromRequest || len(phases) != 2 || phases[0].Name != roleNameResearcher {
		t.Fatalf("the request's shape did not win: %v", phases)
	}
}

// A daemon with no pipeline configured is the shipped default, and a request
// must be able to ask for one turn of specialists without the user editing a
// config file -- that is the whole point of the command.
func TestARequestCanOrchestrateAnOtherwiseUnorchestratedDaemon(t *testing.T) {
	phases, _, fromRequest := pipelineForTurn(MCPConfig{}, []string{"researcher", "coder"})
	if !fromRequest || len(phases) != 2 {
		t.Fatalf("got %d phases, want the two the request asked for", len(phases))
	}
}

// THE PROPERTY THAT MAKES THIS SAFE TO ACCEPT FROM A CLIENT AT ALL.
//
// A phase is a RESTRICTION, never a grant: the unorchestrated agent is a nil
// role and unrestricted, and every named role allows exactly the tools it lists.
// So no shape a request can name reaches a tool the same client could not
// already have reached by sending an ordinary prompt. If that ever stops being
// true, accepting a shape from a client becomes a privilege escalation, and this
// test is what says so.
func TestNoRequestedShapeCanReachATooltheUnorchestratedAgentCannot(t *testing.T) {
	for name, role := range knownRoles() {
		for _, tool := range role.Tools {
			var unrestricted *agentRole
			if !unrestricted.allowsBuiltinName(tool) {
				t.Errorf("role %q allows %q, which the unorchestrated agent does not: "+
					"naming a phase would GRANT access rather than restrict it", name, tool)
			}
		}
	}
}

// An absurd list is a bug in a client, not a request to run forty phases. The
// budgets already make it pointless -- they are whole-turn -- so the cap is
// about removing the question rather than about the spend.
func TestAnAbsurdlyLongRequestedPipelineIsCapped(t *testing.T) {
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, roleNameCoder)
	}
	phases, _, _ := pipelineForTurn(MCPConfig{}, many)
	if len(phases) != maxRequestedPhases {
		t.Fatalf("got %d phases from a 200-phase request, want the cap of %d", len(phases), maxRequestedPhases)
	}
}

// A typo in a requested role must degrade to the phases that ARE recognised and
// be reported, exactly as a typo in config does -- one path, one behaviour.
func TestAnUnknownRequestedRoleIsDroppedAndReported(t *testing.T) {
	phases, unknown, _ := pipelineForTurn(MCPConfig{}, []string{"resercher", "coder"})
	if len(phases) != 1 || phases[0].Name != roleNameCoder {
		t.Fatalf("got %v, want the one role that exists", phases)
	}
	if len(unknown) != 1 || unknown[0] != "resercher" {
		t.Fatalf("the typo was not reported: %v", unknown)
	}
}

// An empty request must be indistinguishable from no request. Anything else and
// every existing client's behaviour changes the moment this field exists.
func TestAnEmptyRequestedPipelineLeavesTheConfiguredOneAlone(t *testing.T) {
	cfg := MCPConfig{Pipeline: []string{"researcher", "coder"}}
	for _, requested := range [][]string{nil, {}} {
		phases, _, fromRequest := pipelineForTurn(cfg, requested)
		if fromRequest {
			t.Errorf("%v was treated as a request-supplied shape", requested)
		}
		if len(phases) != 2 {
			t.Errorf("%v changed the configured shape to %v", requested, phases)
		}
	}
}

// A WARNING NOBODY READS IS A COMMENT WITH EXTRA STEPS.
//
// pipelineWarnings has gone to s.logger since the shapes were measured. That
// was fine while choosing a shape meant editing a config file; once "/team:"
// let a user choose one per turn, the chooser became someone watching a chat
// window who will never see a daemon log.
func TestAMeasuredBadShapeIsReportedToTheUserWhoAskedForIt(t *testing.T) {
	fourPhase, _, _ := pipelineForTurn(MCPConfig{}, []string{"planner", "researcher", "coder", "tester"})

	notices := pipelineNotices(fourPhase, nil, "what you just asked for", true)
	if len(notices) == 0 {
		t.Fatal("a request for the measured-losing shape told the user nothing")
	}
	for _, n := range notices {
		if n.Component != protocol.DegradedPipelineShape {
			t.Errorf("notice component %q, want %q", n.Component, protocol.DegradedPipelineShape)
		}
		if n.Detail == "" {
			t.Error("a degradation with no detail tells the user something is wrong and not what")
		}
	}

	// THE OTHER HALF OF THE RULE. The same shape set in config is a standing
	// choice the user made once; repeating the warning on every single turn is
	// nagging, not informing. It still logs.
	if got := pipelineNotices(fourPhase, nil, "mcp.pipeline", false); len(got) != 0 {
		t.Errorf("a CONFIGURED shape produced %d per-turn notices; that is nagging: %v", len(got), got)
	}
}

// A typo is a correctness problem whatever its source, so unlike a measured-bad
// shape it is reported from config too: a phase that silently did not run is
// not a choice anyone made.
func TestAnUnknownRoleIsReportedToTheUserFromEitherSource(t *testing.T) {
	for _, fromRequest := range []bool{true, false} {
		phases, unknown, _ := pipelineForTurn(MCPConfig{}, []string{"resercher", "coder"})
		notices := pipelineNotices(phases, unknown, "mcp.pipeline", fromRequest)
		if len(notices) != 1 {
			t.Fatalf("fromRequest=%v: got %d notices, want the one typo reported", fromRequest, len(notices))
		}
		if !strings.Contains(notices[0].Detail, "resercher") {
			t.Errorf("the notice does not name the typo: %q", notices[0].Detail)
		}
		if !strings.Contains(notices[0].Detail, "researcher") {
			t.Errorf("the notice does not say what the real names are: %q", notices[0].Detail)
		}
	}
}
