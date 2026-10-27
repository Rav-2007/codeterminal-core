package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// A TOOL CALL, DRAWN AS ONE SHORT HIGHLIGHTED LINE (2026-10-25).
//
// It used to be a grey sentence per call, a blank line under each:
//
//	⚙ builtin__read_file — 17747 bytes to the model in 0ms
//
// The owner: "so lame and stale, the reading file should look something
// highlighted in the minimal". Now it says what was done and to what, the way
// a person would, with the thing acted on picked out, and the calls of one run
// stacked with no gaps:
//
//	● Read   clients/tui/chat.go · 17 KB
//	● List   clients/tui · 2 KB
//	○ Run    go test ./... …
//
// The size stays, small, at the end: it is what went back to the model after
// scrubbing and truncation -- in agent mode the quantity that leaves the
// machine, which a privacy-positioned product lets the user watch. The time
// is shown only from a second up; "in 0ms" said nothing.
//
// The target (path, command, query, URL) is the daemon's to name
// (daemon/activitytarget.go): an activity carries no arguments.

// roleActivity is a tool call's line. Display-only like roleSystem, so
// buildHistory drops it; separate from roleSystem because it is drawn from its
// parts (turn.act) rather than as one grey string, and because the lines of one
// run of calls are stacked without the blank line between turns.
const roleActivity turnRole = roleUnanswered + 1

// activityState is where a call has got to.
type activityState int

const (
	activityRunning activityState = iota
	activityDone
	activityFailed
	activityRefused
)

// activityView is one tool call's line, in parts.
type activityView struct {
	state  activityState
	verb   string // "Read", "Run", ... or the tool's own name for a tool not built in
	target string // what it acted on; empty when the daemon did not say
	meta   string // after the target: the size, or that it failed or did not run
	why    string // after that: why, the first part of the line to give way
}

// activityFor is the line for a tool call. ok is false for what is not one: a
// pipeline step (drawn as before) or a phase no line is shown for.
func activityFor(a protocol.ToolActivity) (v activityView, ok bool) {
	v = activityView{verb: sanitizeText(activityVerb(a)), target: sanitizeText(a.Target)}
	detail := sanitizeText(a.Detail)
	switch a.Phase {
	case protocol.ToolPhaseRunning:
		v.state = activityRunning
	case protocol.ToolPhaseSucceeded:
		v.state, v.meta = activityDone, humanBytes(a.ResultBytes)
		if a.DurationMS >= 1000 {
			v.meta += fmt.Sprintf(" · %.1fs", float64(a.DurationMS)/1000)
		}
	case protocol.ToolPhaseFailed:
		v.state, v.meta, v.why = activityFailed, "failed", shortReason(detail, v.target)
	case protocol.ToolPhaseDenied:
		v.state, v.meta, v.why = activityRefused, "not run", shortReason(detail, v.target)
	default:
		return activityView{}, false
	}
	return v, true
}

// shortReason is the part of why a call failed, or did not run, that earns a
// place on its line: the first sentence, less a leading clause that names the
// target again. The daemon's reason is the first line of what the MODEL was
// told, and that runs on -- "cannot read a.go: no such file. No file named a.go
// exists anywhere in this project (1 files checked), and nothing has a similar
// name. It does not …" -- which took the line onto a second row (2026-10-25).
func shortReason(detail, target string) string {
	if i := strings.Index(detail, target+": "); target != "" && i >= 0 {
		detail = detail[i+len(target)+2:]
	}
	if i := strings.Index(detail, ". "); i >= 0 {
		detail = detail[:i]
	}
	return strings.TrimSuffix(strings.TrimSpace(detail), ".")
}

// activityTurn is the turn noteToolActivity records for an activity: a tool
// call's line in parts, or -- for a pipeline step -- the grey line it always was.
// line is toolActivityLine's, sanitized.
func activityTurn(a protocol.ToolActivity, line string) turn {
	if v, ok := activityFor(a); ok {
		return turn{role: roleActivity, text: line, act: &v}
	}
	return turn{role: roleSystem, text: line}
}

// activity is the turn's call parts, or the zero view for any other turn.
func (t turn) activity() activityView {
	if t.act == nil {
		return activityView{}
	}
	return *t.act
}

// activityVerbs are the built-in tools, said as what they do.
var activityVerbs = map[string]string{
	"read_file": "Read", "list_directory": "List", "search_code": "Search", "grep": "Grep",
	"find_files": "Find", "repo_map": "Repo map", "sandbox_exec": "Run", "run_command": "Run", "web_search": "Search web",
	"web_fetch": "Fetch", "propose_edit": "Edit", "propose_ast_edit": "Edit", "rename_symbol": "Rename",
	"query_compiler_definition": "Definition", "query_compiler_references": "References",
	"git_history": "Git", "github": "GitHub", "investigate": "Investigate", "find_tools": "Find tools",
	"checkpoint": "Checkpoint", "record_finding": "Finding", "finish_task": "Finish",
	"update_tasks": "Tasks", "record_criterion": "Criterion",
}

