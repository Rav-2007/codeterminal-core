package tui

import (
	"fmt"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// SETUP: choose a provider, paste its key, choose a model -- one screen at a
// time, before the chat.
//
// WHY IT EXISTS (OWNER, 2026-10-11). The client used to open straight on the
// prompt and ask for a key only when the first question was sent, telling the
// provider from the key. That was a decision of 2026-09-26 ("ask at the moment
// of need, not at the door"), and it left a new user at an empty prompt with no
// sign that anything had to be connected, or to whom. The owner compared it
// with a client that opens on exactly these three steps and asked for the same
// order here.
//
// WHAT WAS KEPT FROM THE OLDER DECISION: the door is not locked. Esc on the
// first screen goes to the chat, where /help and everything else work with no
// key, and the first question still opens the key prompt it always did
// (beginConnectForPrompt in connect.go). A keyless local model server never
// sees this screen at all -- the daemon says it needs no key.
//
// WHERE IT IS SHOWN: at start, when the daemon says a prompt sent now would
// have no credential; and by /connect, at any time.
//
// THE KEY STEP IS connect.go's, NOT A SECOND ONE. It is the same masked input
// in the same state (stateConnect), read once and cleared by the same code, so
// the rule that a key never reaches the transcript has one implementation. This
// file draws that step full-screen and decides what comes before and after it.

// setupStep is which screen of the setup is showing.
type setupStep int

const (
	setupNone     setupStep = iota // setup is not on screen
	setupProvider                  // the list of providers
	setupKey                       // the masked key prompt (stateConnect)
	setupChecking                  // the daemon is asking the provider
	setupModel                     // the list of the provider's models
	setupAccount                   // /account: what is connected, and the spend (account.go)
	setupLocal                     // the model servers on this computer (localmodels.go)
)

// setupFlow is where the setup is. The zero value is "not showing".
type setupFlow struct {
	step setupStep
	// firstRun is set when the setup opened by itself at start, not by
	// /connect -- it changes the heading and what Esc on the first screen says.
	firstRun bool
	// viaList is set when the key step was reached from the provider list, so
	// Esc there goes back to the list instead of out of the setup.
	viaList bool
	picker  listPicker
	// provider is the name picked from the list, "" for "any key" and for a
	// provider given on the command line; base is the address that went with
	// it, kept so a refused key returns to the same provider's key step.
	provider string
	base     string
	// notice is one line saying why this screen is showing again: the last key
	// was refused, the provider could not be told from it.
	notice string
	// checking names who the key was sent to, while the answer is awaited.
	checking string
	// loading is set while the model list has been asked for and not arrived.
	loading bool
	// inForce is the model prompts go to before any is chosen here, and
	// modelCount how many the list offers.
	inForce    string
	modelCount int
	// account is what the account screen shows, once it has arrived.
	account accountMsg
	// sent is set once a key has gone to the daemon from this setup. Leaving
	// after that must not say "nothing was sent" -- something was, and the
	// line above it in the transcript says what became of it.
	sent bool
	// localServers are the servers on this computer the setupLocal list shows.
	localServers []protocol.LocalServer
	// modelOnly is set when the model list was opened to change the model --
	// by /model, or by keeping the key in use -- and not reached by connecting
	// a key. Nothing was just connected, so the screen does not say so; and a
	// list that failed to arrive is said out loud, because the user asked for
	// it by name and a screen that closes without a word looks broken.
	modelOnly bool
}

// anyKeyRowID is the first row of the provider list: paste a key and let the
// daemon tell whose it is. It keeps the 2026-10-05 requirement -- "/connect,
// paste the key, ready" -- one Enter away.
const anyKeyRowID = "*"

const (
	setupProviderTitle = "Select Provider"
	setupModelTitle    = "Select Model"
	// What each list's search field says before anything is typed.
	setupProviderSearch = "Search providers…"
	setupModelSearch    = "Search models…"
	// setupKeyPlaceholder is what the masked input shows before anything is
	// typed. Shorter than connectPrompt, which also names the keys: here the
	// line under the box does that.
	setupKeyPlaceholder = "paste the key here"

	setupWelcome     = "connect a model provider to get started"
	setupFromCommand = "connect a model provider"
	setupFromModel   = "choose a model"

	setupSkippedNote = "Setup skipped. You will be asked for a key when you send your first question, and " +
		"/connect opens this again at any time."
	setupCancelledNote = "connect cancelled"
	// setupNothingSent is added to either note when it is true.
	setupNothingSent = "nothing was sent or stored"
	setupPasteNote   = "pick a provider first — the key is pasted on the next screen (or choose \"Any key\")"
	setupNoKeyNote   = "no key entered — paste it, then press enter"
	setupLeftNote    = "Left the setup while the key was being checked. The check carries on, and its answer will appear here."
)

// The help line of each step. It is drawn INSIDE the box, under what it is
// about, in the words of the screen the owner asked this to look like
// (2026-10-11) -- it used to sit under the box in the chat's own "a · b · c"
// form, where it read as the chat's help and not this screen's.
const (
	setupListHelp = "Type to search, ↑/↓ navigate, Enter to select, "
	// setupListHelpShort is the same for a box too narrow for all of it.
	setupListHelpShort = "Enter to select, "
	setupKeyHelp       = "Enter to save, "
	setupCheckingHelp  = "Esc to leave (the check carries on)"

	setupEscBack      = "Esc to go back"
	setupEscSkip      = "Esc to skip to the chat"
	setupEscCancel    = "Esc to cancel"
	setupEscKeepModel = "Esc to keep the model"

	setupModelsFailed = "The model list could not be read: "
	setupNoModels     = "The provider in use offers no model here. /provider connects another."
)

// providerRows is the provider list: "any key" first, then every provider the
// daemon knows, in the order protocol.Providers offers them. currentBase is the
// address prompts go to now, "" when no provider is connected; the provider it
// belongs to is marked.
func providerRows(currentBase string) []pickerRow {
	current := ""
	if p, ok := protocol.ProviderForBase(currentBase); ok && currentBase != "" {
		current = p.ID
	}
	rows := []pickerRow{{
		ID: anyKeyRowID, Label: "Any key",
		Hint:  "the provider is told from the key",
		Match: "any auto detect recognise paste",
	}}
	rows = append(rows, localProviderRow(currentBase))
	for _, p := range protocol.Providers() {
		rows = append(rows, pickerRow{
			ID: p.ID, Label: p.Name, Hint: hostOfBase(p.APIBase),
			Match: strings.Join(p.Aliases, " "), Current: p.ID == current,
		})
	}
	return rows
}

// providerPicker is the provider list as it opens: on the provider in use,
// when there is one, so Enter-by-reflex changes nothing.
func (m chatModel) providerPicker() listPicker {
	p := newListPicker(setupProviderTitle, providerRows(m.conn.connectedBase()))
	p.placeholder = setupProviderSearch
	for _, row := range p.rows {
		if row.Current {
			p.selectID(row.ID)
		}
	}
	return p
}

// hostOfBase is the host of an API address, for a hint beside a provider's name.
func hostOfBase(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// modelRows turns the daemon's tiers into the model list, and names the one in
// force: the model this client chose (preferred), else the daemon's default.
// Only models a prompt could be sent to are offered, in the daemon's order --
// the one in force is MARKED and the list opens on it, it is not moved to the
// top, so a model is always found where its name puts it. Models that did not
// answer when the key was connected come last and dimmed -- still a choice,
// never the easy one.
func modelRows(tiers []protocol.StatusTier, preferred string) (rows []pickerRow, inForce string) {
	// This client's own choice outranks the daemon's default: it is named on
	// every prompt, whatever the daemon remembers. A choice that is no longer
	// offered names nothing, and the default stands.
	chosen := false
	for _, t := range tiers {
		switch {
		case !t.Active:
		case preferred != "" && t.Name == preferred:
			inForce, chosen = t.Name, true
		case t.Default && !chosen:
			inForce = t.Name
		}
	}
	var answering, quiet []pickerRow
	for _, t := range tiers {
		if !t.Active {
			continue
		}
		name := sanitizeText(t.Name)
		row := pickerRow{ID: t.Name, Label: name, Current: t.Name == inForce}
		if slug := sanitizeText(t.Slug); slug != "" && slug != name {
			row.Hint = slug
			row.Match = slug
		}
		if t.Quiet && !row.Current {
			row.Dim = true
			row.Hint = strings.TrimPrefix(row.Hint+" · did not answer when the key was connected", " · ")
			quiet = append(quiet, row)
			continue
		}
		answering = append(answering, row)
	}
	return append(answering, quiet...), inForce
}

// setupActive reports whether the setup owns the screen.
func (m chatModel) setupActive() bool { return m.setup.step != setupNone }

// withFirstRunSetup opens the client on the setup instead of the splash. Called
// once, by main, when the daemon's handshake says no credential is in force.
func (m chatModel) withFirstRunSetup() chatModel {
	m.setup = setupFlow{
		step: setupProvider, firstRun: true, viaList: true,
		picker: m.providerPicker(),
	}
	m.state = stateSetup
	return m
}

// beginSetup opens the provider list from the chat: /provider, or /connect on
// its own.
func (m chatModel) beginSetup() (tea.Model, tea.Cmd) {
	m.setup = setupFlow{
		step: setupProvider, viaList: true,
		picker: m.providerPicker(),
	}
	m.state = stateSetup
	m.statusErr = ""
	return m, nil
}

// beginModelPicker opens the model list from the chat: /model on its own.
//
// WHY (OWNER, 2026-10-12): "I can't even select the model". The list existed
// from 2026-10-11, but only as the step after a key was accepted -- so a user
// who already had a key never saw it, and /model went on printing every model
// as text to be typed back in full.
func (m chatModel) beginModelPicker() (tea.Model, tea.Cmd) {
	m.setup = setupFlow{step: setupModel, loading: true, modelOnly: true, checking: m.conn.providerName()}
	m.state = stateSetup
	m.statusErr = ""
	return m, tea.Batch(m.spinner.Tick, fetchTiersCmd(m.clientName))
}

// setupKeepsKey reports whether the key step showing is for the provider
// already in use, with a key in force -- so an empty entry can mean "keep it".
func (m chatModel) setupKeepsKey() bool {
	if m.setup.step != setupKey || m.setup.base == "" {
		return false
	}
	current := m.conn.connectedBase()
	if current == "" {
		return false
	}
	a, okA := protocol.ProviderForBase(m.setup.base)
	b, okB := protocol.ProviderForBase(current)
	if okA && okB {
		return a.ID == b.ID
	}
	return strings.TrimRight(m.setup.base, "/") == strings.TrimRight(current, "/")
}

// setupKeepKey is Enter on an empty key field for the provider in use: nothing
// is sent anywhere, and the setup moves on to that provider's models. Picking
// the provider you are already on is how someone who only wants another model
// arrives here, and being told "no key entered" was a dead end for them.
func (m chatModel) setupKeepKey() (tea.Model, tea.Cmd) {
	provider := providerShort(m.setup.base)
	m.endConnect()
	m.state = stateSetup
	m.setup.step = setupModel
	m.setup.loading = true
	m.setup.modelOnly = true
	m.setup.notice = ""
	m.setup.checking = provider
	m.syncInputWidth()
	return m, tea.Batch(m.spinner.Tick, fetchTiersCmd(m.clientName))
}

// beginSetupKey opens the masked key prompt as a step of the setup. base is
// where the key will be sent, "" to let the daemon tell from the key.
func (m chatModel) beginSetupKey(base, provider string) (tea.Model, tea.Cmd) {
	m.setup.step = setupKey
	m.setup.provider = provider
	m.setup.base = base
	next, cmd := m.beginConnect()
	cm := next.(chatModel)
	cm.connectBase = base
	cm.input.Placeholder = setupKeyPlaceholder
	cm.syncInputWidth()
	return cm, cmd
}

// leaveSetup closes the setup and returns to the prompt, saying note in the
// transcript when there is something to say.
func (m *chatModel) leaveSetup(note string) tea.Cmd {
	m.setup = setupFlow{}
	m.endConnect() // ordinary input again, whatever step this was left from
	m.syncInputWidth()
	if note != "" {
		m.appendTurn(turn{role: roleAssistant, local: true, text: note})
	}
	m.resizeViewport()
	m.refreshViewport()
	return m.input.Focus()
}

// setupBoxWidth is how wide the setup's box is in a terminal of this width.
func setupBoxWidth(terminalWidth int) int {
	// Wide enough, in an 80-column terminal, for the longest help line to stay
	// on one line: a wrapped help line is a list row spent on punctuation.
	w := terminalWidth - 6
	if w > 76 {
		w = 76
	}
	if w < 16 {
		w = 16
	}
	return w
}

// syncInputWidth sizes the input for where it is being drawn: the whole
// terminal for the prompt, the inside of the box for the setup's key step.
func (m *chatModel) syncInputWidth() {
	if m.setup.step == setupKey {
		// The box's inside, less the prompt and the cursor's own column.
		// setupBoxWidth never goes low enough for this to reach zero, which
		// the input would read as "no limit".
		width, _ := m.setupSize()
		// ... and less the key field's own border and padding (renderSetupKey).
		m.input.Width = max(setupBoxWidth(width)-4-4-len(m.input.Prompt)-1, 4)
		return
	}
	// The prompt's own box, when the terminal has one (promptbox.go).
	m.input.Width = inputWidthFor(m.promptCols(), m.input.Prompt)
}

// handleSetupKey owns the keyboard on the provider list, the checking screen
// and the model list. The key step in between is handleConnectKey's.
func (m chatModel) handleSetupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.setup.step {
	case setupAccount:
		// Nothing to choose here: any of the keys a person tries to leave a
		// screen with leaves it.
		switch msg.String() {
		case "esc", "enter", "q":
			cmd := m.leaveSetup("")
			return m, cmd
		}
		return m, nil

	case setupChecking:
		if msg.String() == "esc" {
			// The request cannot be recalled, and pretending it was would be a
			// lie about what left the machine. The answer is shown when it
			// comes: handleConnectResult takes it from here.
			cmd := m.leaveSetup(setupLeftNote)
			return m, cmd
		}
		return m, nil

	case setupModel:
		if m.setup.loading {
			if msg.String() == "esc" {
				cmd := m.leaveSetup("")
				return m, cmd
			}
			return m, nil
		}
		switch msg.String() {
		case "esc":
			cmd := m.leaveSetup("")
			return m, cmd
		case "enter":
			row, ok := m.setup.picker.selected()
			if !ok {
				return m, nil
			}
			inForce := m.setup.inForce
			focus := m.leaveSetup("")
			if row.ID == inForce {
				return m, focus
			}
			next, cmd := m.chooseModel(row.ID, row.Match)
			return next, tea.Batch(focus, cmd)
		}
		return m.setupListKey(msg), nil

	case setupLocal:
		return m.setupLocalKey(msg)

	case setupProvider:
		switch msg.String() {
		case "esc":
			cmd := m.leaveSetup(m.setupLeavingNote())
			return m, cmd
		case "enter":
			row, ok := m.setup.picker.selected()
			if !ok {
				return m, nil
			}
			m.setup.notice = ""
			if row.ID == anyKeyRowID {
				return m.beginSetupKey("", "")
			}
			if row.ID == localRowID {
				m.setup.viaList = true
				return m.beginLocalSetup()
			}
			p, known := protocol.ProviderByName(row.ID)
			if !known {
				return m, nil
			}
			return m.beginSetupKey(p.APIBase, p.Name)
		}
		return m.setupListKey(msg), nil
	}
	return m, nil
}

// setupListKey is the keys both lists share: moving, and the search text.
func (m chatModel) setupListKey(msg tea.KeyMsg) chatModel {
	switch msg.String() {
	case "up", "ctrl+p":
		m.setup.picker.move(-1)
	case "down", "ctrl+j":
		m.setup.picker.move(1)
	case "pgup":
		m.setup.picker.move(-8)
	case "pgdown":
		m.setup.picker.move(8)
	case "home":
		m.setup.picker.move(-len(m.setup.picker.rows))
	case "end":
		m.setup.picker.move(len(m.setup.picker.rows))
	case "backspace":
		m.setup.picker.backspace()
		m.setup.notice = ""
	default:
		if msg.Type != tea.KeyRunes && msg.Type != tea.KeySpace {
			return m
		}
		runes := msg.Runes
		if msg.Type == tea.KeySpace {
			runes = []rune{' '}
		}
		if m.setup.picker.typeRunes(runes, msg.Paste) {
			m.setup.notice = ""
		} else if m.setup.step == setupProvider {
			// A paste on the provider list is, as likely as not, the key.
			// It was not taken and is not on screen; say where it goes.
			m.setup.notice = setupPasteNote
		}
	}
	return m
}

// setupKeyEsc is Esc on the key step: back to the list it came from, or out.
func (m chatModel) setupKeyEsc() (tea.Model, tea.Cmd) {
	if m.setup.viaList {
		m.endConnect()
		m.syncInputWidth()
		m.state = stateSetup
		m.setup.step = setupProvider
		m.setup.notice = ""
		return m, nil
	}
	cmd := m.leaveSetup(m.setupLeavingNote())
	return m, cmd
}

// setupLeavingNote is what leaving the setup by Esc says.
//
// "NOTHING WAS SENT" IS SAID ONLY WHEN IT IS TRUE. Seen in the real app
// (2026-10-11): a key was refused by the provider, the user pressed Esc twice,
// and the chat said "nothing was sent or stored" directly under the provider's
// own refusal of what had just been sent.
func (m chatModel) setupLeavingNote() string {
	if m.setup.firstRun {
		if m.setup.sent {
			return setupSkippedNote
		}
		return strings.Replace(setupSkippedNote, "Setup skipped.", "Setup skipped — "+setupNothingSent+".", 1)
	}
	if m.setup.sent {
		return setupCancelledNote + "; the key and provider in use are unchanged"
	}
	return setupCancelledNote + "; " + setupNothingSent
}

// setupSubmitKey sends a key typed on the setup's key step, and shows the
// checking screen until the daemon answers. The caller has already cleared the
// input: from here the key is in this argument and the request, nowhere else.
//
// A KEY IS NEVER SENT TO A COMPANY THAT DID NOT ISSUE IT. Picking Groq and then
// pasting a key that starts "nvapi-" is a slip, and sending it where the list
// said would hand a working NVIDIA credential to Groq to be refused. When the
// key's own prefix names its issuer for certain, and the user picked a
// DIFFERENT named provider, the key goes to its issuer and the screen says so.
// An address typed by hand is left alone: a company gateway may well take keys
// that look like somebody else's.
func (m chatModel) setupSubmitKey(key, base string) (tea.Model, tea.Cmd) {
	sendTo := base
	if owners, certain := protocol.ProvidersForKey(key); certain && len(owners) == 1 {
		if picked, named := protocol.ProviderForBase(base); named && picked.ID != owners[0].ID {
			sendTo = owners[0].APIBase
			m.appendTurn(turn{role: roleAssistant, local: true, text: "That is a " + owners[0].Name +
				" key — told from how it starts — so it is being checked with " + owners[0].Name +
				", not with " + picked.Name + ". A key is only ever sent to the provider that issued it."})
		}
	}
	m.state = stateSetup
	m.setup.step = setupChecking
	m.setup.sent = true
	m.setup.notice = ""
	m.setup.checking = "the provider"
	if p, ok := protocol.ProviderForBase(sendTo); ok {
		m.setup.checking = p.Name
	} else if sendTo != "" {
		m.setup.checking = providerLabel(sendTo)
	}
	return m, tea.Batch(m.spinner.Tick, submitConnect(m.clientName, protocol.ConnectRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Connect:         true,
		APIKey:          key,
		APIBase:         sendTo,
	}))
}

