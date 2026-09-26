package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// /mouse must actually change the terminal's mode, not merely say so. The Cmd
// it returns is the only thing that reaches the terminal, so that is what is
// asserted -- a version that flipped the flag and returned nil would leave the
// UI claiming one thing and the terminal doing another.
func TestMouseToggleSwitchesTheTerminalMode(t *testing.T) {
	m := newTestModel()
	if m.mouseCaptured {
		t.Fatal("the model starts with capture ON; main.go starts without it so text can be selected")
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
	for _, want := range []string{"select", "OFF"} {
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
// Capture now starts OFF, so selecting text needs no toggle at all; the idle
// hint spends its room on copy and paste instead, and /mouse stays findable
// through /help.
func TestTheMouseToggleIsDiscoverable(t *testing.T) {
	if !strings.Contains(helpText, "copy") || !strings.Contains(helpText, "paste") {
		t.Errorf("the idle hint does not say how to copy and paste: %q", helpText)
	}
	help := formatSlashHelp()
	if !strings.Contains(help, "/mouse") {
		t.Error("/help does not list /mouse")
	}
	if !strings.Contains(help, "select text") {
		t.Error("/help lists /mouse but does not say it is about selecting text, " +
			"which is the symptom someone will be searching for")
	}
}

// With capture off the wheel arrives as up/down keys, so those must scroll the
// transcript -- otherwise turning capture off would have cost scrolling.
func TestArrowKeysScrollTheTranscriptWhenNoPopupIsOpen(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 80; i++ {
		m.turns = append(m.turns, turn{role: roleAssistant, text: fmt.Sprintf("line %d", i)})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(chatModel)
	if m.viewport.YOffset != bottom-1 {
		t.Fatalf("up: YOffset %d -> %d, want one line up", bottom, m.viewport.YOffset)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(chatModel)
	if m.viewport.YOffset != bottom {
		t.Errorf("down: YOffset = %d, want back at %d", m.viewport.YOffset, bottom)
	}
}

// ctrl+v with no clipboard tool used to do nothing at all. It now says how to
// paste -- once, not on every press.
func TestCtrlVWithoutAClipboardToolSaysHowToPaste(t *testing.T) {
	prev := clipboardToolAvailable
	clipboardToolAvailable = func() bool { return false }
	t.Cleanup(func() { clipboardToolAvailable = prev })

	m := newTestModel()
	for i := 0; i < 2; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
		m = updated.(chatModel)
	}
	hints := 0
	for _, tn := range m.turns {
		if strings.Contains(tn.text, "ctrl+shift+v") {
			hints++
		}
	}
	if hints != 1 {
		t.Errorf("%d paste hint(s) after two ctrl+v presses, want exactly 1", hints)
	}
}
