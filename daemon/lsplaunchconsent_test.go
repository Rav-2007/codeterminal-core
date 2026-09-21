package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// REGISTER ITEM 32: the launch is the act that carries the risk, so the launch
// is what a human approves.
//
// A language server is a third-party program that reads project-supplied
// configuration -- a tsconfig.json can load plugins -- and the bridge keeps it
// for the daemon's lifetime. Before this, the only consent anywhere was to a
// tool CALL: the first approved lookup started gopls without the user being
// asked about gopls, and every later prompt said "STARTS ANOTHER PROGRAM" about
// a server already running. The prompt that mattered was indistinguishable from
// the ones that did not.
//
// These tests hold the properties that replace that, and several of them judge
// "did it start?" from the FAR SIDE OF EXEC: the fake server writes env.dump the
// moment it runs (testdata/fakelsp/main.go), so "nothing started" is observed
// rather than inferred from an error value.

// fakeServerStarted reports whether the fake language server process ever ran.
func fakeServerStarted(home string) bool {
	_, err := os.Stat(filepath.Join(home, "env.dump"))
	return err == nil
}

// --- the bridge ------------------------------------------------------------

// THE GATE. Neuter check: delete the launchApproved test in GetServer and the
// fake process starts, env.dump appears, and this fails.
func TestNoLanguageServerStartsWithoutAnApprovedLaunch(t *testing.T) {
	b, home := newFakeBridge(t, "ok")

	_, err := b.GetServer(context.Background(), "go")
	if !errors.Is(err, errLaunchNotApproved) {
		t.Fatalf("GetServer with no approval returned %v, want errLaunchNotApproved", err)
	}
	if fakeServerStarted(home) {
		t.Fatal("the language server process STARTED with no approval")
	}
	if b.Running("go") {
		t.Error("Running reports a server that was never approved to start")
	}
}

// An approval names ONE launch. A key that matched any language would let the
// first "yes" start every server the workspace could name.
func TestAnApprovalForOneLanguageStartsNoOther(t *testing.T) {
	b, home := newFakeBridge(t, "ok")

	_, err := b.GetServer(withApprovedLaunch(context.Background(), "python"), "go")
	if !errors.Is(err, errLaunchNotApproved) {
		t.Fatalf("an approval for python let go start: %v", err)
	}
	if fakeServerStarted(home) {
		t.Fatal("gopls started on the strength of an approval for pyright")
	}
}

// A server that is running needs no second approval: handing it out launches
// nothing, and asking again would be asking about an event that already
// happened -- the original defect from the other side.
func TestARunningServerNeedsNoFurtherApproval(t *testing.T) {
	b, _ := newFakeBridge(t, "ok")

	first, err := b.GetServer(approvedLaunch("go"), "go")
	if err != nil {
		t.Fatalf("an approved launch failed: %v", err)
	}
	second, err := b.GetServer(context.Background(), "go")
	if err != nil {
		t.Fatalf("the running server was refused without an approval: %v", err)
	}
	if first != second {
		t.Error("a second server was started instead of the running one being reused")
	}
}

// --- the probe -------------------------------------------------------------

// The probe is what the user is TOLD. It must say "starts gopls" exactly when
// the call would, "asks gopls" once it runs, start nothing itself, and resolve
// the path the way the handler does -- so the program named is the program
// reached.
func TestTheLaunchProbeTellsTheTruthBeforeAndAfter(t *testing.T) {
	b, home := newFakeBridge(t, "ok")
	real, err := filepath.EvalSymlinks(b.workspace)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: discardLogger(), workspace: real, lspBridge: b}
	astFixture(t, s.workspace)

	before := s.lspLaunchPlan(lspArgs(t, "main.go", 0, 0))
	if !before.Needed || before.Program != "gopls" || before.Key != "go" {
		t.Fatalf("before any server: plan = %+v, want Needed gopls/go", before)
	}
	if fakeServerStarted(home) {
		t.Fatal("PROBING started the server: the probe runs before consent and must have no side effects")
	}

	if _, err := b.GetServer(approvedLaunch("go"), "go"); err != nil {
		t.Fatal(err)
	}
	after := s.lspLaunchPlan(lspArgs(t, "main.go", 0, 0))
	if after.Needed || after.Program != "gopls" {
		t.Fatalf("with gopls running: plan = %+v, want not Needed, still naming gopls", after)
	}

	// Everything the handler would refuse starts nothing, and names nothing.
	for name, raw := range map[string][]byte{
		"an unsupported extension": lspArgs(t, "lib.rs", 0, 0),
		"a path out of bounds":     lspArgs(t, "../outside.go", 0, 0),
		"malformed arguments":      []byte(`{"path":`),
		"no path at all":           []byte(`{}`),
	} {
		if plan := s.lspLaunchPlan(raw); plan != (mcp.LaunchPlan{}) {
			t.Errorf("%s: plan = %+v, want the zero plan", name, plan)
		}
	}
}

