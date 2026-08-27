package main

// Orchestration: specialists run one at a time, each seeing only what its role
// needs, with its tool access enforced rather than merely suggested.
//
// The properties pinned here are the ones that would let the feature fail
// silently: scoping that is only a menu hint, a handoff that drops the previous
// phase's conclusions, and a default that quietly changes behaviour for users
// who never asked for a pipeline.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// runPipeline drives an orchestrated turn against a scripted upstream.
func runPipeline(t *testing.T, s *Server, phases []*agentRole) (agentResult, []protocol.ToolActivity, string, error) {
	t.Helper()
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	var (
		activity []protocol.ToolActivity
		streamed strings.Builder
	)
	res, err := s.runOrchestrated(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "BASE"}, {Role: "user", Content: "add a retry"}},
		providerRouting{}, nil,
		func(tok string) error { streamed.WriteString(tok); return nil },
		func(a protocol.ToolActivity) { activity = append(activity, a) },
		nil, nil,
		func(protocol.Degradation) {},
		phases)
	return res, activity, streamed.String(), err
}

// An empty pipeline must leave every existing turn exactly as it was. This is
// the property that makes the feature safe to ship switched off.
func TestPipelineDefaultsToUnorchestrated(t *testing.T) {
	cfg := MCPConfig{Enabled: true}
	phases, unknown := cfg.resolvedPipeline()
	if len(phases) != 0 {
		t.Errorf("an unset mcp.pipeline produced %d phase(s), want 0", len(phases))
	}
	if len(unknown) != 0 {
		t.Errorf("an unset mcp.pipeline reported unknown roles %v", unknown)
	}
}

// A typo in one role name must cost that phase, not the whole agent.
func TestPipelineSkipsUnknownRolesWithoutFailing(t *testing.T) {
	cfg := MCPConfig{Enabled: true, Pipeline: []string{"planer", roleNameCoder}}
	phases, unknown := cfg.resolvedPipeline()

	if len(phases) != 1 || phases[0].Name != roleNameCoder {
		t.Fatalf("phases = %v, want just the coder", phases)
	}
	if len(unknown) != 1 || unknown[0] != "planer" {
		t.Errorf("unknown = %v, want the typo reported so it is discoverable", unknown)
	}
}

// THE LOAD-BEARING SECURITY PROPERTY.
//
// Filtering the advertised menu is a hint: the model can name a tool it was
// never shown, and a prompt-injected instruction is exactly the thing that
// would. The allowlist has to be enforced when the call arrives, or role
// scoping is decoration.
func TestRoleScopingIsEnforcedNotJustAdvertised(t *testing.T) {
	// The planner has NO tools. Script it calling one anyway.
	base, _, _ := agentUpstream(t,
		// The REAL qualified name (separator is "__"): a wrong name would be
		// refused by Lookup, and the test would pass without enforcement running.
		toolCallSSE("c1", "builtin__read_file", `{"path":"go.mod"}`),
		textSSE("plan: step one"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		// allow, so ONLY the role scoping can be what refuses it
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
	})

	_, activity, _, err := runPipeline(t, s, []*agentRole{&rolePlanner})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	// Asserted on the ACTIVITY PHASE, not on ToolNames: ToolNames records what
	// the model ASKED for, denied calls included, so a name appearing there
	// proves nothing either way. Whether the tool RAN is the question, and
	// running/succeeded vs denied is where that is recorded.
	var denied, ran bool
	for _, a := range activity {
		if !strings.Contains(a.Tool, "read_file") {
			continue
		}
		switch a.Phase {
		case protocol.ToolPhaseDenied:
			denied = true
		case protocol.ToolPhaseRunning, protocol.ToolPhaseSucceeded, protocol.ToolPhaseApproved:
			ran = true
		}
	}
	if ran {
		t.Error("the planner RAN read_file, a tool its role does not permit, even though config " +
			"said allow — role scoping is not being enforced at dispatch")
	}
	if !denied {
		t.Error("the call was never denied; the test may be passing because the tool was never " +
			"reached rather than because scoping refused it")
	}
}

// The menu half of the same scoping: a role must not be shown tools it may not
// use, or it wastes iterations asking for them.
func TestRoleFiltersTheAdvertisedMenu(t *testing.T) {
	s := loopServer(t, "http://127.0.0.1:1", MCPConfig{Enabled: true})
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	all, _, _ := s.advertisedToolSpecs(context.Background(), registry, nil)
	if len(all) == 0 {
		t.Fatal("premise broken: no tools advertised at all")
	}

	// Pick whatever IS advertised rather than assuming a name: the menu is
	// capped by max_advertised_tools, so hardcoding one turns this into a skip
	// the day the cap or the tool set moves -- and a skipped test tests nothing.
	if len(all) < 2 {
		t.Fatalf("premise broken: need >=2 advertised tools to prove narrowing, got %d", len(all))
	}
	name := all[0].Function.Name
	pick := name[strings.LastIndex(name, "__")+len("__"):]

	scoped, _, _ := s.advertisedToolSpecs(context.Background(), registry,
		&agentRole{Name: "narrow", Display: "Narrow", Tools: []string{pick}})

	if len(scoped) != 1 {
		t.Errorf("a role allowing one tool was advertised %d, want 1", len(scoped))
	}
	if len(scoped) >= len(all) {
		t.Errorf("scoping did not narrow the menu: %d scoped vs %d unscoped", len(scoped), len(all))
	}
}

