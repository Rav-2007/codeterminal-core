package main

import (
	"bytes"
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

// M6b -- control sequences in tool output are removed at the egress choke
// point, and their removal is announced.
//
// Rated below M6 and fixed anyway. Tool output does not reach a client
// directly, so the path is longer than the tool-name one: escapes enter the
// model's context and the model may echo them into an answer that does stream
// to a terminal. renderToolResult is the choke point that exists for bytes an
// unconfined subprocess chose, and this costs one strings.Map.
//
// Fails if stripControlCharacters is neutered.
func TestControlSequencesInToolOutputAreStripped(t *testing.T) {
	payload := "before\x1b]52;c;cGF5bG9hZA==\x07\x1b[2Jafter\x00\r\nkept\ttab\n"
	rendered, _, emitted := renderToolResult(payload, 4096, false)

	if got := countControl(rendered); got != 0 {
		t.Errorf("%d control character(s) survived to the model: %q", got, rendered)
	}
	if strings.ContainsRune(rendered, '\r') {
		t.Error("a bare carriage return survived; on its own it overwrites the line already drawn")
	}
	// Text is text: dropping the escapes must not drop what they were wrapped
	// around, or the model is reasoning about a result it never got.
	for _, want := range []string{"before", "after", "kept\ttab"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("stripping removed real content: %q is missing from %q", want, rendered)
		}
	}
	// Announced, not silent -- the same rule truncation follows.
	if !strings.Contains(rendered, "control character(s) removed") {
		t.Errorf("control characters were removed without saying so: %q", rendered)
	}
	if emitted != len(rendered) {
		t.Errorf("emitted %d but rendered %d bytes; the budget must count the bytes that go out",
			emitted, len(rendered))
	}
}

// Clean output is passed through untouched, notice and all. A scrubber that
// announces work it did not do is a scrubber nobody reads after a while.
func TestCleanToolOutputGetsNoControlNotice(t *testing.T) {
	rendered, _, _ := renderToolResult("ordinary\noutput\twith tabs\n", 4096, false)
	if strings.Contains(rendered, "control character") {
		t.Errorf("clean output was annotated anyway: %q", rendered)
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
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
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

// P2-2 -- THE ADVERTISED-TOOL CAP AND THE MEASUREMENT IT CAME FROM.
//
// The invariant is unchanged and is the whole point: the default must never sit
// past the widest menu anyone has actually measured. A default beyond the
// evidence is a guess with a number on it.
//
// WHAT CHANGED IS THE EVIDENCE, and this test is the record of that. It was
// written with widestMeasuredMenu = 5, because the Phase 0 eval had measured
// 100% at one tool and 85.7% at five and nothing wider
// (docs/TOOLCALL_RELIABILITY_2026-07-31.md). It then did its job: when the
// default moved to 12 it failed, loudly, naming the gap.
//
// 8 and 12 have since been measured (docs/TOOL_MENU_SIZE_2026-08-01.md; 105
// trials, zero transport errors, zero timeouts):
//
//	menu  5  ->  88.6%   (31/35)
//	menu  8  ->  85.7%   (30/35)
//	menu 12  ->  85.7%   (30/35)
//
// Flat -- the entire spread is one trial at n=35 -- and every failure at every
// size is the same run_tests/list_directory confusion, so excluding that one
// prompt the score is 30/30 at all three sizes. So the constant below moves to
// 12 because the MEASUREMENT moved to 12, not because the default wanted room.
//
// It is still not a claim that 12 is optimal. Nothing between 13 and the
// maxMaxAdvertisedTools ceiling of 64 has been run, and the token cost of a
// wider menu (2.1x the tool JSON at 12 versus 5) is a real cost this accuracy
// number does not capture.
func TestTheAdvertisedToolDefaultDoesNotExceedWhatWasMeasured(t *testing.T) {
	const widestMeasuredMenu = 12
	if defaultMaxAdvertisedTools > widestMeasuredMenu {
		t.Errorf("defaultMaxAdvertisedTools is %d, but the widest menu ever measured is %d "+
			"(88.6%%@5, 85.7%%@8, 85.7%%@12). A default past the evidence is a guess with a number on it",
			defaultMaxAdvertisedTools, widestMeasuredMenu)
	}
}

// And when the cap bites, the user is told.
//
// A dropped tool and a tool the server never offered are indistinguishable from
// outside -- both look like the model not using it. Registry.Advertised has
// always recorded what it left out, but until now nothing in a live turn read
// it: the report existed only in `mcp list`. Lowering the default makes this
// reachable in ordinary use, so it has to be visible in ordinary use.
//
// Fails if the Dropped() notice is removed from runAgentLoop.
func TestATrimmedToolMenuIsReportedToTheUser(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("done"))

	// Built-ins on (4 tools) with a cap of 2, so two are certainly dropped.
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Budget:  MCPBudgetConfig{MaxAdvertisedTools: 2},
	})

	if _, _, err := runLoopWith(t, s, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	var found *protocol.Degradation
	for i := range lastDegradations {
		if lastDegradations[i].Component == protocol.DegradedToolMenuTruncated {
			found = &lastDegradations[i]
		}
	}
	if found == nil {
		t.Fatalf("the tool menu was trimmed and nothing said so; degradations were %+v", lastDegradations)
	}
	if !strings.Contains(found.Detail, "max_advertised_tools") {
		t.Errorf("the notice does not name the setting that caused it: %q", found.Detail)
	}
}

