package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

func TestTheBudgetReportShowsBothLimits(t *testing.T) {
	got := budgetReport(&protocol.SpendStatus{
		Session: protocol.SpendMeter{Tokens: 1_200_000, LimitTokens: 3_000_000, USD: 0.21, LimitUSD: 1},
		Day:     protocol.SpendMeter{Tokens: 4_800_000, LimitTokens: 15_000_000, LimitUSD: 5},
	})
	for _, want := range []string{
		"this session  1.20M of 3.00M tokens (40%) · $0.21 of $1.00",
		"today         4.80M of 15.00M tokens (32%) · $0.00 of $5.00",
		"every project on this machine",
		"/budget more",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not show %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 100 {
			t.Errorf("a line of the report is %d columns wide:\n%s", len([]rune(line)), line)
		}
	}
}

func TestTheBudgetReportSaysWhenALimitIsReachedOrWasRaised(t *testing.T) {
	reached := budgetReport(&protocol.SpendStatus{
		Session: protocol.SpendMeter{Tokens: 3_000_100, LimitTokens: 3_000_000},
		Day:     protocol.SpendMeter{Tokens: 3_000_100, LimitTokens: 15_000_000},
		Reached: true,
	})
	if !strings.Contains(reached, "a limit is reached") || !strings.Contains(reached, "/budget more") {
		t.Errorf("a reached limit is not said, or not how to go on:\n%s", reached)
	}
	raised := budgetReport(&protocol.SpendStatus{
		Session: protocol.SpendMeter{Tokens: 3_000_100, LimitTokens: 6_000_000},
		Day:     protocol.SpendMeter{Tokens: 3_000_100, LimitTokens: 15_000_000},
		Notice:  "Allowed another 3,000,000 tokens and $1.00 for this session.",
	})
	if !strings.Contains(raised, "Allowed another 3,000,000 tokens") || strings.Contains(raised, "a limit is reached") {
		t.Errorf("the daemon's answer to /budget more is not shown as it was given:\n%s", raised)
	}
}

// A client with no session name is told so, not shown a row of zeros that
// reads as "nothing spent".
func TestTheBudgetReportDoesNotInventASession(t *testing.T) {
	got := budgetReport(&protocol.SpendStatus{Day: protocol.SpendMeter{Tokens: 10, LimitTokens: 100}})
	if !strings.Contains(got, "this session  not counted") {
		t.Errorf("a status with no session meter was drawn as one:\n%s", got)
	}
}

func TestEveryRunOfTheClientIsItsOwnSession(t *testing.T) {
	a, b := newClientSession(), newClientSession()
	if a == "" || a == b {
		t.Errorf("two sessions are %q and %q; want two different, non-empty names", a, b)
	}
	if len(a) > protocol.MaxSessionIDBytes {
		t.Errorf("a session name is %d bytes, over the %d the daemon reads", len(a), protocol.MaxSessionIDBytes)
	}
}

// The daemon's warning is shown once, under the turn that carried it, and in
// the transcript's own words -- a daemon's text never draws on the terminal.
func TestASpendWarningIsShownOnceAndSanitized(t *testing.T) {
	m := &chatModel{}
	m.recordSpend(&protocol.SpendStatus{Notice: "This session has used 2,400,000 of 3,000,000 tokens (80%).\x1b[2J"})
	m.recordSpend(&protocol.SpendStatus{}) // a later message with no notice must not erase it
	m.showSpendNotice()
	if len(m.turns) != 1 || m.turns[0].role != roleSystem ||
		!strings.Contains(m.turns[0].text, "budget: This session has used 2,400,000 of 3,000,000 tokens (80%).") {
		t.Fatalf("the warning is not in the transcript: %+v", m.turns)
	}
	if strings.Contains(m.turns[0].text, "\x1b") {
		t.Errorf("an escape sequence from the daemon reached the transcript: %q", m.turns[0].text)
	}
	m.showSpendNotice()
	if len(m.turns) != 1 {
		t.Errorf("the warning was shown %d times", len(m.turns))
	}
}

func TestBudgetIsACommandAndItsUsageIsShownForAnythingElse(t *testing.T) {
	if lookupSlash("budget") == nil {
		t.Fatal("/budget is not in the catalog, so it is not in /help or the popup")
	}
	if !strings.Contains(budgetUsage, "/budget more") {
		t.Errorf("the usage line does not name /budget more: %s", budgetUsage)
	}
}

// fakeDaemonForBudget answers one request after a handshake naming features,
// and hands back the request it was sent.
func fakeDaemonForBudget(t *testing.T, features []string, reply protocol.TokenResponse) <-chan protocol.PromptRequest {
	t.Helper()
	addr := testAddress(t)
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan protocol.PromptRequest, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
		var hs protocol.HandshakeRequest
		if dec.Decode(&hs) != nil {
			return
		}
		_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true, Features: features})
		var req protocol.PromptRequest
		if dec.Decode(&req) != nil {
			return
		}
		got <- req
		_ = enc.Encode(reply)
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return got
}

