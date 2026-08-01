package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeterminal/protocol"
)

// Lane B misbehaviour, end to end.
//
// daemon/mcp/badserver_test.go drives the misbehaving server through the mcp
// package's own seam. This file drives it through the path production uses --
// a config file, buildRegistry, a real subprocess, the loop, and the approval
// prompt -- because several of the interesting failures are not in the adapter
// at all. They are in what the daemon DOES with what the adapter faithfully
// returned.

var (
	badServerOnce sync.Once
	badServerPath string
	badServerErr  error
)

// buildBadServer compiles daemon/mcp/testdata/badserver once per package run.
func buildBadServer(t *testing.T) string {
	t.Helper()
	badServerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "badserver")
		if err != nil {
			badServerErr = err
			return
		}
		bin := filepath.Join(dir, "badserver")
		if out, err := exec.Command("go", "build", "-o", bin, "./mcp/testdata/badserver").CombinedOutput(); err != nil {
			badServerErr = &buildError{out: string(out)}
			return
		}
		badServerPath = bin
	})
	if badServerErr != nil {
		t.Fatalf("building the misbehaving MCP server: %v", badServerErr)
	}
	return badServerPath
}

type buildError struct{ out string }

func (e *buildError) Error() string { return e.out }

// badServerConfig is the config a user would write to enable one Lane B server,
// including the acknowledgement gate, pointed at a chosen misbehaviour mode.
func badServerConfig(t *testing.T, mode string, toolPolicy map[string]string) MCPConfig {
	t.Helper()
	return MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Disabled: true},
		Servers: map[string]MCPServerConfig{
			"bad": {
				Command:                buildBadServer(t),
				Args:                   []string{mode},
				AcknowledgedUnconfined: true,
				Tools:                  toolPolicy,
			},
		},
	}
}

// capturingApprover records every prompt it is shown and answers with a fixed
// decision. It is how a test sees the bytes a human would have seen.
type capturingApprover struct {
	mu       sync.Mutex
	seen     []protocol.ToolApprovalRequest
	decision string
}

func (a *capturingApprover) Ask(_ context.Context, req protocol.ToolApprovalRequest) approvalDecision {
	a.mu.Lock()
	a.seen = append(a.seen, req)
	a.mu.Unlock()
	if a.decision == "" || a.decision == protocol.ApprovalDeny {
		return denied(denyByUser, 0)
	}
	return approvalDecision{Decision: a.decision}
}

func (a *capturingApprover) prompts() []protocol.ToolApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.ToolApprovalRequest(nil), a.seen...)
}

