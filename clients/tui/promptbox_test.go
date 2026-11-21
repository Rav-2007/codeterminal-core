package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// promptAndBelow is the prompt's three rows and every row under them, as a
// reader sees them: the rules and the prompt, the status rows (statusRows), the
// keys.
func promptAndBelow(m chatModel) []string { return bottomRows(m, 4+m.statusRows()) }

// bottomRows is the last n rows of the screen, as a reader sees them.
func bottomRows(m chatModel, n int) []string {
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) < n {
		return lines
	}
	return lines[len(lines)-n:]
}

// THE REQUEST THIS ANSWERS (OWNER, 2026-10-12): "highlight the prompt like the
// images I added"; and since 2026-11-21, showing Cline's screen, "the prompt
// section should change like that". The prompt is the line between two rules
// the whole width of the terminal, after a ➤, directly above the line that
// names the model.
//
// Neuter check: return content unchanged from promptBox -- there are no rules,
// and the first assertion fails.
func TestThePromptIsBetweenTwoRules(t *testing.T) {
	m := connectedModel(t, nvidiaInForce(t))
	rows := promptAndBelow(m)
	top, prompt, bottom, status, help := rows[0], rows[1], rows[2], rows[3], rows[len(rows)-1]

	rule := strings.Repeat(promptRule, m.width)
	if top != rule || bottom != rule {
		t.Fatalf("the prompt is not between two rules:\n%s", strings.Join(rows, "\n"))
	}
	if w := lipgloss.Width(prompt); w != m.width {
		t.Errorf("the prompt's row is %d columns wide, want the terminal's %d", w, m.width)
	}
	if !strings.HasPrefix(prompt, promptMark+" ") || !strings.Contains(prompt, defaultInputPlaceholder) {
		t.Errorf("the prompt is not the ➤ line: %q", prompt)
	}
	if !strings.HasPrefix(status, "z-ai/glm-5.3 · NVIDIA") || help != helpText {
		t.Errorf("the lines under the box are not the model line and the help:\n%s\n%s", status, help)
	}

	// What is typed is typed inside it.
	typed := promptAndBelow(typeText(m, "hello there"))[1]
	if !strings.HasPrefix(typed, "➤ hello there") {
		t.Errorf("typed text is not on the prompt's line: %q", typed)
	}
}

// THE PROMPT IS ALWAYS THREE ROWS AND NEVER WIDER THAN THE TERMINAL -- with a
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
			// The rules and the input's rows between them -- more than one for a
			// long prompt (promptinput.go) -- directly above the status rows.
			below, pr := 1+m.statusRows(), m.promptRows()
			box := lines[len(lines)-pr-below : len(lines)-below]
			if rule := strings.Repeat(promptRule, size.w); ansi.Strip(box[0]) != rule || ansi.Strip(box[pr-1]) != rule {
				t.Errorf("%s at %dx%d: the rules are not the prompt's first and last rows above the status rows:\n%s", name, size.w, size.h, ansi.Strip(strings.Join(box, "\n")))
			}
			for _, row := range box {
				if w := lipgloss.Width(row); w != size.w {
					t.Errorf("%s at %dx%d: a row of the prompt is %d columns wide: %q", name, size.w, size.h, w, ansi.Strip(row))
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
		if strings.Contains(view, promptRule+promptRule) {
			t.Errorf("at %dx%d the prompt has its rules:\n%s", size.w, size.h, view)
		}
		if got := len(strings.Split(view, "\n")); got != size.h {
			t.Errorf("at %dx%d the screen is %d lines tall", size.w, size.h, got)
		}
		if m.input.Width != inputWidthFor(size.w, m.input.Prompt) {
			t.Errorf("at %dx%d the input is %d wide, want the bare prompt's %d", size.w, size.h, m.input.Width, inputWidthFor(size.w, m.input.Prompt))
		}
	}
	// And in a terminal with the rules, the input is sized for them.
	m := newTestModel()
	if want := inputWidthFor(m.width, m.input.Prompt); m.input.Width != want {
		t.Errorf("the input is %d wide, want %d", m.input.Width, want)
	}
}

// THE RULES SAY WHOSE TURN IT IS: blue while the user is waited for -- to
// type, or to answer an approval -- and grey while a turn runs.
func TestTheRulesAreBlueWhenTheUserIsWaitedFor(t *testing.T) {
	blue, grey := promptRuleStyle.GetForeground(), promptRuleQuiet.GetForeground()
	if blue == grey {
		t.Fatal("the two rules are the same colour")
	}
	m := newTestModel()
	for state, want := range map[chatState]lipgloss.TerminalColor{
		stateIdle: blue, stateError: blue, stateToolApproval: blue, stateEditReview: blue,
		stateSending: grey, stateStreaming: grey,
	} {
		m.state = state
		if got := m.promptRuleFor().GetForeground(); got != want {
			t.Errorf("in state %v the rules are %v, want %v", state, got, want)
		}
	}
}

// The command popup sits on top of the prompt, and neither loses a row to the
// other.
func TestThePopupSitsOnThePrompt(t *testing.T) {
	m := typeText(connectedModel(t, nvidiaInForce(t)), "/acc")
	rows := bottomRows(m, 7+m.statusRows())
	if !strings.Contains(rows[1], "> /account") || !strings.HasPrefix(rows[2], "╰") {
		t.Errorf("the popup is not directly above the prompt:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[3], promptRule) || !strings.HasPrefix(rows[4], promptMark+" /acc") || !strings.HasPrefix(rows[5], promptRule) {
		t.Errorf("the prompt under the popup is broken:\n%s", strings.Join(rows, "\n"))
	}
}
