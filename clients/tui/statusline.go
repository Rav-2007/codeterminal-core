package tui

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// THE LINE UNDER THE PROMPT: which model, at which provider, and what this
// session has cost.
//
// WHY IT EXISTS (OWNER, 2026-10-12): "I can't see my account, I can't select
// the provider." Both were true in a narrower sense than they sound -- the
// provider and model could be changed -- but nothing on the screen ever said
// what they WERE, so there was no starting point to change them from, and no
// sign that a command for it existed. The owner was shown this line beside a
// clean screen and chose the line.
//
// THIS REVISES AN EARLIER DECISION BY ONE ROW. The header was cut to a single
// row and every notice moved to /context because the owner asked for a clean
// screen with only the prompt (see renderHeader). That stands: the header is
// still one row and no notice came back. This is one fixed row at the bottom,
// which never appears or disappears, so the transcript never moves for it.
//
// WHERE THE FACTS COME FROM. The model and provider are the daemon's answer to
// ConnectRequest.Show (protocol.ConnectInForce): what it is sending to now, not
// what is stored. It is asked at start and again after anything that can
// change it -- a key connected, a model chosen -- and it asks no provider. The
// tokens and cost are this client's own sum of the provider's bills (usage.go).
//
// THE SAME ROW ALSO SAYS WHO IS RUNNING THE CLIENT (2026-10-13), at its right
// edge. That part is whoami.go's: statusLine here is the left of the row and
// is laid out first, and statusRow there puts the name in what it leaves.

// chatExtras is chatModel state added on 2026-10-12, embedded in chatModel
// rather than written into it, and handleSideAnswer below is three messages
// answered from one case. Neither is how this would be laid out for its own
// sake.
//
// WHY: clients/tui/chat.go MUST NOT GROW FOR THIS. The retrieval eval indexes
// this repository and a chunk is a fixed window of lines, so a line added high
// in a 3,300-line file re-windows everything under it. daemon/serverextras.go
// has the measurement; chat.go cost that eval one query the same day. So what
// this feature needs of chat.go replaces lines one for one, and everything
// else is in files of its own.
type chatExtras struct {
	// conn is the daemon's last answer about what it is sending to.
	conn connInfo
	// preferredSlug is what the provider calls preferredTier, kept when a
	// model is chosen so the line under the prompt can show the id a user
	// recognises. Meaningless while preferredTier is empty.
	preferredSlug string
	// whoami is who is running this client and on which machine, as one line
	// that is safe to draw ("ravi@laptop"); empty when the system would not say.
	// Stored once, before anything is drawn, and only ever drawn (whoami.go).
	whoami string
	// githubTokenEntry says the masked box is taking a GitHub token, not a
	// provider key (github.go). Cleared by endConnect, with the box.
	githubTokenEntry bool
}

// connInfo is what the daemon last said it is sending to.
type connInfo struct {
	// known is set once an answer -- or a failure to get one -- has arrived.
	known bool
	resp  protocol.ConnectResponse
	err   error
}

// ok reports whether there is an answer to read.
func (c connInfo) ok() bool { return c.known && c.err == nil && c.resp.Error == "" }

// inForceKnown reports whether the daemon said what is in force. A daemon
// started before it could leaves KeySource empty (protocol.ConnectInForce).
func (c connInfo) inForceKnown() bool { return c.ok() && c.resp.KeySource != "" }

// activeBase is the address prompts go to, as far as this client can tell: the
// daemon's own word, or -- from an older daemon -- the stored key's address
// when that key is the one in use.
func (c connInfo) activeBase() string {
	switch {
	case c.inForceKnown():
		return c.resp.ActiveAPIBase
	case c.ok() && c.resp.InUse:
		return c.resp.APIBase
	}
	return ""
}

// connectedBase is activeBase when a prompt sent there would be answered: a
// key is in force, or the address is a model server on this machine that wants
// none. Empty otherwise -- the daemon always has SOME address, and naming it
// as "connected" with no key would be naming a provider that will refuse.
func (c connInfo) connectedBase() string {
	base := c.activeBase()
	switch {
	case base == "":
		return ""
	case !c.inForceKnown():
		return base // an older daemon: activeBase already required a key in use
	case c.resp.KeySource != protocol.KeySourceNone, isLocalBase(base):
		return base
	}
	return ""
}

// providerName is the provider in force by name, "" when none is connected.
func (c connInfo) providerName() string {
	base := c.connectedBase()
	if base == "" {
		return ""
	}
	if c.inForceKnown() && c.resp.KeySource == protocol.KeySourceProxy {
		return "the Mochiii proxy"
	}
	return providerBrief(base)
}

// maskedKey is the key in force, as the daemon masked it.
func (c connInfo) maskedKey() string {
	switch {
	case c.inForceKnown():
		return c.resp.ActiveMaskedKey
	case c.ok() && c.resp.InUse:
		return c.resp.MaskedKey
	}
	return ""
}

