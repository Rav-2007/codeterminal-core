package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// typedRequest types raw into m, presses Enter, and returns the PromptRequest
// the daemon receives: from a keystroke to the bytes on the socket.
func typedRequest(t *testing.T, m chatModel, raw string) (chatModel, protocol.PromptRequest) {
	t.Helper()
	requests := make(chan protocol.PromptRequest, 1)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	t.Cleanup(cleanup)
	t.Cleanup(setLockPathForTest(t, lockPath))

	m = typeText(m, raw)
	m, cmd := pressEnter(m)
	if cmd == nil {
		t.Fatalf("Enter on %q started nothing", raw)
	}
	go runCmdTree(cmd)
	select {
	case req := <-requests:
		return m, req
	case <-time.After(5 * time.Second):
		t.Fatalf("the daemon never received a request for %q", raw)
		return m, protocol.PromptRequest{}
	}
}

// THE LONG-TASK COMMANDS SEND A MODE, NOT A PREAMBLE. The daemon enforces a
// mode -- method, budget, finish gate -- where a preamble was only words in
// front of the user's own.
//
// Neuter check: put /fix back to its preamble, or drop the isLongTaskMode
// branch in handleSlash, and the mode or the budget is missing on the wire.
func TestTheLongTaskCommandsPutAModeOnTheWire(t *testing.T) {
	for _, c := range []struct{ raw, mode, prompt string }{
		{"/fix the failing total", modeFix, "the failing total"},
		{"/debug why the parser hangs", modeDebug, "why the parser hangs"},
		{"/refactor split the router", modeRefactor, "split the router"},
		{"/hunt the billing package", modeHunt, "the billing package"},
		{"/task add retries to the fetcher", modeTask, "add retries to the fetcher"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			m := newTestModel()
			m.taskBudget = &protocol.TaskBudget{Minutes: 45}
			_, req := typedRequest(t, m, c.raw)
			if req.Mode != c.mode || req.Prompt != c.prompt || req.PromptKind != "" {
				t.Errorf("wire = mode %q, prompt %q, kind %q; want mode %q, prompt %q, no kind",
					req.Mode, req.Prompt, req.PromptKind, c.mode, c.prompt)
			}
			if req.TaskBudget == nil || req.TaskBudget.Minutes != 45 {
				t.Errorf("the session's budget did not go with the task: %+v", req.TaskBudget)
			}
			if req.Task != "" || req.TaskAction != "" {
				t.Errorf("a new task named task %q, action %q", req.Task, req.TaskAction)
			}
		})
	}
}

// /task's subcommands, and the goals that merely begin with one's word.
func TestTheTaskSubcommandsReachTheWire(t *testing.T) {
	cases := []struct {
		raw, wantMode, wantTask, wantAction, wantPrompt string
	}{
		{"/task resume", "", protocol.TaskLatest, protocol.TaskActionResume, "continue"},
		{"/task resume look at the parser first", "", protocol.TaskLatest, protocol.TaskActionResume, "look at the parser first"},
		{"/task review", "", protocol.TaskLatest, protocol.TaskActionReview, "review"},
		{"/task review 20260930-120000-abcd", "", "20260930-120000-abcd", protocol.TaskActionReview, "review"},
		{"/task discard", "", protocol.TaskLatest, protocol.TaskActionDiscard, "discard"},
		// A goal that begins with a subcommand's word is a goal.
		{"/task review the auth module for bugs", modeTask, "", "", "review the auth module for bugs"},
		{"/task budget the quarterly report generator", modeTask, "", "", "budget the quarterly report generator"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			_, req := typedRequest(t, newTestModel(), c.raw)
			if req.Mode != c.wantMode || req.Task != c.wantTask || req.TaskAction != c.wantAction || req.Prompt != c.wantPrompt {
				t.Errorf("wire = mode %q, task %q, action %q, prompt %q; want %q, %q, %q, %q",
					req.Mode, req.Task, req.TaskAction, req.Prompt, c.wantMode, c.wantTask, c.wantAction, c.wantPrompt)
			}
		})
	}
}

// A task this session ran is the one /task resume carries on.
func TestResumeCarriesOnThisSessionsTask(t *testing.T) {
	m := newTestModel()
	m.taskStatus = &protocol.TaskStatus{ID: "20260930-120000-beef", State: protocol.TaskStateBudget}
	_, req := typedRequest(t, m, "/task resume")
	if req.Task != "20260930-120000-beef" {
		t.Errorf("resumed task %q, want this session's", req.Task)
	}
}

