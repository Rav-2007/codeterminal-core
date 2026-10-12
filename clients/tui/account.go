package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// /account: WHAT THIS CLIENT IS CONNECTED TO, AND WHAT IT HAS SPENT.
//
// WHY IT EXISTS (OWNER, 2026-10-12): "where is the slash command to check the
// account details". There was none. /connect show printed one line about the
// stored key, /model listed models, /usage and /budget each had half of the
// spending -- four commands, none of them named for the question being asked.
//
// WHAT "ACCOUNT" MEANS HERE. Mochiii has no sign-in: the user brings a key, so
// there is no account of ours to show. The owner was offered three readings and
// chose this one -- provider, key, model, spend against the limits -- and
// turned down asking the provider for a balance. So NOTHING HERE LEAVES THE
// MACHINE: both requests go to the local daemon, which answers from what it
// already holds.
//
// A SCREEN, NOT A TURN. It is drawn in the same box as the provider and model
// lists (setup.go) and closed with Esc. A turn in the transcript cannot hold
// it: the transcript strips styling and re-wraps to the terminal, so a box
// written into it comes out grey and broken at the next resize.

// accountMsg is everything /account shows, fetched in one go.
type accountMsg struct {
	conn     connInfoMsg
	spend    *protocol.SpendStatus
	spendErr error
}

// accountCmd asks the daemon what is in force and where the limits stand, off
// the UI thread.
func accountCmd(clientName string) tea.Cmd {
	return func() tea.Msg {
		msg := accountMsg{conn: fetchConnInfo(clientName)}
		msg.spend, msg.spendErr = sendBudget(clientName, protocol.BudgetActionStatus)
		return msg
	}
}

// handleSpendCommand answers /budget and /account, which share one case of
// handleLocalSlash for the reason chatExtras gives (statusline.go) and nothing
// else: they are two commands.
func (m chatModel) handleSpendCommand(name, args string) (tea.Model, tea.Cmd) {
	if name == "account" {
		return m.beginAccount()
	}
	return m.handleBudgetCommand(args)
}

// beginAccount opens the account screen: /account.
func (m chatModel) beginAccount() (tea.Model, tea.Cmd) {
	m.setup = setupFlow{step: setupAccount, loading: true}
	m.state = stateSetup
	m.statusErr = ""
	return m, tea.Batch(m.spinner.Tick, accountCmd(m.clientName))
}

// handleAccount takes the daemon's answers. They are kept even if the screen
// was closed while they were on their way: what is in force is also what the
// line under the prompt reads.
func (m chatModel) handleAccount(msg accountMsg) (tea.Model, tea.Cmd) {
	m.conn = connInfo{known: true, resp: msg.conn.resp, err: msg.conn.err}
	if m.setup.step != setupAccount {
		return m, nil
	}
	m.setup.loading = false
	m.setup.account = msg
	return m, nil
}

const (
	accountTitle   = "Account"
	accountHeading = "what this client is connected to"
	accountHelp    = "Esc to close"
	accountChange  = "/provider changes the provider or the key · /model changes the model"
	// accountLabelWidth is the width of the left-hand column.
	accountLabelWidth = 11

	accountOlderDaemon = "The daemon running is older than this client, so it cannot say which model is in " +
		"force or where its key comes from. Restart it — ./run-tui.sh does."
)

var accountConnectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))

// accountField is one labelled group: a first line and what is said under it.
type accountField struct {
	label string
	value string
	// mark follows the value in green ("● connected").
	mark string
	// more are further lines under the value, dimmer.
	more []string
}

