package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// WHAT /git AND /search PRINT GOES TO THE MODEL ONCE, WITH THE NEXT QUESTION.
//
// THE DEFECT (docs/OPEN_ITEMS.md item 53). Every other local command's output
// has been kept out of the conversation since 2026-10-06. These two were left
// in on purpose -- they fetch something to talk about, "write a commit message
// for this" -- and the way they were left in was as an ASSISTANT turn, re-sent
// as history with every later question. So the model was shown, as words it
// had said itself, text it never wrote and the user never typed: `git status`
// prints file and branch names chosen by whoever made the repository, and a
// search prints whatever the saved chats held. Every turn, for the rest of the
// chat, and gone after a restart.
//
// NOW the output is shown here like any other command's and is not part of the
// conversation. It is kept aside and sent with the user's NEXT question, once,
// as protocol.AttachedOutput -- and the daemon, not this client, puts it in
// front of the model, bounded, scrubbed and fenced as command output.

// attachNote is the line under the output that says where it will go.
const attachNote = "↳ attached: it goes to the model with your next question, once, marked as command output"

// attachableFailures are the answers that are not output: there is nothing in
// them to ask the model about, and "git status failed" attached to the next
// question would only be a puzzle.
var attachableFailures = []string{
	"git status failed", "no workspace is set", "(git status: empty)",
	"search failed", "no matching turns found",
}

// attachOutput keeps a command's output to go with the next question and
// returns what to show for it. A second run of the same command replaces the
// first: what is asked about is what is on screen now.
func (m *chatModel) attachOutput(name, output string) string {
	for _, failure := range attachableFailures {
		if strings.HasPrefix(output, failure) {
			return output
		}
	}
	command := "/" + name
	kept := m.attached[:0:0]
	for _, a := range m.attached {
		if a.Command != command {
			kept = append(kept, a)
		}
	}
	m.attached = append(kept, protocol.AttachedOutput{Command: command, Output: output})
	return output + "\n\n" + attachNote
}

// takeAttached is what the request about to be sent carries. It is taken, not
// copied: once means once. The transcript says what went, under the question
// it went with, because nothing else on screen would.
func (m *chatModel) takeAttached() []protocol.AttachedOutput {
	if len(m.attached) == 0 {
		return nil
	}
	taken := m.attached
	m.attached = nil
	m.appendTurn(turn{role: roleSystem, text: "↳ sent with this question, once: the output of " + attachedNames(taken)})
	m.refreshViewport()
	return taken
}

// attachedNames lists the commands for a sentence: "/git", "/git and /search".
func attachedNames(atts []protocol.AttachedOutput) string {
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Command
	}
	return strings.Join(names, " and ")
}

// attachedNotSentMsg says the daemon that took this question is too old to
// take what was attached to it.
type attachedNotSentMsg struct{ commands string }

// handleAttachedNotSent says so in the transcript. A daemon without the feature
// ignores the field, so without this the line above -- "sent with this
// question" -- would be false and nothing would ever say it.
func (m chatModel) handleAttachedNotSent(msg attachedNotSentMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	m.appendTurn(turn{role: roleSystem, text: "⚠ NOT sent after all: the running daemon is older and cannot take the output of " +
		msg.commands + ". Restart it (./run-tui.sh --stop && ./run-tui.sh), or paste what you want asked about."})
	m.resizeViewport()
	m.refreshViewport()
	return m, waitForNext(m.streamCh)
}
