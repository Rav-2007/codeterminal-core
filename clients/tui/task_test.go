package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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

// A STALE CHECK IS NEVER A TICK on the meter or in /task, and an unknown state
// is text, never a format verb.
//
// Neuter checks: drop the LastCheckPassed && LastCheckStale case in
// renderTaskMeter; or put the state back into taskEndLine's format string.
func TestAStaleTaskCheckIsNeverATick(t *testing.T) {
	st := protocol.TaskStatus{ID: "20261001-093501-5027", Mode: "fix", State: protocol.TaskStateRunning, Segment: 2,
		LastCheck: "go test ./...", LastCheckPassed: true, LastCheckStale: true}
	meter := renderTaskMeter(st)
	if strings.Contains(meter, "✓") || !strings.Contains(meter, "before the last edit") {
		t.Errorf("a stale passing check on the meter: %q", meter)
	}
	st.LastCheckStale = false
	if meter := renderTaskMeter(st); !strings.Contains(meter, "✓") {
		t.Errorf("a current passing check lost its tick: %q", meter)
	}
	odd := taskEndLine(protocol.TaskStatus{ID: "20261001-093501-5027", State: "weird%s%d", Segment: 1})
	if strings.Contains(odd, "MISSING") || strings.Contains(odd, "%!") || !strings.Contains(odd, "weird%s%d") {
		t.Errorf("an unknown state garbled the end line: %q", odd)
	}
}

// WHAT THE JUDGE DID IS ON THE METER: a user whose files were put back by the
// daemon sees that it happened, not only that they changed. A daemon's words
// are sanitized like every other field; nothing is drawn when it did nothing.
//
// Neuter check: drop the judge line from renderTaskMeter.
func TestTheMeterSaysWhatTheJudgeDid(t *testing.T) {
	st := protocol.TaskStatus{ID: "20261015-093501-5027", Mode: "fix", State: protocol.TaskStateRunning, Segment: 3,
		Judge: "1 restore · 2 re-checks\x1b[2J"}
	meter := renderTaskMeter(st)
	if !strings.Contains(meter, "judge: 1 restore · 2 re-checks") || strings.Contains(meter, "\x1b[2J") {
		t.Errorf("meter: %q", meter)
	}
	st.Judge = ""
	if meter := renderTaskMeter(st); strings.Contains(meter, "judge") {
		t.Errorf("a judge that did nothing is drawn: %q", meter)
	}
}

// A MISTYPED BUDGET STARTS NOTHING. "/task budget 1 hour" started a paid task
// whose goal was "budget 1 hour"; it now says what a budget looks like. A goal
// that merely begins with the word -- no number in it -- is still a goal
// (TestTheTaskSubcommandsReachTheWire).
//
// Neuter check: drop the triesTaskBudget case from handleTaskCommand.
func TestAMistypedTaskBudgetStartsNothing(t *testing.T) {
	for _, args := range []string{"budget 1 hour", "budget 45 m", "budget $", "budget 30 minutes please"} {
		next, cmd := newTestModel().handleTaskCommand(args)
		if cmd != nil {
			t.Errorf("/task %s started a turn", args)
			continue
		}
		m := next.(chatModel)
		if len(m.turns) == 0 || !strings.Contains(m.turns[len(m.turns)-1].text, "is not a budget") {
			t.Errorf("/task %s did not say what a budget looks like", args)
		}
		if m.taskBudget != nil {
			t.Errorf("/task %s set a budget: %+v", args, m.taskBudget)
		}
	}
}

// driveTurn runs a turn that Enter started through Update, the way the runtime
// does: every message the stream produces goes in, and the commands that come
// back are run, until the stream has ended.
func driveTurn(t *testing.T, m chatModel, cmd tea.Cmd) chatModel {
	t.Helper()
	msgs := make(chan tea.Msg, 64)
	var run func(c tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range batch {
					run(sub)
				}
				return
			}
			msgs <- msg
		}()
	}
	run(cmd)
	deadline := time.After(10 * time.Second)
	for m.streamCh != nil {
		select {
		case msg := <-msgs:
			updated, next := m.Update(msg)
			m = updated.(chatModel)
			run(next)
		case <-deadline:
			t.Fatalf("the turn never ended; state %v, turns %+v", m.state, m.turns)
		}
	}
	return m
}