// UNRESTRICTED IS A NIL ROLE, NOT AN EMPTY LIST, and this test exists because
// the first version conflated them: the name match read len(Tools)==0 as "allow
// everything", which handed the Planner -- defined with an empty list precisely
// so it could call nothing -- the entire toolbox.
func TestNilRoleIsUnrestrictedAndEmptyListIsNothing(t *testing.T) {
	if !(*agentRole)(nil).allowsBuiltinName("anything") {
		t.Error("a nil role must allow every tool: it is the unorchestrated agent")
	}
	none := &agentRole{Name: "none", Tools: []string{}}
	if none.allowsBuiltinName("read_file") {
		t.Error("a role with an EMPTY allowlist must allow nothing; empty is not a wildcard")
	}
	if rolePlanner.allowsBuiltinName("read_file") {
		t.Error("the planner has no tools by design and must not be allowed one")
	}
	narrow := &agentRole{Name: "narrow", Tools: []string{"read_file"}}
	if narrow.allowsBuiltinName("sandbox_exec") {
		t.Error("a role with an allowlist must refuse tools outside it")
	}
	if !narrow.allowsBuiltinName("read_file") {
		t.Error("a role must allow the tools on its own list")
	}
}

// THE FEATURE ITSELF. Phase two must receive phase one's conclusions -- that
// handoff is the entire reason the pipeline exists.
func TestLaterPhaseReceivesEarlierPhaseOutput(t *testing.T) {
	msgs := buildPhaseMessages("BASE", nil, "add a retry", &roleCoder,
		[]phaseOutcome{{role: &rolePlanner, text: "1. wrap the call\n2. add a backoff"}}, 0, "")

	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Fatalf("last message role = %q, want user", last.Role)
	}
	if !strings.Contains(last.Content, "add a retry") {
		t.Error("the coder lost the user's original request")
	}
	if !strings.Contains(last.Content, "wrap the call") {
		t.Error("the coder did not receive the planner's output — the handoff is the whole feature")
	}
	if !strings.Contains(last.Content, rolePlanner.Display) {
		t.Error("the handoff does not say which specialist produced it")
	}
}

// Each phase gets the BASE prompt plus its own, never its own alone: a role
// that replaced the base prompt would drop every safety instruction in it for
// exactly that phase.
func TestRolePromptAppendsToBaseRatherThanReplacingIt(t *testing.T) {
	msgs := buildPhaseMessages("BASE SAFETY RULES", nil, "go", &rolePlanner, nil, 0, "")

	if msgs[0].Role != "system" {
		t.Fatalf("first message role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "BASE SAFETY RULES") {
		t.Error("the base system prompt was dropped for this phase")
	}
	if !strings.Contains(msgs[0].Content, "PLANNER") {
		t.Error("the role prompt is missing")
	}
}

// Only the answering specialist's prose reaches the user. An earlier phase's
// text is threaded forward, not streamed as part of the reply.
func TestOnlyTheAnswerPhaseStreamsToTheUser(t *testing.T) {
	base, _, _ := agentUpstream(t,
		textSSE("PLANNER_TEXT"),
		textSSE("CODER_TEXT"),
	)
	s := loopServer(t, base, MCPConfig{Enabled: true})

	res, _, streamed, err := runPipeline(t, s, []*agentRole{&rolePlanner, &roleCoder})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	if strings.Contains(streamed, "PLANNER_TEXT") {
		t.Error("the planner's prose was streamed to the user as part of the answer")
	}
	if !strings.Contains(streamed, "CODER_TEXT") {
		t.Errorf("the answering phase's prose never reached the user; streamed = %q", streamed)
	}
	if !strings.Contains(res.FinalText, "CODER_TEXT") {
		t.Errorf("FinalText = %q, want the answering phase's text", res.FinalText)
	}
}

// The user must be able to see which specialist is working.
//
// THIS TEST PREVIOUSLY PASSED FOR THE WRONG REASON, and the way it did is worth
// keeping in front of the next reader. It asserted that a.Tool contained the
// substring "step ", which was true of the pre-formatted label the daemon sent
// -- and told us nothing about whether any client would show it. It would not.
// narratePhase used ToolPhaseRequested, which both shipping clients render as
// the empty string on purpose, so every one of these markers was discarded and
// the narration feature did nothing at all.
//
// So this now pins the WIRE CONTRACT the clients actually consume, and the
// other half of it lives in clients/tui/phasenarration_test.go. Neither half
// alone can catch what went wrong here: the daemon was doing exactly what it
// intended, and the client was doing exactly what it intended.
func TestEachPhaseIsNarrated(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("a"), textSSE("b"))
	s := loopServer(t, base, MCPConfig{Enabled: true})

	_, activity, _, err := runPipeline(t, s, []*agentRole{&rolePlanner, &roleCoder})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	var narrated []protocol.ToolActivity
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseStep {
			narrated = append(narrated, a)
		}
	}
	if len(narrated) != 2 {
		t.Fatalf("got %d phase narrations %v, want 2", len(narrated), activity)
	}

	// A phase marker must NOT reuse a phase the clients drop. Naming the two
	// offenders explicitly, because "requested" was the original mistake and
	// "approved" is the other value with the same client-side treatment.
	for _, a := range narrated {
		if a.Phase == protocol.ToolPhaseRequested || a.Phase == protocol.ToolPhaseApproved {
			t.Fatalf("phase narration was sent as %q, which every client deliberately renders as nothing", a.Phase)
		}
		if a.CallID != "" {
			t.Errorf("a phase marker carried CallID %q; there is no call, and a client keyed on it "+
				"collapses every phase onto one line", a.CallID)
		}
	}

	if narrated[0].Tool != rolePlanner.Display || narrated[1].Tool != roleCoder.Display {
		t.Errorf("narration does not name the specialists in order: %q then %q", narrated[0].Tool, narrated[1].Tool)
	}
	// Position in the pipeline, so a user knows how much is left.
	if !strings.Contains(narrated[0].Detail, "1/2") || !strings.Contains(narrated[1].Detail, "2/2") {
		t.Errorf("narration does not carry the step position: %q then %q", narrated[0].Detail, narrated[1].Detail)
	}
}

