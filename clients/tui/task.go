package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// LONG TASKS in the terminal (daemon/longtask.go): /task <goal>, and the
// long-task commands /fix, /debug, /refactor and /hunt, run as one request of
// many segments with a budget. This file is the client's half: the /task
// command, the meter drawn while a task runs, and the lines left in the
// transcript as it moves from segment to segment and when it stops.

// taskIDShape matches a daemon task ID (daemon/taskledger.go, validTaskID).
var taskIDShape = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// taskRunning reports whether a long task is running in this turn.
func (m chatModel) taskRunning() bool {
	return m.taskStatus != nil && m.taskStatus.State == protocol.TaskStateRunning
}

// currentTaskID is the task /task resume|review|discard act on: this session's
// last one, or the workspace's latest saved one.
func (m chatModel) currentTaskID() string {
	if m.taskStatus != nil && m.taskStatus.ID != "" {
		return m.taskStatus.ID
	}
	return protocol.TaskLatest
}

// handleTaskCommand is /task and its subcommands.
//
// A GOAL MAY BEGIN WITH A SUBCOMMAND'S WORD -- "/task review the auth module
// for bugs" is a task, not a review -- so review and discard count as
// subcommands only on their own or with a task ID, and budget only when what
// follows reads as a budget. Anything else is a goal.
func (m chatModel) handleTaskCommand(args string) (tea.Model, tea.Cmd) {
	args = strings.TrimSpace(args)
	sub, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	id := m.currentTaskID()
	if taskIDShape.MatchString(rest) {
		id = rest
	}
	reply := ""
	switch word := strings.ToLower(sub); {
	case word == "" || ((word == "show" || word == "status") && rest == ""):
		reply = m.taskStatusText()
	case word == "resume":
		note := rest
		if taskIDShape.MatchString(rest) || note == "" {
			note = "continue"
		}
		return m.beginTaskTurn("/task "+args, note, "", taskFields{id: id, action: protocol.TaskActionResume, budget: m.taskBudget})
	case word == "review" && (rest == "" || taskIDShape.MatchString(rest)):
		return m.beginTaskTurn("/task "+args, "review", "", taskFields{id: id, action: protocol.TaskActionReview})
	case word == "discard" && (rest == "" || taskIDShape.MatchString(rest)):
		return m.beginTaskTurn("/task "+args, "discard", "", taskFields{id: id, action: protocol.TaskActionDiscard})
	case word == "budget" && (rest == "" || looksLikeTaskBudget(rest)):
		reply = m.setTaskBudget(rest)
	case word == "budget" && triesTaskBudget(rest):
		// A BUDGET MISTYPED IS NOT A GOAL. "/task budget 1 hour" started a paid
		// task whose goal was "budget 1 hour" (FOUND 2026-10-01). A goal about
		// budgets has no number in it; one that does can be worded another way.
		reply = m.setTaskBudget(rest) + "\n(for a task about budgets, word the goal another way)"
	default:
		return m.beginTaskTurn("/task "+args, args, modeTask, taskFields{budget: m.taskBudget})
	}
	m.appendTurn(turn{role: roleAssistant, text: reply})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// setTaskBudget is /task budget [30m] [$1] [200 calls] [default].
func (m *chatModel) setTaskBudget(args string) string {
	if args == "" {
		return "long tasks in this session run on " + describeTaskBudget(m.taskBudget) +
			"\n/task budget 45m $1 200 sets minutes, dollars and model calls · /task budget default"
	}
	if strings.EqualFold(args, "default") || strings.EqualFold(args, "off") {
		m.taskBudget = nil
		return "long tasks in this session run on the daemon's defaults (mcp.budget.task)"
	}
	b, err := parseTaskBudget(args)
	if err != nil {
		return err.Error()
	}
	m.taskBudget = b
	return "long tasks in this session run on " + describeTaskBudget(b) +
		" (the daemon caps each at its ceiling)"
}

func describeTaskBudget(b *protocol.TaskBudget) string {
	if b == nil {
		return "the daemon's defaults (mcp.budget.task)"
	}
	var parts []string
	if b.Minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d min", b.Minutes))
	}
	if b.USD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", b.USD))
	}
	if b.Calls > 0 {
		parts = append(parts, fmt.Sprintf("%d model calls", b.Calls))
	}
	return strings.Join(parts, ", ") + ", and the daemon's defaults for the rest"
}