// --- the loop --------------------------------------------------------------

// observingApprover answers from a script and records, at the moment of each
// question, whether the language server had already started. That is the
// property "pgrep gopls is empty until the user says yes", checked from the far
// side of exec.
type observingApprover struct {
	scriptedApprover
	home           string
	startedWhenAsk []bool
}

func (a *observingApprover) Ask(ctx context.Context, req protocol.ToolApprovalRequest) approvalDecision {
	a.startedWhenAsk = append(a.startedWhenAsk, fakeServerStarted(a.home))
	return a.scriptedApprover.Ask(ctx, req)
}

// launchLoopServer is a loop server backed by the fake language server, with a
// Go file to ask about and an audit sink to read back.
func launchLoopServer(t *testing.T, base string, tools map[string]string) (*Server, string, string) {
	t.Helper()
	b, home := newFakeBridge(t, "ok")
	s, audit := launchLoopServerOn(t, base, tools, b)
	return s, home, audit
}

// launchLoopServerOn is launchLoopServer on a bridge the caller already built --
// for a test whose upstream must reach the bridge, which then has to exist
// BEFORE the upstream's goroutine starts, or the race detector (rightly) cannot
// see the handoff.
func launchLoopServerOn(t *testing.T, base string, tools map[string]string, b *LSPBridge) (*Server, string) {
	t.Helper()
	s := loopServer(t, base, MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: tools}})
	s.lspBridge = b
	auditPath := filepath.Join(t.TempDir(), "toolcalls.jsonl")
	s.toolAudit = newToolAuditSink(auditPath)
	writeWorkspaceFile(t, s.workspace, "main.go", "package main\n\nfunc main() {}\n")
	return s, auditPath
}

const defQualified = "builtin__query_compiler_definition"

var defArgs = `{"path":"main.go","line":2,"character":5}`

// THE DEFECT, AS THE USER SAW IT, now correct. Two lookups under the default
// "ask": the first prompt says it STARTS gopls and nothing has started when it
// is asked; the second says gopls is already running.
//
// Neuter check: send LaunchesSubprocess: spec.LaunchesSubprocess (the static
// flag, as it was) and the second prompt claims a launch that is not happening.
func TestTheFirstPromptStartsGoplsAndTheSecondSaysItIsRunning(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", defQualified, defArgs),
		toolCallSSE("c2", defQualified, defArgs),
		textSSE("done"),
	)
	s, home, auditPath := launchLoopServer(t, base, nil)
	appr := &observingApprover{home: home, scriptedApprover: scriptedApprover{answers: []approvalDecision{
		says(protocol.ApprovalApprove), says(protocol.ApprovalApprove),
	}}}

	res, _, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(appr.asked) != 2 {
		t.Fatalf("got %d prompts, want 2 (the policy is ask, per call): %+v", len(appr.asked), appr.asked)
	}
	first, second := appr.asked[0], appr.asked[1]

	if !first.LaunchesSubprocess || first.Program != "gopls" {
		t.Errorf("first prompt: launches=%t program=%q, want a launch of gopls", first.LaunchesSubprocess, first.Program)
	}
	if appr.startedWhenAsk[0] {
		t.Error("gopls was already running when the user was asked whether to start it")
	}
	if second.LaunchesSubprocess {
		t.Error("the second prompt says it STARTS gopls, which is already running -- item 32's false sentence")
	}
	if second.Program != "gopls" {
		t.Errorf("second prompt program = %q, want it to name the running server", second.Program)
	}
	if len(res.ToolNames) != 2 {
		t.Errorf("both approved lookups should have run, got %v", res.ToolNames)
	}

	// The audit names the launch on the call that caused it, and only there.
	events := readAudit(t, auditPath)
	if len(events) != 2 {
		t.Fatalf("got %d audit records, want 2", len(events))
	}
	if events[0].Launch != "gopls" || events[1].Launch != "" {
		t.Errorf("audit launch fields = %q, %q; want \"gopls\" then empty", events[0].Launch, events[1].Launch)
	}
}

