//go:build unix

package main

// AGENTBENCH, AGAINST THE REAL DAEMON.
//
// scripts/agent-cost-bench.sh is how a turn's cost is measured, and its client
// is daemon/testdata/agentbench -- which go test never builds, because it sits
// under testdata/. So when the daemon's lockfile became per workspace, nothing
// noticed that agentbench still read the per-user daemon.lock: every run failed
// with "no daemon lockfile" (FOUND 2026-10-24). And when it did connect, its
// step count was tool_calls+1, which a step that asks for several tools at
// once makes wrong: a 2-call turn that read four files at once came out as 5.
//
// These build agentbench as the script does and point it at a real daemon on
// loopback, with a scripted model whose request count is the ground truth.
//
// Neuter checks: put protocol.LockPath() back in runTurn, and both fail on
// "no daemon lockfile"; put back `rec.Iterations = rec.ToolCalls + 1` (as
// model_calls), and the first reports 3 where the model saw 2 requests, and
// the second reports 3 where nothing was known.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type agentbenchTurn struct {
	ModelCalls *int   `json:"model_calls"`
	ToolCalls  int    `json:"tool_calls"`
	Error      string `json:"error"`
}

var agentbenchFiles = map[string]string{"alpha.txt": "alpha\n", "beta.txt": "beta\n"}

// runAgentbench builds agentbench and runs one turn against d. The daemon's
// runtime directory reaches it through XDG_RUNTIME_DIR, which startE2E sets
// for this test process.
func runAgentbench(t *testing.T, d *e2eDaemon) agentbenchTurn {
	t.Helper()
	bin := filepath.Join(t.TempDir(), exeName("agentbench"))
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/agentbench").CombinedOutput(); err != nil {
		t.Fatalf("building agentbench: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "-turns", "1", "-workspace", d.workspace, "-decision", "approve",
		"-prompt", "read alpha.txt and beta.txt")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("agentbench: %v\n%s", err, out)
	}
	var report struct {
		Turns []agentbenchTurn `json:"turns"`
	}
	if err := json.Unmarshal(out, &report); err != nil || len(report.Turns) != 1 {
		t.Fatalf("agentbench printed %d turn(s), err %v:\n%s", len(report.Turns), err, out)
	}
	return report.Turns[0]
}

// One step asks for two reads at once, the next answers: two model calls and
// two tool calls, where tool_calls+1 says three.
func TestAgentbenchConnectsAndReportsTheDaemonsModelCallCount(t *testing.T) {
	model := newE2EModel(t, byToolResults(
		withUsage(toolCallsSSE(
			[3]string{"c1", "builtin__read_file", `{"path":"alpha.txt"}`},
			[3]string{"c2", "builtin__read_file", `{"path":"beta.txt"}`},
		), 1000, 10, 0.001),
		withUsage(textSSE("Both read."), 1200, 12, 0.001),
	))
	d := startE2E(t, model, agentbenchFiles, nil)

	got := runAgentbench(t, d)
	if got.Error != "" {
		t.Fatalf("agentbench could not run the turn: %s", got.Error)
	}
	// The ground truth, and the reason this scenario can tell the two counts
	// apart: two requests, two tool calls.
	requests := len(model.requests())
	if requests != 2 || got.ToolCalls != 2 {
		t.Fatalf("the scripted turn made %d model request(s) and %d tool call(s); want 2 and 2", requests, got.ToolCalls)
	}
	if got.ModelCalls == nil || *got.ModelCalls != requests {
		t.Errorf("model_calls = %v, want %d -- the requests the model actually received", deref(got.ModelCalls), requests)
	}
}

// A provider that sends no usage leaves the daemon without a count, and
// agentbench says so rather than computing one.
func TestAgentbenchReportsUnknownWhenTheDaemonSendsNoUsage(t *testing.T) {
	model := newE2EModel(t, byToolResults(
		toolCallsSSE(
			[3]string{"c1", "builtin__read_file", `{"path":"alpha.txt"}`},
			[3]string{"c2", "builtin__read_file", `{"path":"beta.txt"}`},
		),
		textSSE("Both read."),
	))
	d := startE2E(t, model, agentbenchFiles, nil)

	got := runAgentbench(t, d)
	if got.Error != "" {
		t.Fatalf("agentbench could not run the turn: %s", got.Error)
	}
	// Positive control: the turn really ran, so a nil count is the absence of
	// usage and not the absence of a turn.
	if len(model.requests()) != 2 || got.ToolCalls != 2 {
		t.Fatalf("the scripted turn made %d model request(s) and %d tool call(s); want 2 and 2", len(model.requests()), got.ToolCalls)
	}
	if got.ModelCalls != nil {
		t.Errorf("model_calls = %d with no usage on the wire; want null (unknown)", *got.ModelCalls)
	}
}

func deref(p *int) any {
	if p == nil {
		return "null"
	}
	return *p
}
