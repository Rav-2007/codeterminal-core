package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mochiii/editapply"
	"mochiii/protocol"
)

// THE TASK LEDGER is a long task's memory (longtask.go): what the goal is, the
// plan, what has been established and with what evidence, what the last check
// said, and the handoff one segment leaves the next. Each segment starts from a
// small fresh context built from it, so the context a call carries stays
// bounded however long the task runs -- and the ledger, not the transcript, is
// what carries the work forward.
//
// ON DISK, NOT IN THE PROJECT: under StateDir()/tasks/<workspace tag>/<id>/,
// beside conversation memory and for the same reason (memory.go): a task's
// record must never be something the project's own repository could commit.
// Saved after every segment, with the working copy's net diff beside it, so a
// stop or a restart loses at most the segment in flight.
//
// WHAT IS IN IT IS UNTRUSTED where the model or a tool wrote it -- the plan,
// the findings, the handoff, a command's output -- and it goes back to the
// model in every segment. So it is rendered through neutralizeDelimiters and
// fenced as reference (renderTaskState), never as instructions.

// taskLedgerVersion is the on-disk format. A ledger from a newer daemon is
// refused rather than half-read.
const taskLedgerVersion = 1

// Bounds on what one ledger holds, so the block each segment carries stays
// small and a model cannot grow it without limit.
const (
	maxTaskGoalRunes     = 8000
	maxTaskNotes         = 10
	maxTaskNoteRunes     = 1500
	maxTaskFindings      = 40
	maxTaskClaimRunes    = 400
	maxTaskEvidenceRunes = 500
	maxTaskHandoffRunes  = 2500
	maxTaskOutputRunes   = 1200
	maxTaskListedFiles   = 30
	// maxTaskDiffBytes bounds reading a saved diff back. The diff is the task's
	// own net change, so this is far above any real one.
	maxTaskDiffBytes = 32 << 20
)

