package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// /copy: THE WHOLE CHAT, WHERE THE TERMINAL CAN SELECT ALL OF IT (2026-11-21).
//
// The chat is drawn on the terminal's alternate screen (main.go,
// tea.WithAltScreen), and the alternate screen has no scrollback. A drag
// selects only the rows in the window, so an answer taller than the window
// could be copied only a screen at a time. The owner, 2026-11-21: "i can't be
// able to copy the complete information, i can only be able to select the
// visible part". ADR-001 had named the limit when capture was turned off.
//
// This program cannot put text on the clipboard there. GNOME Terminal ignores
// OSC 52, a stock desktop has no clipboard program, and needing one was
// refused ("no new clipboards", 2026-10-25). So the text goes where the
// terminal itself can select all of it: its normal screen, which has
// scrollback. /copy leaves the alternate screen, prints the chat there and
// keeps one line of keys under it. The wheel scrolls through it, a drag
// selects any length of it (the terminal scrolls when a drag reaches its
// edge), and the terminal's own copy key copies. esc brings the chat back as
// it was.
//
// THE CHAT IS PRINTED AT NO WIDTH: renderTranscript at 0, the width before a
// terminal has reported one. Nothing is cut at the window's edge; the
// terminal wraps a long line itself, and copies a line it wrapped as the one
// line it is. A paragraph copied off the chat screen comes out cut into
// window-wide pieces; copied from here it comes out as written.
//
// What is printed stays in the terminal's scrollback after Mochiii quits, as
// any program's output does.

// copyViewPhase is where /copy is.
type copyViewPhase int

const (
	copyViewOff     copyViewPhase = iota
	copyViewShowing               // the normal screen holds the printed chat
	copyViewLeaving               // on the way back to the chat
)

// copyViewSettle is how long the key line is left blank before the chat screen
// comes back. The renderer draws at most once a frame (about 17ms), and the
// blank has to reach the terminal while the normal screen is still showing, or
// the key line would be left behind in the scrollback.
const copyViewSettle = 100 * time.Millisecond

// copyViewMsg moves /copy on a step: back is false when the key line has had
// time to be erased, true once the chat screen is up again.
type copyViewMsg struct{ back bool }

// copyViewHeader opens what /copy prints, so two of them in the scrollback can
// be told apart.
const copyViewHeader = "── Mochiii: this chat, to select and copy ──"

// copyViewKeys is the one line held under the printed chat. It fits in 80
// columns: a line wider than the window is cut, and esc comes first so a
// narrower one still says how to get back.
func copyViewKeys() string {
	return "esc back to the chat · drag to select, " + copyKey() + " copies · scroll up for more"
}

// openCopyView is /copy.
func (m chatModel) openCopyView() (tea.Model, tea.Cmd) {
	body := trimLineEnds(renderTranscript(m.turns, 0))
	if strings.TrimSpace(ansi.Strip(body)) == "" {
		return m.sayLocally("Nothing to copy yet: the chat is empty.")
	}
	if m.inline.on {
		// The chat is in the terminal's scrollback already, cut at the window's
		// width. /copy prints it again with no width, for a copy that keeps
		// each line whole; there is no other screen to go to.
		m.inline.pending = append(m.inline.pending, helpStyle.Render(copyViewHeader)+"\n\n"+body)
		return m, nil
	}
	m.copyView = copyViewShowing
	var cmds []tea.Cmd
	if m.mouseCaptured {
		// The terminal selects with a plain drag and scrolls with the wheel only
		// while this program is not asking for mouse reports.
		cmds = append(cmds, tea.DisableMouse)
	}
	// In this order: Bubble Tea drops a print made on the alternate screen.
	cmds = append(cmds, tea.ExitAltScreen, tea.Println(helpStyle.Render(copyViewHeader)+"\n\n"+body+"\n"))
	return m, tea.Sequence(cmds...)
}

// trimLineEnds takes the spaces off the end of every line. Lip Gloss pads each
// line of a multi-line block (a command's reply, a prompt of several lines) to
// the width of the longest. On the chat screen that is invisible; printed with
// no width to cut it, the padding wrapped into rows of blanks between every two
// lines (first live run, 2026-11-21), and a copy would carry it.
func trimLineEnds(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// keyOrCopyView sends a key to handleKey, unless /copy is showing.
//
// /copy ANSWERS ITS OWN KEYS, ctrl+c among them, which brings the chat back
// and never quits. Quitting from here would ask its question on the chat
// screen, which is not showing; and a ctrl+c that reaches this program from
// here was most likely meant as "copy".
func (m chatModel) keyOrCopyView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.copyView {
	case copyViewOff:
		// By its TYPE, not msg.String(): a paste arrives as one key holding all
		// of it, and stringifying a megabyte here -- before handleKey bounds
		// it -- took a 1 MB paste from half a millisecond to fifty
		// (repaintceiling_test.go). handleKey's own first line says the same.
		if msg.Type == tea.KeyCtrlAt && !msg.Alt {
			return m.toggleAutoApprove() // ctrl+space, in every state (autoapprove.go)
		}
		return m.handleKey(msg)
	case copyViewShowing:
		switch msg.String() {
		case "esc", "enter", "q", "ctrl+c":
			m.copyView = copyViewLeaving
			return m, tea.Tick(copyViewSettle, func(time.Time) tea.Msg { return copyViewMsg{} })
		}
	}
	return m, nil
}

// handleCopyView takes /copy back to the chat, one step at a time.
func (m chatModel) handleCopyView(msg copyViewMsg) (tea.Model, tea.Cmd) {
	if m.copyView != copyViewLeaving {
		return m, nil
	}
	if !msg.back {
		return m, tea.Sequence(tea.EnterAltScreen, func() tea.Msg { return copyViewMsg{back: true} })
	}
	m.copyView = copyViewOff
	if m.mouseCaptured {
		return m, tea.EnableMouseCellMotion
	}
	return m, nil
}

// orCopyView is the screen: chat, or what /copy shows in its place.
func (m chatModel) orCopyView(chat string) string {
	switch m.copyView {
	case copyViewShowing:
		return accentStyle.Render(copyViewKeys())
	case copyViewLeaving:
		return "" // erases the key line, so it is not left in the scrollback
	}
	return chat
}