var taskBudgetField = regexp.MustCompile(`^(\$\d+(\.\d+)?|\d+(\.\d+)?\$|\d+(\.\d+)?(h|m|min|mins)|\d+(calls?)?|calls?)$`)

// looksLikeTaskBudget reports whether every word reads as part of a budget.
func looksLikeTaskBudget(s string) bool {
	fields := strings.Fields(strings.ToLower(s))
	for _, f := range fields {
		if !taskBudgetField.MatchString(f) && f != "default" && f != "off" {
			return false
		}
	}
	return len(fields) > 0
}

// triesTaskBudget reports whether s was meant as a budget that did not parse:
// it holds a number or a dollar sign.
func triesTaskBudget(s string) bool {
	return strings.ContainsAny(s, "0123456789$")
}

// parseTaskBudget reads "45m", "2h", "$1.50" or "1.5$", and a bare number (or
// "200 calls") as model calls.
func parseTaskBudget(s string) (*protocol.TaskBudget, error) {
	b := &protocol.TaskBudget{}
	bad := func(f string) error {
		return fmt.Errorf("%q is not a budget: use minutes (45m, 2h), dollars ($1.50) or model calls (200)", f)
	}
	for _, f := range strings.Fields(strings.ToLower(s)) {
		switch {
		case f == "call" || f == "calls":
			continue
		case strings.HasPrefix(f, "$") || strings.HasSuffix(f, "$"):
			v, err := strconv.ParseFloat(strings.Trim(f, "$"), 64)
			if err != nil || v <= 0 {
				return nil, bad(f)
			}
			b.USD = v
		case strings.HasSuffix(f, "h"):
			v, err := strconv.ParseFloat(strings.TrimSuffix(f, "h"), 64)
			if err != nil || v <= 0 {
				return nil, bad(f)
			}
			b.Minutes = int(v * 60)
		case strings.HasSuffix(f, "mins"), strings.HasSuffix(f, "min"), strings.HasSuffix(f, "m"):
			v, err := strconv.Atoi(strings.TrimRight(f, "mins"))
			if err != nil || v <= 0 {
				return nil, bad(f)
			}
			b.Minutes = v
		default:
			v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(f, "calls"), "call"))
			if err != nil || v <= 0 {
				return nil, bad(f)
			}
			b.Calls = v
		}
	}
	if *b == (protocol.TaskBudget{}) {
		return nil, bad(s)
	}
	return b, nil
}

// handleTaskStatus records a long task's progress, and leaves a line in the
// transcript when a new segment starts and when the task stops.
func (m chatModel) handleTaskStatus(msg taskStatusMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	st := msg.status
	prev := m.taskStatus
	m.taskStatus = &st
	switch {
	case st.State == protocol.TaskStateRunning && (prev == nil || prev.Segment != st.Segment):
		m.appendTurn(turn{role: roleSystem, text: taskSegmentLine(st, prev == nil)})
	case st.State != protocol.TaskStateRunning && prev != nil:
		// Only for a run watched here: a /task review's status arrives with no
		// run before it, and its own summary already said where the task is.
		m.appendTurn(turn{role: roleSystem, text: taskEndLine(st)})
	}
	m.refreshViewport()
	return m, waitForNext(m.streamCh)
}

func shortTaskID(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 {
		return id[i+1:]
	}
	return id
}

func taskSegmentLine(st protocol.TaskStatus, first bool) string {
	if first && st.Segment == 1 {
		return fmt.Sprintf("▶ %s task %s started — budget %d min, $%.2f, %d model calls · esc stops it (it is saved)",
			sanitizeText(st.Mode), st.ID, st.MaxSeconds/60, st.MaxUSD, st.MaxCalls)
	}
	if first {
		return fmt.Sprintf("▶ %s task %s resumed at segment %d — budget %d min, $%.2f, %d model calls",
			sanitizeText(st.Mode), st.ID, st.Segment, st.MaxSeconds/60, st.MaxUSD, st.MaxCalls)
	}
	return fmt.Sprintf("— segment %d · %s —", st.Segment, taskSpentText(st))
}

// taskSpentText is what a run has spent, against its budget when the status
// carries one (a /task review's status carries the task's total, and none).
func taskSpentText(st protocol.TaskStatus) string {
	if st.MaxCalls == 0 {
		return fmt.Sprintf("%d calls · $%.2f · %d min", st.Calls, st.USD, st.Seconds/60)
	}
	return fmt.Sprintf("%d/%d calls · $%.2f/$%.2f · %d/%d min",
		st.Calls, st.MaxCalls, st.USD, st.MaxUSD, st.Seconds/60, st.MaxSeconds/60)
}