// setupTiersMsg carries the model list back for the setup's last step.
type setupTiersMsg struct {
	tiers []protocol.StatusTier
	err   error
}

// fetchTiersCmd asks the daemon what models it offers, off the UI thread.
func fetchTiersCmd(clientName string) tea.Cmd {
	return func() tea.Msg {
		tiers, err := fetchAvailableTiers(clientName)
		return setupTiersMsg{tiers: tiers, err: err}
	}
}

// handleSetupConnectResult is the daemon's answer about a key sent from the
// setup. The answer is written into the transcript whatever it was -- that is
// the record the user finds when the setup closes -- and then decides which
// screen comes next.
func (m chatModel) handleSetupConnectResult(msg connectResultMsg) (tea.Model, tea.Cmd) {
	m.appendTurn(turn{role: roleAssistant, local: true, text: formatConnectResult(msg)})
	r := msg.resp

	switch {
	case msg.err != nil && isLocalBase(m.setup.base):
		return m.localConnectFailed("could not reach the daemon: " + msg.err.Error())
	case r.Error != "" && isLocalBase(m.setup.base):
		return m.localConnectFailed(r.Error)
	case msg.err != nil:
		m.setup.notice = "could not reach the daemon: " + sanitizeText(msg.err.Error())
		return m.beginSetupKey(m.setup.base, m.setup.provider)

	case r.Error != "":
		m.setup.notice = sanitizeText(r.Error)
		return m.beginSetupKey(m.setup.base, m.setup.provider)

	case r.Outcome == protocol.ConnectNeedsProvider:
		// The daemon sent the key nowhere: it could be several providers'.
		// Back to the list, narrowed when one word narrows it, to pick which.
		m.state = stateSetup
		m.setup.step = setupProvider
		m.setup.viaList = true
		m.setup.picker = newListPicker(setupProviderTitle, candidateRows(r.Candidates))
		m.setup.picker.placeholder = setupProviderSearch
		m.setup.notice = "that key was sent nowhere: " + sanitizeText(r.Detail) + " — pick its provider, then paste it again"
		return m, nil

	case r.Outcome == protocol.ConnectRejected:
		m.setup.notice = "refused by " + providerShort(r.APIBase) + ": " + sanitizeText(r.Detail) + " — nothing was stored"
		return m.beginSetupKey(m.setup.base, m.setup.provider)

	case (r.Outcome == protocol.ConnectAccepted || r.Outcome == protocol.ConnectUnverified) && r.InUse:
		m.needsAPIKey = false
		// A model named for the provider that was left names nothing here.
		m.preferredTier = ""
		m.state = stateSetup
		m.setup.step = setupModel
		m.setup.loading = true
		m.setup.notice = ""
		m.setup.checking = providerShort(r.APIBase)
		return m, tea.Batch(m.spinner.Tick, fetchTiersCmd(m.clientName), refreshConnCmd(m.clientName))
	}

	// Stored but not in use (a key in the environment wins), or an outcome this
	// client does not know: the line just written to the transcript says which,
	// and there is no model step to offer for a key that is not being sent.
	cmd := m.leaveSetup("")
	return m, cmd
}

