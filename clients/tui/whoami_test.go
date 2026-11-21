package tui

import (
	"go/ast"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// modelRunBy is the chat with the daemon's answer in, on a terminal of a given
// size, started by who. Tests say who: a model made any other way has no name,
// which is what keeps every other test's screen the same on every machine.
func modelRunBy(t *testing.T, who string, width, height int) chatModel {
	t.Helper()
	m := connectedModel(t, nvidiaInForce(t))
	m.whoami = who
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(chatModel)
}

// keysRowText is the last row of the screen, as a reader sees it.
func keysRowText(m chatModel) string { return ansi.Strip(m.keysRow(helpStyle.Render(helpText))) }

// THE REPORT THIS ANSWERS (OWNER, 2026-10-13): "it should display the
// user/desktop name in the bottom of the prompt bar". The row under the prompt
// said which model and provider; nothing on the screen said who was running the
// client or on which machine.
//
// Since 2026-11-21 (owner: "the directory name in the bottom right corner, with
// below the system and user name") the name is at the right end of the keys
// line, the last row of the screen.
//
// Neuter check: return help from keysRow -- the name is not drawn and this
// fails on its first assertion.
func TestTheRowUnderThePromptSaysWhoIsAtWhichMachine(t *testing.T) {
	const who = "ravi-kiran@SCARX"
	m := modelRunBy(t, who, 100, 24)

	row := keysRowText(m)
	if !strings.HasPrefix(row, helpText+"  ") || !strings.HasSuffix(row, "  "+who) {
		t.Errorf("the row reads %q, want the keys, then %q at its right end", row, who)
	}
	if w := lipgloss.Width(row); w != 100-rightEdgeGap {
		t.Errorf("the row is %d columns wide in a 100-column terminal: the name does not end one column in from the edge", w)
	}

	// It is this row the screen draws, last.
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if last := lines[len(lines)-1]; last != row {
		t.Errorf("the last row is %q, want %q", last, row)
	}
	// And the line itself says exactly what it said before.
	if got := statusText(m); got != "z-ai/glm-5.3 · NVIDIA" {
		t.Errorf("the line reads %q", got)
	}
}

// THE KEYS ARE NEVER SHORTENED TO MAKE ROOM FOR THE NAME. They are what the row
// is for; the name takes the columns they leave. So in every width: the keys
// are whole, the name is whole or cut with "…" or absent, and the frame is
// exactly as tall as the terminal.
//
// Neuter check: drop the whoamiMinCols test in withRightEnd -- at the widths
// that leave one or two columns the row ends in a bare "…".
func TestTheNameTakesOnlyTheColumnsTheLineLeaves(t *testing.T) {
	const who, height = "ravi-kiran@SCARX", 24
	sawWhole, sawCut, sawAbsent := false, false, false
	for width := 10; width <= 100; width++ {
		m := modelRunBy(t, who, width, height)
		m.sessionUsage = usageTotals{turns: 3, prompt: 120000, completion: 9000, cost: 1.25}
		line, row := helpText, keysRowText(m)

		if !strings.HasPrefix(row, line) {
			t.Fatalf("at %d columns the keys %q were changed to fit the name: %q", width, line, row)
		}
		if w := lipgloss.Width(row); w > max(width, lipgloss.Width(line)) {
			t.Fatalf("at %d columns the row is %d wide: %q", width, w, row)
		}
		if n := len(strings.Split(m.View(), "\n")); n != height {
			t.Fatalf("at %d columns the screen is %d lines tall, want %d", width, n, height)
		}

		rest := strings.TrimPrefix(row, line)
		room := width - rightEdgeGap - lipgloss.Width(line) - whoamiGap
		if rest == "" {
			sawAbsent = true
			if room >= whoamiMinCols {
				t.Errorf("at %d columns there were %d columns for the name and it was not drawn: %q", width, room, row)
			}
			continue
		}
		name := strings.TrimLeft(rest, " ")
		switch cut := strings.TrimSuffix(name, "…"); {
		case name == who:
			sawWhole = true
		case cut != name && strings.HasPrefix(who, cut):
			sawCut = true
		default:
			t.Errorf("at %d columns the name was drawn as %q", width, name)
		}
		if len(rest)-len(name) < whoamiGap {
			t.Errorf("at %d columns the name runs into the line: %q", width, row)
		}
		if w := lipgloss.Width(row); w != width-rightEdgeGap {
			t.Errorf("at %d columns the name does not end one column in from the edge (row is %d wide): %q", width, w, row)
		}
		if w := lipgloss.Width(name); w < whoamiMinCols {
			t.Errorf("at %d columns the name was cut to %d column(s): %q", width, w, name)
		}
	}
	// Otherwise the loop proved nothing about one of the three.
	if !sawWhole || !sawCut || !sawAbsent {
		t.Errorf("the widths tried did not cover whole=%v cut=%v absent=%v", sawWhole, sawCut, sawAbsent)
	}
}

// The name is this machine's own knowledge, so it does not wait for the daemon
// -- nor for anything to be on the row: with no keys it is the name alone, at
// the right edge (a question waiting has no key line).
func TestTheNameDoesNotWaitForTheDaemon(t *testing.T) {
	const who = "ravi-kiran@SCARX"
	m := newTestModel() // 80 columns, nothing heard from the daemon yet
	m.whoami = who
	if got, want := ansi.Strip(m.keysRow("")), strings.Repeat(" ", 80-rightEdgeGap-len(who))+who; got != want {
		t.Errorf("with no keys the row reads %q, want %q", got, want)
	}
}

// NO NAME, NO CHANGE: a client that could not find one draws the keys exactly as
// they were drawn before there was a name at all.
func TestWithoutANameTheRowIsTheLine(t *testing.T) {
	m := connectedModel(t, nvidiaInForce(t))
	if help := helpStyle.Render(helpText); m.keysRow(help) != help {
		t.Errorf("with no name the row is %q, the keys %q", m.keysRow(help), help)
	}
}

// The name is drawn in the status line's grey.
func TestTheNameIsDrawnInTheLinesColour(t *testing.T) {
	m := modelRunBy(t, "ravi-kiran@SCARX", 100, 24)
	var row string
	withColorProfile(t, termenv.ANSI256, func() { row = m.keysRow(helpText) })
	const grey = "\x1b[38;5;243m"
	if !strings.Contains(row, grey+"ravi-kiran@SCARX") {
		t.Errorf("the name is not drawn in the line's grey: %q", row)
	}
}

// whoamiLabel is what the two names become on the screen: one line, as short as
// a shell prompt writes it, with nothing in it a terminal would act on.
func TestWhoamiLabel(t *testing.T) {
	for _, c := range []struct{ user, host, want string }{
		{"ravi-kiran", "SCARX", "ravi-kiran@SCARX"},
		// Windows names the user with the machine or the domain in front.
		{`SCARX\ravi`, "SCARX", "ravi@SCARX"},
		{`CORP\ravi.kiran`, "DESKTOP-4F7G2H1", "ravi.kiran@DESKTOP-4F7G2H1"},
		// A fully qualified name is cut where a shell prompt cuts it.
		{"ravi", "Ravis-MacBook-Pro.local", "ravi@Ravis-MacBook-Pro"},
		{"ravi", "build-7.eu.corp.example", "ravi@build-7"},
		// Half an answer is still an answer; none is none.
		{"", "SCARX", "SCARX"},
		{"ravi", "", "ravi"},
		{"", "", ""},
		{" \t", "\n", ""},
		// Neither name is this program's: a machine can be called anything.
		{"ev\x1b[2Jil", "bo\x1b]0;title\x07x", "evil@box"},
		{"two\nlines", "a\tb\r\nc", "two lines@a b c"},
	} {
		if got := whoamiLabel(c.user, c.host); got != c.want {
			t.Errorf("whoamiLabel(%q, %q) = %q, want %q", c.user, c.host, got, c.want)
		}
	}
}

// THE REAL ANSWER, ON WHATEVER MACHINE RUNS THIS. Every test above says who is
// running the client; this is the one that asks. A machine that can run it has
// a name, so an empty answer means the asking is broken -- and whatever comes
// back has to be one drawable line, since it goes on the screen as it is.
func TestThisMachineHasAName(t *testing.T) {
	got := lookupWhoami()
	if got == "" {
		t.Fatal("lookupWhoami found neither a user nor a machine name here")
	}
	if got != sanitizeText(got) || strings.ContainsAny(got, "\r\n\t") || got != strings.TrimSpace(got) {
		t.Errorf("lookupWhoami returned something that is not one clean line: %q", got)
	}
}

// THE NAME IS DRAWN, AND NOTHING ELSE. A user's name and their machine's are
// facts about a person. They are on this screen because that person asked to
// see them; nothing asked for them to be in a prompt, a saved chat, a log line
// or a request to the daemon, and from any of those they would reach a
// provider. So, like the two raw-byte structures (sanitize_guard_test.go), who
// reads it is a list a person has to edit on purpose.
//
// It also holds the other end: runChat is the only place the system is asked,
// and it must go on asking -- every test above sets the name by hand, so
// without this the real client could stop showing it with the suite green.
//
// Neuter checks: read m.whoami from any other function (try renderHeader) --
// it is named below; delete the assignment in runChat -- "never asks".
func TestTheNameIsDrawnAndNothingElse(t *testing.T) {
	// Reviewed 2026-10-13; again 2026-11-21, when the name moved to the keys line.
	reviewed := map[string]bool{
		"keysRow": true, // draws it at the right end of the keys line (was statusRow, the model line)
		"runChat": true, // stores what lookupWhoami found, before anything is drawn
		// Measures it, to make the first screen's column wide enough to draw it
		// at the right end of the keys line, as under the chat. Sends nothing.
		"welcomeWidth": true,
	}
	fset, files := parsePackageSource(t)
	var novel []string
	asked := false
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			var what string
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if n.Sel.Name == "whoami" {
					what = "reads the name"
				}
			case *ast.CallExpr:
				if callsFunc(n, "lookupWhoami") {
					what = "asks the system for the name"
				}
			case *ast.AssignStmt:
				if len(n.Lhs) == 1 && len(n.Rhs) == 1 && isSel(n.Lhs[0], "whoami") &&
					callsFunc(n.Rhs[0], "lookupWhoami") && enclosingFunc(f, n.Pos()) == "runChat" {
					asked = true
				}
			}
			if what == "" {
				return true
			}
			if fn := enclosingFunc(f, n.Pos()); !reviewed[fn] {
				novel = append(novel, fset.Position(n.Pos()).String()+" "+fn+"() "+what)
			}
			return true
		})
	}
	if len(novel) > 0 {
		sort.Strings(novel)
		t.Errorf("the user's name and their machine's are used somewhere new:\n  %s\n\n"+
			"They are drawn on the row under the prompt and go nowhere else (whoami.go).\n"+
			"If this only draws them too, add the function to the reviewed list in this\n"+
			"test and say where they appear. If it sends, stores or logs them: do not.",
			strings.Join(novel, "\n  "))
	}
	if !asked {
		t.Error("runChat never asks who is running the client (model.whoami = lookupWhoami()):\n" +
			"the real app draws no name, whatever the tests that set one by hand say")
	}
}