// taskStateWords says a task's state the way a person would.
func taskStateWords(state string) string {
	switch state {
	case protocol.TaskStateRunning:
		return "running"
	case protocol.TaskStateFinished:
		return "finished"
	case protocol.TaskStateBlocked:
		return "waiting for you"
	case protocol.TaskStateStuck:
		return "stuck"
	case protocol.TaskStateBudget:
		return "paused at its budget"
	case protocol.TaskStateStopped:
		return "stopped"
	case protocol.TaskStateFailed:
		return "stopped by a provider failure"
	}
	return sanitizeText(state)
}

func taskEndLine(st protocol.TaskStatus) string {
	head := map[string]string{
		protocol.TaskStateFinished: "✓ task %s finished",
		protocol.TaskStateBlocked:  "⚑ task %s needs you",
		protocol.TaskStateStuck:    "⚠ task %s stopped: it was going in circles",
		protocol.TaskStateBudget:   "⏸ task %s paused at its budget",
		protocol.TaskStateFailed:   "✗ task %s stopped: the provider failed",
		protocol.TaskStateStopped:  "⏹ task %s stopped",
	}[st.State]
	var b strings.Builder
	if head == "" {
		// An unknown state is text, never part of the format: a '%' in it
		// would garble the line.
		fmt.Fprintf(&b, "task %s: %s", st.ID, sanitizeText(st.State))
	} else {
		fmt.Fprintf(&b, head, st.ID)
	}
	fmt.Fprintf(&b, " after %d segment(s) · %s", st.Segment, taskSpentText(st))
	if st.FilesChanged > 0 {
		fmt.Fprintf(&b, " · %d file(s) changed", st.FilesChanged)
	}
	if st.Findings > 0 {
		fmt.Fprintf(&b, " · %d finding(s)", st.Findings)
	}
	if d := strings.TrimSpace(sanitizeText(st.Detail)); d != "" {
		b.WriteString("\n  " + d)
	}
	if st.State != protocol.TaskStateFinished {
		b.WriteString("\n  /task resume [note] carries on · /task review shows its changes")
	}
	return b.String()
}

// renderTaskMeter is the line drawn under the transcript while a task runs.
func renderTaskMeter(st protocol.TaskStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "task %s · %s · segment %d · %s", shortTaskID(st.ID), sanitizeText(st.Mode), st.Segment, taskSpentText(st))
	if st.LastCheck != "" {
		mark := "✗"
		switch {
		case st.LastCheckPassed && st.LastCheckStale:
			mark = "!" // it passed, but the changes went on after it
		case st.LastCheckPassed:
			mark = "✓"
		}
		fmt.Fprintf(&b, "\nlast check %s %s", mark, sanitizeText(st.LastCheck))
		if st.LastCheckStale {
			b.WriteString(" (before the last edit)")
		}
		if st.FilesChanged > 0 {
			fmt.Fprintf(&b, " · %d file(s) changed", st.FilesChanged)
		}
	}
	return helpStyle.Render(b.String())
}

// taskStatusText is what a bare /task shows.
func (m chatModel) taskStatusText() string {
	var b strings.Builder
	if st := m.taskStatus; st == nil {
		b.WriteString("no task in this session. /task <goal> starts one; /fix, /debug, /refactor and /hunt " +
			"run as tasks too. /task resume picks up this workspace's latest saved task.")
	} else {
		fmt.Fprintf(&b, "task %s (%s) is %s after %d segment(s) · %s", st.ID, sanitizeText(st.Mode),
			taskStateWords(st.State), st.Segment, taskSpentText(*st))
		if goal := strings.TrimSpace(sanitizeText(st.Goal)); goal != "" {
			b.WriteString("\n  goal: " + goal)
		}
		if st.LastCheck != "" {
			verdict := "failed"
			if st.LastCheckPassed {
				verdict = "passed"
			}
			if st.LastCheckStale {
				verdict += ", before the last edit"
			}
			fmt.Fprintf(&b, "\n  last check: %s — %s", sanitizeText(st.LastCheck), verdict)
		}
		if d := strings.TrimSpace(sanitizeText(st.Detail)); d != "" {
			b.WriteString("\n  " + d)
		}
	}
	b.WriteString("\nbudget: " + describeTaskBudget(m.taskBudget))
	b.WriteString("\n\n/task <goal> · /task resume [note] · /task review · /task discard · /task budget 45m $1 200")
	return b.String()
}