// candidateRows is the provider list cut down to the providers a key could be
// from, in the order the daemon named them.
func candidateRows(ids []string) []pickerRow {
	var rows []pickerRow
	for _, id := range ids {
		if p, ok := protocol.ProviderByName(id); ok {
			rows = append(rows, pickerRow{ID: p.ID, Label: p.Name, Hint: hostOfBase(p.APIBase), Match: strings.Join(p.Aliases, " ")})
		}
	}
	if len(rows) == 0 {
		return providerRows("")
	}
	return rows
}

// providerShort is a provider's name alone when it has one, for a line with no
// room for the address as well.
func providerShort(apiBase string) string {
	if p, ok := protocol.ProviderForBase(apiBase); ok {
		return p.Name
	}
	return providerLabel(apiBase)
}

// handleSetupTiers shows the model list once the daemon has sent it.
func (m chatModel) handleSetupTiers(msg setupTiersMsg) (tea.Model, tea.Cmd) {
	if m.setup.step != setupModel || !m.setup.loading {
		return m, nil // the setup was left before the list arrived
	}
	m.setup.loading = false
	rows, inForce := modelRows(msg.tiers, m.preferredTier)
	switch {
	case m.setup.modelOnly && msg.err != nil:
		// Asked for by name, so its failure is said. See setupFlow.modelOnly.
		cmd := m.leaveSetup(setupModelsFailed + sanitizeText(msg.err.Error()))
		return m, cmd
	case m.setup.modelOnly && len(rows) == 0:
		cmd := m.leaveSetup(setupNoModels)
		return m, cmd
	case m.setup.modelOnly:
		// One model is still a list: it answers "which model am I on".
	case msg.err != nil || len(rows) < 2:
		// NOTHING TO CHOOSE BETWEEN is not a step of connecting a key. A list
		// that failed to arrive, or that holds the one model already in use,
		// would be a screen with a single possible answer; the transcript
		// already says which model prompts go to.
		cmd := m.leaveSetup("")
		return m, cmd
	}
	m.setup.picker = newListPicker(setupModelTitle, rows)
	m.setup.picker.placeholder = setupModelSearch
	m.setup.picker.selectID(inForce)
	m.setup.inForce = inForce
	m.setup.modelCount = len(rows)
	return m, nil
}

