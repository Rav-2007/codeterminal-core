package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
	"mochiii/protocol"
)

// SPECS: the user's agreed description of a change -- what it does, what is in
// and out of scope, the edge cases, and how to tell it is done -- kept as a
// Markdown file in the project's specs/ folder, where it is reviewed and
// versioned like code.
//
// Three things use it:
//
//   - /spec <goal> (mode "spec") WRITES one. The agent may look at the code but
//     not change it, run it or reach the network; the only file it can write is
//     under specs/. The spec arrives through the ordinary edit review.
//   - An ACTIVE spec (PromptRequest.Spec) is read at the start of every turn and
//     put in the system message, so the work stays anchored to what was agreed
//     rather than to what the model remembers of it.
//   - /spec check (mode "check") GRADES the project against it: each success
//     criterion is recorded met, unmet or unknown through record_criterion, and
//     "met" is accepted only with evidence the daemon can verify. Commands run
//     in a working copy that is thrown away, so checking changes nothing.

const (
	modeSpec  = "spec"
	modeCheck = "check"
	modeBuild = "build"
)

func isSpecMode(mode string) bool  { return strings.ToLower(strings.TrimSpace(mode)) == modeSpec }
func isCheckMode(mode string) bool { return strings.ToLower(strings.TrimSpace(mode)) == modeCheck }
func isBuildMode(mode string) bool { return strings.ToLower(strings.TrimSpace(mode)) == modeBuild }

// needsSpec reports whether a mode cannot run without an active spec.
func needsSpec(mode string) bool { return isCheckMode(mode) || isBuildMode(mode) }

// modeWithholds reports whether a mode withholds a built-in tool. Plan and spec
// modes run nothing and reach nothing (planModeDenies: every capability flag);
// check mode may run the project's own build and tests -- that is how it checks
// -- but not reach the network or start other programs.
func modeWithholds(mode string, t mcp.Tool) bool {
	switch {
	case isPlanMode(mode), isSpecMode(mode):
		return planModeDenies(t)
	case isCheckMode(mode):
		return t.ReachesNetwork || t.LaunchesSubprocess
	case isLongTaskMode(mode):
		// A long task runs unattended for many segments, working on the local
		// project: web pages are the one intake it does not need, and every
		// page is text an attacker may have written. Withholding the network
		// tools also keeps the menu inside max_advertised_tools once the task's
		// own tools join it (longtask.go).
		return t.ReachesNetwork
	}
	return false
}

// modeWithholdsLaneB reports whether a mode connects no third-party servers.
// All three restricted modes: a third-party tool can do anything, which is the
// opposite of what each of them promises.
func modeWithholdsLaneB(mode string) bool {
	return isPlanMode(mode) || isSpecMode(mode) || isCheckMode(mode)
}

// specsDir is the one folder specs live in, and the only place spec mode can
// write.
const specsDir = "specs"

// maxSpecBytes bounds a spec read into every turn's system message.
const maxSpecBytes = 32 << 10

// specRelPath accepts "specs/<name>.md" (at any depth under specs/) and returns
// it slash-separated and cleaned. ok is false for anything else.
func specRelPath(p string) (string, bool) {
	p = filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if !strings.HasPrefix(p, specsDir+"/") || !strings.EqualFold(filepath.Ext(p), ".md") {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "" {
			return "", false
		}
	}
	return p, true
}

// activeSpec is a spec read for one turn.
type activeSpec struct {
	Path     string // specs/..., slash-separated
	Content  string
	Criteria []specCriterion
}

// specCriterion is one success criterion: a checkbox line with an ID,
//
//   - [ ] C1: `slug.Slugify("a b")` returns "a-b" -- check: go test ./slug
type specCriterion struct {
	ID, Text string
	Done     bool
	Line     int // 0-based line index in Content
}

var criterionLine = regexp.MustCompile(`^\s*[-*]\s+\[([ xX])\]\s+(C\d+)\s*[:.)\-\x{2013}\x{2014}]\s*(.+?)\s*$`)

