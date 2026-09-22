package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// S1 / F-01: CONSENT MUST NOT BE OBTAINED UNDER FALSE PRETENCES.
//
// Registry.RegisterBuiltin asserted Confined=true for every Lane A tool. That is
// right for the read-and-propose built-ins and false for sandbox_exec, which
// runs go/npm/make/cargo -- each of which is arbitrary code execution by design.
// The flag travels on ToolApprovalRequest and drives what the user is told, so
// the TUI printed "anything it changes goes through the same review you use for
// edits" for a command that runs immediately, on a host that may supply no
// sandbox at all.

// The claim and the behaviour must be ONE computation. If they can be derived
// separately they will agree on the day they are written and drift afterwards.
func TestTheConfinementClaimIsWhatTheSandboxActuallyDoes(t *testing.T) {
	s := builtinTestServer(t)
	cfg := s.sandboxExecConfig()

	claimed := s.sandboxExecConfined()
	actual := mcp.ResolveMode(cfg) != mcp.SandboxNone

	if claimed != actual {
		t.Fatalf("the approval prompt would claim confined=%v while WrapCommand selects %q",
			claimed, mcp.ResolveMode(cfg))
	}
}

// With no backend available, the tool must report itself UNCONFINED -- which is
// what makes the client render its existing NOT-SANDBOXED banner instead of the
// reassurance.
func TestSandboxExecReportsUnconfinedWhenTheHostHasNoBackend(t *testing.T) {
	restore := stubNoSandboxBackend(t)
	defer restore()

	s := builtinTestServer(t)
	if s.sandboxExecConfined() {
		t.Fatal("claimed confinement on a host with neither bwrap nor docker")
	}

	spec := builtinSpec(t, s, "sandbox_exec")
	if spec.Confined {
		t.Fatal("the registered tool carries Confined=true, which is what reaches the " +
			"approving human as \"this tool ships with Mochiii\"")
	}
}

// The read-and-propose built-ins are confined by construction and must STAY
// asserted -- the fix must not turn every tool into a scary banner.
//
// THE EXEMPTION IS A PROPERTY, NOT A NAME. This used to skip the literal
// "sandbox_exec", which was right when that was the only exempt tool and
// silently wrong the moment web_search and web_fetch arrived: they are forced
// unconfined because a tool that talks to the internet is not confined on any
// host, and this loop would have demanded the opposite. It was never reached,
// because those two were also missing from allBuiltinNames -- two gaps that
// concealed each other. Asking the tool what it IS survives the next arrival.
//
// AND THEN IT DID NOT SURVIVE THE NEXT ARRIVAL, which is the correction here.
// The exemption asked about two properties, so query_compiler_definition and
// query_compiler_references -- which declare only ReadOnlyHint and start a
// language server through exec.Command -- fell into the "must be confined"
// branch, and this test DEMANDED the false stamp. The comment above was right
// about names versus properties and still enumerated the properties by hand,
// which is the same failure one level up. TestNoBuiltinSpawnsWithoutDeclaringIt
// exists because this list cannot be trusted to be finished.
func TestTheReadAndProposeBuiltinsAreStillConfined(t *testing.T) {
	restore := stubNoSandboxBackend(t)
	defer restore()

	s := builtinTestServer(t)
	for _, name := range allBuiltinNames {
		spec := builtinSpec(t, s, name)
		if spec.ExecutesCode || spec.ReachesNetwork || spec.LaunchesSubprocess {
			continue // exempt by construction; the tests below pin who qualifies
		}
		if !spec.Confined {
			t.Errorf("%s reports unconfined; it is this daemon's own code behind the same gates", name)
		}
	}
}

// Exactly the two tools that leave the machine, named. An exemption nobody
// enumerates is an exemption anything can join.
func TestOnlyTheWebToolsReachTheNetwork(t *testing.T) {
	s := builtinTestServer(t)

	var marked []string
	for _, name := range allBuiltinNames {
		if builtinSpec(t, s, name).ReachesNetwork {
			marked = append(marked, name)
		}
	}
	slices.Sort(marked)
	if strings.Join(marked, ",") != "web_fetch,web_search" {
		t.Fatalf("tools marked as reaching the network: %v, want exactly [web_fetch web_search]", marked)
	}
}

