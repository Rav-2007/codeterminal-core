package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// THE CHAT SAYS IT IS WORKING FOR THE WHOLE TURN (owner, 2026-11-21: "show any
// indicator while it working") -- after the answer has begun, while a tool runs
// -- and not while a question waits for the person.
//
// Neuter check: make workingShown return awaitingAnswer() (the old rule) and
// the tool-running assertion fails.
func TestTheChatSaysItIsWorkingWhileAToolRuns(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "fix the build")
	m, _ = pressEnter(m)
	next, _ := m.Update(tokenMsg("Let me look at the build first."))
	m = next.(chatModel)
	m.noteToolActivity(call("c1", "sandbox_exec", "go build ./...", protocol.ToolPhaseRunning, 0))
	m.refreshViewport()

	view := ansi.Strip(m.viewport.View())
	if !strings.Contains(view, "working… (esc to stop)") {
		t.Fatalf("nothing says it is working while a tool runs:\n%s", view)
	}
	lines := strings.Split(strings.TrimRight(view, "\n "), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); !strings.HasSuffix(last, "working… (esc to stop)") {
		t.Errorf("the working line is not the last line of the chat: %q", last)
	}

	// A tool running before any answer text is working too, not thinking.
	m2 := newTestModel()
	m2 = typeText(m2, "run the tests")
	m2, _ = pressEnter(m2)
	m2.noteToolActivity(call("c1", "sandbox_exec", "go test ./...", protocol.ToolPhaseRunning, 0))
	m2.refreshViewport()
	if v := ansi.Strip(m2.viewport.View()); !strings.Contains(v, "working… (esc to stop)") || strings.Contains(v, "thinking…") {
		t.Errorf("a tool running before the answer is not called working:\n%s", v)
	}

	req := inProjectRequest()
	next, _ = m.Update(toolApprovalMsg{req: req, reply: make(chan string, 1)})
	if v := ansi.Strip(next.(chatModel).viewport.View()); strings.Contains(v, "(esc to stop)") {
		t.Errorf("the working line is drawn over a question waiting for the person:\n%s", v)
	}
}