func parseCriteria(content string) []specCriterion {
	var out []specCriterion
	seen := map[string]bool{}
	for i, line := range strings.Split(content, "\n") {
		m := criterionLine.FindStringSubmatch(line)
		if m == nil || seen[strings.ToUpper(m[2])] {
			continue
		}
		id := strings.ToUpper(m[2])
		seen[id] = true
		out = append(out, specCriterion{ID: id, Text: m[3], Done: m[1] != " ", Line: i})
	}
	return out
}

// loadSpec reads the spec a request names, confined to the project's specs/
// folder and bounded, the way every other read of the project is.
func loadSpec(realRoot, p string) (*activeSpec, error) {
	rel, ok := specRelPath(p)
	if !ok {
		return nil, fmt.Errorf("%q is not a spec: specs are .md files under %s/", p, specsDir)
	}
	if _, err := os.Lstat(filepath.Join(realRoot, filepath.FromSlash(rel))); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist", rel)
	}
	full, err := editapply.ResolveSafeTargetPath(realRoot, filepath.FromSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", rel, err)
	}
	data, size, err := readBoundedFile(full, maxSpecBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s does not exist", rel)
		}
		return nil, fmt.Errorf("%s cannot be read", rel)
	}
	if size > maxSpecBytes {
		return nil, fmt.Errorf("%s is %d bytes; a spec can be at most %d", rel, size, maxSpecBytes)
	}
	content := string(data)
	return &activeSpec{Path: rel, Content: content, Criteria: parseCriteria(content)}, nil
}

// Tag text for the spec envelope. Registered in protectedTagFamilies, so a spec
// that contains "</active_spec>" cannot close its own fence.
const (
	activeSpecOpenTagPrefix = "<active_spec path=\""
	activeSpecCloseTag      = "</active_spec>"
)