// The notice does not fire when nothing was dropped. A degradation that is
// always on is a degradation nobody reads.
func TestAnUntrimmedToolMenuIsNotReportedAsDegraded(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("done"))
	s := loopServer(t, base, MCPConfig{Enabled: true})

	if _, _, err := runLoopWith(t, s, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	for _, d := range lastDegradations {
		if d.Component == protocol.DegradedToolMenuTruncated {
			t.Errorf("a full tool menu was reported as trimmed: %+v", d)
		}
	}
}

// A server that writes to stderr without ever sending a newline must not grow
// the daemon's log buffer without bound.
//
// This is M1a's shape on the channel M1a did not cover. That fix capped stdio
// MESSAGES via max_message_bytes; stderr never goes through the transport, it
// goes to prefixWriter, which accumulated until a '\n' arrived. An unconfined
// third-party process choosing never to send one is not a malfunction, it is an
// input.
func TestPrefixWriterBoundsANewlineFreeFlood(t *testing.T) {
	logged := 0
	w := &prefixWriter{
		prefix: "mcp/flood: ",
		logger: log.New(writerFunc(func(p []byte) (int, error) {
			logged += len(p)
			return len(p), nil
		}), "", 0),
	}

	// 8 MiB with no newline anywhere.
	chunk := bytes.Repeat([]byte("A"), 64<<10)
	for i := 0; i < 128; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if len(w.buf) > maxLogLineBytes {
			t.Fatalf("after %d MiB the retained buffer is %d bytes, above the %d cap — a server that never sends a newline grows this without limit",
				(i+1)*64/1024, len(w.buf), maxLogLineBytes)
		}
	}
	if logged == 0 {
		t.Error("nothing was logged; the flood should be emitted at the cap, not dropped")
	}
}

// The ordinary case must be untouched: whole lines still log one per line, and
// a partial line is still held until its newline arrives.
func TestPrefixWriterStillBuffersPartialLines(t *testing.T) {
	var got []string
	w := &prefixWriter{
		prefix: "mcp/x: ",
		logger: log.New(writerFunc(func(p []byte) (int, error) {
			got = append(got, strings.TrimRight(string(p), "\n"))
			return len(p), nil
		}), "", 0),
	}

	w.Write([]byte("first line\nsec"))
	if len(got) != 1 || got[0] != "mcp/x: first line" {
		t.Fatalf("after a complete line plus a partial one, logged = %v", got)
	}
	w.Write([]byte("ond line\n"))
	if len(got) != 2 || got[1] != "mcp/x: second line" {
		t.Fatalf("the held partial line was not completed correctly: %v", got)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
