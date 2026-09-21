package main

import (
	"bytes"
	"strings"
	"testing"

	"mochiii/protocol"
)

func baseStatus() protocol.StatusResponse {
	return protocol.StatusResponse{
		ProtocolVersion:  protocol.ProtocolVersion,
		DaemonVersion:    daemonVersion,
		PID:              4242,
		UptimeSeconds:    90,
		Workspace:        "/w",
		Tier:             "primary",
		Model:            "a/b",
		Retrieval:        protocol.StatusRetrieval{Enabled: false, Reason: "no index"},
		APIKeyConfigured: true,
	}
}

func TestPrintStatusRendersCounters(t *testing.T) {
	s := baseStatus()
	s.Counters = &protocol.StatusCounters{
		Prompts: 12, Applies: 5, AppliesFailed: 2, Undos: 1, UndosFailed: 0, Searches: 3,
		PeerAuthRefused: 7, VersionMismatched: 1,
	}

	var out bytes.Buffer
	printStatus(&out, s)
	got := out.String()

	for _, want := range []string{
		"12 prompt(s)", "5 apply(s) (2 failed)", "1 undo(s)", "3 search(es)",
		"refused      8", "peer auth 7", "version 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status output missing %q; got:\n%s", want, got)
		}
	}
	// A clean daemon must not be told it has contained panics.
	if strings.Contains(got, "PANICS") {
		t.Errorf("output claims contained panics with the counter at zero:\n%s", got)
	}
}

func TestPrintStatusOmitsCountersWhenAbsent(t *testing.T) {
	var out bytes.Buffer
	printStatus(&out, baseStatus())

	if got := out.String(); strings.Contains(got, "since start") {
		t.Errorf("a daemon with no counters still printed an activity line:\n%s", got)
	}
}

func TestPrintStatusNamesContainedPanicsAsADefect(t *testing.T) {
	s := baseStatus()
	s.Counters = &protocol.StatusCounters{PanicsRecovered: 3}

	var out bytes.Buffer
	printStatus(&out, s)
	got := out.String()

	if !strings.Contains(got, "PANICS       3") {
		t.Errorf("3 contained panics were not surfaced:\n%s", got)
	}
	// Worded as the defect it is, not as a statistic: a survived fault is still a
	// bug that happened.
	if !strings.Contains(got, "survived, not fixed") {
		t.Errorf("contained panics are reported without saying what they mean:\n%s", got)
	}
}

// TestPrintStatusKeepsDegradedLast is the ordering property the counters could
// most easily have broken. printStatus's own doc commits to it: the degraded list
// prints LAST and unmissably, because it is the thing the reader most likely needs.
func TestPrintStatusKeepsDegradedLast(t *testing.T) {
	s := baseStatus()
	s.Counters = &protocol.StatusCounters{Prompts: 1, PanicsRecovered: 1}
	s.ConfigWarnings = []string{"unknown key \"retreival\""}
	s.Degraded = []protocol.Degradation{{
		Component: protocol.DegradedLexicalRetrieval,
		Detail:    "lexical tier unavailable",
	}}

	var out bytes.Buffer
	printStatus(&out, s)
	got := out.String()

	degraded := strings.Index(got, "DEGRADED (1)")
	if degraded < 0 {
		t.Fatalf("the degraded block is missing entirely:\n%s", got)
	}
	for _, earlier := range []string{"since start", "PANICS", "config warnings", "daemon "} {
		if idx := strings.Index(got, earlier); idx > degraded {
			t.Errorf("%q is printed AFTER the degraded block, which must stay last:\n%s",
				earlier, got)
		}
	}
	// And it is genuinely the tail of the output, not merely late in it.
	if tail := strings.TrimSpace(got[degraded:]); !strings.HasSuffix(tail, "lexical tier unavailable") {
		t.Errorf("something follows the degraded list:\n%s", tail)
	}
}

// Agent counters appear only once a turn has run one, so the status output of
// every daemon with mcp.enabled unset is byte-for-byte what it was.
func TestStatusRendersAgentCountersOnlyWhenUsed(t *testing.T) {
	t.Run("absent on a daemon that has never run an agent turn", func(t *testing.T) {
		s := baseStatus()
		s.Counters = &protocol.StatusCounters{Prompts: 5}
		var buf bytes.Buffer
		printStatus(&buf, s)
		out := buf.String()
		if strings.Contains(out, "agent") {
			t.Errorf("a daemon that never ran an agent turn mentioned agent mode:\n%s", out)
		}
	})

	t.Run("present, with denials called out, once it has", func(t *testing.T) {
		s := baseStatus()
		s.Counters = &protocol.StatusCounters{
			Prompts: 3, AgentTurns: 3, ToolCalls: 7, ToolCallsDenied: 7, BudgetTerminations: 1,
		}
		var buf bytes.Buffer
		printStatus(&buf, s)
		out := buf.String()
		for _, want := range []string{"agent", "3 turn(s)", "7 tool call(s)", "DENIED", "budget"} {
			if !strings.Contains(out, want) {
				t.Errorf("status output is missing %q:\n%s", want, out)
			}
		}
	})
}
