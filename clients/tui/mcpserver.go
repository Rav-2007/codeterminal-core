package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /mcp-server -- what tools the daemon offers, and the user's own MCP servers.
//
// THE DAEMON IS ASKED; NOTHING IS RUN HERE (FOUND 2026-10-11). This command used
// to start a second copy of the daemon binary and print what THAT said. The copy
// loaded a config for itself, which under run-tui.sh was not the one the running
// daemon had been given, so a daemon with its tools on was reported as "Agent
// mode is OFF". It also meant a slash command executed a program found on disk,
// which had to be defended twice against a repository shipping its own (see the
// history in slash_hostileworkspace_test.go). Both problems end the same way:
// the one process that knows the answer is asked for it over its socket.
//
// ADDING A SERVER IS TWO STEPS. The first request carries no acknowledgement, so
// the daemon stores nothing and answers with what the server would be able to
// do; the user types the server's name, and only that sends the request that
// adds it. A program that runs with the user's full privileges is not added by
// one keystroke.

// errDaemonPredatesMCP is a daemon started before it could answer this.
var errDaemonPredatesMCP = errors.New("the running daemon is older than this client and cannot answer /mcp-server -- " +
	"restart it: ./run-tui.sh --stop && ./run-tui.sh")

// mcpResultMsg carries the daemon's answer back into Update, with the request
// it answers: an add that needs acknowledging is sent again from it.
type mcpResultMsg struct {
	req  protocol.MCPRequest
	resp protocol.MCPResponse
	err  error
}

// mcpServerUsage is what the command says when it cannot read what was typed.
const mcpServerUsage = "/mcp-server                                   the tools this daemon offers, and its MCP servers\n" +
	"/mcp-server add <name> [--env NAME] <command> [args…]   add an MCP server of your own\n" +
	"/mcp-server remove <name>                     remove one you added\n\n" +
	"Example: /mcp-server add files npx -y @modelcontextprotocol/server-filesystem /home/me/notes\n\n" +
	"--env takes the NAME of a variable in the daemon's environment (export it, or put it in .env, before " +
	"starting Mochiii). Never type a secret here: the command and its arguments are stored and shown."

// sendMCP hands the daemon an MCPRequest and returns its answer.
func sendMCP(clientName string, req protocol.MCPRequest) (protocol.MCPResponse, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return protocol.MCPResponse{}, err
	}
	defer func() { _ = sess.Close() }()
	if !hasFeature(sess.handshake, protocol.FeatureMCPAdmin) {
		return protocol.MCPResponse{}, errDaemonPredatesMCP
	}

	req.ProtocolVersion = protocol.ProtocolVersion
	req.MCP = true
	if err := sess.enc.Encode(req); err != nil {
		return protocol.MCPResponse{}, fmt.Errorf("sending the request: %w", err)
	}
	var resp protocol.MCPResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return protocol.MCPResponse{}, fmt.Errorf("reading the answer: %w", err)
	}
	return resp, nil
}

// submitMCP performs the round trip off the UI thread. A list starts the
// configured servers to ask them their tools, and an add starts the new one
// once: either can take seconds, and a synchronous call froze the client.
func submitMCP(clientName string, req protocol.MCPRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendMCP(clientName, req)
		return mcpResultMsg{req: req, resp: resp, err: err}
	}
}

// splitCommandLine cuts a line into words the way a shell user expects: on
// spaces, with "double" and 'single' quotes keeping a path with spaces whole.
// There is no escaping and no expansion -- nothing here is run by a shell.
func splitCommandLine(s string) ([]string, error) {
	var (
		words   []string
		current strings.Builder
		quote   rune
		inWord  bool
	)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("a %c quote is never closed", quote)
	}
	if inWord {
		words = append(words, current.String())
	}
	return words, nil
}

