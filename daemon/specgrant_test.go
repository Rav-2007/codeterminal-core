package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// specTurn drives one agent turn with ctx (which may carry the spec grants a
// client sent) and returns what the approver was asked and what the model was
// sent back after its first tool call.
func specTurn(t *testing.T, ctx context.Context, mcpCfg MCPConfig, answer string, calls ...string) (*recordingApprover, string) {
	t.Helper()
	var responses [][]string
	for i, args := range calls {
		responses = append(responses, toolCallSSE("c"+string(rune('1'+i)), "builtin__sandbox_exec", args))
	}
	responses = append(responses, textSSE("done"))
	base, _, bodies := agentUpstream(t, responses...)
	s := loopServer(t, base, mcpCfg)
	// THE SINK A REAL TURN HAS (newTurnSink): one that knows which project to
	// copy. An empty one means "this turn has no working copy", so its commands
	// would run in the project itself -- and a command that does is never
	// offered a spec grant (mcp.Builtin.InPlace). The helper used an empty sink
	// until 2026-10-07, while specGrantFor decided from the configuration alone,
	// so these tests were being offered grants for commands that ran in place.
	sink := &proposalSink{stageFrom: s.workingCopySource("auto")}
	t.Cleanup(sink.discard)
	registry, _ := s.buildRegistry(context.Background(), s.logger, sink, "")
	t.Cleanup(func() { _ = registry.Close() })
	appr := &recordingApprover{answer: answer}
	if _, err := s.runAgentLoop(ctx, time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "go"}}, providerRouting{}, appr,
		func(string) error { return nil }, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	afterFirst := ""
	if len(*bodies) > 1 {
		afterFirst = string((*bodies)[1])
	}
	return appr, afterFirst
}

func askConfig() MCPConfig {
	return MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"sandbox_exec": "ask"}},
		Budget:  MCPBudgetConfig{MaxIterations: 5},
	}
}

const goVersion = `{"command":"go version"}`

// grantFromATurn answers approve_for_spec in a spec turn and returns the grant
// the client would now hold.
func grantFromATurn(t *testing.T) string {
	t.Helper()
	appr, _ := specTurn(t, withSpecGrants(context.Background(), nil), askConfig(), protocol.ApprovalApproveForSpec, goVersion)
	requireConfinedCommands(t, appr)
	if len(appr.requests) != 1 || appr.requests[0].SpecGrant == "" {
		t.Fatalf("a sandboxed command in a spec turn was not offered a spec grant: %+v", appr.requests)
	}
	return appr.requests[0].SpecGrant
}

// requireConfinedCommands skips a test that needs this host to sandbox
// commands -- a spec grant is offered only for a command that is -- unless
// MOCHIII_REQUIRE_SANDBOX says the host must, as the egress tests do. FOUND
// 2026-10-01: on Windows, which has no sandbox, these tests failed rather than
// saying they could not run there.
func requireConfinedCommands(t *testing.T, appr *recordingApprover) {
	t.Helper()
	if len(appr.requests) == 0 || appr.requests[0].Confined {
		return
	}
	if os.Getenv("MOCHIII_REQUIRE_SANDBOX") != "" {
		t.Fatal("MOCHIII_REQUIRE_SANDBOX is set, but this host does not confine sandbox_exec, so spec grants cannot be exercised")
	}
	t.Skip("NOT RUN: this host does not confine sandbox_exec, and a spec grant is only ever offered for a confined command")
}

// THE WORKFLOW: approve `go version` while the spec is active, and a later
// turn -- a new connection, a new agentTurn -- runs the same command, spelled
// differently, without asking.
func TestASpecGrantCoversTheSameCommandInALaterTurn(t *testing.T) {
	grant := grantFromATurn(t)
	ctx := withSpecGrants(context.Background(), []string{grant})
	appr, sent := specTurn(t, ctx, askConfig(), protocol.ApprovalDeny, `{ "command" : "go version" }`)
	if len(appr.requests) != 0 {
		t.Fatalf("the granted command asked again in a later turn: %+v", appr.requests)
	}
	if !strings.Contains(sent, "go1.") {
		t.Errorf("the granted command did not run; the model was sent:\n%.600s", sent)
	}
}

