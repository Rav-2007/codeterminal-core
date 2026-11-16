package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mochiii/protocol"
)

// WHAT THE MODEL READS when one call to a third-party server goes wrong, through
// the real path: a config, buildRegistry, a real subprocess, the loop, and the
// request the next model call carries. The transport-level behaviour (one
// oversized message fails one call; a dead server is started again once) is
// tested in daemon/mcp/serverfailure_test.go; this file is about the words.

const failureTestLimit = 64 << 10

// failureConfig runs testdata/badserver in "scripted" mode with every tool it
// has allowed (no prompts) and a small message limit. env names are passed
// through to the server.
func failureConfig(t *testing.T, env ...string) MCPConfig {
	t.Helper()
	names := make([]string, 0, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
		names = append(names, k)
	}
	allow := map[string]string{}
	for _, tool := range []string{"small", "pid", "read", "die", "rpcerror", "rpcerrorlong"} {
		allow[tool] = PolicyAllow
	}
	return MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Disabled: true},
		Budget:  MCPBudgetConfig{MaxMessageBytes: failureTestLimit},
		Servers: map[string]MCPServerConfig{
			"bad": {
				Command:                buildBadServer(t),
				Args:                   []string{"scripted"},
				Env:                    names,
				AcknowledgedUnconfined: true,
				Tools:                  allow,
			},
		},
	}
}

