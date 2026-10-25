package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"mochiii/protocol"
)

// A TOOL CALL AS ONE SHORT HIGHLIGHTED LINE (activityline.go). The owner, on
// "⚙ builtin__read_file — 17747 bytes to the model in 0ms" under a blank line
// per call: "so lame and stale, the reading file should look something
// highlighted in the minimal".

func call(id, tool, target, phase string, bytes int) protocol.ToolActivity {
	return protocol.ToolActivity{CallID: id, Server: "builtin", Tool: tool, Target: target, Phase: phase, ResultBytes: bytes}
}

// The calls of one run are stacked, each saying what was done to what, and a
// call's "running" line becomes its result line rather than a second line.
func TestARunOfToolCallsIsStackedAndSaysWhatItDid(t *testing.T) {
	m := newTestModel()
	m.appendTurn(turn{role: roleUser, text: "show me"})
	m.noteToolActivity(call("c1", "read_file", "clients/tui/chat.go", protocol.ToolPhaseRunning, 0))
	m.noteToolActivity(call("c1", "read_file", "clients/tui/chat.go", protocol.ToolPhaseSucceeded, 17747))
	m.noteToolActivity(call("c2", "list_directory", "clients/tui", protocol.ToolPhaseSucceeded, 2287))
	m.noteToolActivity(call("c3", "sandbox_exec", "go test ./...", protocol.ToolPhaseRunning, 0))
	m.appendTurn(turn{role: roleAssistant, text: "done"})

	got := ansi.Strip(renderTranscript(m.turns, 80))
	want := "● Read   clients/tui/chat.go · 17 KB\n" +
		"● List   clients/tui · 2 KB\n" +
		"○ Run    go test ./... …"
	if !strings.Contains(got, "show me\n\n"+want+"\n\n") {
		t.Errorf("the run of calls is not three stacked lines between blank lines:\n%s", got)
	}
	if n := strings.Count(got, "chat.go"); n != 1 {
		t.Errorf("chat.go appears %d times: a call's running line was not replaced by its result", n)
	}
}

// The thing acted on is picked out in its own colour, the verb is bold, and
// the colours change nothing about the text.
func TestTheTargetIsPickedOut(t *testing.T) {
	withColorProfile(t, termenv.ANSI256, func() {
		v, _ := activityFor(call("c1", "read_file", "clients/tui/chat.go", protocol.ToolPhaseSucceeded, 17747))
		line := renderTurnBlock(turn{role: roleActivity, text: v.plain(), act: &v}, 80) // the transcript's own path
		if !strings.Contains(line, activityTargetStyle.Render("clients/tui/chat.go")) {
			t.Errorf("the target is not drawn in the target colour: %q", line)
		}
		if !strings.Contains(line, activityVerbStyle.Render("Read  ")) {
			t.Errorf("the verb is not drawn bold: %q", line)
		}
		if ansi.Strip(line) != v.plain() {
			t.Errorf("drawn as %q, want the plain line %q", ansi.Strip(line), v.plain())
		}
	})
}

// "IF THE AGENT FAILS TO READ, LIST, MODIFY, IT SHOULD INDICATE BY THE BLUE
// COLOR TO RED" (the owner, 2026-10-25). A call that failed, or was not run,
// is red -- its target, its glyph and its reason; one that ran, or is
// running, keeps its target blue.
func TestAFailedCallIsDrawnInRed(t *testing.T) {
	withColorProfile(t, termenv.ANSI256, func() {
		drawn := map[string]string{"blue": activityTargetStyle.Render("a.go"), "red": activityFailStyle.Render("a.go")}
		if drawn["blue"] == drawn["red"] {
			t.Fatal("blue and red draw alike under this profile: the test would prove nothing")
		}
		for phase, colour := range map[string]string{
			protocol.ToolPhaseRunning: "blue", protocol.ToolPhaseSucceeded: "blue",
			protocol.ToolPhaseFailed: "red", protocol.ToolPhaseDenied: "red",
		} {
			for _, tool := range []string{"read_file", "list_directory", "propose_edit"} {
				v, _ := activityFor(call("c1", tool, "a.go", phase, 10))
				line := renderTurnBlock(turn{role: roleActivity, text: v.plain(), act: &v}, 80)
				if !strings.Contains(line, drawn[colour]) {
					t.Errorf("%s %s: want its target %s: %q", tool, phase, colour, line)
				}
				if colour != "red" {
					continue
				}
				for _, part := range []string{v.glyph(), v.tail()} {
					if !strings.Contains(line, activityFailStyle.Render(part)) {
						t.Errorf("%s %s: %q is not red: %q", tool, phase, part, line)
					}
				}
			}
		}
	})
}