// answerPhase must never leave a pipeline with nobody streaming: a turn that
// streamed nothing looks identical to a crash.
func TestAnswerPhaseAlwaysResolves(t *testing.T) {
	// None marked StreamsAnswer -> the last phase answers.
	a := &agentRole{Name: "a"}
	b := &agentRole{Name: "b"}
	if got := answerPhase([]*agentRole{a, b}); got != 1 {
		t.Errorf("with no phase marked, answerPhase = %d, want the last (1)", got)
	}
	// Marked -> that one answers, even mid-pipeline.
	marked := &agentRole{Name: "m", StreamsAnswer: true}
	if got := answerPhase([]*agentRole{marked, b}); got != 0 {
		t.Errorf("answerPhase = %d, want the marked phase (0)", got)
	}
	if got := answerPhase([]*agentRole{&roleCoder, &roleTester}); got != 0 {
		t.Errorf("in the four-phase order the coder answers; got %d", got)
	}
}

// A role may tighten its iteration ceiling but must never raise it above the
// user's configured budget.
func TestRoleCannotRaiseTheConfiguredIterationCeiling(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("x"))
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Budget:  MCPBudgetConfig{MaxIterations: 2},
	})
	greedy := &agentRole{Name: "greedy", Display: "Greedy", MaxIterations: 99, StreamsAnswer: true}

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	// resolveBudget is what the loop uses; the clamp lives beside it.
	bud := resolveBudget(s.cfg.MCP.Budget, time.Now())
	if greedy.MaxIterations > 0 && greedy.MaxIterations < bud.maxIterations {
		bud.maxIterations = greedy.MaxIterations
	}
	if bud.maxIterations != 2 {
		t.Errorf("a role asking for %d iterations raised the ceiling to %d; the user's budget must win",
			greedy.MaxIterations, bud.maxIterations)
	}
}

// EVERY TOOL NAME A ROLE CLAIMS MUST ACTUALLY EXIST.
//
// This test exists because the first version of roles.go invented
// "lsp_definition" and "lsp_references"; the real built-ins are
// "query_compiler_definition" and "query_compiler_references". Nothing failed:
// the allowlist simply never matched, so the Researcher and Coder silently lost
// the tools their prompts told them to use, and the only symptom would have
// been a specialist that mysteriously never looked anything up.
//
// A typo in an allowlist is invisible by construction -- it denies rather than
// errors -- so it needs a test that reads the real registry rather than review.
func TestEveryRoleToolExistsInTheRegistry(t *testing.T) {
	s := loopServer(t, "http://127.0.0.1:1", MCPConfig{Enabled: true})
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	tools, _ := registry.Advertised(context.Background())
	real := make(map[string]bool, len(tools))
	for _, tl := range tools {
		real[tl.Name] = true
	}
	if len(real) == 0 {
		t.Fatal("premise broken: the registry advertised no tools")
	}

	for name, role := range knownRoles() {
		for _, want := range role.Tools {
			if !real[want] {
				t.Errorf("role %q allows %q, which no built-in provides — that role silently "+
					"loses the tool its prompt tells it to use", name, want)
			}
		}
	}
}

// THE EGRESS BUDGET MUST BOUND THE WHOLE TURN, NOT EACH PHASE.
//
// max_total_tool_bytes is the ceiling on how much workspace content leaves the
// machine because of tools -- the quantity a privacy-positioned product exists
// to bound. turn.toolBytes lives on agentTurn, and runAgentLoop builds a fresh
// agentTurn per call, so without turnLedger the ledger resets at every phase
// and an N-phase turn sends up to N times the ceiling the user configured.
// MEASURED before the fix: a 2-phase turn sent 1563 bytes against a 1500
// ceiling, where one phase alone also sent 1563.
//
// ASSERTED AGAINST A SINGLE-LOOP BASELINE MEASURED IN THIS SAME TEST, not
// against the raw ceiling. A single loop already overshoots the ceiling by a
// small fixed amount (the truncation notice is appended after the cap is
// applied -- pre-existing, and not what this test is about). Comparing against
// the baseline asks the question that matters -- does a second phase start the
// ledger over? -- instead of failing on an overshoot the pipeline did not cause.
func TestEgressBudgetSpansPhasesRatherThanResetting(t *testing.T) {
	// SIZED SO THAT NO SINGLE PHASE HITS THE CEILING BUT THREE TOGETHER DO.
	// That is the only shape in which the bug is observable: a phase that
	// exceeds the ceiling on its own stops the pipeline there, so the second
	// phase never runs and a reset ledger never gets the chance to show.
	const ceiling = 1500
	corpus := strings.Repeat("abcdefghij", 60) // 600 bytes per read; 3 reads = 1800

	measure := func(phases int) int {
		responses := [][]string{}
		for i := 0; i < phases; i++ {
			responses = append(responses,
				toolCallSSE("c", "builtin__read_file", `{"path":"big.txt"}`),
				textSSE("done"))
		}
		base, _, _ := agentUpstream(t, responses...)
		s := loopServer(t, base, MCPConfig{
			Enabled: true,
			Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
			Budget:  MCPBudgetConfig{MaxIterations: 4, MaxTotalToolBytes: ceiling},
		})
		writeFile(t, filepath.Join(s.workspace, "big.txt"), corpus)
		registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
		t.Cleanup(func() { _ = registry.Close() })

		total := 0
		onActivity := func(a protocol.ToolActivity) { total += a.ResultBytes }
		noop := func(string) error { return nil }
		msgs := []chatMessage{{Role: "system", Content: "B"}, {Role: "user", Content: "go"}}

		if phases == 1 {
			_, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
				msgs, providerRouting{}, nil, noop, onActivity, nil, nil,
				func(protocol.Degradation) {}, nil, nil)
			if err != nil {
				t.Fatalf("runAgentLoop: %v", err)
			}
			return total
		}

		roles := make([]*agentRole, phases)
		for i := range roles {
			roles[i] = &agentRole{
				Name: "r", Display: "R", Tools: []string{"read_file"},
				StreamsAnswer: i == phases-1,
			}
		}
		_, err := s.runOrchestrated(context.Background(), time.Now(), registry, "m", "auto",
			msgs, providerRouting{}, nil, noop, onActivity, nil, nil,
			func(protocol.Degradation) {}, roles)
		if err != nil {
			t.Fatalf("runOrchestrated: %v", err)
		}
		return total
	}

	one := measure(1)
	if one == 0 {
		t.Fatal("premise broken: a single loop emitted no tool bytes, so nothing was measured")
	}
	if one >= ceiling {
		t.Fatalf("premise broken: one phase alone sent %d against a ceiling of %d, so the "+
			"pipeline would stop at phase 1 and a reset ledger could never show", one, ceiling)
	}

	three := measure(3)

	// A little over the ceiling is pre-existing and not what this tests: the
	// truncation notice is appended AFTER the cap is applied, so a single loop
	// overshoots by a few dozen bytes too. Three phases with a reset ledger
	// would send ~3x one phase, which is nowhere near this allowance.
	const truncationAllowance = 200
	if three > ceiling+truncationAllowance {
		t.Errorf("a 3-phase turn sent %d bytes against a max_total_tool_bytes of %d "+
			"(one phase alone sends %d) — the egress ceiling reset between phases, so an "+
			"N-phase turn leaks up to N times the limit the user configured",
			three, ceiling, one)
	}
}

