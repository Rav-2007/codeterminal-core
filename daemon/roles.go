package main

import (
	"fmt"

	"codeterminal/daemon/mcp"
	"codeterminal/protocol"
)

// Specialist roles for the orchestrated agent.
//
// The problem this solves is CONTEXT DILUTION, not speed, and that distinction
// is the whole reason the design looks like this. A single loop accumulates
// every tool result, every dead end and every file it happened to read into one
// message list, and by the time it writes code it is reasoning over a context
// where the relevant three lines sit among forty pages. Splitting the work
// across specialists, each seeing only what its job needs, is a fix for that.
//
// It is NOT concurrency, and the difference is load-bearing:
//
//   - The consent channel (connApprover, toolapproval.go) is one encoder/decoder
//     pair over one connection. Two concurrent asks race its decoder and get
//     denied by answersAnotherQuestion -- safe, but wrong.
//   - Every unlisted tool resolves to "ask" (policyForTool, mcpconfig.go),
//     read-only ones included, so parallel researchers would mean parallel
//     approval prompts into a channel that serves one at a time.
//   - Applies take an exclusive per-workspace flock (LockWorkspaceApply), so two
//     writing agents are a lock queue, not parallelism.
//
// Phases therefore run ONE AT A TIME. Context isolation is a property of how
// each phase's message list is built, and needs no goroutine to achieve. See
// docs/MULTI_AGENT_DESIGN.md for the options that were rejected and why.

// agentRole is one specialist: what it is told, and what it may touch.
//
// A nil *agentRole means the unorchestrated single agent -- the behaviour every
// caller had before this file existed. That is why every field here is additive
// and why nil is threaded through rather than a "default role": an existing turn
// must not change shape because roles now exist.
type agentRole struct {
	// Name is the stable identifier used in config, logs and audit records.
	Name string

	// Display is what a human sees in the TUI's phase narration.
	Display string

	// Prompt is APPENDED to the daemon's system prompt rather than replacing it.
	// The base prompt carries the things that are true of every phase -- how to
	// format edits, what the workspace is, what it must not do -- and a role
	// that replaced it would have to restate all of them, which is how a
	// safety instruction goes missing in exactly one phase.
	Prompt string

	// Tools is the COMPLETE list of unqualified built-in tool names this role
	// may use. Empty means NO tools, not "all tools" -- the Planner is defined
	// with an empty list precisely because it should not be able to call
	// anything, and an empty-means-everything reading silently handed it the
	// whole toolbox.
	//
	// "Unrestricted" is spelled as a nil *agentRole (the unorchestrated agent),
	// not as an empty list on a real role. Keeping those two states in different
	// types is what stops them being confused again.
	//
	// Enforced in TWO places on purpose (advertisedToolSpecs filters the menu,
	// resolveExecutable refuses the call), because a filtered menu is a hint and
	// this codebase does not let the model's cooperation be the control. The
	// model can name a tool it was not shown; naming it must not run it.
	Tools []string

	// MaxIterations overrides the turn budget for this phase. Zero inherits.
	// A Planner that cannot call tools has no reason to loop, and giving it the
	// same ceiling as a Coder is how a phase burns iterations doing nothing.
	MaxIterations int

	// StreamsAnswer marks the phase whose prose is the user's answer. Earlier
	// phases are narrated as activity instead, so a two-phase turn does not
	// hand the user a plan and an implementation stapled together and call it a
	// reply.
	StreamsAnswer bool
}

// allowsTool reports whether this role may be OFFERED, or may CALL, one
// concrete tool. It is the only correct entry point: allowsBuiltinName below
// answers half the question and is deliberately awkward to reach.
//
// A NIL role allows everything: that is the unorchestrated agent, which has no
// specialist scoping.
//
// LANE IS PART OF THE DECISION, not a detail of it. Tools is a list of
// FIRST-PARTY names, curated one at a time by someone who read what each one
// does -- that is the entire basis on which "read_file belongs to the
// Researcher" is a true statement. A third-party server may expose a tool
// called read_file too, and nothing about the name makes the claim true of
// THAT one: it is a different program, in a different lane, unconfined, whose
// behaviour this repository has never seen. Matching on the bare name admitted
// it into a slot that was reserved for the confined tool of the same name,
// through BOTH halves of the two-part control at once, because both halves
// compared the same lane-blind string.
//
// This is the same reservation ValidateServerName already makes at the server
// level (mcp.BuiltinServerName is refused as a configured server name, "so a
// user cannot shadow the confined tools with unconfined ones of the same
// name"). That reasoning was correct and simply had not been carried down to
// the tool level, which is where a role allowlist does its matching.
//
// So: Lane B is never admitted by a role's Tools list. It is not silently
// dropped either -- see thirdPartyExcluded, which makes the exclusion something
// the user is told about rather than something they have to infer from an agent
// that mysteriously stopped using their server.
func (r *agentRole) allowsTool(t mcp.Tool) bool {
	if r == nil {
		return true
	}
	if t.Lane != protocol.LaneFirstParty {
		return false
	}
	return r.allowsBuiltinName(t.Name)
}

