package main

import (
	"bytes"
	"io"
	"log"
	"strings"
	"testing"

	"codeterminal/protocol"
)

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		secs int64
		want string
	}{
		{30, "30s"},
		{125, "2m5s"},
		{3665, "1h1m"},
	}

	for _, tt := range tests {
		if got := formatUptime(tt.secs); got != tt.want {
			t.Errorf("formatUptime(%d) = %q, want %q", tt.secs, got, tt.want)
		}
	}
}

func TestYesNo(t *testing.T) {
	if got := yesNo(true, "ON", "OFF"); got != "ON" {
		t.Errorf("yesNo(true) = %q, want ON", got)
	}
	if got := yesNo(false, "ON", "OFF"); got != "OFF" {
		t.Errorf("yesNo(false) = %q, want OFF", got)
	}
}

func TestPrintStatus(t *testing.T) {
	var buf bytes.Buffer
	resp := protocol.StatusResponse{
		ProtocolVersion:  protocol.ProtocolVersion,
		DaemonVersion:    "1.0.0",
		PID:              1234,
		UptimeSeconds:    120,
		Workspace:        "/workspace",
		Model:            "test-model",
		Tier:             "fast",
		APIKeyConfigured: true,
		Retrieval: protocol.StatusRetrieval{
			Enabled:            true,
			Lexical:            true,
			TopK:               10,
			ContextBudgetChars: 4000,
			IndexedChunks:      50,
		},
		MemoryAvailable: true,
		ConfigWarnings:  []string{"warning 1"},
		Counters: &protocol.StatusCounters{
			Prompts:            5,
			Applies:            2,
			Searches:           1,
			AgentTurns:         1,
			ToolCalls:          2,
			ToolCallsDenied:    2,
			BudgetTerminations: 1,
			PanicsRecovered:    1,
		},
		Degraded: []protocol.Degradation{
			{Component: "mcp", Detail: "server offline"},
		},
	}

	printStatus(&buf, resp)
	out := buf.String()

	for _, want := range []string{
		"daemon      1.0.0",
		"workspace   /workspace",
		"api key     configured",
		"retrieval   semantic ON",
		"DEGRADED (1):",
		"server offline",
		"DENIED by policy",
		"PANICS       1 contained",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printStatus output missing %q:\n%s", want, out)
		}
	}
}

func TestRunStatusCommandNoDaemon(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	err := runStatusCommand(nil, logger)
	if err == nil || !strings.Contains(err.Error(), "no daemon is responding") {
		t.Errorf("expected no daemon responding error, got %v", err)
	}
}
