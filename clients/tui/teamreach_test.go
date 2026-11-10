package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// THE TRAP THIS FEATURE WALKS INTO, pinned so it cannot be walked into again.
//
// startTurn runs parseSlash BEFORE parseTeamCommand. The moment "team" joined
// the catalog, "/team fix this" began matching parseSlash — as a steered
// command with no preamble, which sends the raw text and NO pipeline. Every
// existing test would have stayed green: they all call parseTeamCommand
// directly, and it still works perfectly on a string nothing routes to it.
//
// The catalog entry would have broken the command it was advertising, silently.
// slash.go carries the matching exclusion; this is what fails if it is deleted.
func TestParseSlashHandsTheTeamCommandOnward(t *testing.T) {
	for _, raw := range []string{
		"/team why is this slow",
		"/team:researcher,coder why is this slow",
		"/team:planner, coder fix the parser",
	} {
		sp := parseSlash(raw)
		if !sp.RawPassthrough {
			t.Errorf("%q was captured by parseSlash (def=%v, usageOnly=%v); the pipeline never "+
				"reaches the wire when that happens", raw, sp.Def, sp.UsageOnly)
		}
	}
}

// The bare form has no question to ask, so it gets the usage line every other
// NeedsArgs command gets rather than being sent to the model as the literal
// text "/team".
func TestBareTeamShowsUsageRatherThanBecomingAPrompt(t *testing.T) {
	sp := parseSlash("/team")
	if sp.RawPassthrough {
		t.Fatal("bare /team fell through as ordinary text, so the model is asked to answer \"/team\"")
	}
	if !sp.UsageOnly {
		t.Errorf("usageOnly = false for a command that needs args")
	}
	if sp.Def == nil || sp.Def.Name != "team" {
		t.Fatalf("def = %v, want the team command", sp.Def)
	}
}

// DISCOVERABILITY IS THE WHOLE FEATURE. The pipeline was complete and reachable
// by nobody: a command in no help text and no autocomplete popup is a command
// that does not exist for anyone who was not told about it in person.
func TestTheTeamCommandIsDiscoverable(t *testing.T) {
	if lookupSlash("team") == nil {
		t.Fatal("/team is not in the catalog")
	}

	if help := formatSlashHelp(); !strings.Contains(help, "/team") {
		t.Errorf("/help does not list /team:\n%s", help)
	}

	// The autocomplete popup, which is where it is actually found: typing "/te"
	// has to offer it.
	m := newTestModel()
	m = typeText(m, "/te")
	found := false
	for _, d := range m.slashMatches() {
		if d.Name == "team" {
			found = true
		}
	}
	if !found {
		t.Errorf("typing /te does not offer /team: %v", m.slashMatches())
	}
}

// /model was printed TWICE in every /help this product has ever shown — it is
// both a catalog entry and separately appended. Caught while adding /team, and
// worth a test rather than a fix alone: the next command with two sources gets
// the same treatment for free.
func TestHelpListsEveryCommandExactlyOnce(t *testing.T) {
	help := formatSlashHelp()
	seen := map[string]int{}
	for _, line := range strings.Split(help, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "/") {
			continue
		}
		name, _, _ := strings.Cut(line[1:], " ")
		seen[name]++
	}
	if len(seen) < 20 {
		t.Fatalf("parsed only %d commands out of /help; this test is not reading it", len(seen))
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("/%s appears %d times in /help, want exactly 1", name, n)
		}
	}
}