// Only the tool that executes code is exempt from the assertion. If a second one
// ever is, it should be because somebody wrote ExecutesCode on purpose.
func TestOnlySandboxExecIsMarkedAsExecutingCode(t *testing.T) {
	s := builtinTestServer(t)

	// Every built-in by name rather than through Advertised: the advertised
	// menu is filtered by config and policy, so a tool missing from it proves
	// nothing about how it was registered. Lookup is what resolveExecutable
	// itself uses.
	var marked []string
	for _, name := range allBuiltinNames {
		if builtinSpec(t, s, name).ExecutesCode {
			marked = append(marked, name)
		}
	}
	if strings.Join(marked, ",") != "sandbox_exec" {
		t.Fatalf("tools marked as executing code: %v, want exactly [sandbox_exec]", marked)
	}
}

// THE HONEST SENTENCE MUST REACH THE SCREEN.
//
// sandbox_exec's description is the one place the truth about it is written --
// that it is confined only when a backend is installed, and that approving a
// call approves whatever the project's build files do. ToolApprovalRequest
// carried no field for it, so the one place the truth existed was the one place
// the user never saw.
func TestTheApprovalPromptCarriesTheToolsOwnDescription(t *testing.T) {
	// DRIVEN THROUGH dispatchToolCall, not reconstructed. The first version of
	// this test built a ToolApprovalRequest itself and asserted its own Detail
	// field -- so it passed with the production line deleted, which is the exact
	// "the test asserts a copy of the rule" failure this codebase keeps finding.
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go build ./..."}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"sandbox_exec": "ask"}},
		Budget:  MCPBudgetConfig{MaxIterations: 4},
	})

	seen := &recordingApprover{answer: protocol.ApprovalDeny}
	if _, _, err := runLoopWith(t, s, seen); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(seen.requests) == 0 {
		t.Fatal("no approval was requested for sandbox_exec")
	}

	req := seen.requests[0]
	if req.Detail == "" {
		t.Fatal("the approval request the human sees carries no description, so the tool's own " +
			"honest sentence is unreachable at the moment of consent")
	}
	if !strings.Contains(req.Detail, "build files") {
		t.Errorf("the description reaching the user does not say that approving a call approves "+
			"whatever the project's build files do: %q", req.Detail)
	}
	// And that what the command writes in the workspace outlives the sandbox: a
	// planted .git/hooks or Makefile runs unsandboxed the next time the repo is
	// used. The confinement is real, but it does not reach forward in time, and
	// the person consenting is not told that unless the sentence says so.
	if !strings.Contains(req.Detail, "runs unsandboxed the next time") {
		t.Errorf("the description does not disclose that workspace writes run unsandboxed later: %q", req.Detail)
	}

	// THE SENTENCE MUST BE THE RIGHT ONE FOR THIS HOST, not merely present.
	//
	// This assertion used to look for the literal "full privileges", which was
	// the honest sentence back when the description was a fixed string written
	// for the unconfined case. It is now derived per host
	// (sandboxExecDescription), so a machine with a working bwrap correctly says
	// "Confined to this workspace on this host" instead -- and pinning the old
	// wording would have meant asserting the description is WRONG on any host
	// that can actually confine. What the test is really for is that the
	// sentence reaching the moment of consent agrees with what will happen, so
	// that is what it now checks.
	cfg := s.sandboxExecConfig()
	confinedClaim := strings.Contains(req.Detail, "Confined to this workspace")
	unconfinedClaim := strings.Contains(req.Detail, "full privileges")
	if !confinedClaim && !unconfinedClaim {
		t.Errorf("the description says nothing definite about confinement at the moment of "+
			"consent: %q", req.Detail)
	}
	if confinedClaim != mcp.Confines(cfg) {
		t.Errorf("the approval prompt claims confined=%v but this host will actually confine=%v: %q",
			confinedClaim, mcp.Confines(cfg), req.Detail)
	}
	if limited := strings.Contains(req.Detail, "Capped at"); limited != mcp.LimitsApply(cfg) {
		t.Errorf("the approval prompt claims resource limits=%v but LimitsApply=%v: %q",
			limited, mcp.LimitsApply(cfg), req.Detail)
	}
}

