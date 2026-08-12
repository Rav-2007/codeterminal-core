package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/protocol"

	tea "github.com/charmbracelet/bubbletea"
)

// /model is how a user picks what they are paying for, so its failure modes are
// user-visible in a way most of this client is not: an unknown tier, an
// inactive one, and a daemon that cannot be reached all have to say something
// true rather than silently doing nothing.
//
// handleModelCommand measured 21.2% — the listing branch and every refusal
// branch were unexercised.

// fakeDaemonServingTiers stands up a daemon that answers a StatusRequest with a
// fixed tier list, using the same lockfile-and-socket handshake the real client
// performs. Same shape as fakeDaemonCapturingRequests in stream_test.go.
func fakeDaemonServingTiers(t *testing.T, tiers []protocol.StatusTier) string {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath := filepath.Join(dir, "daemon.lock")

	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	data, err := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				dec := json.NewDecoder(conn)
				enc := json.NewEncoder(conn)

				var hs protocol.HandshakeRequest
				if dec.Decode(&hs) != nil {
					return
				}
				_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})

				var req protocol.StatusRequest
				if dec.Decode(&req) != nil {
					return
				}
				_ = enc.Encode(protocol.StatusResponse{AvailableTiers: tiers})
			}(conn)
		}
	}()

	restore := setLockPathForTest(t, lockPath)
	t.Cleanup(restore)
	return lockPath
}

func tierFixture() []protocol.StatusTier {
	return []protocol.StatusTier{
		{Name: "primary", Slug: "anthropic/claude-opus-4", Active: true},
		{Name: "fast", Slug: "anthropic/claude-haiku-4.5", Active: true},
		{Name: "retired", Slug: "old/model", Active: false},
	}
}

// lastTurn returns the text of the most recent assistant turn.
func lastTurn(t *testing.T, m tea.Model) string {
	t.Helper()
	cm, ok := m.(chatModel)
	if !ok {
		t.Fatalf("model was not a chatModel")
	}
	if len(cm.turns) == 0 {
		t.Fatal("no turns were appended")
	}
	return cm.turns[len(cm.turns)-1].text
}

func TestHandleModelCommand_ListsAllTiersAndMarksInactiveOnes(t *testing.T) {
	fakeDaemonServingTiers(t, tierFixture())

	m := newTestModel()
	m.preferredTier = "fast"

	got, _ := m.handleModelCommand("")
	text := lastTurn(t, got)

	if !strings.Contains(text, "primary") || !strings.Contains(text, "fast") {
		t.Errorf("active tiers missing from the listing:\n%s", text)
	}
	if !strings.Contains(text, "retired") || !strings.Contains(text, "[inactive]") {
		t.Errorf("inactive tier missing or not marked with [inactive]:\n%s", text)
	}
	if !strings.Contains(text, "* fast") {
		t.Errorf("the current tier was not marked:\n%s", text)
	}
	if !strings.Contains(text, "current: fast") {
		t.Errorf("the current tier was not stated:\n%s", text)
	}
}

// With no preference set, "primary" is the implicit default and must be the one
// marked — otherwise the list tells the user nothing is selected when something
// is.
func TestHandleModelCommand_MarksPrimaryWhenNoPreferenceIsSet(t *testing.T) {
	fakeDaemonServingTiers(t, tierFixture())

	m := newTestModel()
	got, _ := m.handleModelCommand("list")
	text := lastTurn(t, got)

	if !strings.Contains(text, "* primary") {
		t.Errorf("primary was not marked as the implicit default:\n%s", text)
	}
	if !strings.Contains(text, "current: (default)") {
		t.Errorf("the default state was not stated:\n%s", text)
	}
}

func TestHandleModelCommand_SelectsAnActiveTier(t *testing.T) {
	fakeDaemonServingTiers(t, tierFixture())

	m := newTestModel()
	got, _ := m.handleModelCommand("fast")

	cm := got.(chatModel)
	if cm.preferredTier != "fast" {
		t.Errorf("preferredTier = %q, want %q", cm.preferredTier, "fast")
	}
	if text := lastTurn(t, got); !strings.Contains(text, "anthropic/claude-haiku-4.5") {
		t.Errorf("the confirmation did not name the slug the user will be billed for: %q", text)
	}
}

// Selecting an inactive tier must be REFUSED, not silently accepted: the daemon
// would fall back to something else and the user would be paying for a model
// they did not choose.
func TestHandleModelCommand_RefusesAnInactiveTier(t *testing.T) {
	fakeDaemonServingTiers(t, tierFixture())

	m := newTestModel()
	got, _ := m.handleModelCommand("retired")

	cm := got.(chatModel)
	if cm.preferredTier != "" {
		t.Errorf("an inactive tier was selected: preferredTier = %q", cm.preferredTier)
	}
	if text := lastTurn(t, got); !strings.Contains(text, "inactive") {
		t.Errorf("the refusal did not say why: %q", text)
	}
}