// allowsBuiltinName is the NAME half of allowsTool, split out only so the two
// halves can be tested and read separately. It is lane-blind by construction,
// so calling it directly on a tool of unknown lane is the bug allowsTool
// exists to prevent -- call allowsTool.
//
// A non-nil role allows exactly what its Tools list names, and an empty list
// therefore allows nothing -- see the Tools field for why that distinction is
// load-bearing rather than pedantic.
func (r *agentRole) allowsBuiltinName(name string) bool {
	if r == nil {
		return true
	}
	for _, t := range r.Tools {
		if t == name {
			return true
		}
	}
	return false
}

// Role names, exported as constants because they appear in user config and an
// audit log, and a typo in either should fail loudly rather than silently
// selecting nothing.
const (
	roleNamePlanner    = "planner"
	roleNameResearcher = "researcher"
	roleNameCoder      = "coder"
	roleNameTester     = "tester"
)

// The role prompts are deliberately short and negative-first. Each one's job is
// to stop a specialist doing the NEXT specialist's work, which is the failure
// mode that collapses a pipeline back into one undifferentiated agent.

var rolePlanner = agentRole{
	Name:          roleNamePlanner,
	Display:       "Planner",
	MaxIterations: 1,
	Tools:         []string{}, // no tools at all: planning is reasoning over the request
	Prompt: "You are the PLANNER for this task. Produce a short, ordered plan and nothing else.\n\n" +
		"Do NOT write code, do not produce edit blocks, and do not attempt to solve the task -- " +
		"a later specialist does that, and it will have your plan.\n\n" +
		"State each step as one line. Name the files or symbols you believe are involved, and say " +
		"plainly when you do not know one rather than inventing a plausible path. If the request is " +
		"already a single obvious step, say so in one line instead of padding it into several.",
}

var roleResearcher = agentRole{
	Name:    roleNameResearcher,
	Display: "Researcher",
	Tools:   []string{"search_code", "read_file", "list_directory", "query_compiler_definition", "query_compiler_references"},
	Prompt: "You are the RESEARCHER for this task. Gather the specific code the plan needs and report what you found.\n\n" +
		"Do NOT propose edits, do not write code, and do not judge the plan -- report evidence.\n\n" +
		"For each finding give the file path, the line range, and what is actually there. Quote only " +
		"the lines that matter. If you searched for something and it does not exist, say that " +
		"explicitly: a later specialist acting on an assumed function is worse than one told it is missing.",
}

var roleCoder = agentRole{
	Name:          roleNameCoder,
	Display:       "Coder",
	StreamsAnswer: true,
	Tools:         []string{"search_code", "read_file", "list_directory", "propose_edit", "propose_ast_edit", "query_compiler_definition", "query_compiler_references"},
	Prompt: "You are the CODER for this task. Implement it using the plan and research you were given.\n\n" +
		"Prefer the findings you were handed over re-deriving them; read a file again only when you " +
		"actually need something the research did not carry.\n\n" +
		"Your edits are PROPOSALS -- a human reviews every one before it reaches disk -- so make them " +
		"complete and minimal rather than hedged. If the plan turns out to be wrong once you see the " +
		"real code, say so and do the right thing instead of implementing something you know is wrong.",
}

var roleTester = agentRole{
	Name:    roleNameTester,
	Display: "Tester",
	Tools:   []string{"sandbox_exec", "read_file", "search_code"},
	Prompt: "You are the TESTER for this task. Validate the work that was just proposed.\n\n" +
		"Do NOT write new features and do not fix what you find -- report it.\n\n" +
		"Run the project's own build or test command where one applies. Report exactly what you ran " +
		"and what it said. A command that failed is a useful result, not a problem to hide; report the " +
		"failure verbatim rather than summarising it into something reassuring.",
}

// knownRoles maps a config name to its role.
func knownRoles() map[string]*agentRole {
	return map[string]*agentRole{
		roleNamePlanner:    &rolePlanner,
		roleNameResearcher: &roleResearcher,
		roleNameCoder:      &roleCoder,
		roleNameTester:     &roleTester,
	}
}

// resolvePipeline turns configured role names into roles, dropping unknown ones
// with a reason rather than failing the turn.
//
// An unknown name is a config typo, and the cost of being strict here is a user
// whose whole agent stops working because they wrote "planer". Degrading to the
// phases that ARE recognised keeps the turn useful, and the caller logs what it
// dropped so the typo is still discoverable.
func resolvePipeline(names []string) (phases []*agentRole, unknown []string) {
	all := knownRoles()
	for _, n := range names {
		if role, ok := all[n]; ok {
			phases = append(phases, role)
			continue
		}
		unknown = append(unknown, n)
	}
	return phases, unknown
}