// END TO END, FROM A KEYSTROKE TO THE BYTES ON THE SOCKET — the only assertion
// that covers the whole chain the catalog entry could have broken: parseSlash
// declining it, parseTeamCommand claiming it, startTurn threading the shape
// into the request, and the daemon receiving it.
func TestTypingTheTeamCommandPutsThePipelineOnTheWire(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 1)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	m := newTestModel()
	m = typeText(m, "/team why is this slow")
	m, cmd := pressEnter(m)
	if cmd == nil {
		t.Fatal("Enter on /team produced no command; the turn never started")
	}
	if m.state != stateSending {
		t.Fatalf("state = %v, want stateSending", m.state)
	}
	go runCmdTree(cmd)

	select {
	case req := <-requests:
		if !slices.Equal(req.Pipeline, []string{"researcher", "coder"}) {
			t.Errorf("Pipeline on the wire = %v, want the measured two-phase shape", req.Pipeline)
		}
		if req.Prompt != "why is this slow" {
			t.Errorf("Prompt = %q, want the question with the command stripped", req.Prompt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never received a PromptRequest")
	}
}

// The explicit shape reaches the wire too — this is the form that makes the
// planner and tester usable at all.
func TestTypingAnExplicitShapePutsThatShapeOnTheWire(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 1)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	m := newTestModel()
	m = typeText(m, "/team:planner,tester check the parser")
	_, cmd := pressEnter(m)
	if cmd == nil {
		t.Fatal("Enter on /team:… produced no command")
	}
	go runCmdTree(cmd)

	select {
	case req := <-requests:
		if !slices.Equal(req.Pipeline, []string{"planner", "tester"}) {
			t.Errorf("Pipeline on the wire = %v, want exactly what was named", req.Pipeline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never received a PromptRequest")
	}
}

// runCmdTree executes a Cmd and any Cmds a tea.Batch fans out into, which is
// what startTurn returns (the spinner tick alongside the stream).
func runCmdTree(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			go runCmdTree(c)
		}
	}
}

// A TYPO IN A ROLE NAME IS SAID AT ONCE. "/team:nonsense do a thing" drops the
// role the daemon does not know and runs on, and the daemon says so in a
// pipeline_shape notice before the first word of the answer. The terminal app
// kept the notice for /context (the header no longer draws notices), so the
// person who mistyped was told nothing and the answer came back as though
// nothing had been dropped (Phase 3 audit, 2026-11-09). The VS Code client
// shows it as it arrives.
//
// Through a fake daemon that sends what the real one sends (pipelineNotices),
// driven through Update.
//
// Neuter check: take DegradedPipelineShape out of handleDegraded's condition
// and the screen has no notice until /context.
func TestAnUnknownTeamRoleIsSaidAtOnce(t *testing.T) {
	const detail = `"nonsense" is not a specialist this daemon knows, so that step did not run. ` +
		`The steps that exist are planner, researcher, coder and tester.`
	fakeDaemonReplying(t,
		protocol.TokenResponse{Degraded: []protocol.Degradation{{Component: protocol.DegradedPipelineShape, Detail: detail}}},
		protocol.TokenResponse{Token: "the answer"},
		protocol.TokenResponse{Done: true},
	)
	m := typeText(newTestModel(), "/team:nonsense do a thing")
	m, cmd := pressEnter(m)
	m = driveTurn(t, m, cmd)

	screen := ansi.Strip(m.View())
	if !strings.Contains(screen, `"nonsense" is not a specialist`) {
		t.Errorf("the turn is over and the screen never said the role was dropped:\n%s", screen)
	}
	// It is a note, not conversation: the model is not shown it next turn.
	for _, h := range buildHistory(m.turns) {
		if strings.Contains(h.Content, "not a specialist") {
			t.Errorf("the notice went into the history sent to the model: %+v", h)
		}
	}
	// And it is still in /context, where it was.
	if got := ansi.Strip(strings.Join(m.noticeLines(), "\n")); !strings.Contains(got, "not a specialist") {
		t.Errorf("/context lost the notice: %q", got)
	}
}

// The other degradations stay where they were (/context): only the notice the
// person's own typing caused is promoted to the transcript.
func TestOtherDegradationsStayOutOfTheTranscript(t *testing.T) {
	fakeDaemonReplying(t,
		protocol.TokenResponse{Degraded: []protocol.Degradation{{Component: protocol.DegradedProviderRouting, Detail: "routing detail"}}},
		protocol.TokenResponse{Token: "the answer"},
		protocol.TokenResponse{Done: true},
	)
	m := typeText(newTestModel(), "a plain question")
	m, cmd := pressEnter(m)
	m = driveTurn(t, m, cmd)
	for _, tn := range m.turns {
		if strings.Contains(tn.text, "routing detail") {
			t.Errorf("a routing notice was drawn in the transcript: %+v", tn)
		}
	}
}