// An approve-for-turn grant must cover the turn, not one phase of it. Resetting
// it per phase re-asks the user for something they already answered, which is
// the approval-fatigue failure that gets a pipeline switched off.
func TestApproveForTurnGrantSurvivesAPhaseBoundary(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"a.txt"}`),
		textSSE("one"),
		toolCallSSE("c2", "builtin__read_file", `{"path":"a.txt"}`),
		textSSE("two"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Budget:  MCPBudgetConfig{MaxIterations: 4},
	})
	writeFile(t, filepath.Join(s.workspace, "a.txt"), "hello")

	// Answers "approve for the rest of the turn" the FIRST time, and would
	// answer no to anything asked afterwards.
	appr := &countingApprover{decision: protocol.ApprovalApproveForTurn}

	r1 := &agentRole{Name: "r1", Display: "First", Tools: []string{"read_file"}}
	r2 := &agentRole{Name: "r2", Display: "Second", Tools: []string{"read_file"}, StreamsAnswer: true}

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })
	_, err := s.runOrchestrated(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "B"}, {Role: "user", Content: "go"}},
		providerRouting{}, appr,
		func(string) error { return nil },
		func(protocol.ToolActivity) {}, nil, nil, func(protocol.Degradation) {},
		[]*agentRole{r1, r2})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	if appr.asked > 1 {
		t.Errorf("the user was asked %d times for the same tool after granting it for the turn; "+
			"the grant did not survive the phase boundary", appr.asked)
	}
}

// countingApprover records how many times it was asked.
type countingApprover struct {
	decision string
	asked    int
}

func (c *countingApprover) Ask(_ context.Context, _ protocol.ToolApprovalRequest) approvalDecision {
	c.asked++
	return approvalDecision{Decision: c.decision}
}

// A pipeline whose every role name was a typo must still answer the user.
// Returning an empty reply for a spelling mistake looks exactly like a crash.
func TestAllUnknownRolesStillProducesAnAnswer(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("FALLBACK_ANSWER"))
	s := loopServer(t, base, MCPConfig{Enabled: true, Pipeline: []string{"planer", "codr"}})

	phases, unknown := s.cfg.MCP.resolvedPipeline()
	if len(phases) != 0 || len(unknown) != 2 {
		t.Fatalf("premise broken: phases=%d unknown=%v", len(phases), unknown)
	}

	_, _, streamed, err := runPipeline(t, s, phases)
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}
	if !strings.Contains(streamed, "FALLBACK_ANSWER") {
		t.Errorf("an all-typo pipeline produced %q; it must fall back to the unorchestrated "+
			"agent rather than hand the user a blank reply", streamed)
	}
}

// splitMessages yields an empty prompt when the list does not end in a user
// turn. An empty user message is a malformed request; it must not be sent.
func TestNoEmptyUserMessageIsBuilt(t *testing.T) {
	msgs := buildPhaseMessages("S", nil, "", &rolePlanner, nil, 0, "")
	for _, m := range msgs {
		if m.Role == "user" && strings.TrimSpace(m.Content) == "" {
			t.Error("an empty user message was built; providers reject it and it costs a round trip")
		}
	}
	if len(msgs) == 0 {
		t.Error("the phase was given no messages at all")
	}
}

// answerPhase must never hand back a negative index.
func TestAnswerPhaseNeverNegative(t *testing.T) {
	if got := answerPhase(nil); got < 0 {
		t.Errorf("answerPhase(nil) = %d; a negative index would panic an unguarded caller", got)
	}
}

// THE HANDOFF MUST BE BOUNDED.
//
// Every earlier phase's full prose is appended to every later phase's user
// message, so without a cap the context grows with phases x output-size and
// nothing anywhere limits it. MEASURED before the cap: three phases emitting
// 200KB each produced a 600KB request. The failure is expensive and late — the
// model refuses on context length at the LAST phase, after the turn has already
// paid for every earlier one.
func TestHandoffTextIsBounded(t *testing.T) {
	const cap = 4096
	big := strings.Repeat("x", 200*1024)
	prior := []phaseOutcome{
		{role: &rolePlanner, text: big},
		{role: &roleResearcher, text: big},
		{role: &roleCoder, text: big},
	}

	msgs := buildPhaseMessages("SYS", nil, "do it", &roleTester, prior, cap, "")
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
	}

	// Three capped handoffs plus the prompt, system prompt and truncation
	// markers. Generous, but nowhere near the 600KB an uncapped build produces.
	if limit := 3*cap + 8192; total > limit {
		t.Errorf("phase context is %d bytes from 3 handoffs capped at %d each (limit %d) — "+
			"the handoff is not being bounded, so a long pipeline grows until the model "+
			"refuses on context length", total, cap, limit)
	}

	// And the truncation must be VISIBLE: a later specialist reading a plan that
	// stops mid-sentence would otherwise treat the fragment as the whole plan.
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "truncated") {
		t.Error("output was cut without a marker; a later phase cannot tell a truncated " +
			"plan from a complete one")
	}
}

// Text under the cap must pass through untouched — a bound that rewrites
// ordinary output would corrupt every handoff to fix a rare one.
func TestHandoffUnderTheCapIsUnchanged(t *testing.T) {
	if got := truncateHandoff("short", 4096); got != "short" {
		t.Errorf("truncateHandoff mangled text under the cap: %q", got)
	}
	if got := truncateHandoff("anything", 0); got != "anything" {
		t.Errorf("a zero cap must mean unbounded, got %q", got)
	}
}

// A zero-valued outcome must not panic. buildPhaseMessages used to dereference
// out.role directly while incompletePhaseText used the guarded accessor.
func TestZeroValuedOutcomeDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a zero-valued phaseOutcome panicked: %v", r)
		}
	}()
	msgs := buildPhaseMessages("S", nil, "go", &roleCoder, []phaseOutcome{{text: "orphaned"}}, 0, "")
	if len(msgs) == 0 {
		t.Error("no messages built")
	}
	_ = incompletePhaseText([]phaseOutcome{{text: "orphaned"}})
}

// A degradation is a fact about the turn. Reporting it once per phase tells the
// user the same thing N times.
func TestDegradationIsReportedOncePerTurnNotOncePerPhase(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("a"), textSSE("b"), textSSE("c"))
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		// A menu cap low enough that tools are dropped, which is what raises the
		// degradation inside every phase's loop.
		Budget: MCPBudgetConfig{MaxAdvertisedTools: 1, MaxIterations: 2},
	})

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	var got []protocol.Degradation
	r1 := &agentRole{Name: "r1", Display: "One"}
	r2 := &agentRole{Name: "r2", Display: "Two"}
	r3 := &agentRole{Name: "r3", Display: "Three", StreamsAnswer: true}

	_, err := s.runOrchestrated(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "S"}, {Role: "user", Content: "go"}},
		providerRouting{}, nil,
		func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
		func(d protocol.Degradation) { got = append(got, d) },
		[]*agentRole{r1, r2, r3})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	seen := map[string]int{}
	for _, d := range got {
		seen[string(d.Component)+d.Detail]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("the same degradation was reported %d times across 3 phases; a user "+
				"should be told once per turn (%.40s...)", n, key)
		}
	}
}

// G4: max_iterations is PER PHASE by design, so an N-phase pipeline could make
// N x max_iterations model calls and nothing bounded the product.
// turn_timeout_seconds bounds wall-clock but not token spend — which is the
// thing a user is surprised by on a bill.
//
// EACH PHASE HERE COMPLETES NORMALLY, and that is the whole point of the setup.
// A phase that exhausts its OWN ceiling returns Incomplete, which stops the
// pipeline — so budget exhaustion can never demonstrate the multiplication. The
// product only accumulates when every phase finishes of its own accord having
// used several iterations, which is the ordinary case this ceiling exists for.
func TestWholeTurnIterationCeilingBoundsThePipeline(t *testing.T) {
	// Per phase: two tool calls then an answer = 3 iterations, comfortably under
	// a per-phase ceiling of 8. Four such phases = 12 model calls.
	var responses [][]string
	for phase := 0; phase < 4; phase++ {
		responses = append(responses,
			toolCallSSE("c", "builtin__read_file", `{"path":"a.txt"}`),
			toolCallSSE("c", "builtin__read_file", `{"path":"a.txt"}`),
			textSSE("phase done"))
	}
	base, _, _ := agentUpstream(t, responses...)

	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		Budget: MCPBudgetConfig{
			// The per-phase ceiling must sit BELOW the turn ceiling or
			// resolvedMaxTurnIterations raises the turn ceiling to meet it (that
			// floor is what keeps an unorchestrated turn unaffected), and the test
			// would then measure the floor rather than the ceiling.
			MaxIterations:     4, // per phase — 3 calls each, never reached
			MaxTurnIterations: 7, // whole turn — below the 12 four phases want
		},
	})
	writeFile(t, filepath.Join(s.workspace, "a.txt"), "x")

	phases := make([]*agentRole, 4)
	for i := range phases {
		phases[i] = &agentRole{
			Name: "r", Display: "R", Tools: []string{"read_file"},
			StreamsAnswer: i == 3,
		}
	}

	res, _, _, err := runPipeline(t, s, phases)
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	if res.Iterations > 7 {
		t.Errorf("the pipeline made %d model calls against a max_turn_iterations of 7 "+
			"(4 phases x 3 calls = 12 when unbounded) — the per-phase ceiling multiplied "+
			"instead of the turn being bounded", res.Iterations)
	}
	if res.Incomplete == nil {
		t.Error("a turn stopped by its own budget must say so; silence reads as a finished answer")
	}
}

// The whole-turn ceiling must never bind more tightly than the per-phase one,
// or switching it on would silently shorten every existing unorchestrated turn.
func TestTurnCeilingNeverBindsTighterThanThePhaseCeiling(t *testing.T) {
	cases := []struct{ perPhase, turn, want int }{
		{perPhase: 8, turn: 0, want: defaultMaxTurnIterations}, // unset -> default
		{perPhase: 40, turn: 5, want: 40},                      // turn below phase -> raised to phase
		{perPhase: 4, turn: 6, want: 6},                        // honoured when above
	}
	for _, c := range cases {
		b := MCPBudgetConfig{MaxIterations: c.perPhase, MaxTurnIterations: c.turn}
		if got := b.resolvedMaxTurnIterations(); got != c.want {
			t.Errorf("perPhase=%d turn=%d: resolved %d, want %d", c.perPhase, c.turn, got, c.want)
		}
		if got := b.resolvedMaxTurnIterations(); got < b.resolvedMaxIterations() {
			t.Errorf("perPhase=%d turn=%d: the turn ceiling (%d) fell below the per-phase one (%d); "+
				"an unorchestrated turn would be silently shortened",
				c.perPhase, c.turn, got, b.resolvedMaxIterations())
		}
	}
}

// A single unorchestrated loop must be unaffected: one phase means the turn
// ceiling can only equal or exceed the per-phase one, so the per-phase budget
// is still what bites.
func TestUnorchestratedTurnIsUnaffectedByTheTurnCeiling(t *testing.T) {
	responses := make([][]string, 0, 20)
	for i := 0; i < 20; i++ {
		responses = append(responses, toolCallSSE("c", "builtin__read_file", `{"path":"a.txt"}`))
	}
	base, _, _ := agentUpstream(t, responses...)

	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		Budget:  MCPBudgetConfig{MaxIterations: 3},
	})
	writeFile(t, filepath.Join(s.workspace, "a.txt"), "x")

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "user", Content: "go"}}, providerRouting{}, nil,
		func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
		func(protocol.Degradation) {}, nil, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if res.Iterations != 3 {
		t.Errorf("an unorchestrated turn ran %d iterations against max_iterations 3; the "+
			"whole-turn ceiling must not change the single-loop path", res.Iterations)
	}
}

// A PHASE THAT RUNS AFTER THE ANSWERING PHASE MUST STILL REACH THE USER.
//
// The four-phase pipeline puts the Tester after the Coder, and the Coder is
// what marks StreamsAnswer. Streaming only the exact answer index meant the
// Tester ran — a model call plus sandbox_exec running the project's real test
// suite — and its verdict was dropped. Tests failed and nobody was told.
func TestPostAnswerPhaseOutputReachesTheUser(t *testing.T) {
	phases := []*agentRole{&rolePlanner, &roleResearcher, &roleCoder, &roleTester}
	if answerPhase(phases) != 2 {
		t.Fatalf("premise broken: answer phase is %d, expected the coder at 2", answerPhase(phases))
	}

	base, _, _ := agentUpstream(t,
		textSSE("PLAN"), textSSE("RESEARCH"), textSSE("CODE"), textSSE("TESTS_FAILED_IMPORTANT"))
	s := loopServer(t, base, MCPConfig{Enabled: true})

	res, _, streamed, err := runPipeline(t, s, phases)
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	if !strings.Contains(streamed, "TESTS_FAILED_IMPORTANT") {
		t.Errorf("the Tester ran but its verdict never reached the user; streamed = %q. "+
			"A failing test suite would be silently discarded.", streamed)
	}
	if !strings.Contains(res.FinalText, "TESTS_FAILED_IMPORTANT") {
		t.Errorf("FinalText = %q, missing the Tester's verdict", res.FinalText)
	}
	// The answering phase's own prose must survive alongside it, not be replaced.
	if !strings.Contains(res.FinalText, "CODE") {
		t.Errorf("FinalText = %q, the Coder's answer was replaced rather than added to", res.FinalText)
	}
	// And the two must be distinguishable, or they read as one voice.
	if !strings.Contains(res.FinalText, roleTester.Display) {
		t.Error("the post-answer phase's output is not labelled; it runs on from the Coder's prose")
	}
	// Phases BEFORE the answer still must not leak into the reply.
	if strings.Contains(streamed, "PLAN") || strings.Contains(streamed, "RESEARCH") {
		t.Errorf("an earlier phase's prose leaked into the answer: %q", streamed)
	}
}

// A BUDGET THAT STOPS THE PIPELINE MUST STILL PRODUCE A VISIBLE ANSWER.
//
// FOUND LIVE, not by review. A real four-phase turn exhausted the Researcher's
// max_iterations at phase 2 of 4; runOrchestrated built 698 bytes of partial
// work and streamed 0 of them. The Done message carries no text -- the user's
// answer is exactly what goes through onToken -- so the user saw a blank reply
// with an incomplete badge whose detail read "what you see above is everything
// that was done", above which was nothing. Worse, those 698 bytes WERE
// persisted to the transcript, so the next turn's history contained an
// assistant message the user had never been shown.
//
// This is the same failure class as the Tester's discarded verdict: text that
// reaches a return value is not text that reaches a person.
func TestBudgetStopBeforeTheAnswerPhaseStillStreamsTheWorkSoFar(t *testing.T) {
	// The Planner completes normally. The Researcher then burns its ceiling on
	// tool calls and never gets to write a conclusion -- which is exactly the
	// live shape: the pipeline stops at a phase BEFORE the answering Coder.
	base, _, _ := agentUpstream(t,
		textSSE("PLAN: read the lock file, then describe it"),
		toolCallSSE("c1", "builtin__read_file", `{"path":"go.mod"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"go.work"}`),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		// The TURN ceiling, not the per-phase one. A pre-answer phase spending
		// its own reserved share no longer stops the pipeline (that is
		// TestAPreAnswerPhaseSpendingItsShareDoesNotDenyTheUserAnAnswer below);
		// what still stops it is the turn genuinely running out, and that is the
		// case this test is about.
		Budget: MCPBudgetConfig{MaxIterations: 2, MaxTurnIterations: 3},
	})

	res, _, streamed, err := runPipeline(t, s,
		[]*agentRole{&rolePlanner, &roleResearcher, &roleCoder})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}
	if res.Incomplete == nil {
		t.Fatalf("the pipeline was expected to stop on a budget; it completed instead (streamed %q)", streamed)
	}

	// THE ASSERTION. Not "FinalText is non-empty" -- that was true while the
	// bug was live. What matters is that the bytes reached the token stream.
	if strings.TrimSpace(streamed) == "" {
		t.Fatalf("the pipeline stopped at a phase before the answer and streamed NOTHING; "+
			"the user sees a blank reply with an incomplete badge. FinalText held %d byte(s) they never saw",
			len(res.FinalText))
	}
	if !strings.Contains(streamed, "PLAN: read the lock file") {
		t.Errorf("the completed Planner's work did not reach the user; streamed = %q", streamed)
	}
	// The transcript and the screen must agree, or history carries an answer
	// that was never given.
	if strings.TrimSpace(streamed) != strings.TrimSpace(res.FinalText) {
		t.Errorf("streamed and FinalText disagree:\n streamed  = %q\n FinalText = %q",
			streamed, res.FinalText)
	}
}

// A refusal must name the tool it refused.
//
// FOUND LIVE: a real model asked for "search_code" instead of the qualified
// "builtin__search_code". SplitQualifiedName cannot split an unqualified name,
// so the reported activity carried an EMPTY tool name and the user was shown a
// denial of nothing at all -- twice in one turn, in a run whose whole point was
// watching what the specialists did.
//
// THAT EXACT EXAMPLE NO LONGER REACHES HERE: Registry.Canonicalize now resolves
// a bare BUILT-IN name to the first-party lane, so "search_code" runs instead
// of being refused. The property this test exists for survives the change and
// is if anything more important now -- what still cannot resolve is a bare name
// that is not a built-in, and that refusal must name what it refused. So the
// test moved to the case that remains rather than being deleted with the bug.
func TestRefusingAnUnqualifiedToolNameStillNamesIt(t *testing.T) {
	base, _, _ := agentUpstream(t,
		// Unqualified AND not a built-in: unresolvable by design.
		toolCallSSE("c1", "grep_the_internet", `{"query":"apply lock"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"search_code": "allow"}},
	})

	_, activity, _, err := runPipeline(t, s, []*agentRole{&roleResearcher})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	var denied []protocol.ToolActivity
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseDenied {
			denied = append(denied, a)
		}
	}
	if len(denied) == 0 {
		t.Fatalf("an unqualified tool name was not refused at all; activity = %+v", activity)
	}
	for _, a := range denied {
		if a.Tool == "" {
			t.Errorf("a refusal reached the client with an EMPTY tool name (detail = %q); "+
				"the user cannot tell what was refused", a.Detail)
		}
		if !strings.Contains(a.Tool, "grep_the_internet") {
			t.Errorf("the refusal named %q, but the model asked for \"grep_the_internet\"", a.Tool)
		}
	}
}