// activityVerb is a built-in tool's verb, else the name the call was made by:
// an MCP server's tool keeps its qualified name, and one the model named with
// no server keeps the name it used (qualifiedActivityName).
func activityVerb(a protocol.ToolActivity) string {
	if a.Server == "builtin" && a.Creates {
		return "Write" // an edit that makes a new file (protocol.ToolActivity.Creates)
	}
	if verb, ok := activityVerbs[a.Tool]; ok && a.Server == "builtin" {
		return verb
	}
	return qualifiedActivityName(a)
}

// verbColumn is how wide the verb is drawn, so the targets of the common
// calls start in one column.
const verbColumn = 6

func (v activityView) glyph() string {
	switch v.state {
	case activityRunning:
		return "○"
	case activityRefused:
		return "✗"
	}
	return "●"
}

// verbCell is the verb, padded to verbColumn when a target follows it.
func (v activityView) verbCell() string {
	if v.target == "" {
		return v.verb
	}
	return v.verb + strings.Repeat(" ", max(verbColumn-utf8.RuneCountInString(v.verb), 0))
}

// tail is what follows the target.
func (v activityView) tail() string {
	if v.state == activityRunning {
		return " …"
	}
	if v.meta == "" {
		return ""
	}
	return " · " + v.meta + detailSuffix(v.why)
}

// plain is the line without colour: the turn's text, which is what the render
// cache keys on and what a test or a search reads.
func (v activityView) plain() string {
	line := v.glyph() + " " + v.verbCell()
	if v.target != "" {
		line += " " + v.target
	}
	return line + v.tail()
}

var (
	activityDoneStyle   = lipgloss.NewStyle().Foreground(colorTeal)
	activityFailStyle   = lipgloss.NewStyle().Foreground(colorRed)
	activityDimStyle    = lipgloss.NewStyle().Foreground(colorDim)
	activityVerbStyle   = lipgloss.NewStyle().Foreground(colorGray).Bold(true)
	activityTargetStyle = lipgloss.NewStyle().Foreground(colorBlue)
)

// renderActivity draws a call's line in colour, on one row: what is too long
// for the width is cut with "…" (fitActivity) rather than wrapped onto a
// second row.
//
// A CALL THAT DID NOT HAPPEN IS RED, ITS TARGET TOO (2026-10-25). The owner:
// "if the agent fails to read, list, modify, it should indicate by the blue
// color to red". A call that failed and one that was not run are drawn alike:
// glyph, target and reason in red, so blue only ever means it was done.
func renderActivity(v activityView, width int) string {
	glyphStyle, targetStyle, metaStyle := activityDoneStyle, activityTargetStyle, activityDimStyle
	switch v.state {
	case activityRunning:
		glyphStyle = activityDimStyle
	case activityFailed, activityRefused:
		glyphStyle, targetStyle, metaStyle = activityFailStyle, activityFailStyle, activityFailStyle
	}
	verb, tail, target := v.verbCell(), v.tail(), v.target
	if width > 0 {
		target, tail = fitActivity(width-ansi.StringWidth(v.glyph()+" "+verb), v)
	}
	line := glyphStyle.Render(v.glyph()) + " " + activityVerbStyle.Render(verb)
	if target != "" {
		line += " " + targetStyle.Render(target)
	}
	if tail != "" {
		line += metaStyle.Render(tail)
	}
	return line
}

// fitActivity cuts a call's target and tail to fit room columns. What gives
// way, in order: why the call failed, then the target -- never what came of
// it, the size or "failed". A long reason once pushed the target off the line
// altogether, so a failed call said what went wrong but not to what.
func fitActivity(room int, v activityView) (target, tail string) {
	target, tail = v.target, v.tail()
	if target != "" {
		room-- // the space in front of it
	}
	tw := ansi.StringWidth(target)
	if v.why != "" && tw+ansi.StringWidth(tail) > room {
		// The reason keeps what the whole target leaves it, if that is a
		// character or more; else it goes, and " · failed" stays.
		head := strings.TrimSuffix(tail, detailSuffix(v.why))
		if keep := room - tw; keep > ansi.StringWidth(head+": …") {
			tail = strings.TrimRight(ansi.Truncate(tail, keep-1, ""), " ") + "…"
		} else {
			tail = head
		}
	}
	lw := ansi.StringWidth(tail)
	switch {
	case target == "" || tw+lw <= room:
	case room-lw < 1:
		target = ""
	default:
		target = ansi.Truncate(target, room-lw, "…")
	}
	return target, tail
}

// renderSystemTurn draws the two kinds of turn this client writes itself.
func renderSystemTurn(t turn, width int) string {
	if t.role == roleActivity && t.act != nil {
		return renderActivity(*t.act, width)
	}
	return helpStyle.Render(t.text)
}
