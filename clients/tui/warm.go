package tui

import (
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// THE PROVIDER CONNECTION, OPENED WHILE THE QUESTION IS TYPED (1.2.0).
//
// The daemon opens its provider connection when a client handshakes
// (daemon/providerconn.go). The app handshakes when it opens, which covers the
// first question, and again to send each question -- too late to help that
// one, which follows at once. After a pause long enough for the provider to
// have closed its idle connection, the next question would pay for a new one
// while the person watched. So the first key of a new question sends a
// handshake of its own, at most once every warmEvery: the daemon opens the
// connection while the rest is typed, or nothing, if one is still open.
//
// Only in the one binary, whose daemon is its own: a client built alone talks
// to whatever answers, and its tests' fake daemons count their handshakes.

const warmEvery = 20 * time.Second

var lastWarm atomic.Int64 // UnixNano of the last warm-up handshake

// updateInput applies msg to the prompt, and starts a warm-up when it turns an
// empty prompt into a question being typed.
func (m chatModel) updateInput(msg tea.Msg) (textinput.Model, tea.Cmd) {
	wasEmpty := m.input.Value() == ""
	in, cmd := m.input.Update(msg)
	if wasEmpty && in.Value() != "" && InOneBinary {
		if w := warmDaemon(time.Now()); w != nil {
			cmd = tea.Batch(cmd, w)
		}
	}
	return in, cmd
}

// warmDaemon is the warm-up handshake as a command, or nil within warmEvery of
// the last one.
func warmDaemon(now time.Time) tea.Cmd {
	last := lastWarm.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < warmEvery {
		return nil
	}
	if !lastWarm.CompareAndSwap(last, now.UnixNano()) {
		return nil
	}
	return func() tea.Msg {
		daemonAnswers()
		return nil
	}
}