// looksLikeAssignment reports whether a word is NAME=VALUE in the shape a shell
// puts in front of a command.
func looksLikeAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	if !found || name == "" {
		return false
	}
	for i, r := range name {
		letter := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// errMCPSecret is the refusal for a value typed where only a name belongs. It
// never repeats what was typed.
var errMCPSecret = errors.New("that puts a VALUE on the command line, where it would be stored and shown. " +
	"Nothing was sent. Give the variable's NAME with --env NAME, and set its value in the daemon's " +
	"environment (export it, or put it in .env) before starting Mochiii")

// parseMCPAdd reads what follows "/mcp-server add": a name, any --env NAME
// flags, then the command and its arguments.
func parseMCPAdd(words []string) (protocol.MCPRequest, error) {
	if len(words) == 0 {
		return protocol.MCPRequest{}, errors.New("add needs a name for the server and the command that runs it")
	}
	req := protocol.MCPRequest{Action: protocol.MCPActionAdd, Name: words[0]}
	rest := words[1:]
	for len(rest) > 0 {
		word := rest[0]
		switch {
		case word == "--":
			rest = rest[1:]
		case word == "--env":
			if len(rest) < 2 {
				return protocol.MCPRequest{}, errors.New("--env needs the name of a variable after it")
			}
			if strings.Contains(rest[1], "=") {
				return protocol.MCPRequest{}, errMCPSecret
			}
			req.Env = append(req.Env, rest[1])
			rest = rest[2:]
			continue
		case strings.HasPrefix(word, "--env="):
			name := strings.TrimPrefix(word, "--env=")
			if name == "" || strings.Contains(name, "=") {
				return protocol.MCPRequest{}, errMCPSecret
			}
			req.Env = append(req.Env, name)
			rest = rest[1:]
			continue
		}
		break
	}
	if len(rest) == 0 {
		return protocol.MCPRequest{}, errors.New("add needs the command that runs the server, after its name")
	}
	// TOKEN=abc npx ... is how a shell is told to set a variable for one
	// command. It is not a command, and the value in it is exactly what must
	// not be stored.
	if looksLikeAssignment(rest[0]) {
		return protocol.MCPRequest{}, errMCPSecret
	}
	req.Command, req.Args = rest[0], rest[1:]
	return req, nil
}

// mcpAddIsRefused reports whether raw is a "/mcp-server add" this client will
// not send -- which is every one that could be carrying a secret. Such a line
// is kept out of the up-arrow recall, like a key typed after /connect.
func mcpAddIsRefused(raw string) bool {
	rest, ok := strings.CutPrefix(raw, "/mcp-server")
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return false
	}
	words, err := splitCommandLine(rest)
	if err != nil {
		return true
	}
	if len(words) == 0 || words[0] != "add" {
		return false
	}
	_, err = parseMCPAdd(words[1:])
	return err != nil
}

// handleMCPServerCommand is /mcp-server and its two subcommands.
func (m chatModel) handleMCPServerCommand(args string) (tea.Model, tea.Cmd) {
	say := func(text string) {
		m.appendTurn(turn{role: roleAssistant, local: true, verbatim: true, text: text})
		m.resizeViewport()
		m.refreshViewport()
	}
	words, err := splitCommandLine(args)
	if err != nil {
		say("mcp-server: " + err.Error() + "\n\n" + mcpServerUsage)
		return m, nil
	}
	if len(words) == 0 || (len(words) == 1 && words[0] == "list") {
		say("asking the daemon which tools it offers…")
		return m, submitMCP(m.clientName, protocol.MCPRequest{Action: protocol.MCPActionList})
	}
	switch words[0] {
	case "add":
		req, err := parseMCPAdd(words[1:])
		if err != nil {
			say("mcp-server: " + err.Error() + ".\n\n" + mcpServerUsage)
			return m, nil
		}
		// No acknowledgement yet: the daemon stores nothing and answers with
		// what this server would be able to do (handleMCPResult asks for the name).
		return m, submitMCP(m.clientName, req)
	case "remove", "rm":
		if len(words) != 2 {
			say("mcp-server: remove takes the name of one server.\n\n" + mcpServerUsage)
			return m, nil
		}
		return m, submitMCP(m.clientName, protocol.MCPRequest{Action: protocol.MCPActionRemove, Name: words[1]})
	}
	say("mcp-server: " + sanitizeText(words[0]) + " is not something it does.\n\n" + mcpServerUsage)
	return m, nil
}

