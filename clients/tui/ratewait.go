package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// A RATE-LIMIT WAIT IS SHOWN WHILE IT LASTS, AND THEN IT IS GONE (2026-10-25).
//
// The daemon waits out a provider's rate limit before trying a call again
// (daemon/retry.go) and says so, as a step activity whose Tool is
// protocol.ToolWaiting. Drawn as a line of the chat, every wait stayed behind
// after it was over -- the owner, looking at two of them over a stopped turn:
// "there is something still visible?". A wait is a moment, not an event: it is
// drawn where "thinking" is, counts down, and is gone when it ends.

// rateWaitState is the wait in progress, if any. gen tells a tick of this wait
// from a tick of a wait it replaced.
type rateWaitState struct {
	until time.Time
	gen   int
	// silent is a wait on a provider that is not answering at all, which the
	// daemon also waits out within a turn (its patience.go): said as that, not
	// as a rate limit.
	silent bool
}

// rateWaitTickMsg redraws the countdown once a second while a wait lasts.
type rateWaitTickMsg struct{ gen int }

// noteActivity takes an activity from the daemon. A rate-limit wait becomes the
// wait in progress, and the Cmd returned counts it down; anything else means the
// call went ahead, so it ends the wait and is recorded as it always was.
func (m *chatModel) noteActivity(a protocol.ToolActivity) tea.Cmd {
	if a.Phase == protocol.ToolPhaseStep && a.Tool == protocol.ToolWaiting {
		wait := time.Duration(a.DurationMS) * time.Millisecond
		m.rateWait = rateWaitState{until: time.Now().Add(wait), gen: m.rateWait.gen + 1,
			silent: strings.HasPrefix(a.Detail, "the model provider is not answering")}
		return rateWaitTick(m.rateWait.gen)
	}
	m.rateWait.until = time.Time{}
	m.noteToolActivity(a)
	return nil
}

func rateWaitTick(gen int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return rateWaitTickMsg{gen: gen} })
}

// rateWaitLeft is how much of the wait in progress is left: none once the turn
// has ended, however it ended.
func (m chatModel) rateWaitLeft() time.Duration {
	if !m.turnInFlight() {
		return 0
	}
	return max(time.Until(m.rateWait.until), 0)
}

// rateWaitShown reports whether there is a wait to draw.
func (m chatModel) rateWaitShown() bool { return m.rateWaitLeft() > 0 }

// indicatorLine is what is drawn under the chat while a turn is working: the
// wait, counting down, or the "thinking" it stands in for.
func (m chatModel) indicatorLine() string {
	if left := m.rateWaitLeft(); left > 0 {
		secs := int((left + time.Second - 1) / time.Second)
		why := "rate-limited"
		if m.rateWait.silent {
			why = "the model provider is not answering"
		}
		return helpStyle.Render(fmt.Sprintf("⏳ %s — trying again in %ds", why, secs))
	}
	return m.thinkingLine()
}

// handleRateWaitTick redraws the countdown, and once more when it runs out.
func (m chatModel) handleRateWaitTick(msg rateWaitTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.rateWait.gen {
		return m, nil // a wait that a later one replaced
	}
	m.refreshViewport()
	if m.rateWaitShown() {
		return m, rateWaitTick(msg.gen)
	}
	return m, nil
}
