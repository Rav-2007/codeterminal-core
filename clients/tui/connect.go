package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /connect -- hand the daemon a provider API key, from inside the client.
//
// THE KEY NEVER ENTERS THE TRANSCRIPT. It is typed into a masked input, read
// once, cleared immediately, and sent straight to the daemon; it is never
// appended as a turn, never sanitized into the history that buildHistory sends
// with the next prompt, and never written to the conversation store. That is the
// whole reason this is a small state of its own rather than an argument to the
// command: "/connect sk-..." would put the key in the transcript, in the scroll
// buffer, and in the history of the very next request -- the client-side twin of
// the argv leak the CLI refuses.
//
// The daemon does the verifying and the storing (see daemon/connect_handler.go),
// so this file collects a secret, shows an answer, and holds no policy of its own.

// connectResultMsg carries the daemon's answer back into Update.
type connectResultMsg struct {
	resp protocol.ConnectResponse
	err  error
}

// connectPrompt is what the input line asks while a key is being typed, and
// defaultInputPlaceholder is what it goes back to afterwards -- named here so the
// restore in endConnect cannot drift from the value chat.go sets at startup.
const (
	connectPrompt           = "paste your provider API key, then enter (esc cancels)"
	defaultInputPlaceholder = "ask something…"
)

// beginConnectForPrompt asks for a key BECAUSE a question is waiting on one, and
// remembers the question so the user does not have to type it twice.
//
// THIS IS WHY THE CLIENT DOES NOT ASK AT STARTUP. Someone opening Mochiii for the
// first time should meet the prompt, not a credential form; the key is only
// actually needed at the moment a question is submitted. Sending it anyway would
// spend a round trip to earn a provider's 401, which tells the user nothing about
// what to do -- so the question is held here instead, and sent the moment the
// provider accepts the key.
//
// The held prompt is restored to the input on every path that does not send it
// (cancel, empty entry, a refused key), so no question is ever lost.
func (m chatModel) beginConnectForPrompt(prompt string) (tea.Model, tea.Cmd) {
	m.pendingPrompt = prompt
	m.appendTurn(turn{role: roleAssistant, text: "This needs your provider API key before it can ask anything.\n\n" +
		"Paste it below and your question is sent as soon as the provider accepts it — " +
		"nothing is stored if the key is refused, and the key is not shown as you type."})
	return m.beginConnect()
}

// resumePendingPrompt sends the question that was waiting on a key, if there is
// one and the daemon is now actually using that key.
//
// Gated on InUse rather than on "accepted": a key stored while one in the
// environment takes precedence has NOT taken effect, and sending the question
// then would repeat the failure that held it back in the first place.
func (m chatModel) resumePendingPrompt() (tea.Model, tea.Cmd) {
	prompt := m.pendingPrompt
	m.pendingPrompt = ""
	m.input.SetValue(prompt)
	m.input.SetCursor(len(prompt))
	return m.startTurn()
}

// restorePendingPrompt puts a held question back where the user typed it, for
// every path that ends without sending it.
func (m *chatModel) restorePendingPrompt() {
	if m.pendingPrompt == "" {
		return
	}
	m.input.SetValue(m.pendingPrompt)
	m.input.SetCursor(len(m.pendingPrompt))
	m.pendingPrompt = ""
}

// beginConnect switches the client into masked key entry.
func (m chatModel) beginConnect() (tea.Model, tea.Cmd) {
	m.state = stateConnect
	m.statusErr = ""
	m.input.SetValue("")
	m.input.Placeholder = connectPrompt
	// The bubbles input renders EchoCharacter in place of every rune, so the key
	// is not on screen and not in the terminal's scrollback afterwards.
	m.input.EchoMode = textinput.EchoPassword
	m.input.EchoCharacter = '•'
	m.resizeViewport()
	m.refreshViewport()
	return m, m.input.Focus()
}

// endConnect restores ordinary input. Always called through a defer-like path on
// both the submit and the cancel branch, because an input left in password mode
// would silently hide the user's next prompt from them.
func (m *chatModel) endConnect() {
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoNormal
	m.input.Placeholder = defaultInputPlaceholder
	m.state = stateIdle
}