// And within the turn it was given in, the same command does not ask twice.
func TestASpecGrantCoversTheSameCommandLaterInItsOwnTurn(t *testing.T) {
	appr, _ := specTurn(t, withSpecGrants(context.Background(), nil), askConfig(),
		protocol.ApprovalApproveForSpec, goVersion, `{"command": "go version"}`)
	requireConfinedCommands(t, appr)
	if len(appr.requests) != 1 {
		t.Fatalf("asked %d times for one command approved while the spec is active", len(appr.requests))
	}
}

// F-02 MUST STAY CLOSED: a grant names one command, never the tool.
func TestASpecGrantNeverCoversADifferentCommand(t *testing.T) {
	grant := grantFromATurn(t)
	ctx := withSpecGrants(context.Background(), []string{grant})
	appr, _ := specTurn(t, ctx, askConfig(), protocol.ApprovalDeny, `{"command":"go env GOPATH"}`)
	if len(appr.requests) != 1 || !strings.Contains(appr.requests[0].Arguments, "go env") {
		t.Fatalf("a different command ran under another command's spec grant: %+v", appr.requests)
	}
}

// No active spec, no spec grant: not offered, and one sent anyway is not honoured.
func TestASpecGrantNeedsAnActiveSpec(t *testing.T) {
	grant := grantFromATurn(t)
	appr, _ := specTurn(t, context.Background(), askConfig(), protocol.ApprovalDeny, goVersion)
	if len(appr.requests) != 1 {
		t.Fatalf("a spec grant was honoured in a turn with no spec (asked %d times)", len(appr.requests))
	}
	if appr.requests[0].SpecGrant != "" {
		t.Error("a spec grant was offered in a turn with no spec")
	}
	_ = grant
}

// Where the command would run unsandboxed, it is neither offered nor honoured.
func TestASpecGrantNeedsARealSandbox(t *testing.T) {
	grant := grantFromATurn(t)
	defer stubNoSandboxBackend(t)()
	ctx := withSpecGrants(context.Background(), []string{grant})
	appr, _ := specTurn(t, ctx, askConfig(), protocol.ApprovalDeny, goVersion)
	if len(appr.requests) != 1 {
		t.Fatalf("a spec grant covered a command that runs with the user's full privileges")
	}
	if appr.requests[0].SpecGrant != "" {
		t.Error("a spec grant was offered for an unsandboxed command")
	}
}

// Where commands run in the real project (no working copy), likewise.
func TestASpecGrantNeedsTheWorkingCopy(t *testing.T) {
	grant := grantFromATurn(t)
	cfg := askConfig()
	cfg.NoWorkingCopy = true
	ctx := withSpecGrants(context.Background(), []string{grant})
	appr, _ := specTurn(t, ctx, cfg, protocol.ApprovalDeny, goVersion)
	if len(appr.requests) != 1 || appr.requests[0].SpecGrant != "" {
		t.Fatalf("a spec grant was offered or honoured where commands run in the real project: %+v", appr.requests)
	}
}

// A deny in config is the user's own word, and no grant outranks it.
func TestDenyInConfigBeatsASpecGrant(t *testing.T) {
	grant := grantFromATurn(t)
	cfg := askConfig()
	cfg.Builtin.Tools["sandbox_exec"] = "deny"
	ctx := withSpecGrants(context.Background(), []string{grant})
	_, sent := specTurn(t, ctx, cfg, protocol.ApprovalDeny, goVersion)
	if strings.Contains(sent, "go1.") {
		t.Fatal("a command denied in config ran under a spec grant")
	}
}