// A recording approver: what the human would actually have been shown.
type recordingApprover struct {
	requests []protocol.ToolApprovalRequest
	answer   string
}

func (r *recordingApprover) Ask(_ context.Context, req protocol.ToolApprovalRequest) approvalDecision {
	r.requests = append(r.requests, req)
	return approvalDecision{Decision: r.answer, Cause: "test"}
}

// --- helpers ---------------------------------------------------------------

// allBuiltinNames is Lane A's complete surface. Written out rather than
// enumerated from the registry because there is no accessor for it, and because
// a list that has to be edited when a tool is added is the point: a new built-in
// should have to answer "does this execute code?" once, deliberately.
var allBuiltinNames = []string{
	"read_file", "list_directory", "search_code", "repo_map",
	"query_compiler_definition", "query_compiler_references",
	"sandbox_exec", "propose_edit", "propose_ast_edit",
	"web_search", "web_fetch",
}

// AND THE LIST HAS TO BE COMPLETE, or the sentence above is not true.
//
// It said "a list that has to be edited when a tool is added is the point" --
// and nothing checked, so a tool could be added and never answer the question.
// Measured: repo_map was registered, advertised, and callable while this list
// still named eight tools, and every test here passed. A list that silently
// covers a subset is worse than no list, because it reads as an inventory.
func TestEveryBuiltinAnswersTheConfinementQuestion(t *testing.T) {
	s := builtinTestServer(t)
	// Built-ins are registered only when agent mode is configured, which is the
	// state this list describes.
	s.cfg.MCP.Enabled = true
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	advertised, _ := registry.Advertised(context.Background())
	var registered []string
	for _, tool := range advertised {
		if tool.Server == mcp.BuiltinServerName {
			registered = append(registered, tool.Name)
		}
	}
	if len(registered) == 0 {
		t.Fatal("no built-ins advertised; this test is comparing against nothing")
	}

	listed := map[string]bool{}
	for _, name := range allBuiltinNames {
		listed[name] = true
	}
	for _, name := range registered {
		if !listed[name] {
			t.Errorf("built-in %q is registered but missing from allBuiltinNames, so it has never "+
				"been asked whether it executes code -- add it there and answer that question", name)
		}
	}
	for _, name := range allBuiltinNames {
		if !slices.Contains(registered, name) {
			t.Errorf("allBuiltinNames names %q, which is not registered; the list is describing a "+
				"tool that no longer exists", name)
		}
	}
}

// builtinSpec returns the tool exactly as resolveExecutable would see it --
// through Lookup on the qualified name, not by reconstructing it, so the test
// reads what the approval prompt is actually built from.
func builtinSpec(t *testing.T, s *Server, name string) mcp.Tool {
	t.Helper()
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })
	spec, _, err := registry.Lookup(context.Background(), mcp.BuiltinServerName+"__"+name)
	if err != nil {
		t.Fatalf("looking up built-in %q: %v", name, err)
	}
	return spec
}

// stubNoSandboxBackend makes the host look like one with neither bwrap nor a
// usable docker image -- the configuration F-01's attack path assumes and the
// one this machine is NOT in, so it cannot be reached by observation.
func stubNoSandboxBackend(t *testing.T) func() {
	t.Helper()
	// EVERY backend sandbox_exec can reach. Docker is already out (no image,
	// see sandboxExecImage); bwrap and landlock are the two a host decides.
	// Before landlock existed, stubbing bwrap alone was the whole of "no
	// backend", and on any Landlock kernel it stopped being so.
	origBwrap, origLandlock := mcp.BwrapUsable, mcp.LandlockUsable
	mcp.BwrapUsable = func() bool { return false }
	mcp.LandlockUsable = func() bool { return false }
	return func() { mcp.BwrapUsable, mcp.LandlockUsable = origBwrap, origLandlock }
}