// A TURN GRANT NEVER COVERS A LAUNCH. The user approved the tool for the turn;
// the server then exits; the next call would start it again. That restart is a
// new launch, and a grant is an answer to a different question.
//
// Neuter check: consult turn.grants even when a launch is Needed. The second
// call then gets no prompt -- and, because the bridge fails closed, no server
// either: the test fails on the prompt count before anything unapproved could
// have run. Both layers would have to break for a silent relaunch.
func TestATurnGrantNeverCoversARelaunch(t *testing.T) {
	responses := [][]string{
		toolCallSSE("c1", defQualified, defArgs),
		toolCallSSE("c2", defQualified, defArgs),
		textSSE("done"),
	}
	b, home := newFakeBridge(t, "ok")
	var n atomic.Int64
	base := rawSSEServerFunc(t, func([]byte) []string {
		i := int(n.Add(1)) - 1
		if i == 1 {
			// Between the two tool calls: the server exits, and the evidence of
			// the first launch is cleared so a second one is observable.
			b.mu.Lock()
			srv := b.servers["go"]
			b.mu.Unlock()
			if srv != nil {
				srv.Close()
			}
			_ = os.Remove(filepath.Join(home, "env.dump"))
		}
		if i >= len(responses) {
			return textSSE("done")
		}
		return responses[i]
	})
	s, _ := launchLoopServerOn(t, base, nil, b)
	appr := &observingApprover{home: home, scriptedApprover: scriptedApprover{answers: []approvalDecision{
		says(protocol.ApprovalApproveForTurn), says(protocol.ApprovalApprove),
	}}}

	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(appr.asked) != 2 {
		t.Fatalf("got %d prompts, want 2: the grant from the first must not cover restarting gopls", len(appr.asked))
	}
	if !appr.asked[1].LaunchesSubprocess {
		t.Error("the restart was asked about, but not as a launch")
	}
	if appr.startedWhenAsk[1] {
		t.Error("gopls had already restarted before the user was asked about restarting it")
	}
	if !fakeServerStarted(home) {
		t.Error("the approved restart did not actually start gopls")
	}
}

// Declining the launch starts nothing, and the model is told that in words that
// stop it asking again for the same thing.
func TestDecliningTheLaunchStartsNothingAndTheModelIsTold(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", defQualified, defArgs),
		textSSE("Understood."),
	)
	s, home, _ := launchLoopServer(t, base, nil)
	appr := &scriptedApprover{answers: []approvalDecision{says(protocol.ApprovalDeny)}}

	res, _, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if fakeServerStarted(home) {
		t.Fatal("gopls started although the user declined to start it")
	}
	if len(res.ToolNames) != 0 {
		t.Errorf("a declined launch still ran the tool: %v", res.ToolNames)
	}
	if len(*bodies) < 2 {
		t.Fatal("the loop stopped instead of telling the model")
	}
	if second := string((*bodies)[1]); !strings.Contains(second, "declined to start gopls") {
		t.Errorf("the model was not told the user declined to start gopls: %s", second)
	}
}

// "allow" SKIPS THE PER-CALL PROMPT AND NEVER THE LAUNCH. A user who allowed
// lookups did not agree to start a third-party program that reads their
// project's configuration; the first call asks about exactly that, and the
// second runs unprompted.
//
// Neuter check: return early on PolicyAllow before the probe, as the code did.
// No prompt is asked, the bridge refuses the unapproved launch, and both the
// prompt count and the tool count fail.
func TestAllowStillAsksBeforeStartingTheServer(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", defQualified, defArgs),
		toolCallSSE("c2", defQualified, defArgs),
		textSSE("done"),
	)
	s, home, _ := launchLoopServer(t, base, map[string]string{"query_compiler_definition": PolicyAllow})
	appr := &scriptedApprover{answers: []approvalDecision{says(protocol.ApprovalApprove)}}

	res, _, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(appr.asked) != 1 {
		t.Fatalf("got %d prompts under allow, want exactly 1 -- the launch", len(appr.asked))
	}
	if !appr.asked[0].LaunchesSubprocess || appr.asked[0].Program != "gopls" {
		t.Errorf("the one prompt under allow was not about starting gopls: %+v", appr.asked[0])
	}
	if len(res.ToolNames) != 2 || !fakeServerStarted(home) {
		t.Errorf("tools run = %v, started = %t; want both lookups run on the approved server",
			res.ToolNames, fakeServerStarted(home))
	}
}

// With nobody to ask, "allow" still starts nothing: a launch needs an answer,
// and no channel is not a yes.
func TestAllowWithNobodyToAskStartsNothing(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", defQualified, defArgs),
		textSSE("done"),
	)
	s, home, _ := launchLoopServer(t, base, map[string]string{"query_compiler_definition": PolicyAllow})

	if _, _, err := runLoopWith(t, s, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if fakeServerStarted(home) {
		t.Fatal("gopls started under allow with no approval channel at all")
	}
}
