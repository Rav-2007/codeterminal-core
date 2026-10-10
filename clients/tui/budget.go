package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /budget: WHAT THIS SESSION AND TODAY HAVE SPENT, AGAINST A LIMIT.
//
// The daemon keeps both counts (daemon/spend.go) and stops at a limit: a turn
// in flight ends with a summary, and the next question is refused until the
// limit is raised. This file is the three things a client owes that: it names
// its session, so there is one to count; it shows the daemon's one-time
// warning under the turn that crossed the mark; and it gives the person the
// way on -- "/budget more" -- which is the only thing that raises a limit
// without editing the daemon's configuration.
//
// /usage (usage.go) stays what it was: this client's own sum of the bills it
// was sent. /budget asks the daemon, whose count also holds turns that were
// stopped, other clients on the same session, and -- for today -- every other
// project.

// clientSession names this run of the client on every request it sends
// (protocol.PromptRequest.Session). Random, so it identifies nothing but
// itself; a new one each start, so a session is one run.
var clientSession = newClientSession()

func newClientSession() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// No session is a valid thing to send: the daemon then holds this
		// client to the day's limit only.
		return ""
	}
	return hex.EncodeToString(b[:])
}

// errDaemonPredatesBudget is what /budget says to a daemon started before the
// limits existed, which would read the request as an empty question.
var errDaemonPredatesBudget = errors.New("the running daemon is older than /budget -- " +
	"restart it: ./run-tui.sh --stop && ./run-tui.sh")

// sendBudget asks the daemon where the limits stand (action status) or to
// allow more (action more). It calls no model.
func sendBudget(clientName, action string) (*protocol.SpendStatus, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	if !hasFeature(sess.handshake, protocol.FeatureSpendLimits) {
		return nil, errDaemonPredatesBudget
	}
	if err := sess.enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Session:         clientSession,
		BudgetAction:    action,
	}); err != nil {
		return nil, fmt.Errorf("sending the request: %w", err)
	}
	var resp protocol.TokenResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.Spend == nil {
		return nil, errors.New("the daemon answered without the limits")
	}
	return resp.Spend, nil
}

const budgetUsage = "usage: /budget shows the limits · /budget more allows another allotment of a limit that is near or reached"

func (m chatModel) handleBudgetCommand(args string) (tea.Model, tea.Cmd) {
	var note string
	switch strings.TrimSpace(args) {
	case "", "show", "status":
		note = budgetNote(sendBudget(m.clientName, protocol.BudgetActionStatus))
	case "more":
		note = budgetNote(sendBudget(m.clientName, protocol.BudgetActionMore))
	default:
		note = budgetUsage
	}
	m.appendTurn(turn{role: roleSystem, text: note})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

func budgetNote(st *protocol.SpendStatus, err error) string {
	if err != nil {
		return "budget: " + err.Error()
	}
	return budgetReport(st)
}

// budgetReport renders the two limits.
func budgetReport(st *protocol.SpendStatus) string {
	var b strings.Builder
	b.WriteString("budget (counted by the daemon from the provider's bill)")
	if st.Session == (protocol.SpendMeter{}) {
		b.WriteString("\n  this session  not counted: this client sent no session name")
	} else {
		writeBudgetRow(&b, "this session", st.Session)
	}
	writeBudgetRow(&b, "today", st.Day)
	b.WriteString("\n  today counts every project on this machine")
	switch {
	case st.Notice != "":
		b.WriteString("\n  " + st.Notice)
	case st.Reached:
		b.WriteString("\n  a limit is reached: questions are refused until you type /budget more")
	default:
		b.WriteString("\n  at a limit Mochiii stops and asks; /budget more allows another allotment once one is near")
	}
	return b.String()
}

func writeBudgetRow(b *strings.Builder, label string, m protocol.SpendMeter) {
	fmt.Fprintf(b, "\n  %-13s %s of %s tokens", label, compactTokens(m.Tokens), compactTokens(m.LimitTokens))
	if m.LimitTokens > 0 {
		fmt.Fprintf(b, " (%s)", percent(100*float64(m.Tokens)/float64(m.LimitTokens)))
	}
	if m.LimitUSD > 0 {
		fmt.Fprintf(b, " · $%.2f of $%.2f", m.USD, m.LimitUSD)
	}
}

// recordSpend keeps the daemon's warning from a turn's last message, to be
// shown under that turn once it has finished (handleStreamDone).
func (m *chatModel) recordSpend(st *protocol.SpendStatus) {
	if st != nil && st.Notice != "" {
		m.spendNotice = st.Notice
	}
}

// showSpendNotice puts a pending warning into the transcript, once.
func (m *chatModel) showSpendNotice() {
	if m.spendNotice == "" {
		return
	}
	// appendTurn is where a daemon's text is made safe to draw, for every turn.
	m.appendTurn(turn{role: roleSystem, text: "budget: " + m.spendNotice})
	m.spendNotice = ""
}
