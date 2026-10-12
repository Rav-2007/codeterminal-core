package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// spendFixture is the daemon's count for a session that has done some work.
func spendFixture() *protocol.SpendStatus {
	return &protocol.SpendStatus{
		Session: protocol.SpendMeter{Tokens: 18400, LimitTokens: 2000000, USD: 0.01, LimitUSD: 1},
		Day:     protocol.SpendMeter{Tokens: 96000, LimitTokens: 5000000, USD: 0.06, LimitUSD: 2},
	}
}

// accountScreen is /account with the daemon's answers already in, in a window
// tall enough to draw all of it.
func accountScreen(t *testing.T, m chatModel, msg accountMsg) chatModel {
	t.Helper()
	m, cmd := enterSlash(m, "/account")
	if m.state != stateSetup || m.setup.step != setupAccount || !m.setup.loading || cmd == nil {
		t.Fatalf("/account: state %v, step %v, loading %v; want the account screen being fetched", m.state, m.setup.step, m.setup.loading)
	}
	updated, _ := m.Update(msg)
	updated, _ = updated.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return updated.(chatModel)
}

// THE REPORT THIS ANSWERS (OWNER, 2026-10-12): "where is the slash command to
// check the account details". /account shows the provider, the key, the model
// and the spend, in the box the owner picked.
//
// Neuter check: take "account" out of the `case "budget", "account":` in
// handleLocalSlash -- the command answers "unknown local command".
func TestAccountShowsWhatIsConnectedAndWhatWasSpent(t *testing.T) {
	m := accountScreen(t, newTestModel(), accountMsg{conn: connInfoMsg{resp: nvidiaInForce(t)}, spend: spendFixture()})
	if m.setup.loading {
		t.Fatal("the answer arrived and the screen still says it is asking")
	}
	screen := plainScreen(m)
	for _, want := range []string{
		accountTitle, "Provider NVIDIA ● connected", "https://integrate.api.nvidia.com/v1",
		"API key ...3f2a (73 characters)", "stored on this machine", "accepted by the provider when it was saved",
		"Model z-ai/glm-5.3 (remembered)", "52 models to choose from",
		"Session 18.4K of 2.00M tokens (<1%) · $0.01 of $1.00",
		"Today 96.0K of 5.00M tokens (2%) · $0.06 of $2.00", "every project on this machine",
		"/provider changes the provider or the key", "/model changes the model", accountHelp,
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the account screen does not show %q:\n%s", want, screen)
		}
	}
	// The line under the prompt reads the same answer.
	if !m.conn.inForceKnown() || m.conn.providerName() != "NVIDIA" {
		t.Errorf("the answer was shown and not kept (known=%v provider=%q)", m.conn.inForceKnown(), m.conn.providerName())
	}

	// Any of the keys a person leaves a screen with leaves it, with nothing said.
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
		before := len(m.turns)
		updated, _ := m.Update(key)
		left := updated.(chatModel)
		if left.state != stateIdle || left.setupActive() {
			t.Errorf("%q: state %v, step %v; want the prompt", key.String(), left.state, left.setup.step)
		}
		if len(left.turns) != before {
			t.Errorf("%q: closing the account screen wrote to the transcript", key.String())
		}
	}
}

// BOTH REQUESTS GO TO THE DAEMON AND NEITHER CARRIES A KEY. /account asks
// what is in force and where the limits stand, and nothing else -- the owner
// turned down asking the provider for a balance.
func TestAccountAsksTheDaemonTwoThingsAndSendsNoKey(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureSpendLimits}, func(raw json.RawMessage) any {
		if strings.Contains(string(raw), `"connect"`) {
			return nvidiaInForce(t)
		}
		return protocol.TokenResponse{Spend: spendFixture()}
	})
	_, cmd := enterSlash(newTestModel(), "/account")
	msg, ok := findMsg[accountMsg](cmd)
	if !ok {
		t.Fatal("/account asked the daemon nothing")
	}
	if msg.conn.err != nil || msg.spendErr != nil || msg.spend == nil || msg.conn.resp.KeySource != protocol.KeySourceStored {
		t.Fatalf("the answers did not come back whole: conn err %v, spend err %v", msg.conn.err, msg.spendErr)
	}
	reqs := daemon.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests were sent, want two", len(reqs))
	}
	var shows int
	for _, raw := range reqs {
		if strings.Contains(string(raw), "api_key") {
			t.Errorf("a request carries a key field: %s", raw)
		}
		var req protocol.ConnectRequest
		if json.Unmarshal(raw, &req) == nil && req.Connect {
			shows++
			if !req.Show || req.Forget || req.ChooseModel != "" {
				t.Errorf("the connect request is not a plain show: %s", raw)
			}
		}
	}
	if shows != 1 {
		t.Errorf("%d show requests, want one", shows)
	}
}