// handleConnectKey owns the keyboard while a key is being entered.
//
// It handles only enter and esc itself and hands everything else to the input, so
// editing behaves normally -- but it must never fall through to the ordinary
// prompt path, which would send the typed key to the model as a question.
func (m chatModel) handleConnectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.endConnect()
		m.restorePendingPrompt()
		m.appendTurn(turn{role: roleAssistant, text: "connect cancelled; nothing was sent or stored"})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil

	case "enter":
		key := strings.TrimSpace(m.input.Value())
		// Cleared before anything else can happen to it: from here the key exists
		// only in the local variable and in the request about to be sent.
		m.endConnect()
		if key == "" {
			m.restorePendingPrompt()
			m.appendTurn(turn{role: roleAssistant, text: "no key entered; nothing was sent or stored"})
			m.resizeViewport()
			m.refreshViewport()
			return m, nil
		}
		m.appendTurn(turn{role: roleAssistant, text: "checking the key with the provider…"})
		m.resizeViewport()
		m.refreshViewport()
		return m, submitConnect(m.clientName, protocol.ConnectRequest{
			ProtocolVersion: protocol.ProtocolVersion,
			Connect:         true,
			APIKey:          key,
		})
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleConnectResult renders what the daemon said, and releases a question that
// was waiting on the key.
func (m chatModel) handleConnectResult(msg connectResultMsg) (tea.Model, tea.Cmd) {
	m.appendTurn(turn{role: roleAssistant, text: formatConnectResult(msg)})
	m.resizeViewport()
	m.refreshViewport()

	// The daemon is now sending this key, so stop holding prompts back. Taken from
	// InUse rather than from the outcome: a key stored behind an environment
	// override has not taken effect, and pretending otherwise would send the held
	// question straight into the same failure.
	if msg.err == nil && msg.resp.InUse {
		m.needsAPIKey = false
		if m.pendingPrompt != "" {
			return m.resumePendingPrompt()
		}
		return m, nil
	}

	// Refused, unreachable, or overridden: give the question back rather than
	// dropping it or sending it into a failure.
	m.restorePendingPrompt()
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// formatConnectResult turns the response into the lines a user reads.
//
// The four outcomes are worded so that no two can be mistaken for each other --
// in particular "stored and proven" and "stored but unproven" never share a
// phrase, because the whole value of verifying is lost if the report blurs them.
func formatConnectResult(msg connectResultMsg) string {
	if msg.err != nil {
		return "connect failed: " + msg.err.Error()
	}
	r := msg.resp
	if r.Error != "" {
		return "connect failed: " + r.Error
	}

	var b strings.Builder
	switch r.Outcome {
	case protocol.ConnectAccepted:
		// "in use now" IS THE CLAIM THAT CAN BE FALSE. The daemon reports InUse,
		// and it is false whenever a key in the daemon's environment wins. Saying
		// it anyway produced a message that contradicted its own closing note.
		fmt.Fprintf(&b, "Connected. %s\n", r.Detail)
		if r.InUse {
			fmt.Fprintf(&b, "Key %s is in use now — no restart needed.", r.MaskedKey)
		} else {
			fmt.Fprintf(&b, "Key %s is stored, but it is NOT what this daemon is sending.", r.MaskedKey)
		}
	case protocol.ConnectUnverified:
		fmt.Fprintf(&b, "Saved %s, but NOT verified: %s\n", r.MaskedKey, r.Detail)
		if r.InUse {
			b.WriteString("It is in use now; if prompts fail, the key is the first thing to suspect.")
		} else {
			b.WriteString("It is stored, but it is NOT what this daemon is sending.")
		}
	case protocol.ConnectRejected:
		fmt.Fprintf(&b, "The provider refused that key: %s\nNothing was stored, and the key this daemon was already using is unchanged.",
			r.Detail)
	case protocol.ConnectRemoved:
		b.WriteString("Stored key removed. " + r.Detail)
	case protocol.ConnectShown:
		if r.MaskedKey == "" || r.MaskedKey == "(none)" {
			b.WriteString("No key is stored.")
		} else {
			fmt.Fprintf(&b, "Stored key %s for %s — %s", r.MaskedKey, r.APIBase, r.Detail)
		}
	default:
		fmt.Fprintf(&b, "connect: unrecognised outcome %q from the daemon", r.Outcome)
	}

	// THE ONE THING A USER CANNOT SEE FOR THEMSELVES. A key in the daemon's
	// environment silently beats the stored one, so without this a successful
	// "connected" would be followed by the old key still being used, with nothing
	// explaining it.
	if r.EnvOverride {
		b.WriteString("\n\nNOTE: this daemon was started with a key in its environment (MOCHIII_API_KEY, " +
			"or proxy mode), and that takes precedence — so the stored key is NOT what it is sending. " +
			"Unset it and restart the daemon to use the stored one.")
	}
	return b.String()
}

// submitConnect performs the round trip off the UI thread. Verification talks to
// the provider and can take seconds, and a synchronous call here would freeze the
// client with no indication of why.
func submitConnect(clientName string, req protocol.ConnectRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendConnect(clientName, req)
		return connectResultMsg{resp: resp, err: err}
	}
}
