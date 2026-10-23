package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /remember, /rules and /forget: the rules the user saves for this project.
//
// WHY (OWNER, 2026-10-13). A correction made in one chat was gone in the next:
// nothing the user said in one conversation reached another. /remember keeps
// one line, and the DAEMON puts every kept line in front of the model at the
// start of each turn in this project (daemon/userrules.go) -- so a rule saved
// here holds in the editor too, and this client remembers nothing.
//
// THE CLIENT ONLY ASKS. It does not read the file, count the rules or decide
// what may be one: the daemon answers every request with the rules as they now
// stand, and what is printed is that answer.
//
// WHAT A COMMAND PRINTS IS NOT PART OF THE CONVERSATION, like every local
// reply (attach.go). The model sees a rule because the daemon adds it to the
// turn, not because "Saved as rule 3" was replayed to it as its own words.

// errDaemonPredatesRules is a daemon started before it could keep rules.
var errDaemonPredatesRules = errors.New("the running daemon is older than this client and cannot keep rules -- " +
	"restart it: ./run-tui.sh --stop && ./run-tui.sh")

const (
	rememberUsage = "/remember <text>   keep a fact or an instruction\n" +
		"Every later chat in this project starts with what you keep.\n\n" +
		"Example: /remember tests in this project are run with make check"
	forgetUsage = "/forget <number>   remove a saved rule; /rules shows the numbers"
)

// rulesResultMsg carries the daemon's answer back into Update, with the request
// it answers.
type rulesResultMsg struct {
	req  protocol.RulesRequest
	resp protocol.RulesResponse
	err  error
}

// sendRules hands the daemon a RulesRequest and returns its answer.
func sendRules(clientName string, req protocol.RulesRequest) (protocol.RulesResponse, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return protocol.RulesResponse{}, err
	}
	defer func() { _ = sess.Close() }()
	if !hasFeature(sess.handshake, protocol.FeatureRules) {
		return protocol.RulesResponse{}, errDaemonPredatesRules
	}

	req.ProtocolVersion = protocol.ProtocolVersion
	req.Rules = true
	if err := sess.enc.Encode(req); err != nil {
		return protocol.RulesResponse{}, fmt.Errorf("sending the request: %w", err)
	}
	var resp protocol.RulesResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return protocol.RulesResponse{}, fmt.Errorf("reading the answer: %w", err)
	}
	return resp, nil
}

// submitRules performs the round trip off the UI thread.
func submitRules(clientName string, req protocol.RulesRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendRules(clientName, req)
		return rulesResultMsg{req: req, resp: resp, err: err}
	}
}

// handleExtraSlash answers the local commands that are not cases of
// handleLocalSlash's switch: the three here, then GitHub's and the command
// nobody has (github.go).
//
// IT IS THAT SWITCH'S default, and the reason is chatExtras' (statusline.go):
// clients/tui/chat.go must not grow, so a command added since is reached
// through a line that was already there.
func (m chatModel) handleExtraSlash(name, args string) (tea.Model, tea.Cmd) {
	args = strings.TrimSpace(args)
	switch name {
	case "remember":
		// Never empty: a command that needs something is answered with its
		// usage before it gets here (slash.go, NeedsArgs).
		return m, submitRules(m.clientName, protocol.RulesRequest{Action: protocol.RulesActionAdd, Text: args})
	case "rules":
		return m, submitRules(m.clientName, protocol.RulesRequest{Action: protocol.RulesActionList})
	case "agent", "web":
		return m.handleLocalModeCommand(name, args) // localmodels.go
	case "forget":
		n, err := strconv.Atoi(args)
		if err != nil || n < 1 {
			return m.sayLocally(forgetUsage)
		}
		return m, submitRules(m.clientName, protocol.RulesRequest{Action: protocol.RulesActionRemove, Number: n})
	}
	return m.handleGitHubSlash(name, args) // github.go: the GitHub commands, and the unknown one
}

// sayLocally shows text as a command's reply: on the screen, and not part of
// what the next question carries.
func (m chatModel) sayLocally(text string) (tea.Model, tea.Cmd) {
	m.appendTurn(turn{role: roleAssistant, text: text, verbatim: true, local: true})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// handleRulesResult shows the daemon's answer.
func (m chatModel) handleRulesResult(msg rulesResultMsg) (tea.Model, tea.Cmd) {
	return m.sayLocally(formatRulesResult(msg))
}

// formatRulesResult turns the answer into the lines a user reads. A rule comes
// back from a file a person can edit, and the path and any refusal are the
// daemon's words: all of it is sanitized on the way to a terminal.
func formatRulesResult(msg rulesResultMsg) string {
	if msg.err != nil {
		return "⚠ " + sanitizeText(msg.err.Error())
	}
	resp := msg.resp
	if resp.Error != "" {
		return "⚠ " + sanitizeText(resp.Error)
	}
	n := len(resp.Rules)
	var b strings.Builder
	switch msg.req.Action {
	case protocol.RulesActionAdd:
		if n == 0 {
			return "Saved."
		}
		fmt.Fprintf(&b, "Saved. From now on every chat in this project starts with it:\n  %d. %s\n\n", n, sanitizeText(resp.Rules[n-1]))
		fmt.Fprintf(&b, "/rules lists the rules; /forget %d removes this one.", n)
	case protocol.RulesActionRemove:
		fmt.Fprintf(&b, "Removed: %s\n", sanitizeText(resp.Removed))
		switch n {
		case 0:
			b.WriteString("No rules left for this project.")
		case 1:
			b.WriteString("1 rule left.")
		default:
			fmt.Fprintf(&b, "%d rules left.", n)
		}
	default:
		if n == 0 {
			b.WriteString("No rules saved for this project.\n\n" + rememberUsage)
			break
		}
		b.WriteString("Every chat in this project starts with these rules:\n")
		for i, rule := range resp.Rules {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, sanitizeText(rule))
		}
		b.WriteString("\n/remember <text> adds one; /forget <number> removes one.")
		if resp.Path != "" {
			b.WriteString("\nKept in " + sanitizeText(resp.Path))
		}
	}
	if resp.Unused > 0 {
		fmt.Fprintf(&b, "\n\n⚠ %d line(s) of that file are over its limits and are not in use.", resp.Unused)
	}
	return b.String()
}
