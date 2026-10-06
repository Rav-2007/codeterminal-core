package main

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

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
	// connectBaseExample is the shape of the argument /connect takes besides its
	// subcommands, in the words every message that mentions it uses.
	connectBaseExample = "/connect <provider>, or /connect <the provider's API address>"
)

// connectProviderNames lists the names /connect accepts, for the moments a user
// has to pick one.
func connectProviderNames() string {
	var ids []string
	for _, p := range protocol.Providers() {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

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
	m.appendTurn(turn{role: roleAssistant, local: true, text: "This needs your provider API key before it can ask anything.\n\n" +
		"Paste it below and your question is sent as soon as the provider accepts it — " +
		"nothing is stored if the key is refused, and the key is not shown as you type.\n\n" + anyProviderLine})
	return m.beginConnect()
}

// anyProviderLine says, wherever a key is asked for, that it need not be from
// any one provider -- which nothing used to say, and which is why a user with a
// perfectly good key from somewhere else concluded the product could not use it.
const anyProviderLine = "Any major provider's key works — OpenRouter, OpenAI, Anthropic, Google Gemini, NVIDIA, " +
	"Groq, xAI, DeepSeek and others. The provider is recognised from the key, and one of its models is " +
	"picked and tried, so there is nothing else to set."

// beginConnectBare is /connect on its own: say what may be pasted, then ask.
func (m chatModel) beginConnectBare() (tea.Model, tea.Cmd) {
	m.appendTurn(turn{role: roleAssistant, local: true, text: "Paste your API key below — it is not shown as you type, and " +
		"nothing is stored if the provider refuses it. Esc cancels.\n\n" + anyProviderLine})
	return m.beginConnect()
}

// beginConnectForRefusedKey asks for a key BECAUSE the provider just refused the
// one in use -- the same moment of need as a first question with no key at all.
// Before this, a spent or revoked key failed every prompt, and the only way out
// was to already know that /connect exists and type it past the error.
//
// Called only when the daemon says a pasted key would actually be used
// (TokenResponse.KeyReplaceable): in proxy mode, or with a key in the daemon's
// environment, it could not take effect, and asking would be asking for the
// wrong thing.
//
// The question is held and asked again only when the failed turn did nothing but
// ask -- no answer text, no tool run. A turn that failed partway is not re-run on
// the user's behalf; it is one up-arrow away.
func (m chatModel) beginConnectForRefusedKey(class string, askAgain bool) (tea.Model, tea.Cmd) {
	why := "The provider refused the API key."
	if class == "quota_exceeded" {
		why = "The API key's credit or spending limit is used up."
	}
	then := "Paste a working key below to switch to it"
	if askAgain && m.turnInput != "" {
		m.pendingPrompt = m.turnInput
		then += ", and your question is asked again as soon as the provider accepts it"
	}
	m.appendTurn(turn{role: roleAssistant, local: true, text: why + "\n\n" + then +
		" — nothing is stored if the key is refused, and the key is not shown as you type. Esc keeps the current key." +
		"\n\n" + otherProviderHint})
	return m.beginConnect()
}

// otherProviderHint is the way out for a key whose provider could not be told
// from the key, said at the two moments someone is holding one: when the key in
// use has just been refused and a new one is being asked for, and when a pasted
// key has just been refused. Without it both moments read as "your key is bad".
const otherProviderHint = "A key only works with the provider that issued it, and most are recognised from the " +
	"key itself. For one that is not, press Esc and name its provider first: " + connectBaseExample

// beginConnectTo asks for a key FOR A NAMED PROVIDER, which is how a user moves
// to a different one without leaving the client.
//
// THE GAP THIS CLOSES (FOUND 2026-10-05): a bare /connect sends only the key, so
// the daemon checks it against the provider it already uses. Someone holding a
// key from another provider pasted it, was told "the provider refused that key"
// -- true, but of the provider they were leaving -- and had no way from here to
// say which one they meant. The address is said back before the key is asked
// for, because it is where the key is about to be sent.
func (m chatModel) beginConnectTo(base string) (tea.Model, tea.Cmd) {
	m.appendTurn(turn{role: roleAssistant, local: true, text: "Switching to " + providerLabel(base) + ".\n\n" +
		"Paste the API key for it below — it is sent to that address to be checked, nothing is stored " +
		"if the key is refused, and the key is not shown as you type. Esc keeps the provider and key in use now."})
	next, cmd := m.beginConnect()
	cm := next.(chatModel)
	cm.connectBase = base
	return cm, cmd
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
	m.connectBase = ""
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
		m.appendTurn(turn{role: roleAssistant, local: true, text: "connect cancelled; nothing was sent or stored"})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil

	case "enter":
		key := strings.TrimSpace(m.input.Value())
		// Read before endConnect forgets it: empty is a bare /connect, which
		// leaves it to the daemon to tell whose key this is.
		base := m.connectBase
		// Cleared before anything else can happen to it: from here the key exists
		// only in the local variable and in the request about to be sent.
		m.endConnect()
		if key == "" {
			m.restorePendingPrompt()
			m.appendTurn(turn{role: roleAssistant, local: true, text: "no key entered; nothing was sent or stored"})
			m.resizeViewport()
			m.refreshViewport()
			return m, nil
		}
		m.appendTurn(turn{role: roleAssistant, local: true, text: "checking the key with the provider…"})
		m.resizeViewport()
		m.refreshViewport()
		return m, submitConnect(m.clientName, protocol.ConnectRequest{
			ProtocolVersion: protocol.ProtocolVersion,
			Connect:         true,
			APIKey:          key,
			APIBase:         base,
		})
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleConnectResult renders what the daemon said, and releases a question that
// was waiting on the key.
func (m chatModel) handleConnectResult(msg connectResultMsg) (tea.Model, tea.Cmd) {
	m.appendTurn(turn{role: roleAssistant, local: true, text: formatConnectResult(msg)})
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
		// SAY WHERE. "Connected." alone left the user asking which platform the
		// key was for; the daemon has always sent the base it verified against.
		fmt.Fprintf(&b, "Connected to %s.\n%s\n", providerLabel(r.APIBase), capitalize(sanitizeText(r.Detail)))
		if r.InUse {
			fmt.Fprintf(&b, "Key %s is in use now — no restart needed.", r.MaskedKey)
		} else {
			fmt.Fprintf(&b, "Key %s is stored, but it is NOT what this daemon is sending.", r.MaskedKey)
		}
		b.WriteString(connectModelLine(r))
	case protocol.ConnectUnverified:
		fmt.Fprintf(&b, "Saved %s for %s, but NOT verified: %s\n", r.MaskedKey, providerLabel(r.APIBase), sanitizeText(r.Detail))
		if r.InUse {
			b.WriteString("It is in use now; if prompts fail, the key is the first thing to suspect.")
		} else {
			b.WriteString("It is stored, but it is NOT what this daemon is sending.")
		}
		b.WriteString(connectModelLine(r))
	case protocol.ConnectNeedsProvider:
		// NOTHING WAS SENT, AND THAT IS THE FIRST THING SAID. The daemon will not
		// find out whose key this is by handing it to each provider in turn, so
		// the one question it cannot answer is asked here.
		fmt.Fprintf(&b, "That key was sent nowhere and nothing was stored: %s.\n\n"+
			"Say which provider it is from, then paste it again:", sanitizeText(r.Detail))
		for _, id := range r.Candidates {
			b.WriteString("\n  /connect " + sanitizeText(id))
		}
	case protocol.ConnectRejected:
		// SAY WHO REFUSED IT (FOUND 2026-10-05). "The provider refused that key"
		// named nobody, so a key from one provider checked against another read as
		// a bad key -- twice, to someone whose key was fine. The daemon has always
		// sent the base it asked; this branch was the one that dropped it.
		fmt.Fprintf(&b, "That key was refused by %s: %s\nNothing was stored, and the key this daemon was already using is unchanged.\n\n%s",
			providerLabel(r.APIBase), sanitizeText(r.Detail), otherProviderHint)
	case protocol.ConnectRemoved:
		b.WriteString("Stored key removed. " + r.Detail)
	case protocol.ConnectShown:
		if r.MaskedKey == "" || r.MaskedKey == "(none)" {
			b.WriteString("No key is stored.")
		} else {
			fmt.Fprintf(&b, "Connected to %s with key %s — %s", providerLabel(r.APIBase), r.MaskedKey, sanitizeText(r.Detail))
		}
	default:
		fmt.Fprintf(&b, "connect: unrecognised outcome %q from the daemon", r.Outcome)
	}

	for _, note := range r.Notes {
		b.WriteString("\n\nNOTE: " + sanitizeText(note))
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

// connectModelLine says which model prompts now go to, when the provider's own
// models replaced the configured tiers -- the last thing "ready" depends on, and
// the one a user could not have guessed.
//
// "Ready" IS SAID ONLY FOR A MODEL THAT ANSWERED. ModelTested is the daemon's
// report that a real request to it succeeded with this key; without it the
// model is named as what will be tried, not as something that works.
func connectModelLine(r protocol.ConnectResponse) string {
	if r.Model == "" {
		return ""
	}
	model := sanitizeText(r.Model)
	if r.ModelTested {
		return fmt.Sprintf("\n\nReady — prompts go to %s. %d models are available: /model lists them, /model <name> switches.",
			model, r.ModelCount)
	}
	return fmt.Sprintf("\n\nPrompts will go to %s, which has NOT answered a test request. %d models are available: "+
		"/model lists them, /model <name> switches.", model, r.ModelCount)
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

// providerLabel turns an API base into "OpenRouter (https://openrouter.ai/api/v1)"
// -- a name when the host is a known one, and always the address itself, since
// the address is what the key will actually be sent to.
func providerLabel(apiBase string) string {
	base := sanitizeText(strings.TrimSpace(apiBase))
	if base == "" {
		return "the model provider"
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return base
	}
	host := strings.ToLower(u.Hostname())
	if p, ok := protocol.ProviderForBase(base); ok {
		return p.Name + " (" + base + ")"
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return "a model server on this machine (" + base + ")"
	}
	return host + " (" + base + ")"
}

// capitalize upper-cases the first letter of a sentence.
func capitalize(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+len(string(r)):]
	}
	return s
}

// connectWithKey reports whether raw is /connect with an argument that is not
// one of its subcommands or a provider address -- which the command refuses as
// a probable key.
func connectWithKey(raw string) bool {
	rest, ok := strings.CutPrefix(raw, "/connect")
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
		return false
	}
	arg := strings.TrimSpace(rest)
	switch arg {
	case "", "show", "forget":
		return false
	}
	_, isBase := connectBaseArg(arg)
	return !isBase
}

// connectBaseArg reports whether arg names where a key is to go -- a provider
// by name ("nvidia") or by API address -- which is the one thing besides "show"
// and "forget" that may follow /connect. It returns the address, without a
// trailing slash.
//
// IT MUST NEVER ACCEPT A KEY. Whatever follows /connect and is not recognised
// here is refused as a probable key and kept out of the transcript and the
// up-arrow recall, so this errs towards refusing: one word, either a name from
// the fixed provider list or an absolute http(s) URL with a host, and nothing
// that can carry a secret -- no user:password@, no query, no fragment. A
// provider's base address has none of those, and "https://host/v1?key=sk-..."
// would otherwise be recorded in clear.
func connectBaseArg(arg string) (string, bool) {
	if arg == "" || strings.ContainsAny(arg, " \t?#") {
		return "", false
	}
	if p, ok := protocol.ProviderByName(arg); ok {
		return p.APIBase, true
	}
	u, err := url.Parse(arg)
	if err != nil || u.Host == "" || u.User != nil {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return strings.TrimRight(arg, "/"), true
}