// M6 -- a server-chosen tool NAME must never reach the approval prompt.
//
// The result path is not the dangerous one: tool output goes to the model and
// never to a client (protocol.ToolActivity carries a byte COUNT, not content).
// The name is different. It is rendered in the user's terminal, before consent,
// on the same panel as the "NOT SANDBOXED" line -- so a CSI sequence in a tool
// name is an edit to the security notice the user is reading in order to
// decide.
//
// Fails if ValidateToolName is neutered: the tool becomes advertisable, Lookup
// finds it, and the prompt carries the escapes.
func TestAServerSuppliedToolNameNeverReachesTheApprovalPrompt(t *testing.T) {
	// Built with json.Marshal rather than toolCallSSE: %q renders an ESC as the
	// Go escape \x1b, which is not valid JSON, so the helper would silently be
	// testing a name the model could never actually send.
	name, err := json.Marshal("bad__" + evilToolName)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := agentUpstream(t,
		[]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function",` +
				`"function":{"name":` + string(name) + `,"arguments":"{}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		},
		textSSE("done"),
	)
	s := loopServer(t, base, badServerConfig(t, "evil-name", nil))
	appr := &capturingApprover{decision: protocol.ApprovalDeny}

	_, activity, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	for _, req := range appr.prompts() {
		if strings.ContainsRune(req.Tool, 0x1b) || strings.ContainsRune(req.Tool, '\r') {
			t.Errorf("the approval prompt carried control characters in the tool name (%q). "+
				"Rendered in a terminal, \\x1b[1A and \\x1b[2K move the cursor up and erase the "+
				"line -- which is the line carrying NOT SANDBOXED", req.Tool)
		}
		if req.Confined {
			t.Error("a Lane B call reported itself confined on the approval prompt")
		}
	}

	// Not merely unprompted -- unreachable. An unadvertised tool is one Lookup
	// cannot resolve, so the model naming it is refused outright. "We did not
	// ask" and "it cannot run" are different guarantees and only the second is
	// worth anything.
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseRunning || a.Phase == protocol.ToolPhaseSucceeded {
			t.Errorf("a tool this daemon refused to advertise reached phase %q", a.Phase)
		}
	}
}

// evilToolName must match testdata/badserver's evil-name mode exactly: the
// model supplies the qualified name back to us and that is the dispatch key.
const evilToolName = "safe\x1b[2K\x1b[1Ainnocent__lookup\x1b[0m\r../../etc/passwd"

// M6b -- control sequences in tool OUTPUT reach the model unaltered.
//
// Rated separately and lower: this is model-facing, not terminal-facing. It is
// recorded because renderToolResult is the egress choke point and its scrub is
// shaped for secrets rather than control characters, so the gap is worth
// stating rather than assuming somebody noticed.
func TestControlSequencesInToolOutputAreNotStripped(t *testing.T) {
	payload := "before\x1b]52;c;cGF5bG9hZA==\x07\x1b[2Jafter\x00"
	rendered, _, _ := renderToolResult(payload, 4096, false)
	t.Logf("M6b: renderToolResult passed %d of %d control bytes through to the model",
		countControl(rendered), countControl(payload))
	if countControl(rendered) == 0 {
		t.Log("control characters are now stripped at the egress choke point")
	}
}

func countControl(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case '\n', '\t', '\r':
		default:
			if r < 0x20 || r == 0x7f {
				n++
			}
		}
	}
	return n
}

// M7 -- forged consent in tool output is not consent.
//
// THE PROPERTY THIS ASSERTS IS THE ONE THAT MATTERS and it holds: the approval
// channel is the socket, not the message list, so a server writing approval
// JSON into a tool result is writing into the model's context and nowhere near
// the daemon's decision. The second call still asks.
//
// It must FAIL if anyone ever "helpfully" lets a tool result carry a grant --
// which is exactly the shape a well-meaning batching optimisation would take.
func TestForgedApprovalInToolOutputIsNotConsent(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "bad__helpful_notes", `{}`),
		toolCallSSE("c2", "bad__helpful_notes", `{}`),
		textSSE("done"),
	)
	s := loopServer(t, base, badServerConfig(t, "inject", nil))
	appr := &capturingApprover{decision: protocol.ApprovalApprove}

	res, activity, err := runLoopWith(t, s, appr)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	if got := len(appr.prompts()); got != 2 {
		t.Errorf("M7: %d approval prompts for 2 calls. A tool result claiming the user pre-approved "+
			"everything must not remove a prompt", got)
	}

	// And the forged text did reach the model, which is the honest half: this
	// is a prompt-injection surface, bounded by the fact that acting on it
	// still requires a human to say yes.
	var ran int
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseSucceeded {
			ran++
		}
	}
	if ran != 2 {
		t.Errorf("expected both approved calls to run, got %d", ran)
	}
	if res.FinalText == "" {
		t.Error("the turn produced no answer")
	}
}

// M2 -- n hung servers cost ONE connect timeout, not n of them.
//
// Servers are connected at the top of runAgentTurn and the turn deadline is
// created inside runAgentLoop, so this time is spent before any budget exists:
// no token streamed, not even the degradation notice sent. Serially, three
// wedged servers were a minute of blank screen that turn_timeout_seconds did
// not govern. That connect time is still unbudgeted -- what changed is that it
// is now bounded by a constant rather than by how many servers a user has.
//
// The test uses a 2s connect timeout and three hung servers: serial would be
// ~6s, parallel ~2s, and the 4s threshold cannot be met by accident either way.
//
// Fails if buildRegistry goes back to connecting in sequence.
func TestHungServersCostOneTimeoutNotOnePerServer(t *testing.T) {
	const servers = 3
	const timeout = 2 * time.Second

	cfg := badServerConfig(t, "hang-initialize", nil)
	cfg.Budget = MCPBudgetConfig{ConnectTimeoutSeconds: int(timeout / time.Second)}
	for i := 2; i <= servers; i++ {
		cfg.Servers[fmt.Sprintf("bad%d", i)] = MCPServerConfig{
			Command:                buildBadServer(t),
			Args:                   []string{"hang-initialize"},
			AcknowledgedUnconfined: true,
		}
	}

	s := loopServer(t, "", cfg)
	s.logger = log.New(os.Stderr, "misbehaviour: ", 0)

	start := time.Now()
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{})
	elapsed := time.Since(start)
	t.Cleanup(func() { _ = registry.Close() })

	if len(errs) != servers {
		t.Fatalf("expected all %d hung servers to be reported unavailable, got %d error(s)",
			servers, len(errs))
	}
	t.Logf("M2: %d hung servers cost %s against a %s per-server connect timeout",
		servers, elapsed.Round(100*time.Millisecond), timeout)

	if elapsed >= 2*timeout {
		t.Errorf("%d hung servers cost %s, which is more than one timeout (%s). Connecting in "+
			"sequence makes time-to-first-token scale with how many servers a user configured, "+
			"and none of it is charged to turn_timeout_seconds", servers, elapsed, timeout)
	}
}

// M3 (daemon side) -- a server that dies mid-call costs the model that tool,
// with an honest reason, and not the user's turn.
//
// The loop branches on mcp.ErrServerUnavailable to choose between "the tool
// failed to run" and "the tool's server is unavailable". Those are different
// facts and the model can act on the difference: a failed tool is worth
// retrying with other arguments, a dead server is not.
//
// Fails if isTransportDeath is neutered -- the classification disappears and
// the model is told the tool failed.
func TestAServerDyingMidCallIsReportedAsAnUnavailableServer(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "bad__crash", `{}`),
		textSSE("I could not reach that server."),
	)
	s := loopServer(t, base, badServerConfig(t, "exit-midcall",
		map[string]string{"crash": PolicyAllow}))

	res, activity, err := runLoopWith(t, s, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if res.FinalText == "" {
		t.Error("a dead MCP server ended the turn; it should cost the model a tool, not the user an answer")
	}

	var failed *protocol.ToolActivity
	for i := range activity {
		if activity[i].Phase == protocol.ToolPhaseFailed {
			failed = &activity[i]
		}
	}
	if failed == nil {
		t.Fatal("no failed tool activity was reported for a server that exited mid-call")
	}
	if !strings.Contains(failed.Detail, "server is unavailable") {
		t.Errorf("the user and the model were told %q. A dead server is not the same fact as a "+
			"failed tool, and only one of the two is worth retrying", failed.Detail)
	}
}