// specAnchor is what an active spec adds to a turn's system message.
func specAnchor(sp *activeSpec) string {
	var b strings.Builder
	b.WriteString("THE AGREED SPEC FOR THIS WORK is below: the user reviewed and accepted it. Work to it. " +
		"Stay within its scope; handle its edge cases; its success criteria are how the work will be " +
		"judged. If the code shows the spec is wrong or incomplete, say so plainly and propose an edit " +
		"to the spec file rather than silently doing something different.\n\n")
	b.WriteString(activeSpecOpenTagPrefix + sanitiseTagAttribute(sp.Path) + "\">\n")
	b.WriteString(neutralizeDelimiters(sp.Content))
	if !strings.HasSuffix(sp.Content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(activeSpecCloseTag)
	return b.String()
}

// specModeDirective is what /spec adds to the system message.
const specModeDirective = "THE USER WANTS A SPEC, NOT CODE. Write a specification for the goal they describe, " +
	"as ONE new Markdown file under specs/ (for example specs/verbose-flag.md), created with propose_edit " +
	"and an empty search. You may read the project first -- do, so the spec names real files and " +
	"functions -- but you cannot change anything else, run anything or use the network in this mode.\n\n" +
	"Use exactly these sections:\n" +
	"# <title>\n" +
	"## Goal -- one or two sentences: what the user wants and why.\n" +
	"## Behaviour -- what the finished change does, concretely, from the user's side.\n" +
	"## Scope -- **In:** what this change covers. **Out:** what it deliberately does not.\n" +
	"## Edge cases -- each one, and what should happen.\n" +
	"## Success criteria -- a checklist. Each item on its own line exactly like\n" +
	"- [ ] C1: <one observable, checkable fact> -- check: <how: a command such as `go test ./x -run TestY`, or what to look at>\n" +
	"numbered C1, C2, ... Every criterion must be something a person or a test can verify; " +
	"prefer ones a test can check.\n" +
	"## Tasks -- the ordered steps to build it, as a checklist (- [ ] ...), tests first.\n\n" +
	"Keep it short and specific to this project. After proposing the file, say in a sentence or two " +
	"what the spec covers; do not repeat it."

// checkModeDirective is what /spec check adds to the system message.
const checkModeDirective = "THE USER WANTS THE WORK CHECKED AGAINST THE SPEC. Do not change anything: " +
	"for EACH success criterion in the spec, find out whether the project meets it, then call " +
	"record_criterion with its ID, a status (met, unmet or unknown) and your evidence. Run the check the " +
	"criterion names with sandbox_exec where there is one -- commands run in a throwaway copy of the " +
	"project -- or read the code. \"met\" is accepted only with evidence that can be verified: the exact " +
	"command you ran this turn that passed, or a file:line in the project that shows it. Record every " +
	"criterion, then summarise in a few lines what is met and what is not."

// buildModeDirective is what /spec build adds to the system message: the
// spec-as-source order, tests before code, and a visible task list.
const buildModeDirective = "THE USER WANTS THE SPEC BUILT. Work through it in this order, and keep the user " +
	"informed with update_tasks (it replaces the whole list each time; mark one task active while you " +
	"work on it and done when it is):\n" +
	"1. Call update_tasks with the plan: the spec's Tasks, or tasks you derive from its success criteria. " +
	"Tasks already ticked in the spec's Tasks section were done in an earlier turn: mark them done and " +
	"continue from the first one that is not.\n" +
	"2. Write the tests for the success criteria FIRST, and run them with sandbox_exec; they should fail.\n" +
	"3. Implement the change.\n" +
	"4. Run the tests again, read what they say, and fix what fails, until they pass.\n" +
	"5. Tick the tasks you finished in the spec's Tasks section (- [x]) with propose_edit.\n" +
	"6. For EACH success criterion call record_criterion -- met only with the command that passed or a " +
	"file:line as evidence.\n" +
	"Then say briefly what you built, what you ran, and anything left undone."

// maxTasks bounds the task list one update may carry.
const maxTasks = 30

func (s *Server) builtinUpdateTasks(raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args struct {
		Tasks []protocol.TaskItem `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if len(args.Tasks) == 0 || len(args.Tasks) > maxTasks {
		return toolError("give between 1 and %d tasks", maxTasks)
	}
	done := 0
	for i := range args.Tasks {
		t := &args.Tasks[i]
		t.ID = truncateRunes(strings.TrimSpace(t.ID), 16)
		t.Title = truncateRunes(strings.TrimSpace(t.Title), 160)
		if t.ID == "" {
			t.ID = strconv.Itoa(i + 1)
		}
		switch t.Status = strings.ToLower(strings.TrimSpace(t.Status)); t.Status {
		case protocol.TaskPending, protocol.TaskActive, protocol.TaskDone, protocol.TaskBlocked:
		default:
			return toolError("task %s: status must be pending, active, done or blocked, not %q", t.ID, t.Status)
		}
		if t.Status == protocol.TaskDone {
			done++
		}
	}
	if proposals != nil {
		proposals.tasks = args.Tasks
		if proposals.onTasks != nil {
			proposals.onTasks(args.Tasks)
		}
	}
	return mcp.Result{Content: fmt.Sprintf("Task list updated: %d of %d done.", done, len(args.Tasks))}, nil
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// updateTasksTool is the built-in, offered only in build mode.
func (s *Server) updateTasksTool(proposals *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "update_tasks",
			Description: "Show the user your plan and your progress through it. Pass the WHOLE task list " +
				"every time (it replaces the previous one): each task has an id, a short title and a " +
				"status of pending, active, done or blocked.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"tasks":{"type":"array","items":{"type":"object","properties":{
						"id":{"type":"string"},
						"title":{"type":"string"},
						"status":{"type":"string","enum":["pending","active","done","blocked"]}
					},"required":["id","title","status"],"additionalProperties":false}}
				},
				"required":["tasks"],
				"additionalProperties":false
			}`),
			// It changes nothing but what the user sees.
			ReadOnlyHint: true,
		},
		Handler: func(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinUpdateTasks(raw, proposals)
		},
	}
}