// THE CACHE KEYS ON THE PARTS, not only the text: two calls can print the same
// plain line and still split it differently into target and size, and a block
// reused on the text alone would draw the old split in the old colours.
func TestTheRenderCacheTellsTwoSplitsOfOneLineApart(t *testing.T) {
	withColorProfile(t, termenv.ANSI256, func() {
		one := activityView{state: activityDone, verb: "Read", target: "a · 5 KB", meta: "1 KB"}
		two := activityView{state: activityDone, verb: "Read", target: "a", meta: "5 KB · 1 KB"}
		if one.plain() != two.plain() {
			t.Fatalf("the two views must print alike for this test to mean anything: %q, %q", one.plain(), two.plain())
		}
		var cache transcriptCache
		turns := []turn{{role: roleActivity, text: one.plain(), act: &one}}
		cache.render(turns, 80)
		turns[0].act = &two
		if got, want := cache.render(turns, 80), renderTranscript(turns, 80); got != want {
			t.Errorf("the cache drew the old split:\ncached: %q\nfresh:  %q", got, want)
		}
	})
}

// A target too long for the terminal is cut to keep the call on one row; the
// size after it survives the cut.
func TestALongTargetIsCutToOneRow(t *testing.T) {
	v, _ := activityFor(call("c1", "read_file", strings.Repeat("very/long/", 12)+"file.go", protocol.ToolPhaseSucceeded, 17747))
	line := renderActivity(v, 40)
	if w := ansi.StringWidth(line); w > 40 {
		t.Errorf("the line is %d columns in a 40-column terminal: %q", w, ansi.Strip(line))
	}
	if plain := ansi.Strip(line); !strings.Contains(plain, "…") || !strings.HasSuffix(plain, " · 17 KB") {
		t.Errorf("want the target cut with … and the size kept: %q", plain)
	}
}

// The reason a real read failed with, as the daemon sends it (2026-10-25): the
// first line of what the model was told. It pushed the target off the line and
// wrapped onto a second row, so a failed read said what went wrong but not to
// what -- and there was no blue left to turn red.
const missingFileReason = "cannot read missing.txt: no such file. No file named missing.txt exists anywhere " +
	"in this project (1 files checked), and nothing has a similar name. It does not ex…"

// A failed call keeps its target on one row, whatever the width; the reason
// is what gives way.
func TestAFailedCallKeepsItsTargetOnOneRow(t *testing.T) {
	failed := call("c1", "read_file", "missing.txt", protocol.ToolPhaseFailed, 0)
	failed.Detail = missingFileReason
	if got := toolActivityLine(failed); got != "● Read   missing.txt · failed: no such file" {
		t.Errorf("a missing file reads %q", got)
	}

	edit := call("c2", "propose_edit", "src/app.go", protocol.ToolPhaseFailed, 0)
	edit.Detail = "that edit cannot be applied: the search text was not found in src/app.go, which has 120 lines; " +
		"read the file again and copy the text exactly"
	long := call("c3", "read_file", strings.Repeat("very/long/", 12)+"file.go", protocol.ToolPhaseFailed, 0)
	long.Detail = edit.Detail
	for _, width := range []int{100, 60, 40} {
		for _, a := range []protocol.ToolActivity{failed, edit, long} {
			v, _ := activityFor(a)
			line := ansi.Strip(renderActivity(v, width))
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("width %d: the line is %d columns: %q", width, w, line)
			}
			prefix := v.glyph() + " " + v.verbCell() + " "
			if !strings.HasPrefix(line, prefix) || strings.HasPrefix(line, prefix+"·") {
				t.Errorf("width %d: no target after the verb: %q", width, line)
				continue
			}
			target := strings.SplitN(strings.TrimPrefix(line, prefix), " · ", 2)[0]
			if a.Target != long.Target && target != a.Target {
				t.Errorf("width %d: the target %q was cut to %q while the reason had room to give: %q", width, a.Target, target, line)
			}
			if !strings.Contains(line, " · failed") {
				t.Errorf("width %d: the line no longer says it failed: %q", width, line)
			}
			if a.Target != long.Target && !strings.Contains(line, " · failed: ") {
				t.Errorf("width %d: the reason went though there was room for some of it: %q", width, line)
			}
			if strings.Contains(line, " …") {
				t.Errorf("width %d: a cut left a space before its …: %q", width, line)
			}
		}
	}
}