func TestHandleModelCommand_UnknownTierIsRefusedWithGuidance(t *testing.T) {
	fakeDaemonServingTiers(t, tierFixture())

	m := newTestModel()
	got, _ := m.handleModelCommand("no-such-tier")

	cm := got.(chatModel)
	if cm.preferredTier != "" {
		t.Errorf("an unknown tier was selected: %q", cm.preferredTier)
	}
	text := lastTurn(t, got)
	if !strings.Contains(text, "unknown model tier") || !strings.Contains(text, "/model") {
		t.Errorf("the refusal did not point at the way to recover: %q", text)
	}
}

func TestHandleModelCommand_ClearResetsToTheDefault(t *testing.T) {
	m := newTestModel()
	m.preferredTier = "fast"

	for _, arg := range []string{"clear", "default"} {
		m.preferredTier = "fast"
		got, _ := m.handleModelCommand(arg)
		cm := got.(chatModel)
		if cm.preferredTier != "" {
			t.Errorf("/model %s left preferredTier = %q", arg, cm.preferredTier)
		}
	}
}

// A daemon that is not running must produce a status error rather than an empty
// list that reads as "you have no models".
func TestHandleModelCommand_UnreachableDaemonReportsAnError(t *testing.T) {
	restore := setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock"))
	defer restore()

	m := newTestModel()

	got, _ := m.handleModelCommand("")
	if cm := got.(chatModel); cm.statusErr == "" {
		t.Error("an unreachable daemon produced no status error")
	}

	got, _ = m.handleModelCommand("fast")
	if cm := got.(chatModel); cm.statusErr == "" {
		t.Error("resolving a tier against an unreachable daemon produced no status error")
	}
}

// toolActivityLine is what the user watches while the agent works. Every phase
// must render, and the succeeded case must state the byte count — a
// privacy-positioned product showing what left the machine.
func TestToolActivityLine_RendersEveryPhase(t *testing.T) {
	base := protocol.ToolActivity{Server: "builtin", Tool: "read_file"}

	running := base
	running.Phase = protocol.ToolPhaseRunning
	if got := toolActivityLine(running); !strings.Contains(got, "running") || !strings.Contains(got, "builtin__read_file") {
		t.Errorf("running = %q", got)
	}

	ok := base
	ok.Phase, ok.ResultBytes, ok.DurationMS = protocol.ToolPhaseSucceeded, 1234, 56
	got := toolActivityLine(ok)
	if !strings.Contains(got, "1234 bytes") || !strings.Contains(got, "56ms") {
		t.Errorf("succeeded = %q, want the bytes-to-the-model and duration", got)
	}

	failed := base
	failed.Phase, failed.Detail = protocol.ToolPhaseFailed, "boom"
	if got := toolActivityLine(failed); !strings.Contains(got, "failed") || !strings.Contains(got, ": boom") {
		t.Errorf("failed = %q", got)
	}

	denied := base
	denied.Phase = protocol.ToolPhaseDenied
	if got := toolActivityLine(denied); !strings.Contains(got, "not run") {
		t.Errorf("denied = %q", got)
	}
	// A denial with no detail must not render a dangling colon.
	if got := toolActivityLine(denied); strings.HasSuffix(got, ":") {
		t.Errorf("denied with no detail rendered a dangling separator: %q", got)
	}

	unknown := base
	unknown.Phase = "some-future-phase"
	if got := toolActivityLine(unknown); got != "" {
		t.Errorf("an unrecognised phase rendered %q, want empty", got)
	}
}

func TestTruncateToWidth(t *testing.T) {
	if got := truncateToWidth("hello", 0); got != "hello" {
		t.Errorf("width 0 must pass the string through, got %q", got)
	}
	if got := truncateToWidth("hello", -5); got != "hello" {
		t.Errorf("negative width must pass the string through, got %q", got)
	}
	if got := truncateToWidth("hello world", 5); got == "hello world" {
		t.Errorf("width 5 did not truncate: %q", got)
	}
}

func TestSteeredPrompt(t *testing.T) {
	if got := steeredPrompt(&slashDef{}, "args"); got != "args" {
		t.Errorf("a definition with no preamble must pass args through, got %q", got)
	}
	if got := steeredPrompt(&slashDef{Preamble: "PRE: "}, "args"); got != "PRE: args" {
		t.Errorf("got %q, want the preamble prepended", got)
	}
}

// resolveDaemonBin must fall back through its candidates in order and return
// empty rather than a CWD-relative guess when it finds nothing.
func TestResolveDaemonBin_ReturnsEmptyWhenNothingIsFound(t *testing.T) {
	t.Setenv(daemonBinEnvVar, "")
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())

	if got := resolveDaemonBin(); got != "" {
		t.Errorf("resolveDaemonBin() = %q, want empty when no trusted candidate exists", got)
	}
	if out := runMCPServerList(""); !strings.Contains(out, "not found") {
		t.Errorf("runMCPServerList did not report the missing binary: %q", out)
	}
}
