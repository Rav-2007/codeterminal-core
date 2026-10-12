package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/editapply"
	"mochiii/protocol"
)

// /spec in the client: choosing, remembering and showing the active spec, and
// rendering a check.

func specWorkspace(t *testing.T) (chatModel, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	for _, rel := range []string{"specs/verbose-flag.md", "specs/api/paging.md"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# spec\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := newChatModel("test", root, root, nil)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return sized.(chatModel), root
}

func TestASpecCanBeNamedTheWayAPersonWould(t *testing.T) {
	_, root := specWorkspace(t)
	for _, arg := range []string{"verbose-flag", "verbose-flag.md", "specs/verbose-flag.md"} {
		if got, err := resolveSpecArg(root, arg); err != nil || got != "specs/verbose-flag.md" {
			t.Errorf("resolveSpecArg(%q) = %q, %v", arg, got, err)
		}
	}
	for _, bad := range []string{"", "missing", "../etc/passwd", "specs/../README"} {
		if _, err := resolveSpecArg(root, bad); err == nil {
			t.Errorf("resolveSpecArg(%q) was accepted", bad)
		}
	}
	if got := listSpecs(root); strings.Join(got, ",") != "specs/api/paging.md,specs/verbose-flag.md" {
		t.Errorf("listSpecs = %v", got)
	}
}

// /spec use makes it active, shows it in the header, sends it with every
// prompt, and remembers it across restarts; /spec off forgets.
func TestTheActiveSpecIsChosenShownAndRemembered(t *testing.T) {
	m, root := specWorkspace(t)
	model, _ := m.handleSpecCommand("use verbose-flag")
	m = model.(chatModel)
	if m.activeSpec != "specs/verbose-flag.md" {
		t.Fatalf("activeSpec = %q", m.activeSpec)
	}
	if !strings.Contains(m.renderHeader(), "spec: verbose-flag") {
		t.Errorf("the header does not show the active spec: %q", m.renderHeader())
	}
	if again := newChatModel("test", root, root, nil); again.activeSpec != "specs/verbose-flag.md" {
		t.Errorf("after a restart the active spec is %q", again.activeSpec)
	}
	model, _ = m.handleSpecCommand("off")
	m = model.(chatModel)
	if m.activeSpec != "" || newChatModel("test", root, root, nil).activeSpec != "" {
		t.Error("/spec off did not forget the spec")
	}
}

// A remembered spec that has since been deleted is not sent forever.
func TestADeletedSpecIsForgotten(t *testing.T) {
	m, root := specWorkspace(t)
	model, _ := m.handleSpecCommand("use verbose-flag")
	_ = model
	if err := os.Remove(filepath.Join(root, "specs", "verbose-flag.md")); err != nil {
		t.Fatal(err)
	}
	if got := newChatModel("test", root, root, nil).activeSpec; got != "" {
		t.Errorf("a deleted spec is still active: %q", got)
	}
}

// /spec <goal> and /spec check are turns in their modes; check needs a spec.
func TestSpecSubcommandsStartTheRightTurns(t *testing.T) {
	m, _ := specWorkspace(t)
	model, _ := m.handleSpecCommand("check")
	if got := model.(chatModel); got.state == stateSending {
		t.Error("/spec check with no active spec started a turn")
	}

	model, cmd := m.handleSpecCommand("add a --verbose flag")
	if got := model.(chatModel); got.state != stateSending || got.turnMode != modeSpec || cmd == nil {
		t.Errorf("/spec <goal>: state=%v mode=%q", got.state, got.turnMode)
	}

	model, _ = m.handleSpecCommand("use verbose-flag")
	m = model.(chatModel)
	model, cmd = m.handleSpecCommand("check")
	if got := model.(chatModel); got.state != stateSending || got.turnMode != modeCheck || cmd == nil {
		t.Errorf("/spec check: state=%v mode=%q", got.state, got.turnMode)
	}
}

// The spec a /spec turn wrote becomes active once the user accepts it.
func TestAnAcceptedSpecBecomesActive(t *testing.T) {
	m, _ := specWorkspace(t)
	m.turnMode = modeSpec
	m.reviewBlocks = []editapply.EditBlock{{FilePath: "specs/new.md", Replace: "# new\n"}}
	m.reviewAppliedPaths = []string{"specs/new.md"}
	model, _ := m.finishReview()
	if got := model.(chatModel).activeSpec; got != "specs/new.md" {
		t.Errorf("activeSpec = %q after accepting a new spec", got)
	}

	// An ordinary turn that happens to edit a spec does not switch specs.
	m, _ = specWorkspace(t)
	m.turnMode = ""
	m.reviewAppliedPaths = []string{"specs/new.md"}
	model, _ = m.finishReview()
	if got := model.(chatModel).activeSpec; got != "" {
		t.Errorf("an ordinary edit to a spec made it active: %q", got)
	}
}

func TestTheCheckReportReadsAtAGlance(t *testing.T) {
	got := specReportText(&protocol.SpecReport{Spec: "specs/v.md", Criteria: []protocol.SpecCriterionResult{
		{ID: "C1", Text: "-v prints files", Status: protocol.SpecMet, Evidence: "go test ./cmd passed"},
		{ID: "C2", Text: "quiet by default", Status: protocol.SpecUnmet, Evidence: "prints a banner"},
		{ID: "C3", Text: "documented", Status: protocol.SpecUnknown, Note: "not checked"},
	}})
	for _, want := range []string{"specs/v.md: 1 of 3 criteria met", "✓ C1: -v prints files", "    go test ./cmd passed",
		"✗ C2: quiet by default", "? C3: documented", "    not checked"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

// The active spec goes out with the prompt, on the real wire path.
func TestTheActiveSpecTravelsWithThePrompt(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 2)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	ch := make(chan tea.Msg, 8)
	streamPromptWith(context.Background(), "test-client", "", "go on", "", modeCheck, "", "specs/v.md", nil, nil, nil, "", taskFields{}, ch)
	for msg := range ch {
		if _, done := msg.(streamDoneMsg); done {
			break
		}
		if e, bad := msg.(streamErrMsg); bad {
			t.Fatal(e.err)
		}
	}
	select {
	case req := <-requests:
		if req.Spec != "specs/v.md" || req.Mode != modeCheck {
			t.Errorf("sent spec=%q mode=%q", req.Spec, req.Mode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request reached the daemon")
	}
}

// /spec build needs a spec, and runs as a build turn.
func TestSpecBuildStartsABuild(t *testing.T) {
	m, _ := specWorkspace(t)
	if model, _ := m.handleSpecCommand("build"); model.(chatModel).state == stateSending {
		t.Error("/spec build with no active spec started a turn")
	}
	model, _ := m.handleSpecCommand("use verbose-flag")
	m = model.(chatModel)
	model, cmd := m.handleSpecCommand("build")
	if got := model.(chatModel); got.state != stateSending || got.turnMode != modeBuild || cmd == nil {
		t.Errorf("/spec build: state=%v mode=%q", got.state, got.turnMode)
	}
}

// The task list is on screen while the build runs, and stays in the
// transcript when it ends.
func TestTheTaskListIsShownWhileTheBuildRuns(t *testing.T) {
	m, _ := specWorkspace(t)
	m.state = stateStreaming
	m.streamCh = make(chan tea.Msg, 1)
	tasks := []protocol.TaskItem{
		{ID: "1", Title: "write TestVerbose", Status: protocol.TaskDone},
		{ID: "2", Title: "add the flag", Status: protocol.TaskActive},
		{ID: "3", Title: "document it", Status: protocol.TaskPending},
	}
	model, _ := m.Update(tasksMsg{tasks})
	m = model.(chatModel)
	view := m.viewport.View()
	for _, want := range []string{"tasks: 1 of 3 done", "☑ write TestVerbose", "▸ add the flag", "☐ document it"} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen lacks %q while the build runs:\n%s", want, view)
		}
	}
	model, _ = m.handleStreamDone()
	m = model.(chatModel)
	if len(m.tasks) != 0 || !strings.Contains(lastTurnText(m), "tasks: 1 of 3 done") {
		t.Errorf("the final task list did not stay in the transcript: %q", lastTurnText(m))
	}
}

func lastTurnText(m chatModel) string {
	for i := len(m.turns) - 1; i >= 0; i-- {
		if strings.Contains(m.turns[i].text, "tasks:") {
			return m.turns[i].text
		}
	}
	return ""
}