var (
	setupBoxStyle    = popupStyle.Padding(1, 2)
	setupNoticeStyle = lipgloss.NewStyle().Foreground(colorRed)
	setupTextStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	setupHelpStyle   = lipgloss.NewStyle().Foreground(colorGray).Faint(true).Italic(true)
)

// How much of the setup is drawn, from everything down to the least that still
// works. A step is drawn at the first of these that fits the terminal.
const (
	setupFull    = iota // heading, the box with air inside it, and the explanation
	setupCompact        // the explanation goes: the list or the input is what is needed
	setupBare           // the heading and the box's air go too
)

const (
	// setupListMax is the most rows a list shows however tall the terminal is:
	// past this the eye loses the highlight.
	setupListMax = 12
	// setupListMin is the fewest rows worth keeping the heading and the
	// explanation for; setupListWithLogo, the fewest worth drawing the logo above.
	setupListMin      = 4
	setupListWithLogo = 6
	// setupListRoomy is the fewest rows worth keeping the box's blank lines
	// for. The bordered search field and the help line inside the box cost
	// rows the first version of this screen gave to the list; in a 24-row
	// terminal the air goes before the list drops under this.
	setupListRoomy = 8
	// setupLogoMinWidth is the narrowest terminal the lotus is drawn in.
	setupLogoMinWidth = 44
	// setupUnsizedWidth and setupUnsizedHeight stand in until the terminal has
	// said how big it is, which is one frame at most.
	setupUnsizedWidth  = 80
	setupUnsizedHeight = 24
)