func TestTaskBudgetParsing(t *testing.T) {
	for _, c := range []struct {
		in   string
		want protocol.TaskBudget
		bad  bool
	}{
		{in: "45m", want: protocol.TaskBudget{Minutes: 45}},
		{in: "2h $1.50 300", want: protocol.TaskBudget{Minutes: 120, USD: 1.5, Calls: 300}},
		{in: "90min 2$ 120 calls", want: protocol.TaskBudget{Minutes: 90, USD: 2, Calls: 120}},
		{in: "$0", bad: true},
		{in: "-5m", bad: true},
		{in: "soon", bad: true},
	} {
		got, err := parseTaskBudget(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("parseTaskBudget(%q) = %+v, want an error", c.in, got)
			}
			continue
		}
		if err != nil || *got != c.want {
			t.Errorf("parseTaskBudget(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	m := newTestModel()
	m.setTaskBudget("45m $1 200")
	if m.taskBudget == nil || *m.taskBudget != (protocol.TaskBudget{Minutes: 45, USD: 1, Calls: 200}) {
		t.Errorf("/task budget set %+v", m.taskBudget)
	}
	m.setTaskBudget("default")
	if m.taskBudget != nil {
		t.Error("/task budget default did not go back to the daemon's defaults")
	}
}

// statusStep feeds one TaskStatus to a model that is mid-stream.
func statusStep(m chatModel, st protocol.TaskStatus) chatModel {
	updated, _ := m.Update(taskStatusMsg{st})
	return updated.(chatModel)
}

// A RUNNING TASK SHOWS WHERE IT IS: a line per segment in the transcript, a
// meter under it, and a closing line that says how to carry on.
func TestATasksProgressIsShown(t *testing.T) {
	m := newTestModel()
	m.streamCh = make(chan tea.Msg, 4)
	m.state = stateStreaming
	st := protocol.TaskStatus{ID: "20260930-120000-abcd", Mode: modeDebug, State: protocol.TaskStateRunning,
		Segment: 1, MaxCalls: 150, MaxUSD: 0.5, MaxSeconds: 1800}
	m = statusStep(m, st)
	st.Segment, st.Calls, st.USD, st.LastCheck, st.LastCheckPassed = 2, 21, 0.07, "go test ./...", false
	m = statusStep(m, st)
	m.refreshViewport()
	view := m.viewport.View()
	for _, want := range []string{"task abcd · debug · segment 2", "21/150 calls", "last check ✗ go test ./..."} {
		if !strings.Contains(view, want) {
			t.Errorf("the meter lacks %q:\n%s", want, view)
		}
	}
	st.State, st.Detail = protocol.TaskStateBudget, "this task's run spent $0.50, its budget of $0.50."
	m = statusStep(m, st)
	text := transcriptText(m)
	for _, want := range []string{"▶ debug task 20260930-120000-abcd started", "— segment 2 ·",
		"⏸ task 20260930-120000-abcd paused at its budget", "/task resume"} {
		if !strings.Contains(text, want) {
			t.Errorf("the transcript lacks %q:\n%s", want, text)
		}
	}
}

// STOPPING A TASK LOSES NOTHING, and the user is told how to carry on.
func TestInterruptingATaskSaysItIsSaved(t *testing.T) {
	m := newTestModel()
	m.streamCh = make(chan tea.Msg, 4)
	m.state = stateStreaming
	m.taskStatus = &protocol.TaskStatus{ID: "20260930-120000-abcd", State: protocol.TaskStateRunning, Segment: 3}
	updated, _ := m.interruptTurn()
	m = updated.(chatModel)
	if text := transcriptText(m); !strings.Contains(text, "the task is saved") || !strings.Contains(text, "/task resume") {
		t.Errorf("an interrupted task did not say it was saved:\n%s", text)
	}
	if m.taskStatus.State != protocol.TaskStateStopped {
		t.Errorf("state after esc = %q, want stopped", m.taskStatus.State)
	}
}

// Everything the daemon's status carries that a model or a tool wrote is
// sanitised before it reaches the terminal.
func TestTaskLinesAreSanitised(t *testing.T) {
	line := taskEndLine(protocol.TaskStatus{ID: "x", State: protocol.TaskStateBlocked,
		Detail: "needs you \x1b]0;pwned\x07 now"})
	if strings.ContainsRune(line, '\x1b') || strings.ContainsRune(line, '\x07') {
		t.Errorf("a control sequence reached the terminal: %q", line)
	}
}
