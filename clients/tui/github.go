package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /github, /issue, /issues and /pr: Mochiii on the project's GitHub repository
// (protocol/github.go has why it is shaped this way).
//
// THE CLIENT ONLY ASKS. The token is the daemon's: this client sends it once,
// from a masked box, the moment it is entered, and keeps no copy -- not on the
// screen, not in the prompt history, not in a field. Everything else is a
// question the daemon answers, and what is printed is its answer.
//
// WHAT IS WORTH ASKING ABOUT GOES WITH THE NEXT QUESTION, ONCE. An issue, a
// pull request and a CI failure are attached the way /git output is
// (attach.go): shown here, then sent to the model with the user's next
// question as command output, which the daemon fences. A list or a status line
// is only shown.

// errDaemonPredatesGitHub is a daemon started before it could reach GitHub.
var errDaemonPredatesGitHub = errors.New("the running daemon is older than this client and cannot reach GitHub -- " +
	"restart it: ./run-tui.sh --stop && ./run-tui.sh")

const (
	githubTokenPrompt = "paste the GitHub token for Mochiii's own account, then enter (hidden; esc cancels)"

	githubUsage = "/github             who Mochiii is on GitHub, and this project's repository\n" +
		"/github connect     paste a token for Mochiii's own GitHub account (hidden as you type)\n" +
		"/github use         set this project's repository from its origin remote\n" +
		"/github disconnect  forget the token\n" +
		"/issue 104          an issue and its comments, attached to your next question\n" +
		"/issues [label]     the open issues\n" +
		"/pr                 the open pull requests\n" +
		"/pr 31              one pull request: checks, reviews, whether it can merge (attached)\n" +
		"/pr 31 log          why its CI failed (attached)"
)

// githubResultMsg carries the daemon's answer back into Update, with the
// request it answers.
type githubResultMsg struct {
	req  protocol.GitHubRequest
	resp protocol.GitHubResponse
	err  error
}

// sendGitHub hands the daemon a GitHubRequest and returns its answer.
func sendGitHub(clientName string, req protocol.GitHubRequest) (protocol.GitHubResponse, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return protocol.GitHubResponse{}, err
	}
	defer func() { _ = sess.Close() }()
	if !hasFeature(sess.handshake, protocol.FeatureGitHub) {
		return protocol.GitHubResponse{}, errDaemonPredatesGitHub
	}
	req.ProtocolVersion = protocol.ProtocolVersion
	req.GitHub = true
	if err := sess.enc.Encode(req); err != nil {
		return protocol.GitHubResponse{}, fmt.Errorf("sending the request: %w", err)
	}
	var resp protocol.GitHubResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return protocol.GitHubResponse{}, fmt.Errorf("reading the answer: %w", err)
	}
	return resp, nil
}

// submitGitHub performs the round trip off the UI thread. The request it
// remembers for the answer never carries the token.
func submitGitHub(clientName string, req protocol.GitHubRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendGitHub(clientName, req)
		req.Token = ""
		return githubResultMsg{req: req, resp: resp, err: err}
	}
}

// handleGitHubSlash answers the GitHub commands, and the command nobody has
// (handleExtraSlash in rules.go hands both here).
func (m chatModel) handleGitHubSlash(name, args string) (tea.Model, tea.Cmd) {
	args = strings.TrimSpace(args)
	fields := strings.Fields(args)
	ask := func(req protocol.GitHubRequest) (tea.Model, tea.Cmd) { return m, submitGitHub(m.clientName, req) }
	switch name {
	case "github":
		switch args {
		case "", "status":
			return ask(protocol.GitHubRequest{Action: protocol.GitHubActionStatus})
		case "connect":
			return m.beginGitHubConnect()
		case "use":
			return ask(protocol.GitHubRequest{Action: protocol.GitHubActionUse})
		case "disconnect":
			return ask(protocol.GitHubRequest{Action: protocol.GitHubActionDisconnect})
		case "help":
			return m.sayLocally(githubUsage)
		}
		// Never echoed: a token pasted after /github connect on one line would
		// otherwise be printed back.
		return m.sayLocally("/github takes connect, use or disconnect. To connect, type /github connect on its " +
			"own and paste the token into the hidden box that opens.\n\n" + githubUsage)
	case "issue":
		n, err := strconv.Atoi(args)
		if err != nil || n < 1 {
			return m.sayLocally("/issue <number>, like /issue 104")
		}
		return ask(protocol.GitHubRequest{Action: protocol.GitHubActionIssue, Number: n})
	case "issues":
		return ask(protocol.GitHubRequest{Action: protocol.GitHubActionIssues, Label: args})
	case "pr":
		if len(fields) == 0 {
			return ask(protocol.GitHubRequest{Action: protocol.GitHubActionPRs})
		}
		n, err := strconv.Atoi(fields[0])
		switch {
		case err != nil || n < 1 || len(fields) > 2 || len(fields) == 2 && fields[1] != "log":
			return m.sayLocally("/pr lists the open pull requests; /pr 31 shows one; /pr 31 log says why its CI failed")
		case len(fields) == 2:
			return ask(protocol.GitHubRequest{Action: protocol.GitHubActionLog, Number: n})
		}
		return ask(protocol.GitHubRequest{Action: protocol.GitHubActionPR, Number: n})
	}
	return m.sayLocally("unknown local command")
}

// beginGitHubConnect opens the masked box /connect uses (connect.go), for a
// GitHub token instead of a provider key.
func (m chatModel) beginGitHubConnect() (tea.Model, tea.Cmd) {
	m.githubTokenEntry = true
	updated, cmd := m.beginConnect()
	next := updated.(chatModel)
	next.input.Placeholder = githubTokenPrompt
	return next, cmd
}

// handleGitHubTokenKey owns enter and esc while a GitHub token is typed.
// handled is false for every other key, which edits the box as usual.
func (m chatModel) handleGitHubTokenKey(key string) (next tea.Model, cmd tea.Cmd, handled bool) {
	switch key {
	case "esc":
		m.endConnect()
		next, cmd = m.sayLocally("GitHub connect cancelled; nothing was sent or stored")
		return next, cmd, true
	case "enter":
		token := strings.TrimSpace(m.input.Value())
		// Cleared before anything else: from here the token exists only in this
		// variable and in the request about to be sent.
		m.endConnect()
		if token == "" {
			next, cmd = m.sayLocally("no token entered; nothing was sent or stored")
			return next, cmd, true
		}
		next, _ = m.sayLocally("checking the token with GitHub…")
		return next, submitGitHub(m.clientName, protocol.GitHubRequest{Action: protocol.GitHubActionConnect, Token: token}), true
	}
	return m, nil, false
}

// handleGitHubResult shows the daemon's answer, and keeps what is worth asking
// about for the next question.
func (m chatModel) handleGitHubResult(msg githubResultMsg) (tea.Model, tea.Cmd) {
	text := formatGitHubResult(msg)
	if msg.err == nil && msg.resp.Error == "" && msg.resp.Attach {
		command := "issue"
		if msg.req.Action != protocol.GitHubActionIssue {
			command = "pr"
		}
		text = m.attachOutput(command, text)
	}
	return m.sayLocally(text)
}

// formatGitHubResult is the answer as the user reads it. Most of it was
// written on GitHub, so all of it is sanitised on the way to the terminal.
func formatGitHubResult(msg githubResultMsg) string {
	switch {
	case msg.err != nil:
		return "⚠ " + sanitizeText(msg.err.Error())
	case msg.resp.Error != "":
		return "⚠ " + sanitizeText(msg.resp.Error)
	}
	return sanitizeText(strings.TrimSpace(msg.resp.Text))
}