// tickSpecInCopy writes the verdicts' ticks into the working copy's spec, so
// a build offers ONE reviewed change to the spec -- its own task ticks and the
// criteria together -- rather than two edits to the same file that collide.
// Criteria are found by ID, not by line: the build may have edited the spec.
func tickSpecInCopy(st *stagedWorkspace, sp *activeSpec, verdicts map[string]specVerdict) {
	if st == nil || sp == nil || len(verdicts) == 0 {
		return
	}
	rel := filepath.FromSlash(sp.Path)
	path := filepath.Join(st.root, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		m := criterionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, ok := verdicts[strings.ToUpper(m[2])]
		if !ok || v.Status == protocol.SpecUnknown {
			continue
		}
		if next := setCheckbox(line, v.Status == protocol.SpecMet); next != line {
			lines[i] = next
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err == nil {
		if !st.touched[rel] {
			st.touched[rel] = true
			st.order = append(st.order, rel)
		}
	}
}

// ---------------------------------------------------------------------------
// record_criterion: the check's verdicts, and what makes "met" believable.
// ---------------------------------------------------------------------------

// specVerdict is what the model recorded for one criterion, after validation.
type specVerdict struct {
	Status, Evidence, Note string
}

// evidenceFileLine finds "path:line" references in evidence text.
var evidenceFileLine = regexp.MustCompile(`([\w.\-/]+\.[A-Za-z0-9]+):(\d+)`)

func (s *Server) builtinRecordCriterion(ctx context.Context, raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if proposals == nil || proposals.spec == nil {
		return toolError("there is no active spec to record against")
	}
	id := strings.ToUpper(strings.TrimSpace(args.ID))
	known := false
	for _, c := range proposals.spec.Criteria {
		if c.ID == id {
			known = true
			break
		}
	}
	if !known {
		return toolError("%s has no criterion %q; its criteria are %s", proposals.spec.Path, args.ID, criterionIDs(proposals.spec))
	}
	status := strings.ToLower(strings.TrimSpace(args.Status))
	switch status {
	case protocol.SpecMet, protocol.SpecUnmet, protocol.SpecUnknown:
	default:
		return toolError("status must be met, unmet or unknown, not %q", args.Status)
	}
	v := specVerdict{Status: status, Evidence: strings.TrimSpace(args.Evidence)}
	if status == protocol.SpecMet {
		if why := s.unverifiedEvidence(ctx, proposals, v.Evidence, true); why != "" {
			v.Status = protocol.SpecUnknown
			v.Note = "claimed met, but " + why
		}
	}
	if proposals.verdicts == nil {
		proposals.verdicts = map[string]specVerdict{}
	}
	proposals.verdicts[id] = v
	if v.Note != "" {
		return mcp.Result{Content: fmt.Sprintf("Recorded %s as UNKNOWN: %s. To record it as met, run the "+
			"check with sandbox_exec and cite the exact command, or cite a file:line that shows it.", id, v.Note)}, nil
	}
	return mcp.Result{Content: fmt.Sprintf("Recorded %s as %s.", id, status)}, nil
}

// unverifiedEvidence returns why evidence cannot be verified, or "" when it
// can: it names a command run this turn -- one that PASSED, when needPass --
// or a file:line that exists in the project.
//
// needPass is the difference between the two callers. A criterion recorded as
// met needs a run that passed; a long task's finding (longtask.go) is often
// proved by a run that FAILED -- the failing test is the bug -- so any run it
// actually made will do.
func (s *Server) unverifiedEvidence(ctx context.Context, proposals *proposalSink, evidence string, needPass bool) string {
	if evidence == "" {
		return "no evidence was given"
	}
	for _, c := range proposals.checks {
		if (c.passed || !needPass) && strings.Contains(evidence, c.command) {
			return ""
		}
	}
	for _, m := range evidenceFileLine.FindAllStringSubmatch(evidence, -1) {
		line, err := strconv.Atoi(m[2])
		if err != nil || line < 1 {
			continue
		}
		full, err := s.resolveToolPath(proposals.readCtx(ctx), m[1])
		if err != nil {
			continue
		}
		data, _, err := readBoundedFile(full, maxBuiltinReadBytes)
		if err == nil && line <= strings.Count(string(data), "\n")+1 {
			return ""
		}
	}
	if needPass {
		return "the evidence names neither a command that passed this turn nor a file:line that exists"
	}
	return "the evidence names neither a command run in this task nor a file:line that exists"
}

func criterionIDs(sp *activeSpec) string {
	ids := make([]string, 0, len(sp.Criteria))
	for _, c := range sp.Criteria {
		ids = append(ids, c.ID)
	}
	if len(ids) == 0 {
		return "none (it has no \"- [ ] C1: ...\" lines)"
	}
	return strings.Join(ids, ", ")
}

// specReport turns the recorded verdicts into the report the user sees, plus
// an edit to the spec that ticks what is met and unticks what is not -- the
// spec stays connected to the code, through the ordinary review.
func (p *proposalSink) specReport() (*protocol.SpecReport, []editapply.EditBlock) {
	if p == nil || p.spec == nil || (!p.checking && !p.building) {
		return nil, nil
	}
	rep := &protocol.SpecReport{Spec: p.spec.Path}
	lines := strings.Split(p.spec.Content, "\n")
	changed := false
	for _, c := range p.spec.Criteria {
		v, ok := p.verdicts[c.ID]
		if !ok {
			v = specVerdict{Status: protocol.SpecUnknown, Note: "not checked"}
		}
		rep.Criteria = append(rep.Criteria, protocol.SpecCriterionResult{
			ID: c.ID, Text: c.Text, Status: v.Status, Evidence: v.Evidence, Note: v.Note})
		want := c.Done
		switch v.Status {
		case protocol.SpecMet:
			want = true
		case protocol.SpecUnmet:
			want = false
		}
		if want != c.Done {
			lines[c.Line] = setCheckbox(lines[c.Line], want)
			changed = true
		}
	}
	if !changed {
		return rep, nil
	}
	return rep, stagedEditBlocks(p.spec.Path, p.spec.Content, strings.Join(lines, "\n"))
}

var checkboxMark = regexp.MustCompile(`\[[ xX]\]`)

func setCheckbox(line string, done bool) string {
	mark := "[ ]"
	if done {
		mark = "[x]"
	}
	loc := checkboxMark.FindStringIndex(line)
	if loc == nil {
		return line
	}
	return line[:loc[0]] + mark + line[loc[1]:]
}

// recordCriterionTool is the built-in, offered only in check mode.
func (s *Server) recordCriterionTool(proposals *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "record_criterion",
			Description: "Record whether the project meets one success criterion of the active spec. " +
				"status is met, unmet or unknown. \"met\" is accepted only with evidence that can be " +
				"verified: the exact sandbox_exec command you ran this turn that passed, or a file:line " +
				"in the project that shows it; otherwise it is recorded as unknown. Recording a " +
				"criterion again replaces the earlier verdict.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"id":{"type":"string","description":"The criterion's ID, e.g. C1."},
					"status":{"type":"string","enum":["met","unmet","unknown"]},
					"evidence":{"type":"string","description":"The command that passed, a file:line, or why it is unmet or unknown."}
				},
				"required":["id","status","evidence"],
				"additionalProperties":false
			}`),
			// It writes nothing but this turn's report.
			ReadOnlyHint: true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinRecordCriterion(ctx, raw, proposals)
		},
	}
}

// specModeWithholdEdits applies the spec modes' write rule to the edits a turn
// is about to offer. filed came through the tools, and the handler already
// held them to the rule; text blocks in the model's prose did not, so a /spec
// turn keeps only text edits to specs/, and a check keeps none. Each withheld
// block is reported, as planModeWithholdEdits reports its own.
func specModeWithholdEdits(mode string, filed, text []editapply.EditBlock, rejections []protocol.EditRejectionWire) ([]editapply.EditBlock, []protocol.EditRejectionWire) {
	kept := append([]editapply.EditBlock(nil), filed...)
	for _, b := range text {
		if _, ok := specRelPath(b.FilePath); ok && isSpecMode(mode) {
			kept = append(kept, b)
			continue
		}
		why := "/spec writes only its spec under specs/"
		if isCheckMode(mode) {
			why = "/spec check changes nothing"
		}
		rejections = append(rejections, protocol.EditRejectionWire{
			Reason: fmt.Sprintf("%s mode: withheld a proposed edit to %s; %s.", mode, b.FilePath, why),
		})
	}
	return kept, rejections
}