// toolMessages is what the model was sent for each tool call, by call id, read
// from the last request the loop made.
func toolMessages(t *testing.T, bodies *[][]byte) map[string]string {
	t.Helper()
	if len(*bodies) == 0 {
		t.Fatal("the model was never called")
	}
	var req struct {
		Messages []struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"tool_call_id"`
			Content    json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal((*bodies)[len(*bodies)-1], &req); err != nil {
		t.Fatalf("decoding the last request: %v", err)
	}
	out := map[string]string{}
	for _, m := range req.Messages {
		if m.Role != "tool" {
			continue
		}
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			s = string(m.Content)
		}
		out[m.ToolCallID] = s
	}
	return out
}

// ITEM 1, as the model sees it.
func TestAnOversizedResultIsReportedToTheModelAsTooLargeAndTheServerStaysUsable(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "bad__read", `{"bytes":`+strconv.Itoa(5*failureTestLimit)+`}`),
		toolCallSSE("c2", "bad__small", `{}`),
		textSSE("done"),
	)
	s := loopServer(t, base, failureConfig(t))

	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if res.FinalText == "" {
		t.Error("one oversized result ended the turn")
	}
	msgs := toolMessages(t, bodies)

	for _, want := range []string{"too large", strconv.Itoa(failureTestLimit) + " bytes", "still running", `"read"`} {
		if !strings.Contains(msgs["c1"], want) {
			t.Errorf("the model was told %q about the oversized result, which lacks %q", msgs["c1"], want)
		}
	}
	for _, bad := range []string{"server is unavailable", "no tool called", "failed to run"} {
		if strings.Contains(msgs["c1"], bad) {
			t.Errorf("the model was told %q, which says %q: that is not what happened", msgs["c1"], bad)
		}
	}
	if !strings.Contains(msgs["c2"], "small from pid") {
		t.Errorf("the next call to the same server did not work: the model was told %q", msgs["c2"])
	}
	var failed int
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseFailed && strings.Contains(a.Detail, "too large") {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("%d failed rows said the result was too large, want 1 (%+v)", failed, activity)
	}
}

// ITEM 2 and 3b, as the model sees it: the call it was in gets the "stopped"
// message and the server is back for the next one.
func TestAServerThatDiesIsRestartedAndTheModelIsToldSoAndThenCanUseIt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "died")
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "bad__die", `{}`),
		toolCallSSE("c2", "bad__small", `{}`),
		textSSE("done"),
	)
	s := loopServer(t, base, failureConfig(t, "BADSERVER_DIE_ONCE_FILE="+marker))

	if _, _, err := runLoop(t, s); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	msgs := toolMessages(t, bodies)
	for _, want := range []string{`the server "bad" stopped while running the tool "die"`, "restarted", "available again"} {
		if !strings.Contains(msgs["c1"], want) {
			t.Errorf("the model was told %q about the crash, which lacks %q", msgs["c1"], want)
		}
	}
	if !strings.Contains(msgs["c2"], "small from pid") {
		t.Errorf("the call after the restart did not work: the model was told %q", msgs["c2"])
	}
}

// ITEM 2 and 3b: a second death leaves the server down, and the model is told
// the server is unavailable and why -- never that the tool does not exist.
func TestAServerThatDiesTwiceIsReportedAsDownAndNeverAsAMissingTool(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "bad__die", `{}`), // first death: restarted
		toolCallSSE("c2", "bad__die", `{}`), // second: left down
		toolCallSSE("c3", "bad__small", `{}`),
		toolCallSSE("c4", "bad__pid", `{}`),
		textSSE("done"),
	)
	s := loopServer(t, base, failureConfig(t)) // no marker: every process dies on `die`

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	msgs := toolMessages(t, bodies)

	if !strings.Contains(msgs["c1"], "restarted") {
		t.Errorf("first death: the model was told %q", msgs["c1"])
	}
	for _, want := range []string{"stopped while running the tool", "unavailable for the rest of this turn", "stopped again"} {
		if !strings.Contains(msgs["c2"], want) {
			t.Errorf("second death: the model was told %q, which lacks %q", msgs["c2"], want)
		}
	}
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		if strings.Contains(msgs[id], "no tool called") || strings.Contains(msgs[id], "does not exist") {
			t.Errorf("%s: the model was told its tool does not exist: %q", id, msgs[id])
		}
	}
	for _, id := range []string{"c3", "c4"} {
		for _, want := range []string{"unavailable for the rest of this turn", "cannot run now"} {
			if !strings.Contains(msgs[id], want) {
				t.Errorf("%s, a call to the server after it was left down: the model was told %q, which lacks %q",
					id, msgs[id], want)
			}
		}
	}
	for _, a := range activity {
		if strings.Contains(a.Detail, "no tool called") {
			t.Errorf("the user was told %q", a.Detail)
		}
	}
}

// ITEM 3a, as the model sees it.
func TestAProtocolErrorReachesTheModelWithTheServersCodeAndMessage(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "bad__rpcerror", `{}`),
		toolCallSSE("c2", "bad__rpcerrorlong", `{}`),
		toolCallSSE("c3", "bad__small", `{}`),
		textSSE("done"),
	)
	s := loopServer(t, base, failureConfig(t))

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	msgs := toolMessages(t, bodies)

	for _, want := range []string{"-32602", "Invalid params: path /etc/shadow is outside the allowed directories",
		"this call FAILED", `<lane_b_output server="bad">`} {
		if !strings.Contains(msgs["c1"], want) {
			t.Errorf("the model was told %q about the protocol error, which lacks %q", msgs["c1"], want)
		}
	}
	if strings.Contains(msgs["c1"], "failed to run") {
		t.Errorf("the model got the generic sentence and not the server's reason: %q", msgs["c1"])
	}
	if !strings.Contains(msgs["c2"], "-32603") || len(msgs["c2"]) > 1200 {
		t.Errorf("a 5000 character error message reached the model as %d bytes: %.200q", len(msgs["c2"]), msgs["c2"])
	}
	if !strings.Contains(msgs["c3"], "small from pid") {
		t.Errorf("the server was not usable after its own protocol error: %q", msgs["c3"])
	}
	var reason string
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseFailed && strings.Contains(a.Detail, "-32602") {
			reason = a.Detail
		}
	}
	if reason == "" {
		t.Errorf("the user's row for the call does not carry the server's code: %+v", activity)
	}
}