// setupSize is the terminal the setup is drawn for.
func (m chatModel) setupSize() (width, height int) {
	if !m.ready {
		return setupUnsizedWidth, setupUnsizedHeight
	}
	return m.width, m.height
}

// setupHasList reports whether the step showing is one of the two lists.
func (m chatModel) setupHasList() bool {
	return m.setup.step == setupProvider || ((m.setup.step == setupModel || m.setup.step == setupLocal) && !m.setup.loading)
}

// setupFit decides how the current step is drawn so that it fits: how many list
// rows, whether the logo is above it, and how much of the rest is kept.
//
// IT MEASURES RATHER THAN COUNTS. An earlier version subtracted a constant for
// "the lines around the list", and was wrong as soon as a notice wrapped onto a
// second line or the box was narrow enough to wrap the explanation: the screen
// came out taller than the terminal and the search line scrolled off the top.
// So each arrangement is rendered with a one-row list, measured, and the list
// gets what is left.
//
// THE LOGO IS THE FIRST THING TO GO, then the explanation, then the heading. On
// a short terminal the list is what the user came for; decoration that pushes
// the highlighted row off the bottom is costing function.
func (m chatModel) setupFit() (listRows int, logo bool, detail int) {
	width, height := m.setupSize()
	type arrangement struct {
		logo   bool
		detail int
		min    int // fewest list rows this arrangement is worth having for
	}
	tries := []arrangement{
		{true, setupFull, setupListMin},
		{true, setupCompact, setupListMin},
		{false, setupFull, setupListRoomy},
		{false, setupCompact, setupListMin},
		{false, setupBare, 1},
	}
	logoFits := m.setupLogoFits(width, height)
	for i, a := range tries {
		if a.logo && !logoFits {
			continue
		}
		last := i == len(tries)-1
		around := lipgloss.Height(m.setupBlock(1, a.logo, a.detail)) - 1
		if !m.setupHasList() {
			if around+1 <= height || last {
				return 1, a.logo, a.detail
			}
			continue
		}
		rows := height - around
		if rows < a.min && !last {
			continue
		}
		if rows > setupListMax {
			rows = setupListMax
		}
		if rows < 1 {
			rows = 1
		}
		return rows, a.logo, a.detail
	}
	return 1, false, setupBare
}