// isLocalBase reports whether an API address is on this machine.
func isLocalBase(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// providerBrief names where an address is in the fewest words that are still
// true: the provider's name, "this machine", or the host.
func providerBrief(base string) string {
	if p, ok := protocol.ProviderForBase(base); ok {
		return p.Name
	}
	if isLocalBase(base) {
		return "this machine"
	}
	if host := hostOfBase(base); host != "" {
		return sanitizeText(host)
	}
	return sanitizeText(base)
}

// connInfoMsg is the daemon's answer to "what are you sending to".
type connInfoMsg struct {
	resp protocol.ConnectResponse
	err  error
}

// fetchConnInfo asks the daemon what is in force. One local round trip; the
// daemon asks nobody (daemon/account.go).
func fetchConnInfo(clientName string) connInfoMsg {
	resp, err := sendConnect(clientName, protocol.ConnectRequest{
		ProtocolVersion: protocol.ProtocolVersion, Connect: true, Show: true,
	})
	return connInfoMsg{resp: resp, err: err}
}

// refreshConnCmd performs fetchConnInfo off the UI thread.
func refreshConnCmd(clientName string) tea.Cmd {
	return func() tea.Msg { return fetchConnInfo(clientName) }
}

// handleSideAnswer takes the answers that change what the line under the
// prompt says: a model choice, what is in force, and /account's. One case in
// chat.go's Update for the three -- see chatExtras for why.
func (m chatModel) handleSideAnswer(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case modelChoiceMsg:
		return m.handleModelChoice(msg)
	case connInfoMsg:
		m.conn = connInfo{known: true, resp: msg.resp, err: msg.err}
		return m, nil
	case accountMsg:
		return m.handleAccount(msg)
	case rulesResultMsg: // not the line's, but chat.go has one case for all of these
		return m.handleRulesResult(msg)
	case githubResultMsg:
		return m.handleGitHubResult(msg)
	}
	return m, nil
}

const (
	// statusLineMinHeight is the shortest terminal the line is drawn in. Under
	// it every row is transcript: the line says what /account says, and the
	// answer being read does not.
	statusLineMinHeight = 8

	statusNoProvider = "no provider connected · /provider connects one"
)

var (
	statusLineStyle  = lipgloss.NewStyle().Foreground(colorDim)
	statusModelStyle = lipgloss.NewStyle().Foreground(colorSteel)
)

// statusRows is how many rows the line takes: one, or none in a terminal too
// short for it. It depends on the terminal's height ALONE, so the viewport --
// sized when the terminal is -- and the frame drawn afterwards always agree
// about whether the row is there.
func (m chatModel) statusRows() int {
	if m.height < statusLineMinHeight {
		return 0
	}
	return 1
}

// statusModel is the model prompts go to, by the id a user recognises: this
// client's own choice when it made one, else the daemon's.
func (m chatModel) statusModel() string {
	if m.preferredTier != "" {
		return firstNonEmptyText(m.preferredSlug, m.preferredTier)
	}
	if !m.conn.inForceKnown() {
		return ""
	}
	return firstNonEmptyText(m.conn.resp.ModelSlug, m.conn.resp.Model)
}

func firstNonEmptyText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// statusLine is the line itself, with no newline, cut to the terminal's width.
// Empty until the daemon has answered: a row that guesses would have to be
// corrected a moment later, and the correction would read as a change.
func (m chatModel) statusLine() string {
	// EACH PART IS STYLED BY ITSELF and the styled parts are joined. Styling
	// the joined line as a whole looks the same in the source and is not: the
	// model's own colour ends in a reset, and everything after it was drawn in
	// the terminal's default white instead of this line's grey (seen in the
	// real app, 2026-10-12).
	var parts []string
	plain := func(text string) { parts = append(parts, statusLineStyle.Render(text)) }
	switch provider := m.conn.providerName(); {
	case !m.conn.known:
		// Not asked yet, or not answered yet.
	case !m.conn.ok():
		// The daemon could not be asked. Whatever is wrong, the next prompt
		// will say it properly; this line does not guess at a provider.
	case provider == "" && !m.conn.inForceKnown():
		// An older daemon whose stored key is not the one in use: something
		// IS in force, and this client cannot tell what. "No provider" would
		// be a guess, and a wrong one.
	case provider == "":
		plain(statusNoProvider)
	default:
		if model := sanitizeText(m.statusModel()); model != "" {
			parts = append(parts, statusModelStyle.Render(model))
		}
		plain(sanitizeText(provider))
	}
	if t := m.sessionUsage; t.turns > 0 {
		plain(compactTokens(t.prompt+t.completion) + " tokens")
		if cost := statusCost(t); cost != "" {
			plain(cost)
		}
	}
	width := m.width
	if width < 1 {
		width = 1
	}
	return ansi.Truncate(strings.Join(parts, statusLineStyle.Render(" · ")), width, "…")
}

// statusCost is the session's cost for the line, "" when the provider reported
// none -- a provider that bills nothing through its API is not "$0.00".
func statusCost(t usageTotals) string {
	if t.costMissing && t.cost == 0 {
		return ""
	}
	amount := fmt.Sprintf("$%.2f", t.cost)
	if t.cost > 0 && t.cost < 0.01 {
		amount = fmt.Sprintf("$%.4f", t.cost)
	}
	if t.costMissing {
		return "at least " + amount
	}
	return amount
}

// withStatusLine puts the row above the help line, when the terminal has the
// row for it (statusRows): the line, and who is running the client (whoami.go).
func (m chatModel) withStatusLine(help string) string {
	if m.statusRows() == 0 {
		return help
	}
	return m.statusRow() + "\n" + help
}