// accountFields is what the screen says, as data -- one place decides the
// words, and the drawing only lays them out.
func (m chatModel) accountFields() []accountField {
	msg := m.setup.account
	c := connInfo{known: true, resp: msg.conn.resp, err: msg.conn.err}
	r := c.resp

	if !c.ok() {
		why := r.Error
		if c.err != nil {
			why = c.err.Error()
		}
		return []accountField{{label: "Daemon", value: "could not be asked", more: []string{sanitizeText(why)}}}
	}

	var out []accountField

	// PROVIDER.
	provider := accountField{label: "Provider", value: "none connected", more: []string{"/provider connects one"}}
	if base := c.connectedBase(); base != "" {
		provider = accountField{label: "Provider", value: sanitizeText(c.providerName()), mark: "● connected",
			more: []string{sanitizeText(base)}}
	}
	out = append(out, provider)

	// KEY. Said by where it comes from, because that is what decides what a
	// user can do about it: a stored key is replaced by /provider, one from the
	// environment is not.
	key := accountField{label: "API key"}
	stored := r.MaskedKey != "" && r.MaskedKey != "(none)"
	verified := "not verified when it was saved"
	if strings.Contains(r.Detail, "accepted") {
		verified = "accepted by the provider when it was saved"
	}
	switch {
	case !c.inForceKnown() && r.InUse:
		key.value = sanitizeText(r.MaskedKey)
		key.more = []string{"stored on this machine", verified}
	case !c.inForceKnown():
		key.value = "not the stored one"
		key.more = []string{"this daemon's key comes from its environment, or it runs in proxy mode"}
	case r.KeySource == protocol.KeySourceStored:
		key.value = sanitizeText(r.ActiveMaskedKey)
		key.more = []string{"stored on this machine", verified}
	case r.KeySource == protocol.KeySourceEnvironment:
		key.value = sanitizeText(r.ActiveMaskedKey)
		key.more = []string{"from MOCHIII_API_KEY in the daemon's environment"}
		if stored {
			key.more = append(key.more, "a stored key ("+sanitizeText(r.MaskedKey)+") is not the one in use")
		}
	case r.KeySource == protocol.KeySourceLauncher:
		key.value = sanitizeText(r.ActiveMaskedKey)
		key.more = []string{"handed over by the program that started the daemon (the editor's own key store)"}
		if stored {
			key.more = append(key.more, "a stored key ("+sanitizeText(r.MaskedKey)+") is not the one in use")
		}
	case r.KeySource == protocol.KeySourceProxy:
		key.value = "none on this machine"
		key.more = []string{"proxy mode: the proxy holds the provider's key"}
	case isLocalBase(r.ActiveAPIBase):
		key.value = "none needed"
		key.more = []string{"a model server on this machine asks for none"}
	default:
		key.value = "none"
		key.more = []string{"the next question will ask for one"}
	}
	out = append(out, key)

	// MODEL.
	if c.inForceKnown() {
		model := accountField{label: "Model", value: sanitizeText(m.statusModel())}
		switch {
		case m.preferredTier != "" && m.preferredTier != r.Model:
			// This client is sending a model the daemon was not able to take
			// as its own: it holds for as long as this window does.
			model.value += "  (this session only)"
		case r.ModelSaved:
			model.value += "  (remembered)"
		}
		if model.value == "" {
			model.value = "not known"
		}
		switch {
		case r.ModelCount == 1:
			model.more = []string{"the only model offered here"}
		case r.ModelCount > 1:
			model.more = []string{fmt.Sprintf("%d models to choose from", r.ModelCount)}
		}
		out = append(out, model)
	}

	// SPEND. The daemon's count when it has one -- it holds turns that were
	// stopped, and every project today -- else this client's own sum.
	switch {
	case msg.spendErr == nil && msg.spend != nil:
		session := "not counted for this client"
		if msg.spend.Session != (protocol.SpendMeter{}) {
			session = meterText(msg.spend.Session)
		}
		out = append(out,
			accountField{label: "Session", value: session},
			accountField{label: "Today", value: meterText(msg.spend.Day), more: []string{"every project on this machine"}})
		if msg.spend.Reached {
			out = append(out, accountField{label: "Limit", value: "reached — /budget more allows another allotment"})
		}
	default:
		session := "nothing yet"
		if t := m.sessionUsage; t.turns > 0 {
			session = compactTokens(t.prompt+t.completion) + " tokens · " + dollars(t)
		}
		out = append(out, accountField{label: "Session", value: session, more: []string{"this client's own count"}})
		if msg.spendErr != nil && !errors.Is(msg.spendErr, errDaemonPredatesBudget) {
			out = append(out, accountField{label: "Today", value: "not available", more: []string{sanitizeText(msg.spendErr.Error())}})
		}
	}
	return out
}

// meterText is one limit in a line: "18.4K of 2.00M tokens (1%) · $0.01 of $2.00".
func meterText(sm protocol.SpendMeter) string {
	text := compactTokens(sm.Tokens)
	if sm.LimitTokens > 0 {
		text += " of " + compactTokens(sm.LimitTokens) + " tokens (" + percent(100*float64(sm.Tokens)/float64(sm.LimitTokens)) + ")"
	} else {
		text += " tokens"
	}
	switch {
	case sm.LimitUSD > 0:
		text += fmt.Sprintf(" · $%.2f of $%.2f", sm.USD, sm.LimitUSD)
	case sm.USD > 0:
		text += fmt.Sprintf(" · $%.2f", sm.USD)
	}
	return text
}

// renderAccount is the inside of the account screen, inner columns wide.
//
// With room, each group has its second lines and a blank line after it; with
// less, the blank lines go; with none to spare, the second lines go too and
// what is left is one line a fact.
func (m chatModel) renderAccount(inner, detail int) string {
	lines := []string{pickerTitleStyle.Render(accountTitle)}
	if m.setup.loading {
		return strings.Join(append(lines, "", m.spinner.View()+" "+setupTextStyle.Render("asking the daemon")), "\n")
	}
	valueW := inner - accountLabelWidth - 1
	if valueW < 8 {
		valueW = 8
	}
	indent := strings.Repeat(" ", accountLabelWidth+1)
	for _, f := range m.accountFields() {
		if detail == setupFull {
			lines = append(lines, "")
		}
		label := pickerHintStyle.Render(fmt.Sprintf("%-*s", accountLabelWidth, f.label))
		mark := ""
		if f.mark != "" && lipgloss.Width(f.value)+lipgloss.Width(f.mark)+2 <= valueW {
			mark = "  " + accountConnectedStyle.Render(f.mark)
		}
		value := ansi.Truncate(f.value, valueW-lipgloss.Width(mark), "…")
		lines = append(lines, label+" "+setupTextStyle.Render(value)+mark)
		if detail == setupBare {
			continue
		}
		for _, more := range f.more {
			lines = append(lines, indent+pickerHintStyle.Render(ansi.Truncate(more, valueW, "…")))
		}
	}
	if !m.connInfoOfAccount().inForceKnown() && m.connInfoOfAccount().ok() {
		lines = append(lines, "", wrapText(setupNoticeStyle, accountOlderDaemon, inner))
	}
	if detail != setupBare {
		lines = append(lines, "", wrapText(pickerHintStyle, accountChange, inner))
	}
	return strings.Join(lines, "\n")
}

// connInfoOfAccount is the answer the screen is showing, as a connInfo.
func (m chatModel) connInfoOfAccount() connInfo {
	return connInfo{known: true, resp: m.setup.account.conn.resp, err: m.setup.account.conn.err}
}