// setupLogoFits reports whether this terminal has room for the logo above the
// setup -- decided ONCE FOR THE WHOLE SETUP, by its tallest screen, the provider
// list. Deciding it per step drew the lotus above the short "checking" screen
// and took it away again on the list either side, so the heading jumped up and
// down as the user moved through three steps.
func (m chatModel) setupLogoFits(width, height int) bool {
	if !m.setup.firstRun || width < setupLogoMinWidth {
		return false
	}
	probe := m
	probe.setup.step = setupProvider
	probe.setup.notice = ""
	probe.setup.picker = newListPicker(setupProviderTitle, providerRows(""))
	around := lipgloss.Height(probe.setupBlock(1, true, setupFull)) - 1
	return height-around >= setupListWithLogo
}

// renderSetup draws the setup full-screen, arranged to fit (setupFit).
func (m chatModel) renderSetup() string {
	width, height := m.setupSize()
	rows, logo, detail := m.setupFit()
	block := m.setupBlock(rows, logo, detail)
	// A terminal too short even for the bare arrangement keeps the TOP: the
	// search line and the highlighted row are there, and what a renderer does
	// with too many lines is drop the first ones.
	if lines := strings.Split(block, "\n"); len(lines) > height && height > 0 {
		block = strings.Join(lines[:height], "\n")
	}
	if !m.ready {
		return block
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}

// setupBlock is the current step drawn one way: listRows rows of list, with or
// without the logo, at one level of detail.
func (m chatModel) setupBlock(listRows int, logo bool, detail int) string {
	width, _ := m.setupSize()
	boxW := setupBoxWidth(width)
	inner := boxW - 4 // the box's own side padding
	if inner < 8 {
		inner = 8
	}
	// On a terminal with no room to spare the search field loses its border:
	// three rows of field are three rows of list.
	picker := m.setup.picker
	picker.compact = detail == setupBare
	picker.airy = detail == setupFull

	help := ""
	var body string
	switch m.setup.step {
	case setupProvider:
		esc := setupEscBack
		if m.setup.firstRun {
			esc = setupEscSkip
		}
		help = setupListHelp + esc
		body = picker.view(inner, listRows)
	case setupKey:
		esc := setupEscCancel
		if m.setup.viaList {
			esc = setupEscBack
		}
		help = setupKeyHelp + esc
		body = m.renderSetupKey(inner, detail)
	case setupChecking:
		help = setupCheckingHelp
		body = m.spinner.View() + " " + wrapText(setupTextStyle, "checking the key with "+m.setup.checking+
			" and finding a model that answers", inner-2)
		if isLocalBase(m.setup.base) {
			body = m.spinner.View() + " " + wrapText(setupTextStyle, "asking "+m.setup.checking+" for its models", inner-2)
		} else if detail == setupFull {
			body += "\n\n" + wrapText(pickerHintStyle, "This is a real request to the provider. It can take up to "+
				"20 seconds when the provider lists many models.", inner)
		}
	case setupLocal:
		help, body = m.renderSetupLocal(picker, inner, listRows, detail)
	case setupAccount:
		help = accountHelp
		body = m.renderAccount(inner, detail)
	case setupModel:
		help = setupEscKeepModel
		if m.setup.loading {
			whose := "the provider"
			if m.setup.checking != "" {
				whose = m.setup.checking
			}
			body = m.spinner.View() + " " + wrapText(setupTextStyle, "asking for the models "+whose+" offers", inner-2)
			break
		}
		help = setupListHelp + setupEscKeepModel
		if detail == setupFull {
			body = wrapText(pickerHintStyle, m.setupModelIntro(), inner) + "\n\n"
		}
		body += picker.view(inner, listRows)
	}

	if m.setup.notice != "" {
		// Bounded: a provider's own words can run long, and the notice must not
		// be what pushes the input off the screen.
		body += "\n\n" + wrapText(setupNoticeStyle, ansi.Truncate(m.setup.notice, inner*3, "…"), inner)
	}
	if m.quitArmed {
		help = quitConfirmHelpText
	}
	// The help line closes the box. With no room to spare it is one line, cut
	// rather than wrapped, directly under what it is about.
	switch detail {
	case setupFull:
		body += "\n\n" + wrapText(setupHelpStyle, help, inner)
	case setupCompact:
		body += "\n" + wrapText(setupHelpStyle, help, inner)
	default:
		// ONE LINE, AND THE WAY OUT IS ON IT. Cut from the right, a list's help
		// lost "Esc to …" first -- the one key a user stuck on a screen needs
		// (seen at 60 columns, 2026-10-12). So what a list does not need said
		// goes first: that typing searches is shown by the field itself.
		if lipgloss.Width(help) > inner {
			help = strings.TrimPrefix(help, setupListHelp)
			if help != quitConfirmHelpText && m.setupHasList() {
				help = setupListHelpShort + help
			}
		}
		body += "\n" + setupHelpStyle.Render(ansi.Truncate(help, inner, "…"))
	}

	boxStyle := setupBoxStyle
	if detail == setupBare {
		boxStyle = popupStyle.Padding(0, 2)
	}
	parts := []string{boxStyle.Width(boxW).Render(body)}
	if detail != setupBare {
		heading := setupFromCommand
		switch {
		case m.setup.firstRun:
			heading = setupWelcome
		case m.setup.step == setupAccount:
			heading = accountHeading
		case m.setup.modelOnly:
			heading = setupFromModel
		}
		parts = append([]string{brandStyle.Render(brandName), taglineStyle.Render(ansi.Truncate(heading, boxW, "…")), ""}, parts...)
	}
	if logo {
		parts = append([]string{renderLogo(), ""}, parts...)
	}
	return lipgloss.JoinVertical(lipgloss.Center, parts...)
}

// setupModelIntro is the sentence above the model list.
//
// "CONNECTED TO" IS SAID ONLY AFTER A CONNECT. Opened by /model nothing was
// connected, and the same words would tell a user their key had just been
// checked when no request left the machine.
func (m chatModel) setupModelIntro() string {
	const remembered = "Choose the model prompts go to — it is remembered after a restart"
	if !m.setup.modelOnly {
		return "Connected to " + m.setup.checking + ". " + remembered + ", and /model changes it later."
	}
	if m.setup.checking == "" {
		return remembered + "."
	}
	offers := fmt.Sprintf("%s offers %d models here. ", m.setup.checking, m.setup.modelCount)
	if m.setup.modelCount == 1 {
		offers = m.setup.checking + " offers one model here. "
	}
	return offers + remembered + "."
}

// renderSetupKey is the inside of the key step: who the key is for, where it is
// about to be sent, and the masked input in a field of its own. The explanation
// is dropped when the terminal has no room for it; who the key goes to never is.
func (m chatModel) renderSetupKey(inner, detail int) string {
	var title, where string
	switch {
	case m.setup.provider != "":
		title = m.setup.provider
		where = "It is sent to " + sanitizeText(m.connectBase) + " to be checked."
	case m.connectBase != "":
		title = providerShort(m.connectBase)
		where = "It is sent to " + sanitizeText(m.connectBase) + " to be checked."
	default:
		title = "Any provider's key"
		where = anyProviderLine
	}
	out := pickerTitleStyle.Render(ansi.Truncate(title, inner, "…")) + "\n"
	switch {
	case detail == setupFull:
		out += "\n" + wrapText(pickerHintStyle, "Paste your API key below. "+where+" Nothing is stored if the key is "+
			"refused, and it is not shown as you type.", inner) + "\n\n"
	case m.connectBase != "":
		// Where a secret is about to go is not explanation; it stays.
		out += pickerHintStyle.Render(ansi.Truncate("sent to "+sanitizeText(m.connectBase), inner, "…")) + "\n"
	}
	out += setupTextStyle.Render("API key") + "\n"
	if detail == setupBare {
		out += m.input.View()
	} else {
		out += pickerFieldLive.Width(inner - 2).Render(m.input.View())
	}
	// A way on that is not explanation either: without it, someone who picked
	// the provider they are already on has to paste a key they may not have to
	// hand, to change something that is not the key.
	if m.setupKeepsKey() {
		keep := "Leave it empty and press Enter to keep the key in use"
		// The daemon's mask is "...3f2a (70 characters)"; the tail is enough to
		// tell which key, and the count would be brackets inside brackets.
		if masked, _, _ := strings.Cut(sanitizeText(m.conn.maskedKey()), " ("); masked != "" {
			keep += " (" + masked + ")"
		}
		out += "\n" + wrapText(pickerHintStyle, keep+".", inner)
	}
	return out
}

// wrapText renders s in style, wrapped to width.
func wrapText(style lipgloss.Style, s string, width int) string {
	if width < 8 {
		width = 8
	}
	return style.Width(width).Render(s)
}