// /task review, /task resume and /task discard WITH NOTHING SAVED are answered,
// not failed. The daemon says "there is no saved task for this workspace" as an
// error, and the terminal app drew it as one: a "You: /task review" turn, a LINK
// SEVERED banner whose second line said "the prompt was sent, but no answer came
// back", and the header's error state (Phase 3 audit, 2026-11-09).
//
// Through a fake daemon that answers exactly as the real one does
// (serveTaskAction's fail()), driven through Update.
//
// Neuter check: remove the noSavedTaskNote branch in handleStreamErr and the
// turn ends in stateError with the banner.
func TestATaskActionWithNoSavedTaskIsAnAnswerNotAnError(t *testing.T) {
	for _, cmdline := range []string{"/task review", "/task resume", "/task discard"} {
		t.Run(cmdline, func(t *testing.T) {
			fakeDaemonReplying(t, protocol.TokenResponse{
				Done: true, Error: "task: there is no saved task for this workspace", ErrorClass: "invalid_request",
			})
			m := typeText(newTestModel(), cmdline)
			m, cmd := pressEnter(m)
			m = driveTurn(t, m, cmd)

			if m.state != stateIdle || m.statusErr != "" {
				t.Errorf("state %v, statusErr %q; want a ready prompt and no error", m.state, m.statusErr)
			}
			for _, tn := range m.turns {
				if tn.role == roleUser || tn.role == roleSevered || tn.role == roleUnanswered {
					t.Errorf("a %v turn was left in the transcript: %q", tn.role, tn.text)
				}
			}
			if len(m.turns) != 1 || m.turns[0].role != roleAssistant || !m.turns[0].local ||
				m.turns[0].text != "no saved task for this workspace" {
				t.Fatalf("turns = %+v, want one local answer %q", m.turns, "no saved task for this workspace")
			}
			screen := ansi.Strip(m.View())
			if strings.Contains(screen, "LINK SEVERED") || strings.Contains(screen, "error:") {
				t.Errorf("the screen still shows a failure:\n%s", screen)
			}
			if !strings.Contains(screen, "no saved task for this workspace") {
				t.Errorf("the screen does not say it:\n%s", screen)
			}
			// A local answer is not part of the conversation the model is sent.
			if h := buildHistory(m.turns); len(h) != 0 {
				t.Errorf("the answer went into the model's history: %+v", h)
			}
		})
	}
}

// A task ID that is not there says so in the daemon's own words.
func TestANamedTaskThatIsNotSavedIsAnswered(t *testing.T) {
	fakeDaemonReplying(t, protocol.TokenResponse{
		Done: true, Error: "task: there is no saved task 20260930-120000-abcd in this workspace", ErrorClass: "invalid_request",
	})
	m := typeText(newTestModel(), "/task review 20260930-120000-abcd")
	m, cmd := pressEnter(m)
	m = driveTurn(t, m, cmd)
	if len(m.turns) != 1 || m.turns[0].text != "no saved task 20260930-120000-abcd in this workspace" || m.state != stateIdle {
		t.Errorf("state %v, turns %+v", m.state, m.turns)
	}
}

// What is NOT "no saved task" stays the failure it was: a task that is running,
// and a goal's own errors, which are not task actions at all.
func TestOtherTaskFailuresAreStillFailures(t *testing.T) {
	t.Run("a task action refused for another reason", func(t *testing.T) {
		fakeDaemonReplying(t, protocol.TokenResponse{
			Done: true, Error: "task: task 20260930-120000-abcd is running; stop it first", ErrorClass: "invalid_request",
		})
		m := typeText(newTestModel(), "/task discard")
		m, cmd := pressEnter(m)
		m = driveTurn(t, m, cmd)
		if m.state != stateError || !strings.Contains(m.statusErr, "is running") {
			t.Errorf("state %v, statusErr %q; want the failure kept", m.state, m.statusErr)
		}
	})
	t.Run("a new goal whose error happens to say it", func(t *testing.T) {
		fakeDaemonReplying(t, protocol.TokenResponse{
			Done: true, Error: "the model said there is no saved task", ErrorClass: "provider_error",
		})
		m := typeText(newTestModel(), "/task add retries to the fetcher")
		m, cmd := pressEnter(m)
		m = driveTurn(t, m, cmd)
		if m.state != stateError {
			t.Errorf("state %v; a goal's failure was swallowed as an answer", m.state)
		}
	})
}

// /task budget with a WORD that is not a budget is refused, like "0" and "-5m"
// are. "/task budget abc" had no number in it, so it fell through as a goal and
// started a billed long task whose goal was "budget abc" (Phase 3 audit).
//
// Neuter check: put triesTaskBudget back to the digit-and-dollar test and "abc"
// starts a task (a request reaches the daemon and the budget stays unset).
func TestATaskBudgetThatIsNotABudgetIsRefused(t *testing.T) {
	for _, arg := range []string{"abc", "soon", "unlimited", "0", "-5m", "1 hour"} {
		t.Run(arg, func(t *testing.T) {
			m := typeText(newTestModel(), "/task budget "+arg)
			m, _ = pressEnter(m)
			if m.state != stateIdle || m.streamCh != nil {
				t.Fatalf("state %v: /task budget %s started a turn", m.state, arg)
			}
			if m.taskBudget != nil {
				t.Errorf("the session's budget was set to %+v", m.taskBudget)
			}
			var said string
			for _, tn := range m.turns {
				if tn.role == roleUser {
					t.Errorf("a You: turn was drawn for a refused command: %q", tn.text)
				}
				said += tn.text
			}
			if !strings.Contains(said, "is not a budget") || !strings.Contains(said, "45m") {
				t.Errorf("the refusal does not say what a budget looks like: %q", said)
			}
		})
	}
}

// And the goals that merely start with "budget" are still goals.
func TestAGoalThatStartsWithBudgetIsStillAGoal(t *testing.T) {
	for _, raw := range []string{"/task budget the quarterly report generator", "/task budget tracker for the team"} {
		_, req := typedRequest(t, newTestModel(), raw)
		if req.Mode != modeTask || !strings.HasPrefix(req.Prompt, "budget ") {
			t.Errorf("%s: wire = mode %q, prompt %q; want a task whose goal is the words typed", raw, req.Mode, req.Prompt)
		}
	}
}
