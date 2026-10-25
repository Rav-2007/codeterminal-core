package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// "THERE IS SOMETHING STILL VISIBLE?" (2026-10-25). Under a stopped turn the owner
// saw two "▸ the model provider is rate-limiting requests … — waiting" lines for
// waits long over. A wait is shown while it lasts (ratewait.go), and nothing of
// it stays in the chat.

func waitActivity(ms int64) toolActivityMsg {
	return toolActivityMsg{protocol.ToolActivity{
		Phase: protocol.ToolPhaseStep, Tool: protocol.ToolWaiting, DurationMS: ms,
		Detail: "the model provider is rate-limiting requests; trying again in 5s",
	}}
}

func TestAWaitIsShownWhileItLastsAndLeavesNothingBehind(t *testing.T) {
	m := streamingModelWithBacklog(t)
	turns := len(m.turns)

	u, cmd := m.Update(waitActivity(4200))
	m = u.(chatModel)
	if len(m.turns) != turns {
		t.Fatalf("a wait added %d line(s) to the chat; it is a moment, not a line", len(m.turns)-turns)
	}
	if cmd == nil {
		t.Fatal("a wait started no countdown")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "rate-limited — trying again in 5s") {
		t.Errorf("the wait is not shown while it lasts:\n%s", view)
	}

	// The call went ahead: the next activity ends the wait.
	u, _ = m.Update(toolActivityMsg{protocol.ToolActivity{CallID: "c9", Server: "builtin", Tool: "read_file", Target: "a.go", Phase: protocol.ToolPhaseRunning}})
	m = u.(chatModel)
	if view := ansi.Strip(m.View()); strings.Contains(view, "rate-limited") {
		t.Errorf("the wait is still shown after the call went ahead:\n%s", view)
	}

	// A wait that runs out is gone at the next tick, with nothing after it.
	u, _ = m.Update(waitActivity(4200))
	m = u.(chatModel)
	m.rateWait.until = time.Now().Add(-time.Millisecond)
	u, _ = m.Update(rateWaitTickMsg{gen: m.rateWait.gen})
	m = u.(chatModel)
	if view := ansi.Strip(m.View()); strings.Contains(view, "rate-limited") {
		t.Errorf("a wait that ran out is still shown:\n%s", view)
	}

	// And a stopped turn leaves no wait behind, however long it had to run.
	u, _ = m.Update(waitActivity(30000))
	m = u.(chatModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = u.(chatModel)
	if view := ansi.Strip(m.View()); strings.Contains(view, "rate-limited") || strings.Contains(view, "— waiting") {
		t.Errorf("a stopped turn left its wait on screen:\n%s", view)
	}
}

// A tick of a wait that a later one replaced draws nothing and stops.
func TestAReplacedWaitsTickIsIgnored(t *testing.T) {
	m := streamingModelWithBacklog(t)
	u, _ := m.Update(waitActivity(5000))
	m = u.(chatModel)
	old := m.rateWait.gen
	u, _ = m.Update(waitActivity(9000))
	m = u.(chatModel)
	if _, cmd := m.Update(rateWaitTickMsg{gen: old}); cmd != nil {
		t.Error("the replaced wait's tick kept counting")
	}
}

// A wait after the answer has started is shown too: "thinking" is gone by then,
// so the wait must not depend on it.
func TestAWaitAfterTheAnswerHasStartedIsShown(t *testing.T) {
	m := deliverToken(streamingModelWithBacklog(t), "Let me look. ")
	u, _ := m.Update(waitActivity(6000))
	m = u.(chatModel)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "rate-limited — trying again in 6s") {
		t.Errorf("a wait after the answer began is not shown:\n%s", view)
	}
}