// An approve_for_spec answer to a question that did not offer it is not
// consent to anything.
func TestAnApproveForSpecAnswerToAnUnofferedQuestionIsRefused(t *testing.T) {
	req := protocol.ToolApprovalRequest{CallID: "c1", ArgumentsSHA256: "ab"}
	raw, _ := json.Marshal(protocol.ToolApprovalResponse{Approval: true, CallID: "c1", ArgumentsSHA256: "ab",
		Decision: protocol.ApprovalApproveForSpec})
	if decision, _ := verifyApproval(req, raw); decision != protocol.ApprovalDeny {
		t.Fatalf("an unoffered approve_for_spec was read as %q", decision)
	}
	req.SpecGrant = strings.Repeat("a", 64)
	if decision, _ := verifyApproval(req, raw); decision != protocol.ApprovalApproveForSpec {
		t.Fatalf("an offered approve_for_spec was read as %q", decision)
	}
}

// The digest names the command, not its spelling -- and never another tool's
// command, or a different one.
func TestTheSpecGrantDigestNamesOneCommand(t *testing.T) {
	a, _ := specGrantDigest("builtin__sandbox_exec", `{"command":"go test ./..."}`)
	b, _ := specGrantDigest("builtin__sandbox_exec", "{ \"command\" :\n \"go test ./...\" }")
	if a == "" || a != b {
		t.Errorf("the same command spelled differently got different grants: %q %q", a, b)
	}
	if c, _ := specGrantDigest("builtin__sandbox_exec", `{"command":"go test ./x"}`); c == a {
		t.Error("two different commands share a grant")
	}
	if d, _ := specGrantDigest("other__exec", `{"command":"go test ./..."}`); d == a {
		t.Error("two different tools share a grant")
	}
	if e, _ := specGrantDigest("builtin__sandbox_exec", `{"command":"go test ./...","timeout":5}`); e == a {
		t.Error("an extra argument did not change the grant")
	}
	for _, bad := range []string{`not json`, `["go"]`, `{"command":"x"} {"command":"y"}`} {
		if _, ok := specGrantDigest("builtin__sandbox_exec", bad); ok {
			t.Errorf("arguments %q were given a grant", bad)
		}
	}
}

// What a client sends is filtered: only well-formed digests, and not too many.
func TestSpecGrantsFromTheClientAreFilteredAndBounded(t *testing.T) {
	var sent []string
	for i := 0; i < maxSpecGrants+10; i++ {
		d, _ := specGrantDigest("builtin__sandbox_exec", `{"command":"go `+strings.Repeat("x", i+1)+`"}`)
		sent = append(sent, d)
	}
	sent = append([]string{"not-a-digest", strings.Repeat("z", 64)}, sent...)
	set := specGrantsFrom(withSpecGrants(context.Background(), sent))
	if len(set.digests) != maxSpecGrants {
		t.Errorf("kept %d grants, want at most %d", len(set.digests), maxSpecGrants)
	}
	if set.has("not-a-digest") || set.has(strings.Repeat("z", 64)) {
		t.Error("a malformed grant was kept")
	}
}

// A turn grant dies with its turn. It always did -- each turn builds a fresh
// agentTurn -- but nothing tested it, and the spec grant is exactly the kind of
// change that could have broken it.
func TestATurnGrantDoesNotSurviveIntoTheNextTurn(t *testing.T) {
	first, _ := specTurn(t, context.Background(), askConfig(), protocol.ApprovalApproveForTurn, goVersion, goVersion)
	if len(first.requests) != 1 {
		t.Fatalf("setup: approve_for_turn did not cover the identical command (%d asks)", len(first.requests))
	}
	second, _ := specTurn(t, context.Background(), askConfig(), protocol.ApprovalDeny, goVersion)
	if len(second.requests) != 1 {
		t.Fatal("a turn grant carried into the next turn")
	}
}