// handleMCPResult shows the daemon's answer, and opens the confirmation when an
// add is waiting for one.
func (m chatModel) handleMCPResult(msg mcpResultMsg) (tea.Model, tea.Cmd) {
	text := formatMCPResult(msg)
	if msg.err == nil && msg.resp.Error == "" && msg.resp.NeedsAcknowledgement && msg.req.Action == protocol.MCPActionAdd {
		pending := msg.req
		m.pendingMCPAdd = &pending
		m.input.Placeholder = "type " + sanitizeText(pending.Name) + " to add it — anything else cancels"
		text += "\n\nType the server's name (" + sanitizeText(pending.Name) + ") and press enter to add it. " +
			"Anything else, or esc, cancels and nothing is added."
	}
	m.appendTurn(turn{role: roleAssistant, local: true, verbatim: true, text: text})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// formatMCPResult turns the response into the lines a user reads. Everything in
// it came from the daemon -- some of it from a third-party server's own
// description of its tools -- so all of it is sanitized on the way to a terminal.
func formatMCPResult(msg mcpResultMsg) string {
	if msg.err != nil {
		if errors.Is(msg.err, errDaemonPredatesMCP) {
			return "mcp-server: " + msg.err.Error()
		}
		return "mcp-server failed: " + sanitizeText(msg.err.Error())
	}
	r := msg.resp
	if r.Error != "" {
		return "mcp-server: " + sanitizeText(r.Error)
	}
	var b strings.Builder
	switch {
	case r.Report != "":
		b.WriteString(strings.TrimRight(sanitizeText(r.Report), "\n"))
		if added := userServerNames(r.Servers); len(added) > 0 {
			b.WriteString("\n\nMCP servers you added: " + strings.Join(added, ", ") + " (/mcp-server remove <name>).")
		} else if r.AgentMode {
			b.WriteString("\n\nAdd an MCP server of your own with /mcp-server add <name> <command> [args…].")
		}
	default:
		b.WriteString(sanitizeText(r.Detail))
	}
	for _, note := range r.Notes {
		b.WriteString("\n\nNOTE: " + sanitizeText(note))
	}
	return b.String()
}

// userServerNames is the names of the servers the user added, with what each
// runs, for the line under a list.
func userServerNames(servers []protocol.MCPServerInfo) []string {
	var out []string
	for _, srv := range servers {
		if srv.Source == protocol.MCPSourceUser {
			out = append(out, sanitizeText(srv.Name)+" ("+sanitizeText(srv.Command)+")")
		}
	}
	return out
}

// answerMCPAdd takes the line typed while an add waits for its confirmation.
// The server's name, exactly, adds it; anything else cancels -- and gives the
// text back, because the likeliest "anything else" is the user's next question,
// typed without noticing a confirmation was open.
func (m chatModel) answerMCPAdd(raw string) (tea.Model, tea.Cmd) {
	req := *m.pendingMCPAdd
	m.endMCPConfirm()
	if raw != req.Name {
		m.input.SetValue(raw)
		m.input.SetCursor(len(raw))
		m.appendTurn(turn{role: roleAssistant, local: true, text: "Not added: the server's name was not typed, so nothing was " +
			"stored. What you typed is back in the box."})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	m.input.SetValue("")
	req.AcknowledgedUnconfined = true
	m.appendTurn(turn{role: roleAssistant, local: true, text: "adding " + sanitizeText(req.Name) +
		" and starting it once to see what it offers…"})
	m.resizeViewport()
	m.refreshViewport()
	return m, submitMCP(m.clientName, req)
}

// cancelMCPAdd is esc while an add waits for its confirmation.
func (m chatModel) cancelMCPAdd() (tea.Model, tea.Cmd) {
	m.endMCPConfirm()
	m.input.SetValue("")
	m.appendTurn(turn{role: roleAssistant, local: true, text: "Not added; nothing was stored."})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// endMCPConfirm puts the prompt back to ordinary input.
func (m *chatModel) endMCPConfirm() {
	m.pendingMCPAdd = nil
	m.input.Placeholder = defaultInputPlaceholder
}