// THE KEY IS SAID BY WHERE IT COMES FROM, because that decides what a user can
// do about it -- and each case must not be mistakable for another.
func TestAccountSaysWhereTheKeyComesFrom(t *testing.T) {
	base := mustProvider(t, "nvidia").APIBase
	in := func(change func(*protocol.ConnectResponse)) protocol.ConnectResponse {
		r := nvidiaInForce(t)
		change(&r)
		return r
	}
	cases := map[string]struct {
		resp    protocol.ConnectResponse
		want    []string
		notWant []string
	}{
		"environment, with another key stored": {
			resp: in(func(r *protocol.ConnectResponse) {
				r.KeySource, r.InUse, r.EnvOverride = protocol.KeySourceEnvironment, false, true
				r.ActiveMaskedKey, r.MaskedKey, r.ModelSaved = "...env1 (40 characters)", "...3f2a (73 characters)", false
			}),
			want:    []string{"...env1", "from MOCHIII_API_KEY in the daemon's environment", "a stored key (...3f2a (73 characters)) is not the one in use"},
			notWant: []string{"stored on this machine", "(remembered)"},
		},
		// A daemon the editor started, with the key from the editor's own store:
		// that key is in no file here, and saying "stored on this machine" named
		// a file that did not hold it (the merge with the launch credential,
		// 2026-10-12).
		"handed over by the launcher, with another key stored": {
			resp: in(func(r *protocol.ConnectResponse) {
				r.KeySource, r.InUse, r.EnvOverride = protocol.KeySourceLauncher, false, true
				r.ActiveMaskedKey, r.MaskedKey, r.ModelSaved = "...lnch (51 characters)", "...3f2a (73 characters)", false
			}),
			want:    []string{"...lnch", "handed over by the program that started the daemon", "a stored key (...3f2a (73 characters)) is not the one in use"},
			notWant: []string{"stored on this machine", "MOCHIII_API_KEY", "(remembered)"},
		},
		"proxy": {
			resp: in(func(r *protocol.ConnectResponse) {
				r.KeySource, r.InUse, r.ActiveMaskedKey, r.MaskedKey = protocol.KeySourceProxy, false, "", "(none)"
			}),
			want:    []string{"the Mochiii proxy", "none on this machine", "the proxy holds the provider's key"},
			notWant: []string{"stored on this machine"},
		},
		"no key at all": {
			resp: in(func(r *protocol.ConnectResponse) {
				r.KeySource, r.InUse, r.ActiveMaskedKey, r.MaskedKey = protocol.KeySourceNone, false, "", "(none)"
			}),
			want:    []string{"Provider none connected", "/provider connects one", "the next question will ask for one"},
			notWant: []string{"● connected", base},
		},
		"a model server on this machine": {
			resp: in(func(r *protocol.ConnectResponse) {
				r.KeySource, r.InUse, r.ActiveMaskedKey, r.MaskedKey = protocol.KeySourceNone, false, "", "(none)"
				r.ActiveAPIBase = "http://127.0.0.1:11434/v1"
			}),
			want:    []string{"Provider this machine ● connected", "http://127.0.0.1:11434/v1", "none needed"},
			notWant: []string{"the next question will ask"},
		},
		"a daemon older than this client": {
			resp: protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectShown, InUse: true, APIBase: base,
				MaskedKey: "...3f2a (73 characters)", Detail: "stored, and not verified when it was saved"},
			want:    []string{"Provider NVIDIA ● connected", "...3f2a", "not verified when it was saved", "older than this client"},
			notWant: []string{"Model "},
		},
	}
	for name, tc := range cases {
		screen := plainScreen(accountScreen(t, newTestModel(), accountMsg{conn: connInfoMsg{resp: tc.resp}, spend: spendFixture()}))
		for _, want := range tc.want {
			if !strings.Contains(screen, want) {
				t.Errorf("%s: the screen does not say %q:\n%s", name, want, screen)
			}
		}
		for _, notWant := range tc.notWant {
			if strings.Contains(screen, notWant) {
				t.Errorf("%s: the screen says %q:\n%s", name, notWant, screen)
			}
		}
	}
}