// /budget more asks the daemon -- with this client's session, the action, and
// NO question, so nothing in it can reach a model -- and shows what it said.
func TestBudgetMoreAsksTheDaemonAndSendsNoQuestion(t *testing.T) {
	want := protocol.SpendStatus{
		Session: protocol.SpendMeter{Tokens: 2_900_000, LimitTokens: 6_000_000},
		Day:     protocol.SpendMeter{Tokens: 2_900_000, LimitTokens: 15_000_000},
		Notice:  "Allowed another 3,000,000 tokens and $1.00 for this session.",
	}
	got := fakeDaemonForBudget(t, []string{protocol.FeatureSpendLimits}, protocol.TokenResponse{Done: true, Spend: &want})
	st, err := sendBudget("test", protocol.BudgetActionMore)
	if err != nil {
		t.Fatal(err)
	}
	if *st != want {
		t.Errorf("the status came back as %+v", st)
	}
	req := <-got
	if req.BudgetAction != protocol.BudgetActionMore || req.Session != clientSession || req.Session == "" || req.Prompt != "" {
		t.Errorf("the request was %+v; want the action, this client's session and no prompt", req)
	}
}

// A daemon started before the limits existed would read the request as an
// empty question. It is named for what it is, and nothing is sent to it.
func TestAnOlderDaemonIsToldApartNotAsked(t *testing.T) {
	got := fakeDaemonForBudget(t, []string{protocol.FeatureSavedChats}, protocol.TokenResponse{Done: true, Error: "prompt is empty"})
	_, err := sendBudget("test", protocol.BudgetActionStatus)
	if err != errDaemonPredatesBudget {
		t.Fatalf("an older daemon answered %v, want the restart message", err)
	}
	select {
	case req := <-got:
		t.Errorf("a budget request was sent to a daemon that cannot read one: %+v", req)
	default:
	}
}

// Every question names the session, or the daemon has no session to count.
func TestEveryQuestionCarriesTheSession(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 2)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	ch := make(chan tea.Msg, 8)
	go streamPrompt(context.Background(), "test-client", "", "hello there", "", "", "", nil, nil, ch)
	req := <-requests
	if req.Session == "" || req.Session != clientSession {
		t.Errorf("the question went out with session %q, want this client's %q", req.Session, clientSession)
	}
}

// The limits on a turn's last message reach the update loop, on a finished
// turn and on a refused one alike.
func TestTheSpendStatusRidesWithTheTurnsLastMessage(t *testing.T) {
	notice := "This session has used 2,400,000 of 3,000,000 tokens (80%)."
	for name, last := range map[string]protocol.TokenResponse{
		"done":    {Done: true, Spend: &protocol.SpendStatus{Notice: notice}},
		"refused": {Done: true, Error: "Mochiii has reached this session's spending limit", Spend: &protocol.SpendStatus{Notice: notice, Reached: true}},
	} {
		fakeDaemonReplying(t, last)
		found := false
		for _, msg := range collect(t) {
			if u, ok := msg.(usageMsg); ok && u.spend != nil && u.spend.Notice == notice {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: the spend status did not reach the update loop", name)
		}
	}
}

// THE COMMAND ITSELF, through the model: "/budget more" asks for more and not
// merely for the numbers, a bare "/budget" only looks, and anything else is
// answered with the usage line and sends nothing.
func TestTheBudgetCommandSendsWhatWasTyped(t *testing.T) {
	for typed, want := range map[string]string{
		"":     protocol.BudgetActionStatus,
		"more": protocol.BudgetActionMore,
	} {
		got := fakeDaemonForBudget(t, []string{protocol.FeatureSpendLimits}, protocol.TokenResponse{Done: true, Spend: &protocol.SpendStatus{
			Session: protocol.SpendMeter{Tokens: 10, LimitTokens: 100},
			Day:     protocol.SpendMeter{Tokens: 10, LimitTokens: 1000},
			Notice:  "the daemon's own words for " + want,
		}})
		updated, _ := newTestModel().handleBudgetCommand(typed)
		m := updated.(chatModel)
		if req := <-got; req.BudgetAction != want {
			t.Errorf("/budget %s sent the action %q, want %q", typed, req.BudgetAction, want)
		}
		last := m.turns[len(m.turns)-1]
		if last.role != roleSystem || !strings.Contains(last.text, "the daemon's own words for "+want) ||
			!strings.Contains(last.text, "10 of 100 tokens") {
			t.Errorf("/budget %s showed:\n%s", typed, last.text)
		}
	}

	// Not a budget word. The lock file named here does not exist, so a request
	// would come back at once as "daemon not found" -- and the usage line shows
	// that none was made.
	defer setLockPathForTest(t, filepath.Join(t.TempDir(), "no-daemon.lock"))()
	updated, _ := newTestModel().handleBudgetCommand("double it")
	m := updated.(chatModel)
	if last := m.turns[len(m.turns)-1]; last.text != budgetUsage {
		t.Errorf("an unknown word was answered with %q, want the usage line", last.text)
	}
}