// The reason on the line is its first sentence, without the clause that names
// the target again; one that does not name it is left as it was.
func TestAReasonIsCutToWhatItSays(t *testing.T) {
	for _, tc := range []struct{ detail, target, want string }{
		{missingFileReason, "missing.txt", "no such file"},
		{"cannot list nowhere: no such folder. No folder named nowhere exists", "nowhere", "no such folder"},
		{"the tool failed to run", "a.go", "the tool failed to run"},
		{`"x" needs the user's approval and no valid answer came back, so it was not run.`, "a.go",
			`"x" needs the user's approval and no valid answer came back, so it was not run`},
		{"a.go is a directory; use list_directory", "a.go", "a.go is a directory; use list_directory"},
		{"cannot search: retrieval is unavailable", "", "cannot search: retrieval is unavailable"},
		{"", "a.go", ""},
	} {
		if got := shortReason(tc.detail, tc.target); got != tc.want {
			t.Errorf("shortReason(%q, %q) = %q, want %q", tc.detail, tc.target, got, tc.want)
		}
	}
}

// A tool that is not built in keeps the name it was called by; a failure and a
// refusal say so after the target.
func TestEveryKindOfCallReadsRight(t *testing.T) {
	for _, tc := range []struct {
		a    protocol.ToolActivity
		want string
	}{
		{protocol.ToolActivity{Server: "notes", Tool: "search", Phase: protocol.ToolPhaseSucceeded, ResultBytes: 10}, "● notes__search · 10 bytes"},
		{call("", "read_file", "a.go", protocol.ToolPhaseFailed, 0), "● Read   a.go · failed"},
		{protocol.ToolActivity{Server: "builtin", Tool: "read_file", Target: "/etc/shadow", Phase: protocol.ToolPhaseDenied, Detail: "refused"}, "✗ Read   /etc/shadow · not run: refused"},
		{call("", "repo_map", "", protocol.ToolPhaseSucceeded, 900), "● Repo map · 900 bytes"},
	} {
		if got := toolActivityLine(tc.a); got != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.a.Tool, tc.a.Phase, got, tc.want)
		}
	}
}

// An edit that makes a new file reads "Write", an edit "Edit"; both with the
// file picked out like a read or a listing.
func TestAWriteReadsAsAWrite(t *testing.T) {
	create := call("c1", "propose_edit", "login.html", protocol.ToolPhaseSucceeded, 120)
	create.Creates = true
	if got := toolActivityLine(create); got != "● Write  login.html · 120 bytes" {
		t.Errorf("a new file reads %q", got)
	}
	if got := toolActivityLine(call("c2", "propose_edit", "login.html", protocol.ToolPhaseSucceeded, 80)); got != "● Edit   login.html · 80 bytes" {
		t.Errorf("an edit reads %q", got)
	}
}

// The target is the model's text and a verb can be an MCP server's: neither
// reaches the screen with an escape sequence in it, drawn or plain.
func TestATargetCannotDrawOnTheTerminal(t *testing.T) {
	m := newTestModel()
	m.noteToolActivity(call("c1", "read_file", "a.go\x1b]52;c;ZXZpbA==\x07\x1b[2J", protocol.ToolPhaseSucceeded, 1))
	tr := m.turns[len(m.turns)-1]
	if tr.role != roleActivity || tr.act == nil {
		t.Fatalf("the call was recorded as %v without its parts", tr.role)
	}
	for _, s := range []string{tr.text, tr.act.target, renderTurnBlock(tr, 80)} {
		if strings.Contains(s, "\x1b]") || strings.Contains(s, "\x1b[2J") {
			t.Errorf("an escape sequence survived: %q", s)
		}
	}
}

// A pipeline step is not a tool call: it stays the grey line it was, apart.
func TestAPipelineStepStaysItsOwnLine(t *testing.T) {
	m := newTestModel()
	m.noteToolActivity(protocol.ToolActivity{Phase: protocol.ToolPhaseStep, Tool: "Researcher", Detail: "step 1/2"})
	m.noteToolActivity(call("c1", "read_file", "a.go", protocol.ToolPhaseSucceeded, 1))
	if m.turns[len(m.turns)-2].role != roleSystem {
		t.Errorf("a step was recorded as %v, want roleSystem", m.turns[len(m.turns)-2].role)
	}
	if got := ansi.Strip(renderTranscript(m.turns[len(m.turns)-2:], 80)); !strings.Contains(got, "Researcher\n\n● Read") {
		t.Errorf("a step and the call after it are not a blank line apart:\n%s", got)
	}
}
