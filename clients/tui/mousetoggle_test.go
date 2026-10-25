package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// /mouse must actually change the terminal's mode, not merely say so. The Cmd
// it returns is the only thing that reaches the terminal, so that is what is
// asserted -- a version that flipped the flag and returned nil would leave the
// UI claiming one thing and the terminal doing another.
func TestMouseToggleSwitchesTheTerminalMode(t *testing.T) {
	m := newTestModel()
	if m.mouseCaptured {
		t.Fatal("the model does not start matching main.go, which starts without mouse capture")
	}

	m = typeText(m, "/mouse")
	updated, cmd := pressEnter(m)
	m = updated
	if !m.mouseCaptured {
		t.Error("/mouse did not turn capture on")
	}
	if cmd == nil {
		t.Fatal("/mouse changed the flag but sent nothing to the terminal")
	}
	assertMouseCmd(t, cmd, "enable")

	m = typeText(m, "/mouse")
	updated, cmd = pressEnter(m)
	m = updated
	if m.mouseCaptured {
		t.Error("/mouse did not turn capture back off")
	}
	assertMouseCmd(t, cmd, "disable")
}

// THE TERMINAL KEEPS THE MOUSE FROM THE START (2026-10-25). A drag selects in
// the terminal, and its copy key copies, only while this program has not asked
// for mouse reports -- so main.go must not ask, and the model must agree with
// it, or /mouse would report the opposite of what the terminal is doing. Read
// from the source, because the options given to tea.NewProgram are visible
// nowhere else.
func TestTheProgramStartsWithoutMouseCapture(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "tea" && strings.HasPrefix(sel.Sel.Name, "WithMouse") {
				t.Errorf("main.go starts the program with tea.%s: the terminal can no longer select "+
					"with a plain drag, so its copy key copies nothing", sel.Sel.Name)
			}
		}
		return true
	})
	if newChatModel("t", "/ws", "/ws", nil).mouseCaptured {
		t.Error("the model starts with mouseCaptured set, but main.go asks for no mouse reports")
	}
}

// assertMouseCmd runs the Cmd and checks which Bubble Tea mouse message it
// produced. The message types are what the renderer acts on.
func assertMouseCmd(t *testing.T, cmd tea.Cmd, want string) {
	t.Helper()
	msg := cmd()
	// tea.Batch wraps; unwrap one level if needed.
	if batch, ok := msg.(tea.BatchMsg); ok && len(batch) > 0 {
		msg = batch[0]()
	}
	got := strings.ToLower(strings.TrimPrefix(typeName(msg), "tea."))
	if !strings.Contains(got, want) {
		t.Errorf("/mouse produced %s, wanted something that %ss mouse reporting", typeName(msg), want)
	}
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }

// The user must be told what changed, in the transcript, in words that say what
// it means for them rather than naming a terminal mode.
func TestMouseToggleSaysWhatItDidInPlainTerms(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "/mouse")
	m, _ = pressEnter(m)
	last := m.turns[len(m.turns)-1].text
	for _, want := range []string{"wheel", "shift", "ON"} {
		if !strings.Contains(strings.ToLower(last), strings.ToLower(want)) {
			t.Errorf("turning capture on did not mention %q: %q", want, last)
		}
	}

	m = typeText(m, "/mouse")
	m, _ = pressEnter(m)
	last = m.turns[len(m.turns)-1].text
	for _, want := range []string{"select", "OFF", copyKey()} {
		if !strings.Contains(last, want) {
			t.Errorf("turning capture off did not mention %q: %q", want, last)
		}
	}
}

// DISCOVERABILITY IS THE POINT. Mouse capture is invisible until it bites:
// dragging to select does nothing and there is no error to search for. If the
// only way to find /mouse were to read the source, the toggle would not have
// fixed anything.
//
// Since 2026-10-25 a plain drag selects and the terminal's copy key copies,
// and prompt history moved to ctrl+up/down -- the idle hint must name both,
// since those are what a user who cannot copy, or cannot find an old prompt,
// will look for.
func TestTheMouseToggleIsDiscoverable(t *testing.T) {
	for _, want := range []string{copyKey() + " copy", "ctrl+↑↓ history"} {
		if !strings.Contains(helpText, want) {
			t.Errorf("the idle hint does not say %q: %q", want, helpText)
		}
	}
	help := formatSlashHelp()
	for _, want := range []string{"ctrl+↑ ctrl+↓", copyKey() + " copies"} {
		if !strings.Contains(help, want) {
			t.Errorf("/help's keys do not say %q", want)
		}
	}
	if !strings.Contains(help, "/mouse") {
		t.Error("/help does not list /mouse")
	}
	if !strings.Contains(help, "select text") {
		t.Error("/help lists /mouse but does not say it is about selecting text, " +
			"which is the symptom someone will be searching for")
	}
}

// ctrl+v with no clipboard program does nothing, and LEAVES NO MARK: the note
// it used to add named clipboard programs to install, and the owner wants that
// note visible nowhere (2026-10-25). Not even a paste attempt is made -- the
// input's own paste would look for the same missing programs.
func TestCtrlVWithoutAClipboardToolLeavesNoMark(t *testing.T) {
	prev := clipboardToolAvailable
	clipboardToolAvailable = func() bool { return false }
	t.Cleanup(func() { clipboardToolAvailable = prev })

	m := newTestModel()
	turns, view := len(m.turns), m.View()
	for i := 0; i < 2; i++ {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
		m = updated.(chatModel)
		if cmd != nil {
			t.Error("ctrl+v with no clipboard program still started a paste")
		}
	}
	if len(m.turns) != turns {
		t.Errorf("ctrl+v added %d line(s) to the chat: %q", len(m.turns)-turns, m.turns[len(m.turns)-1].text)
	}
	if m.View() != view {
		t.Error("ctrl+v with no clipboard program changed the screen")
	}
	if !strings.Contains(formatSlashHelp(), pasteKey()+" ") {
		t.Errorf("/help does not list the terminal's paste key, %s", pasteKey())
	}
}

// The hint is ONE row, and the layout counts it as one: longer than a standard
// 80-column terminal and it wraps, desyncing the row count -- and the PTY tests,
// which wait for it on a 100-column screen, never see it whole.
func TestTheIdleHintFitsEightyColumns(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if hint := idleHelpTextFor(goos); lipgloss.Width(hint) > 80 {
			t.Errorf("on %s the idle hint is %d columns wide, want at most 80: %q", goos, lipgloss.Width(hint), hint)
		}
	}
	if copyKeyFor("darwin") != "cmd+c" || copyKeyFor("linux") != "ctrl+shift+c" {
		t.Errorf("copy keys: darwin %q, linux %q", copyKeyFor("darwin"), copyKeyFor("linux"))
	}
}
