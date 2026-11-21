package tui

import (
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// THE CHAT SAYS IT IS WORKING FOR AS LONG AS IT IS (owner, 2026-11-21: "show any
// indicator while it working"). Until then the one line was "💭 thinking", and
// only until the answer's first word: once a reply had started, nothing at the
// end of the chat moved while a tool ran or the model worked out its next step
// -- only a small spinner on the line above the prompt, easy to miss.
//
// Now the chat ends in one line for the whole turn, from the question to the
// last word: a spinner, "thinking" until anything has happened and "working"
// once a tool has run or the answer has begun, and the key that stops it. It is not there while a question waits for
// you (that panel is the thing to look at) nor during a rate-limit wait, which
// says its own countdown in the same place (ratewait.go).

// workingFrames is the spinner, one frame a tick (thinkingInterval).
var workingFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// workingShown reports whether the line is drawn: a turn is running and is
// not waiting on the person.
func (m chatModel) workingShown() bool {
	return m.turnInFlight()
}

// workingLine is the line as drawn for the current frame.
//
// DRAWN ONCE PER FRAME, NOT PER TOKEN. The line is on screen while an answer
// streams, and the chat is redrawn for every token: styling it each time took
// the allocations per token from under 45 to 55 (renderbench_test.go). There are
// only twenty lines it can be -- ten frames, thinking or working -- so each is
// styled once per colour profile and kept.
func (m chatModel) workingLine() string {
	key := workingKey{frame: m.thinkingFrame % len(workingFrames), thinking: m.awaitingAnswer() && !m.turnActed, profile: lipgloss.ColorProfile()}
	workingCache.Lock()
	defer workingCache.Unlock()
	if line, ok := workingCache.lines[key]; ok {
		return line
	}
	what := "working"
	if key.thinking {
		what = "thinking"
	}
	line := accentStyle.Render(workingFrames[key.frame]) + helpStyle.Render(" "+what+"… (esc to stop)")
	if workingCache.lines == nil {
		workingCache.lines = map[workingKey]string{}
	}
	workingCache.lines[key] = line
	return line
}

type workingKey struct {
	frame    int
	thinking bool
	profile  termenv.Profile
}

// workingCache is shared by every model; the tests run them side by side.
var workingCache struct {
	sync.Mutex
	lines map[workingKey]string
}

// withIndicator puts the line at the end of the chat, after a blank line when
// there is chat above it -- in one concatenation (refreshViewport).
func withIndicator(content, line string) string {
	sep := ""
	if content != "" {
		sep = turnSeparator
	}
	return content + sep + line
}