// pipelineWarnings reports configurations that are legal but measured to be bad.
//
// SEPARATE FROM resolvePipeline, and separate from validation, because these are
// not errors. Every one of them is a shape a user is entitled to run; what they
// are not entitled to is running it without ever being told what happened when
// it was measured. Refusing the config would be overreach -- it is their money
// and their repository -- and saying nothing is how a default that lost an A/B
// stays in somebody's config file for a year.
//
// Returns strings rather than logging, so the caller owns the prefix and this
// stays testable without capturing a logger.
//
// source NAMES WHERE THE SHAPE CAME FROM, and it is a parameter rather than the
// constant "mcp.pipeline" because these strings are now read by a user and not
// only by a log. A person who typed "/team:planner,coder" and was told their
// "mcp.pipeline" is wrong would go looking in a config file that does not say
// that -- the warning would be accurate about the shape and a lie about where
// they set it, which is the kind of detail that costs someone an afternoon.
func pipelineWarnings(phases []*agentRole, source string) []string {
	var out []string
	if len(phases) == 0 {
		return nil
	}
	// A TOOL-LESS PHASE FIRST is the specific shape §14 measured losing. It
	// cannot open a file, so everything it names is a guess, and every later
	// phase inherits the guess as though it were a finding. MEASURED: the
	// Planner invented `src/agent/agent.ts` in this Go repository and the judge
	// attributed both of the four-phase pipeline's losses to exactly that.
	// unverifiedNote (orchestrator.go) labels the handoff, which contains the
	// damage; it does not make the shape a good idea.
	if first := phases[0]; len(first.Tools) == 0 {
		out = append(out, fmt.Sprintf("%s starts with %q, which has no tools and so cannot "+
			"check anything it says. Measured (docs/MULTI_AGENT_DESIGN.md \u00a714): this shape lost 1 win "+
			"to 2 losses against a single agent at the same token cost. [\"researcher\",\"coder\"] won "+
			"2 to 1 and is what a bare \"/team\" runs.", source, first.Name))
	}
	// Four or more phases is the other measured loser, and it loses on the axis
	// nobody argues about: 3.5x the wall-clock for the same tokens.
	if len(phases) >= 4 {
		out = append(out, fmt.Sprintf("%s has %d phases. Measured: four phases cost 3.5x the "+
			"wall-clock of a single agent for the same tokens and a worse answer.", source, len(phases)))
	}
	return out
}

// pipelineNotices turns what pipeline resolution learned into things the USER
// is told, rather than things the daemon log knows.
//
// pipelineWarnings has existed since the shapes were measured and has only ever
// gone to s.logger. That was defensible while the only way to choose a shape was
// to edit a config file and restart -- the person doing that is the person who
// can read a log. It stopped being defensible the moment a shape could be named
// per turn from the TUI: the person choosing is now watching a chat window, and
// a warning they cannot see is a comment with extra steps.
//
// THE TWO KINDS ARE DELIBERATELY NOT TREATED THE SAME:
//
//   - An unknown role name is a TYPO, and a phase silently not running is a
//     correctness problem whatever its source. Always reported.
//   - A measured-bad shape is a CHOICE, and reporting a standing config choice
//     on every single turn is nagging, not informing. Reported only when the
//     shape came from this request -- which is the moment the measurement is
//     actually actionable, because the user just typed it and can retype it.
//     A configured shape still logs, exactly as before.
//
// Pure, and returning values rather than encoding them, so the split above is
// testable without a socket.
func pipelineNotices(phases []*agentRole, unknown []string, source string, fromRequest bool) []protocol.Degradation {
	var out []protocol.Degradation
	for _, name := range unknown {
		out = append(out, protocol.Degradation{
			Component: protocol.DegradedPipelineShape,
			Detail: fmt.Sprintf("%q is not a specialist this daemon knows, so that step did not run. "+
				"The steps that exist are planner, researcher, coder and tester.", name),
		})
	}
	if !fromRequest {
		return out
	}
	for _, w := range pipelineWarnings(phases, source) {
		out = append(out, protocol.Degradation{Component: protocol.DegradedPipelineShape, Detail: w})
	}
	return out
}

// answerPhase returns the index of the phase whose prose is the user's answer.
//
// It is the LAST phase marked StreamsAnswer, and falls back to the final phase
// when none is marked. The fallback is what makes an arbitrary user-configured
// pipeline safe: a turn that streamed nothing because no phase claimed the
// answer would look to the user exactly like a turn that silently failed.
func answerPhase(phases []*agentRole) int {
	if len(phases) == 0 {
		// No phases means no answer index. Zero rather than -1 so a caller that
		// does not guard cannot index out of range on a negative; runOrchestrated
		// guards anyway, and returning a valid-looking index from an invalid
		// input is the lesser of the two wrongs.
		return 0
	}
	answer := len(phases) - 1
	for i, p := range phases {
		if p.StreamsAnswer {
			answer = i
		}
	}
	return answer
}