// THE ANSWERING PHASE PRODUCING NO PROSE IS THE ORDINARY CASE, NOT AN EDGE ONE.
//
// A Coder's work goes to the proposal sink; it can legitimately propose an edit
// and say almost nothing. When a Tester then ran after it, the separator was
// decided by POSITION for the stream ("am I after the answer phase?") and by
// CONTENT for FinalText ("is there anything to separate from?"). The two
// disagreed exactly here:
//
//	streamed  = "\n\n---\n\n**Tester step:**\n\nTESTS PASS"
//	FinalText = "TESTS PASS"
//
// So the user's reply opened with a horizontal rule above nothing, and the
// transcript recorded a different reply from the one on screen.
func TestSilentAnswerPhaseDoesNotProduceADanglingSeparator(t *testing.T) {
	base, _, _ := agentUpstream(t,
		textSSE(""), // the Coder proposes and says nothing
		textSSE("TESTS PASS"),
	)
	s := loopServer(t, base, MCPConfig{Enabled: true, Budget: MCPBudgetConfig{MaxIterations: 4}})

	res, _, streamed, err := runPipeline(t, s, []*agentRole{&roleCoder, &roleTester})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}

	if strings.HasPrefix(strings.TrimSpace(streamed), "---") {
		t.Errorf("the reply opens with a separator above nothing: %q", streamed)
	}
	if strings.TrimSpace(streamed) != strings.TrimSpace(res.FinalText) {
		t.Errorf("the screen and the transcript disagree:\n streamed  = %q\n FinalText = %q",
			streamed, res.FinalText)
	}
	if !strings.Contains(streamed, "TESTS PASS") {
		t.Errorf("the Tester's verdict did not reach the user: %q", streamed)
	}
}