// What cannot be read is said to be unreadable, never drawn as zero: a daemon
// that cannot be asked, and a daemon with no limits to report.
func TestAccountDoesNotInventWhatItCouldNotRead(t *testing.T) {
	down := plainScreen(accountScreen(t, newTestModel(), accountMsg{
		conn: connInfoMsg{err: errors.New("daemon not found")}, spendErr: errors.New("daemon not found")}))
	if !strings.Contains(down, "Daemon could not be asked") || !strings.Contains(down, "daemon not found") {
		t.Errorf("an unreachable daemon is not reported:\n%s", down)
	}
	for _, invented := range []string{"● connected", "$0.00", "0 of"} {
		if strings.Contains(down, invented) {
			t.Errorf("an unreachable daemon is drawn with %q:\n%s", invented, down)
		}
	}

	// No limits from the daemon: this client's own count, named as such.
	m := newTestModel()
	m.sessionUsage = usageTotals{turns: 2, prompt: 4000, completion: 500, cost: 0.0123}
	own := plainScreen(accountScreen(t, m, accountMsg{conn: connInfoMsg{resp: nvidiaInForce(t)}, spendErr: errDaemonPredatesBudget}))
	if !strings.Contains(own, "Session 4.5K tokens · $0.0123") || !strings.Contains(own, "this client's own count") {
		t.Errorf("the client's own count is not shown in place of the daemon's:\n%s", own)
	}
	if strings.Contains(own, "Today") {
		t.Errorf("a day's total nobody counted is shown:\n%s", own)
	}

	// A model this client chose and the daemon did not take holds for the window.
	chosen := newTestModel()
	chosen.preferredTier, chosen.preferredSlug = "kimi", "moonshotai/kimi-k3"
	held := plainScreen(accountScreen(t, chosen, accountMsg{conn: connInfoMsg{resp: nvidiaInForce(t)}, spend: spendFixture()}))
	if !strings.Contains(held, "Model moonshotai/kimi-k3 (this session only)") {
		t.Errorf("a model only this client is sending is not marked as such:\n%s", held)
	}
}

// The answer is kept even when the screen was closed before it arrived, and it
// does not bring the screen back.
func TestALateAccountAnswerIsKeptAndDoesNotReopenTheScreen(t *testing.T) {
	m, _ := enterSlash(newTestModel(), "/account")
	m = press(m, tea.KeyEsc)
	updated, _ := m.Update(accountMsg{conn: connInfoMsg{resp: nvidiaInForce(t)}, spend: spendFixture()})
	m = updated.(chatModel)
	if m.setupActive() || m.state != stateIdle {
		t.Errorf("a late answer reopened the account screen (state %v, step %v)", m.state, m.setup.step)
	}
	if m.conn.providerName() != "NVIDIA" {
		t.Error("a late answer was thrown away")
	}
}

// Nothing a provider or a daemon says is drawn as it came: a model name with
// an escape sequence in it must not reach the terminal.
func TestAccountSanitizesWhatItWasTold(t *testing.T) {
	hostile := nvidiaInForce(t)
	hostile.Model = "evil\x1b[2Jmodel"
	hostile.ActiveMaskedKey = "...\x1b]0;title\x07abcd"
	view := accountScreen(t, newTestModel(), accountMsg{conn: connInfoMsg{resp: hostile}, spend: spendFixture()}).View()
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]0;") {
		t.Error("an escape sequence from the daemon's answer reached the screen")
	}
}

// EVERY SIZE. The account screen is the tallest thing in the box, so it is the
// one most likely to be taller than the window; and a size nobody should
// report must not crash the client.
func TestTheAccountScreenFitsTheTerminal(t *testing.T) {
	msg := accountMsg{conn: connInfoMsg{resp: nvidiaInForce(t)}, spend: spendFixture()}
	for _, size := range []struct{ w, h int }{{120, 50}, {100, 40}, {80, 24}, {60, 16}, {44, 12}} {
		m, _ := enterSlash(newTestModel(), "/account")
		updated, _ := m.Update(msg)
		updated, _ = updated.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		lines := strings.Split(updated.(chatModel).View(), "\n")
		if len(lines) > size.h {
			t.Errorf("at %dx%d the account screen is %d lines tall", size.w, size.h, len(lines))
		}
		for _, line := range lines {
			if w := lipgloss.Width(line); w > size.w {
				t.Errorf("at %dx%d a line is %d columns wide: %q", size.w, size.h, w, ansi.Strip(line))
				break
			}
		}
		// Whatever was dropped to fit, the three facts were not.
		screen := plainScreen(updated.(chatModel))
		for _, want := range []string{"NVIDIA", "...3f2a", "z-ai/glm-5.3"} {
			if !strings.Contains(screen, want) {
				t.Errorf("at %dx%d the account screen lost %q:\n%s", size.w, size.h, want, screen)
			}
		}
	}
	for _, size := range []struct{ w, h int }{{0, 0}, {1, 1}, {10, 3}, {20, 5}} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("at %dx%d the account screen panicked: %v", size.w, size.h, r)
				}
			}()
			m, _ := enterSlash(newTestModel(), "/account")
			updated, _ := m.Update(msg)
			updated, _ = updated.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			_ = updated.(chatModel).View()
		}()
	}
}

// /budget still answers as /budget: the two commands share one case of
// handleLocalSlash and nothing else.
func TestBudgetIsStillBudget(t *testing.T) {
	startRequestRecorder(t, []string{protocol.FeatureSpendLimits}, func(json.RawMessage) any {
		return protocol.TokenResponse{Spend: spendFixture()}
	})
	m, _ := enterSlash(newTestModel(), "/budget")
	if m.setupActive() {
		t.Fatal("/budget opened the account screen")
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "budget (counted by the daemon") {
		t.Errorf("/budget did not print the limits:\n%s", last)
	}
}
