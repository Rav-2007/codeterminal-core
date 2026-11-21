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
	// rateWait is a rate-limit wait in progress: drawn while it lasts, never
	// left in the chat (ratewait.go, 2026-10-25).
	rateWait rateWaitState
	// copyView is /copy: the chat printed on the terminal's normal screen,
	// where a drag can select all of it (copyview.go, 2026-11-21).
	copyView copyViewPhase
	// inline is the chat printed into the terminal's own scrollback, the
	// default since 2026-11-21 (inline.go).
	inline inlineState
	// autoApprove answers the questions a tool call or an edit would ask, as y
	// would; off at every start (autoapprove.go, 2026-11-21).
	autoApprove bool
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
		return m.afterConnInfo() // localmodels.go
	case localServersMsg:
		return m.handleLocalServers(msg)
	case localFitMsg:
		return m.handleLocalFit(msg)
	case localCheckMsg:
		return m.handleLocalCheck(msg) // localagent.go
	case accountMsg:
		return m.handleAccount(msg)
	case rulesResultMsg: // not the line's, but chat.go has one case for all of these
		return m.handleRulesResult(msg)
	case githubResultMsg:
		return m.handleGitHubResult(msg)
	case rateWaitTickMsg: // the countdown of a rate-limit wait (ratewait.go)
		return m.handleRateWaitTick(msg)
	case approvalLostMsg: // an approval answer that could not be sent (approvallost.go)
		return m.handleApprovalLost(msg)
	case copyViewMsg: // /copy on its way back to the chat (copyview.go)
		return m.handleCopyView(msg)
	}
	return m, nil
}

const (
	// statusLineMinHeight is the shortest terminal the line is drawn in. Under
	// it every row is transcript: the line says what /account says, and the
	// answer being read does not.
	statusLineMinHeight = 8
	// statusAutoMinHeight is the shortest terminal the auto-approve row is
	// drawn in (2026-11-21, after Cline's). Auto-approve being on is said above
	// the prompt as well, in every window.
	statusAutoMinHeight = 14

	statusNoProvider = "no provider connected · /provider connects one"
)

var (
	statusLineStyle  = lipgloss.NewStyle().Foreground(colorDim)
	statusModelStyle = lipgloss.NewStyle().Foreground(colorSteel)
)

// statusRows is how many rows go under the prompt above the keys: the model
// line and auto-approve -- fewer, or none, in a terminal too short for them. It depends on the terminal's height ALONE, so the viewport -- sized
// when the terminal is -- and the frame drawn afterwards always agree about how
// many rows are there.
func (m chatModel) statusRows() int {
	switch {
	case m.height >= statusAutoMinHeight:
		return 2
	case m.height >= statusLineMinHeight:
		return 1
	}
	return 0
}

// statusModel is the model prompts go to, by the id a user recognises: this
// client's own choice when it made one, else the daemon's.
func (m chatModel) statusModel() string {
	if chosen := m.chosenModel(); chosen != "" {
		return chosen
	}
	if !m.conn.inForceKnown() {
		return ""
	}
	return firstNonEmptyText(m.conn.resp.ModelSlug, m.conn.resp.Model)
}

// chosenModel is the model picked with /model, by the id the user knows; ""
// when none was, and the daemon's default applies.
func (m chatModel) chosenModel() string {
	if m.preferredTier == "" {
		return ""
	}
	return firstNonEmptyText(m.preferredSlug, m.preferredTier)
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
		if label := localModeLabel(m.conn.connectedBase()); label != "" {
			plain(label)
		}
	}
	if t := m.sessionUsage; t.turns > 0 {
		// How full the context is, when a turn has said (autoapprove.go); the
		// session's tokens otherwise, as before. /usage has both.
		if gauge := m.contextGauge(); gauge != "" {
			parts = append(parts, gauge)
		} else {
			plain(compactTokens(t.prompt+t.completion) + " tokens")
		}
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

// withStatusLine is everything under the prompt, as many rows as the terminal
// has room for (statusRows): the model line, auto-approve (autoapprove.go),
// then the keys. THE BOTTOM RIGHT CORNER (owner, 2026-11-21) is the project's
// folder name, at the right end of the row above the keys -- of the model
// line when that row is not drawn -- and under it, at the right end of the keys,
// who is running the client (whoami.go).
func (m chatModel) withStatusLine(help string) string {
	if w := m.welcomeWidth(); m.welcoming() && m.width > w {
		narrow := m // the first screen: the rows end where its prompt does (welcome.go)
		narrow.width = w
		return narrow.withStatusLine(help)
	}
	keys := m.keysRow(help)
	switch m.statusRows() {
	case 0:
		return keys
	case 1:
		return m.withRightEnd(m.statusLine(), m.repoLabel()) + "\n" + keys
	}
	return m.statusLine() + "\n" + m.withRightEnd(m.autoRow(), m.repoLabel()) + "\n" + keys
}