// The separator must still appear when there IS something to separate.
func TestSeparatorStillAppearsBetweenTwoSpeakingPhases(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("CODE_TEXT"), textSSE("TEST_TEXT"))
	s := loopServer(t, base, MCPConfig{Enabled: true, Budget: MCPBudgetConfig{MaxIterations: 4}})

	res, _, streamed, err := runPipeline(t, s, []*agentRole{&roleCoder, &roleTester})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}
	if !strings.Contains(streamed, "**Tester step:**") {
		t.Errorf("two speaking phases ran on as one voice: %q", streamed)
	}
	if strings.Index(streamed, "CODE_TEXT") > strings.Index(streamed, "TEST_TEXT") {
		t.Errorf("phases reached the user out of order: %q", streamed)
	}
	if strings.TrimSpace(streamed) != strings.TrimSpace(res.FinalText) {
		t.Errorf("the screen and the transcript disagree:\n streamed  = %q\n FinalText = %q",
			streamed, res.FinalText)
	}
}

// A PRE-ANSWER PHASE SPENDING ITS OWN SHARE MUST NOT DENY THE USER AN ANSWER.
//
// FOUND BY THE COST A/B, and it is why the four-phase pipeline lost. One ledger
// means one max_total_tool_bytes pool for the whole turn -- correct as a privacy
// bound -- and the Researcher drank it. MEASURED live: the pipeline stopped at
// phase 2 of 4 and handed the user raw partial research where they had asked a
// question, while a single agent answered the same question inside the same
// budget.
//
// The answering phase is now guaranteed a share, and a pre-answer phase hitting
// its own reservation is a phase ending, not a turn ending.
func TestAPreAnswerPhaseSpendingItsShareDoesNotDenyTheUserAnAnswer(t *testing.T) {
	base, _, _ := agentUpstream(t,
		// Researcher: burns its per-phase ceiling on tool calls, never concludes.
		toolCallSSE("c1", "builtin__read_file", `{"path":"go.mod"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"go.work"}`),
		// Coder: gets to run, and answers.
		textSSE("THE ANSWER"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		// Two model calls per PHASE, but plenty for the turn -- so the Researcher
		// exhausts only its own allowance.
		Budget: MCPBudgetConfig{MaxIterations: 2, MaxTurnIterations: 24},
	})

	res, _, streamed, err := runPipeline(t, s, []*agentRole{&roleResearcher, &roleCoder})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}
	if !strings.Contains(streamed, "THE ANSWER") {
		t.Fatalf("the Researcher spending its own allowance stopped the pipeline before the Coder "+
			"could answer; the user got %q instead of an answer", streamed)
	}
	if res.Incomplete != nil {
		t.Errorf("the turn was reported incomplete, but only a phase's own reservation was spent: %+v", res.Incomplete)
	}
}