// ---------------------------------------------------------------------------
// S2 / F-02: one approval must not authorise every later command.
// ---------------------------------------------------------------------------

// THE ESCALATION. "Allow for this task" is keyed by tool name, which is exactly
// what a user choosing it means for read_file. For a tool that executes code it
// meant: approve `go test ./...` once, and `make <anything>` runs unseen for the
// rest of the turn -- up to max_iterations times, steerable by any content that
// reaches the model's context.
func TestApprovingOneCommandDoesNotApproveADifferentOne(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go build ./..."}`),
		toolCallSSE("c2", "builtin__sandbox_exec", `{"command":"make something-else"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"sandbox_exec": "ask"}},
		Budget:  MCPBudgetConfig{MaxIterations: 5},
	})

	appr := &recordingApprover{answer: protocol.ApprovalApproveForTurn}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	if len(appr.requests) < 2 {
		t.Fatalf("the user was asked %d time(s). Approving %q for the turn also authorised a "+
			"DIFFERENT command with no prompt", len(appr.requests), "go build ./...")
	}
	if !strings.Contains(appr.requests[1].Arguments, "make something-else") {
		t.Errorf("the second prompt was for %q, want the new command", appr.requests[1].Arguments)
	}
}

// THE LEGITIMATE WORKFLOW MUST SURVIVE. A fix-test loop re-runs the same command
// repeatedly, and re-prompting for it would make "allow for this task" useless
// for the one case it exists to serve.
func TestApprovingACommandForTheTurnCoversTheIdenticalCommandAgain(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c2", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"sandbox_exec": "ask"}},
		Budget:  MCPBudgetConfig{MaxIterations: 5},
	})

	appr := &recordingApprover{answer: protocol.ApprovalApproveForTurn}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(appr.requests) != 1 {
		t.Fatalf("the identical command re-prompted: %d request(s). \"Allow for this task\" has to "+
			"cover the fix-test loop or it does nothing", len(appr.requests))
	}
}

// An ordinary tool's grant must STAY tool-scoped. Narrowing read_file to the
// arguments the user happened to see first would make the option do nothing --
// which is the reasoning the original design recorded, and it is still right
// for everything that does not execute code.
func TestAnOrdinaryToolsGrantStillCoversDifferentArguments(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__list_directory", `{"path":"."}`),
		toolCallSSE("c2", "builtin__list_directory", `{"path":"sub"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": "ask"}},
		Budget:  MCPBudgetConfig{MaxIterations: 5},
	})
	if err := os.MkdirAll(filepath.Join(s.workspace, "sub"), 0o700); err != nil {
		t.Fatalf("creating fixture: %v", err)
	}

	appr := &recordingApprover{answer: protocol.ApprovalApproveForTurn}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(appr.requests) != 1 {
		t.Fatalf("read-only tool re-prompted for different arguments: %d request(s)", len(appr.requests))
	}
}

// The grant key is written and read in ONE place. Spelled out twice, a mismatch
// is either a prompt that never stops asking or one that stops asking for
// something the user never saw.
func TestTheGrantKeyBindsExecToolsToTheirArguments(t *testing.T) {
	execTool := mcp.Tool{Name: "sandbox_exec", Server: "builtin", ExecutesCode: true}
	plain := mcp.Tool{Name: "read_file", Server: "builtin"}

	if a, b := grantKey(execTool, "builtin__sandbox_exec", `{"command":"go test"}`),
		grantKey(execTool, "builtin__sandbox_exec", `{"command":"make evil"}`); a == b {
		t.Error("two different commands share one grant key")
	}
	if a, b := grantKey(execTool, "builtin__sandbox_exec", `{"command":"go test"}`),
		grantKey(execTool, "builtin__sandbox_exec", `{"command":"go test"}`); a != b {
		t.Error("the identical command produced two grant keys, so it would re-prompt forever")
	}
	if a, b := grantKey(plain, "builtin__read_file", `{"path":"a"}`),
		grantKey(plain, "builtin__read_file", `{"path":"b"}`); a != b {
		t.Error("an ordinary tool's grant was narrowed to its arguments")
	}
}
