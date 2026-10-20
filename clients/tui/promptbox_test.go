package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// bottomRows is the last n rows of the screen, as a reader sees them.
func bottomRows(m chatModel, n int) []string {
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) < n {
		return lines
	}
	return lines[len(lines)-n:]
}

// THE REQUEST THIS ANSWERS (OWNER, 2026-10-12): "highlight the prompt like the
// images I added". The prompt is drawn in a box the whole width of the
// terminal, directly above the line that names the model.
//
// Neuter check: return content unchanged from promptBox -- there is no border,
// and the first assertion fails.
func TestThePromptIsDrawnInABox(t *testing.T) {
	m := connectedModel(t, nvidiaInForce(t))
	rows := bottomRows(m, 5)
	top, prompt, bottom, status, help := rows[0], rows[1], rows[2], rows[3], rows[4]

	if !strings.HasPrefix(top, "╭") || !strings.HasSuffix(top, "╮") || !strings.HasPrefix(bottom, "╰") || !strings.HasSuffix(bottom, "╯") {
		t.Fatalf("the prompt has no box round it:\n%s", strings.Join(rows, "\n"))
	}
	for name, row := range map[string]string{"top": top, "prompt": prompt, "bottom": bottom} {
		if w := lipgloss.Width(row); w != m.width {
			t.Errorf("the box's %s row is %d columns wide, want the terminal's %d", name, w, m.width)
		}
	}
	if !strings.HasPrefix(prompt, "│ > ") || !strings.HasSuffix(prompt, "│") || !strings.Contains(prompt, defaultInputPlaceholder) {
		t.Errorf("the prompt is not inside the box: %q", prompt)
	}
	if !strings.HasPrefix(status, "z-ai/glm-5.3 · NVIDIA") || help != helpText {
		t.Errorf("the lines under the box are not the model line and the help:\n%s\n%s", status, help)
	}

	// What is typed is typed inside it.
	typed := bottomRows(typeText(m, "hello there"), 5)[1]
	if !strings.HasPrefix(typed, "│ > hello there") || !strings.HasSuffix(typed, "│") {
		t.Errorf("typed text is not inside the box: %q", typed)
	}
}

// THE BOX IS ALWAYS THREE ROWS AND NEVER WIDER THAN THE TERMINAL -- with a
// prompt far longer than the terminal, while a turn runs, and when a tool
// approval's keys have taken the prompt's place. One row out and the top of
// the transcript scrolls away.
func TestTheBoxKeepsTheScreenTheSizeOfTheTerminal(t *testing.T) {
	states := map[string]func(chatModel) chatModel{
		"idle":          func(m chatModel) chatModel { return m },
		"a long prompt": func(m chatModel) chatModel { return typeText(m, strings.Repeat("a long question ", 40)) },
		"a turn running": func(m chatModel) chatModel {
			m.state = stateSending
			return m
		},
		"a tool approval": func(m chatModel) chatModel {
			req := protocol.ToolApprovalRequest{Server: "builtin", Tool: "sandbox_exec"}
			m.state, m.pendingApproval = stateToolApproval, &req
			return m
		},
	}
	for _, size := range []struct{ w, h int }{{120, 50}, {80, 24}, {60, 16}, {30, promptBoxMinHeight}, {promptBoxMinWidth, 14}} {
		for name, reach := range states {
			updated, _ := connectedModel(t, nvidiaInForce(t)).Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			m := reach(updated.(chatModel))
			lines := strings.Split(m.View(), "\n")
			if len(lines) != size.h {
				t.Errorf("%s at %dx%d: the screen is %d lines tall", name, size.w, size.h, len(lines))
			}
			box := lines[len(lines)-5 : len(lines)-2]
			if !strings.HasPrefix(ansi.Strip(box[0]), "╭") || !strings.HasPrefix(ansi.Strip(box[2]), "╰") {
				t.Errorf("%s at %dx%d: the box is not the three rows above the model line:\n%s", name, size.w, size.h, ansi.Strip(strings.Join(box, "\n")))
			}
			for _, row := range box {
				if w := lipgloss.Width(row); w != size.w {
					t.Errorf("%s at %dx%d: a row of the box is %d columns wide: %q", name, size.w, size.h, w, ansi.Strip(row))
				}
			}
		}
	}
}

// A terminal too small for it keeps the bare prompt it always had, and its rows
// for the transcript -- and the screen is still exactly the terminal's height.
func TestASmallTerminalKeepsTheBarePrompt(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, promptBoxMinHeight - 1}, {promptBoxMinWidth - 1, 30}, {40, 6}} {
		updated, _ := connectedModel(t, nvidiaInForce(t)).Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m := updated.(chatModel)
		view := ansi.Strip(m.View())
		if strings.ContainsAny(view, "╭╰│") {
			t.Errorf("at %dx%d the prompt is boxed:\n%s", size.w, size.h, view)
		}
		if got := len(strings.Split(view, "\n")); got != size.h {
			t.Errorf("at %dx%d the screen is %d lines tall", size.w, size.h, got)
		}
		if m.input.Width != inputWidthFor(size.w, m.input.Prompt) {
			t.Errorf("at %dx%d the input is %d wide, want the bare prompt's %d", size.w, size.h, m.input.Width, inputWidthFor(size.w, m.input.Prompt))
		}
	}
	// And in a terminal with the box, the input is sized for the inside of it.
	m := newTestModel()
	if want := inputWidthFor(m.width-promptBoxCols, m.input.Prompt); m.input.Width != want {
		t.Errorf("the boxed input is %d wide, want %d", m.input.Width, want)
	}
}

// THE BORDER SAYS WHOSE TURN IT IS: blue while the user is waited for -- to
// type, or to answer an approval -- and grey while a turn runs.
func TestTheBoxIsBlueWhenTheUserIsWaitedFor(t *testing.T) {
	blue, grey := promptBoxStyle.GetBorderTopForeground(), promptBoxQuiet.GetBorderTopForeground()
	if blue == grey {
		t.Fatal("the two borders are the same colour")
	}
	m := newTestModel()
	for state, want := range map[chatState]lipgloss.TerminalColor{
		stateIdle: blue, stateError: blue, stateToolApproval: blue, stateEditReview: blue,
		stateSending: grey, stateStreaming: grey,
	} {
		m.state = state
		if got := m.promptBoxBorder().GetBorderTopForeground(); got != want {
			t.Errorf("in state %v the border is %v, want %v", state, got, want)
		}
	}
}

// The command popup sits on top of the box, and neither loses a row to the other.
func TestThePopupSitsOnTheBox(t *testing.T) {
	m := typeText(connectedModel(t, nvidiaInForce(t)), "/acc")
	rows := bottomRows(m, 8)
	if !strings.Contains(rows[1], "> /account") || !strings.HasPrefix(rows[2], "╰") {
		t.Errorf("the popup is not directly above the box:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[3], "╭") || !strings.HasPrefix(rows[4], "│ > /acc") || !strings.HasPrefix(rows[5], "╰") {
		t.Errorf("the box under the popup is broken:\n%s", strings.Join(rows, "\n"))
	}
}