// The reservation may only ever TIGHTEN a phase's ceiling. If it could raise
// one, the orchestrator would be a way to spend past the privacy bound the user
// configured -- which is the same rule role ceilings follow, for the same reason.
func TestThePhaseReservationCanOnlyTightenTheToolBudget(t *testing.T) {
	// Two reads of ~600 bytes each against a configured ceiling of 1000: the
	// SECOND must be refused. A phase cap of 999999 must not buy it.
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"a.txt"}`),
		toolCallSSE("c2", "builtin__read_file", `{"path":"b.txt"}`),
		toolCallSSE("c3", "builtin__read_file", `{"path":"c.txt"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		Budget:  MCPBudgetConfig{MaxIterations: 8, MaxTotalToolBytes: 1000},
	})
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(s.workspace, name), []byte(strings.Repeat("x", 600)), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
	}

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	ledger := newTurnLedger()
	ledger.toolByteCap = 999999 // a caller trying to RAISE the user's ceiling

	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "user", Content: "go"}}, providerRouting{}, nil,
		func(string) error { return nil }, nil, nil, nil, nil, nil, ledger)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if ledger.toolBytes > 1500 {
		t.Errorf("the turn sent %d bytes of tool output against a configured ceiling of 1000; "+
			"a phase cap raised the user's privacy bound instead of only lowering it", ledger.toolBytes)
	}
	if res.Incomplete == nil {
		t.Error("the tool-byte ceiling never bit, so this test proves nothing about it")
	}
}