type taskLedger struct {
	Version   int                 `json:"version"`
	ID        string              `json:"id"`
	Workspace string              `json:"workspace"`
	Mode      string              `json:"mode"`
	Goal      string              `json:"goal"`
	Notes     []string            `json:"notes,omitempty"`
	Plan      []protocol.TaskItem `json:"plan,omitempty"`
	Findings  []taskFinding       `json:"findings,omitempty"`
	Handoff   string              `json:"handoff,omitempty"`
	LastCheck *taskCheck          `json:"last_check,omitempty"`
	// FilesChanged are the working copy's changed files at the last save.
	FilesChanged []string `json:"files_changed,omitempty"`
	Segments     int      `json:"segments"`
	Runs         int      `json:"runs"`
	// Spent is every run's spend together; a run's own budget is its own.
	Spent   taskSpend `json:"spent"`
	State   string    `json:"state"`
	Detail  string    `json:"detail,omitempty"`
	Summary string    `json:"summary,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

type taskFinding struct {
	ID       string `json:"id"`
	Claim    string `json:"claim"`
	Evidence string `json:"evidence"`
}

// taskCheck is the last command the task ran in its working copy.
type taskCheck struct {
	Command string `json:"command"`
	Passed  bool   `json:"passed"`
	// AfterLastEdit is whether nothing was edited since it ran: the only kind
	// of passing run that says anything about the code as it now is.
	AfterLastEdit bool   `json:"after_last_edit"`
	Output        string `json:"output,omitempty"`
}

type taskSpend struct {
	Calls   int     `json:"calls"`
	USD     float64 `json:"usd"`
	Seconds int     `json:"seconds"`
}

// taskDiffBlock is one saved edit: the working copy's net change to one part
// of one file, as the review would offer it.
type taskDiffBlock struct {
	Path    string `json:"path"`
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

var taskIDPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// validTaskID is the whole check a client-supplied ID gets before it names a
// directory: a fixed shape with no separators, so it cannot name anything but
// one task's folder.
func validTaskID(id string) bool { return taskIDPattern.MatchString(id) }

func newTaskID(now time.Time) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return now.Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// taskRootDir is where this workspace's tasks live.
func taskRootDir(realRoot string) (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "tasks", protocol.WorkspaceTag(realRoot)), nil
}

func taskDir(realRoot, id string) (string, error) {
	if !validTaskID(id) {
		return "", fmt.Errorf("%q is not a task ID", id)
	}
	root, err := taskRootDir(realRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

func newTaskLedger(realRoot, mode, goal string, now time.Time) *taskLedger {
	return &taskLedger{
		Version:   taskLedgerVersion,
		ID:        newTaskID(now),
		Workspace: realRoot,
		Mode:      mode,
		Goal:      truncateRunes(strings.TrimSpace(goal), maxTaskGoalRunes),
		State:     protocol.TaskStateRunning,
		Created:   now,
		Updated:   now,
	}
}

var errNoTasks = errors.New("there is no saved task for this workspace")

// latestTaskID is the most recently saved task in this workspace.
func latestTaskID(realRoot string) (string, error) {
	root, err := taskRootDir(realRoot)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", errNoTasks
	}
	if err != nil {
		return "", err
	}
	best, bestAt := "", time.Time{}
	for _, e := range entries {
		if !e.IsDir() || !validTaskID(e.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(root, e.Name(), "ledger.json"))
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestAt) {
			best, bestAt = e.Name(), info.ModTime()
		}
	}
	if best == "" {
		return "", errNoTasks
	}
	return best, nil
}

// loadTaskLedger reads a saved task. id may be protocol.TaskLatest.
func loadTaskLedger(realRoot, id string) (*taskLedger, error) {
	if id == protocol.TaskLatest {
		latest, err := latestTaskID(realRoot)
		if err != nil {
			return nil, err
		}
		id = latest
	}
	dir, err := taskDir(realRoot, id)
	if err != nil {
		return nil, err
	}
	data, size, err := readBoundedFile(filepath.Join(dir, "ledger.json"), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("there is no saved task %s in this workspace", id)
	}
	if err != nil {
		return nil, fmt.Errorf("task %s cannot be read: %v", id, err)
	}
	if size > 1<<20 {
		return nil, fmt.Errorf("task %s's ledger is larger than it can be", id)
	}
	var l taskLedger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("task %s's ledger is damaged: %v", id, err)
	}
	if l.Version != taskLedgerVersion {
		return nil, fmt.Errorf("task %s was saved by a different version of Mochiii (format %d)", id, l.Version)
	}
	// The folder named it; the ledger must agree, and must belong here.
	if l.ID != id || l.Workspace != realRoot {
		return nil, fmt.Errorf("task %s does not belong to this workspace", id)
	}
	return &l, nil
}

// save writes the ledger, and the working copy's net diff when blocks is not
// nil, atomically.
func (l *taskLedger) save(blocks []editapply.EditBlock) error {
	dir, err := taskDir(l.Workspace, l.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if blocks != nil {
		saved := make([]taskDiffBlock, 0, len(blocks))
		for _, b := range blocks {
			saved = append(saved, taskDiffBlock{Path: b.FilePath, Search: b.Search, Replace: b.Replace})
		}
		data, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		if err := writeTaskFile(dir, "diff.json", data); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		return err
	}
	return writeTaskFile(dir, "ledger.json", data)
}

// loadTaskDiff reads a task's saved net diff; none saved is an empty diff.
func loadTaskDiff(realRoot, id string) ([]editapply.EditBlock, error) {
	dir, err := taskDir(realRoot, id)
	if err != nil {
		return nil, err
	}
	data, size, err := readBoundedFile(filepath.Join(dir, "diff.json"), maxTaskDiffBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if size > maxTaskDiffBytes {
		return nil, fmt.Errorf("task %s's saved changes are larger than %d MB", id, maxTaskDiffBytes>>20)
	}
	var saved []taskDiffBlock
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("task %s's saved changes are damaged: %v", id, err)
	}
	blocks := make([]editapply.EditBlock, 0, len(saved))
	for _, b := range saved {
		blocks = append(blocks, editapply.EditBlock{FilePath: b.Path, Search: b.Search, Replace: b.Replace})
	}
	return blocks, nil
}

// discardTask deletes a saved task.
func discardTask(realRoot, id string) error {
	dir, err := taskDir(realRoot, id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "ledger.json")); err != nil {
		return fmt.Errorf("there is no saved task %s in this workspace", id)
	}
	return os.RemoveAll(dir)
}

// writeTaskFile replaces dir/name atomically, readable by the user alone.
func writeTaskFile(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}

// addNote records a later instruction from the user, oldest dropped first.
func (l *taskLedger) addNote(note string) {
	note = truncateRunes(strings.TrimSpace(note), maxTaskNoteRunes)
	if note == "" {
		return
	}
	l.Notes = append(l.Notes, note)
	if len(l.Notes) > maxTaskNotes {
		l.Notes = l.Notes[len(l.Notes)-maxTaskNotes:]
	}
}

// setHandoff keeps the end of what a segment said last: its conclusion and next
// step, which is where a handoff puts them.
func (l *taskLedger) setHandoff(text string) {
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > maxTaskHandoffRunes {
		text = "…" + string(r[len(r)-maxTaskHandoffRunes+1:])
	}
	l.Handoff = text
}

// renderTaskState is the <task_state> block a segment starts from. Every piece
// the model or a tool wrote goes through neutralizeDelimiters, so none of it
// can close the block, forge the user's request or open a fence of its own.
func (l *taskLedger) renderTaskState(b taskBudget, run taskSpend, segment int) string {
	var s strings.Builder
	s.WriteString(taskStateOpenTag + "\n")
	fmt.Fprintf(&s, "Task %s (%s), segment %d. This run has used %d of %d model calls, $%.2f of $%.2f, "+
		"and %d of %d minutes.\n", l.ID, l.Mode, segment, run.Calls, b.calls, run.USD, b.usd,
		run.Seconds/60, b.minutes)

	if len(l.Plan) > 0 {
		s.WriteString("\nPlan:\n")
		for _, t := range l.Plan {
			fmt.Fprintf(&s, "- [%s] %s. %s\n", t.Status, neutralizeDelimiters(t.ID), neutralizeDelimiters(t.Title))
		}
	}
	if len(l.Findings) > 0 {
		s.WriteString("\nFindings, each with its evidence:\n")
		for _, f := range l.Findings {
			fmt.Fprintf(&s, "- %s: %s -- evidence: %s\n", f.ID, neutralizeDelimiters(f.Claim), neutralizeDelimiters(f.Evidence))
		}
	}
	if len(l.FilesChanged) > 0 {
		files := l.FilesChanged
		more := ""
		if len(files) > maxTaskListedFiles {
			more = fmt.Sprintf(" and %d more", len(files)-maxTaskListedFiles)
			files = files[:maxTaskListedFiles]
		}
		fmt.Fprintf(&s, "\nFiles changed in the working copy so far: %s%s\n",
			neutralizeDelimiters(strings.Join(files, ", ")), more)
	}
	if c := l.LastCheck; c != nil {
		verdict := "FAILED"
		if c.Passed {
			verdict = "passed"
		}
		when := "before your last edit, so it says nothing about the code as it now is"
		if c.AfterLastEdit {
			when = "after your last edit"
		}
		fmt.Fprintf(&s, "\nLast check: `%s` %s, %s.\n", neutralizeDelimiters(c.Command), verdict, when)
		if c.Output != "" && !c.Passed {
			s.WriteString("Its last lines:\n" + neutralizeDelimiters(c.Output) + "\n")
		}
	}
	if len(l.Notes) > 0 {
		s.WriteString("\nWhat the user added since the task began:\n")
		for _, n := range l.Notes {
			s.WriteString("- " + neutralizeDelimiters(n) + "\n")
		}
	}
	if l.Handoff != "" {
		s.WriteString("\nYour handoff from the last segment:\n" + neutralizeDelimiters(l.Handoff) + "\n")
	}
	s.WriteString(taskStateCloseTag)
	return s.String()
}

// status is the TaskStatus a client shows for this ledger.
func (l *taskLedger) status(b taskBudget, run taskSpend, segment int) protocol.TaskStatus {
	st := protocol.TaskStatus{
		ID: l.ID, Mode: l.Mode, Goal: truncateRunes(l.Goal, 120), State: l.State, Segment: segment,
		Calls: run.Calls, MaxCalls: b.calls, USD: run.USD, MaxUSD: b.usd,
		Seconds: run.Seconds, MaxSeconds: b.minutes * 60,
		FilesChanged: len(l.FilesChanged), Findings: len(l.Findings), Detail: l.Detail,
	}
	if c := l.LastCheck; c != nil {
		st.LastCheck, st.LastCheckPassed, st.LastCheckStale = c.Command, c.Passed, !c.AfterLastEdit
	}
	return st
}

// changedFiles lists the working copy's changed files, sorted.
func changedFiles(st *stagedWorkspace) []string {
	if st == nil {
		return nil
	}
	files := make([]string, 0, len(st.touched))
	for rel := range st.touched {
		files = append(files, filepath.ToSlash(rel))
	}
	sort.Strings(files)
	return files
}
