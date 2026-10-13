package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// THE MODEL THAT WAS CHOSEN IS STILL THE MODEL TOMORROW.
//
// /model <name> used to set a field in this client and nothing else: the choice
// lasted until the window closed, and the next start went back to whatever the
// daemon defaulted to. The owner asked for the model picked at setup to survive
// a restart (2026-10-11), so a choice is now also sent to the daemon, which
// keeps it beside the key it belongs to (daemon/connect_handler.go, chooseModel).
//
// THIS CLIENT STILL SETS ITS OWN FIELD FIRST, and that is deliberate rather than
// left over. It is what makes the choice hold for this session whatever the
// daemon answers -- an older daemon, a key that comes from the environment, a
// disk that cannot be written -- and the line that follows says honestly which
// of "remembered" and "this session only" the user got.

// errDaemonPredatesModelChoice is a daemon started before it could remember a
// model: it would read the request as a connect with no key.
var errDaemonPredatesModelChoice = errors.New("the running daemon is older than this client")

// modelChoiceMsg is the daemon's answer to "remember this model". name is the
// model asked for, "" when the request was to forget the choice.
type modelChoiceMsg struct {
	name string
	resp protocol.ConnectResponse
	err  error
}

// sendChooseModel asks the daemon to make name its default model and keep it,
// or -- with an empty name -- to go back to the model in force before any choice.
func sendChooseModel(clientName, name string) (protocol.ConnectResponse, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return protocol.ConnectResponse{}, err
	}
	defer func() { _ = sess.Close() }()
	if !hasFeature(sess.handshake, protocol.FeatureModelChoice) {
		return protocol.ConnectResponse{}, errDaemonPredatesModelChoice
	}

	if err := sess.enc.Encode(protocol.ConnectRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Connect:         true,
		ChooseModel:     name,
		ClearModel:      name == "",
	}); err != nil {
		return protocol.ConnectResponse{}, fmt.Errorf("sending the request: %w", err)
	}
	var resp protocol.ConnectResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return protocol.ConnectResponse{}, fmt.Errorf("reading the answer: %w", err)
	}
	return resp, nil
}

// chooseModelCmd performs the round trip off the UI thread.
func chooseModelCmd(clientName, name string) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendChooseModel(clientName, name)
		return modelChoiceMsg{name: name, resp: resp, err: err}
	}
}

// chooseModel makes name the model this client's prompts go to, says so, and
// asks the daemon to remember it. slug is what the provider calls the model,
// shown when it differs from the name because it is what the user is billed for.
func (m chatModel) chooseModel(name, slug string) (tea.Model, tea.Cmd) {
	m.preferredTier = name
	m.preferredSlug = slug // what the line under the prompt shows (statusline.go)
	text := fmt.Sprintf("model set to %s", sanitizeText(name))
	if slug != "" && slug != name {
		text += fmt.Sprintf(" (%s)", sanitizeText(slug))
	}
	m.appendTurn(turn{role: roleAssistant, local: true, text: text})
	m.resizeViewport()
	m.refreshViewport()
	return m, chooseModelCmd(m.clientName, name)
}

// handleModelChoice says what became of a choice once the daemon has answered.
func (m chatModel) handleModelChoice(msg modelChoiceMsg) (tea.Model, tea.Cmd) {
	if line := modelChoiceLine(msg); line != "" {
		m.appendTurn(turn{role: roleAssistant, local: true, text: line})
		m.resizeViewport()
		m.refreshViewport()
	}
	// The model in force may have changed, and whether it is remembered has.
	return m, refreshConnCmd(m.clientName)
}

// modelListText is "/model list": every model as text, the one in force marked.
//
// A NAME IS WRITTEN ONCE. Each row used to be "name  slug", which for a
// provider's own models -- where the id IS the name -- printed every model
// twice across the screen (seen by the owner, 2026-10-12). The provider's id is
// added only where it says something the name does not.
func modelListText(tiers []protocol.StatusTier, preferred string) string {
	var b strings.Builder
	b.WriteString("models (/model opens the list to choose from; /model <name> selects; /model clear resets):\n")
	current := preferred
	if current == "" {
		current = "(default)"
	}
	fmt.Fprintf(&b, "current: %s\n", current)
	for _, t := range tiers {
		mark := "  "
		if t.Name == preferred || (preferred == "" && t.Default) {
			mark = "* "
		}
		b.WriteString(mark + t.Name)
		if t.Slug != "" && t.Slug != t.Name {
			b.WriteString("  " + t.Slug)
		}
		if !t.Active {
			b.WriteString(" [inactive]")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// modelChoiceLine words the daemon's answer.
//
// "REMEMBERED" IS SAID ONLY WHEN IT WAS SAVED. The three ways it can fail to be
// -- an older daemon, a daemon that could not be reached, a key that is not the
// stored one -- each end in the same place for the user (the choice holds until
// something restarts) and each say why, because the fix differs.
func modelChoiceLine(msg modelChoiceMsg) string {
	forget := msg.name == ""
	model := sanitizeText(msg.resp.Model)
	switch {
	case errors.Is(msg.err, errDaemonPredatesModelChoice):
		if forget {
			return "" // an older daemon remembered nothing, so there is nothing to undo
		}
		return "Kept for this session only: the daemon running is older than this client and cannot remember a " +
			"model. Restart it (./run-tui.sh does) and choose again."
	case msg.err != nil:
		if forget {
			return "The daemon could not be asked to forget a remembered model: " + sanitizeText(msg.err.Error())
		}
		return "Kept for this session only — the daemon could not be asked to remember it: " + sanitizeText(msg.err.Error())
	case msg.resp.Error != "":
		return "The daemon did not take that: " + sanitizeText(msg.resp.Error)
	case forget:
		return "Prompts go to " + model + " again — the model in force before one was chosen."
	case msg.resp.ModelSaved:
		return "Remembered: " + model + " is still the model after a restart. /model clear goes back."
	default:
		return capitalize(sanitizeText(msg.resp.Detail)) + "."
	}
}