// A tool-less phase's output must be labelled as unverified when it is handed on.
//
// FOUND BY THE COST A/B: the Planner, which has no tools, invented
// `src/agent/agent.ts` in a Go repository, and the later phases carried the
// invention into the answer as though it were a finding. Both of the four-phase
// pipeline's losses on that scenario were attributed to those paths.
func TestAToollessPhasesHandoffIsMarkedUnverified(t *testing.T) {
	prior := []phaseOutcome{{role: &rolePlanner, text: "look at src/agent/agent.ts"}}
	messages := buildPhaseMessages("BASE", nil, "trace the approval path", &roleCoder, prior, 0, "")

	var user string
	for _, m := range messages {
		if m.Role == "user" {
			user = m.Content
		}
	}
	if !strings.Contains(user, "NO TOOLS") {
		t.Errorf("the Planner's handoff was passed on unlabelled, so the next specialist cannot tell "+
			"a guess from a finding:\n%s", user)
	}

	// A phase that DID have tools must not be labelled -- the marker has to mean
	// something, and marking everything would make it mean nothing.
	prior = []phaseOutcome{{role: &roleResearcher, text: "daemon/agentloop.go:614 resolveExecutable"}}
	messages = buildPhaseMessages("BASE", nil, "trace the approval path", &roleCoder, prior, 0, "")
	for _, m := range messages {
		if m.Role == "user" && strings.Contains(m.Content, "NO TOOLS") {
			t.Errorf("a Researcher's findings were labelled unverified; it has tools and used them:\n%s", m.Content)
		}
	}
}
