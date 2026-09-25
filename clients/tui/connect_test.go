package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

const tuiTestKey = "sk-test-0123456789abcdefSECRET"

// newTestChatModel builds a model the way main does, minus the terminal.
func newTestChatModel(t *testing.T) chatModel {
	t.Helper()
	m := newChatModel("test", t.TempDir(), t.TempDir(), nil)
	m.state = stateIdle
	m.width, m.height, m.ready = 80, 24, true
	return m
}

// typeInto feeds a string to the model one key at a time, the way a person does.
func typeInto(m chatModel, s string) chatModel {
	for _, r := range s {
		next, _ := m.handleConnectKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(chatModel)
	}
	return m
}

// THE SECURITY PROPERTY OF THIS WHOLE COMMAND: a key typed at the /connect prompt
// must not end up anywhere it can be read later. The transcript is rendered on
// screen, scrolled back through, and -- through buildHistory -- sent to the model
// with the next prompt. A key in it would leak in all three directions at once.
func TestTheTypedKeyNeverReachesTheTranscript(t *testing.T) {
	m := newTestChatModel(t)
	started, _ := m.beginConnect()
	m = started.(chatModel)

	if m.state != stateConnect {
		t.Fatalf("state = %v, want stateConnect", m.state)
	}
	if m.input.EchoMode != textinput.EchoPassword {
		t.Error("the input is not masked: the key would be shown as it is typed, and left in the scrollback")
	}

	m = typeInto(m, tuiTestKey)
	if got := m.input.Value(); got != tuiTestKey {
		t.Fatalf("the input did not collect the key (got %d chars)", len(got))
	}

	submitted, cmd := m.handleConnectKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = submitted.(chatModel)
	if cmd == nil {
		t.Error("submitting the key produced no request")
	}

	// Cleared, unmasked, and back to normal input -- an input left in password
	// mode would silently hide the user's next question from them.
	if m.input.Value() != "" {
		t.Error("the key is still sitting in the input after submission")
	}
	if m.input.EchoMode != textinput.EchoNormal {
		t.Error("the input was left in password mode, so the next prompt would be invisible")
	}
	if m.state != stateIdle {
		t.Errorf("state = %v after submitting, want stateIdle", m.state)
	}

	for i, tr := range m.turns {
		if strings.Contains(tr.text, tuiTestKey) {
			t.Fatalf("turn %d carries the key into the transcript: %q", i, tr.text)
		}
	}
}

// Esc must abandon the entry without sending anything, and must restore the
// input -- a cancel that left the box masked would be its own bug.
func TestEscapeCancelsConnectWithoutSending(t *testing.T) {
	m := newTestChatModel(t)
	started, _ := m.beginConnect()
	m = typeInto(started.(chatModel), tuiTestKey)

	cancelled, cmd := m.handleConnectKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = cancelled.(chatModel)
	if cmd != nil {
		t.Error("esc sent a request anyway")
	}
	if m.state != stateIdle || m.input.EchoMode != textinput.EchoNormal || m.input.Value() != "" {
		t.Error("esc did not restore ordinary input")
	}
	for _, tr := range m.turns {
		if strings.Contains(tr.text, tuiTestKey) {
			t.Fatal("the cancelled key was written into the transcript")
		}
	}
}

// Enter on an empty prompt must not send an empty key to the daemon.
func TestConnectWithNoKeyEnteredSendsNothing(t *testing.T) {
	m := newTestChatModel(t)
	started, _ := m.beginConnect()
	m = started.(chatModel)

	done, cmd := m.handleConnectKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("an empty entry was sent to the daemon")
	}
	if done.(chatModel).state != stateIdle {
		t.Error("an empty entry left the client stuck in key entry")
	}
}

// THE FOUR OUTCOMES MUST NOT READ ALIKE. "Stored and proven" and "stored but
// unproven" are different facts, and blurring them wastes the entire point of
// verifying; "refused" must say plainly that nothing changed.
func TestConnectResultWordsEachOutcomeDistinctly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resp    protocol.ConnectResponse
		want    []string
		notWant []string
	}{
		{
			name: "accepted",
			resp: protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectAccepted,
				Detail: "the provider accepted it", MaskedKey: "...CRET (30 characters)", InUse: true},
			want:    []string{"Connected", "in use now", "no restart"},
			notWant: []string{"NOT verified"},
		},
		{
			name: "unverified",
			resp: protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectUnverified,
				Detail: "nothing authenticated it", MaskedKey: "...CRET (30 characters)", InUse: true},
			want:    []string{"NOT verified", "in use now"},
			notWant: []string{"Connected."},
		},
		{
			name: "rejected",
			resp: protocol.ConnectResponse{Outcome: protocol.ConnectRejected,
				Detail: "the provider refused it (HTTP 401)"},
			want:    []string{"refused", "Nothing was stored", "unchanged"},
			notWant: []string{"in use now"},
		},
		{
			name: "removed",
			resp: protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectRemoved, Detail: "gone"},
			want: []string{"removed"},
		},
		{
			name: "shown with nothing stored",
			resp: protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectShown, MaskedKey: "(none)"},
			want: []string{"No key is stored"},
		},
		{
			name: "an outcome this client does not know",
			resp: protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectOutcome("something-new")},
			want: []string{"unrecognised outcome"},
		},
		{
			name: "an error from the daemon",
			resp: protocol.ConnectResponse{Error: "credential file unreadable"},
			want: []string{"connect failed", "credential file unreadable"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatConnectResult(connectResultMsg{resp: tc.resp})
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("result does not say %q:\n%s", w, got)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("result wrongly says %q:\n%s", w, got)
				}
			}
		})
	}
}

// The environment silently beating the stored key is the one thing a user cannot
// work out for themselves, so a success that is not actually in force must say so.
func TestConnectResultSaysWhenTheEnvironmentOverrides(t *testing.T) {
	got := formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, Detail: "accepted",
		MaskedKey: "...CRET", EnvOverride: true,
	}})
	for _, want := range []string{"MOCHIII_API_KEY", "precedence", "NOT what it is sending"} {
		if !strings.Contains(got, want) {
			t.Errorf("the override note does not mention %q:\n%s", want, got)
		}
	}
}

// A transport failure is reported as one, not swallowed into a cheerful result.
func TestConnectResultReportsATransportFailure(t *testing.T) {
	got := formatConnectResult(connectResultMsg{err: errors.New("daemon not found")})
	if !strings.Contains(got, "connect failed") || !strings.Contains(got, "daemon not found") {
		t.Errorf("a transport failure was not reported plainly: %s", got)
	}
}

// fakeDaemonForConnect answers a handshake and one ConnectRequest, and records
// what it was sent -- so the wire path can be checked, including that the key
// actually arrives.
func fakeDaemonForConnect(t *testing.T, resp protocol.ConnectResponse) (lockPath string, got *protocol.ConnectRequest, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath = filepath.Join(dir, "daemon.lock")
	got = &protocol.ConnectRequest{}

	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}
	data, err := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
		var hs protocol.HandshakeRequest
		if err := dec.Decode(&hs); err != nil {
			return
		}
		_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})
		if err := dec.Decode(got); err != nil {
			return
		}
		_ = enc.Encode(resp)
	}()

	return lockPath, got, func() { _ = ln.Close(); <-done }
}

// The wire path, end to end against a daemon that answers: the key must arrive,
// the discriminator must be set (without it the daemon reads the request as a
// prompt), and the answer must come back as the outcome the daemon chose.
func TestSendConnectCarriesTheKeyAndReturnsTheOutcome(t *testing.T) {
	lockPath, got, cleanup := fakeDaemonForConnect(t, protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, Detail: "accepted", MaskedKey: "...CRET",
	})
	defer cleanup()
	original := lockPathFunc
	lockPathFunc = func() string { return lockPath }
	defer func() { lockPathFunc = original }()

	resp, err := sendConnect("test", protocol.ConnectRequest{
		ProtocolVersion: protocol.ProtocolVersion, Connect: true, APIKey: tuiTestKey,
	})
	if err != nil {
		t.Fatalf("sendConnect: %v", err)
	}
	if !got.Connect {
		t.Error("the discriminator was not set, so the daemon would read this as a prompt")
	}
	if got.APIKey != tuiTestKey {
		t.Errorf("the daemon received %q, not the key that was typed", maskForTest(got.APIKey))
	}
	if resp.Outcome != protocol.ConnectAccepted || resp.MaskedKey != "...CRET" {
		t.Errorf("the answer did not come back intact: %+v", resp)
	}
}

// And with no daemon to talk to, the failure is reported rather than swallowed.
func TestSendConnectReportsNoDaemon(t *testing.T) {
	original := lockPathFunc
	lockPathFunc = func() string { return filepath.Join(t.TempDir(), "absent.lock") }
	defer func() { lockPathFunc = original }()

	if _, err := sendConnect("test", protocol.ConnectRequest{Connect: true, APIKey: tuiTestKey}); err == nil {
		t.Error("sendConnect reported success with no daemon listening")
	}
}

// The answer has to reach the transcript, or the user watches /connect do nothing.
func TestHandleConnectResultShowsTheAnswer(t *testing.T) {
	m := newTestChatModel(t)
	out, _ := m.handleConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, Detail: "the provider accepted it",
		MaskedKey: "...CRET (30 characters)", InUse: true,
	}})
	got := out.(chatModel)
	if len(got.turns) == 0 {
		t.Fatal("the daemon's answer never reached the transcript")
	}
	last := got.turns[len(got.turns)-1].text
	if !strings.Contains(last, "Connected") || !strings.Contains(last, "...CRET") {
		t.Errorf("the transcript does not carry the outcome: %q", last)
	}
	if strings.Contains(last, tuiTestKey) {
		t.Error("the rendered answer contains a raw key")
	}
}

// maskForTest keeps a failure message from printing a key in full.
func maskForTest(s string) string {
	if len(s) < 8 {
		return "(short)"
	}
	return "..." + s[len(s)-4:]
}

// THE SUBCOMMANDS MUST REACH THE DAEMON, and reach it as a ConnectRequest.
//
// Without the discriminator the daemon reads the request as a prompt and answers
// "prompt is empty" -- the failure the typed-request dispatch exists to prevent,
// and one that only shows up against a real daemon.
func TestConnectShowAndForgetReachTheDaemon(t *testing.T) {
	for _, tc := range []struct {
		args       string
		wantShow   bool
		wantForget bool
	}{
		{"show", true, false},
		{"forget", false, true},
		{"  show  ", true, false}, // surrounding space is not a different command
	} {
		t.Run(tc.args, func(t *testing.T) {
			lockPath, got, cleanup := fakeDaemonForConnect(t, protocol.ConnectResponse{
				Ok: true, Outcome: protocol.ConnectShown, MaskedKey: "...CRET", Detail: "stored",
			})
			defer cleanup()
			original := lockPathFunc
			lockPathFunc = func() string { return lockPath }
			defer func() { lockPathFunc = original }()

			m := newTestChatModel(t)
			out, cmd := m.handleLocalSlash("connect", tc.args)
			if cmd == nil {
				t.Fatalf("/connect %q produced no request", tc.args)
			}
			if out.(chatModel).state == stateConnect {
				t.Errorf("/connect %q opened the masked key prompt instead of running", tc.args)
			}
			cmd() // performs the round trip

			if got.Show != tc.wantShow || got.Forget != tc.wantForget {
				t.Errorf("daemon received Show=%v Forget=%v, want Show=%v Forget=%v",
					got.Show, got.Forget, tc.wantShow, tc.wantForget)
			}
			if !got.Connect {
				t.Error("the discriminator was not set, so the daemon would read this as a prompt")
			}
			if got.APIKey != "" {
				t.Error("a subcommand sent a key field it has no business carrying")
			}
		})
	}
}

// Anything that is not one of the two literals is still refused, because the
// thing being guarded against is a KEY typed where the transcript can keep it.
func TestConnectStillRefusesAnythingThatCouldBeAKey(t *testing.T) {
	for _, arg := range []string{tuiTestKey, "sk-anything", "SHOW", "show me", "--forget"} {
		m := newTestChatModel(t)
		out, cmd := m.handleLocalSlash("connect", arg)
		if cmd != nil {
			t.Errorf("/connect %q was sent to the daemon", arg)
		}
		got := out.(chatModel)
		if got.state == stateConnect {
			t.Errorf("/connect %q opened the key prompt", arg)
		}
		last := got.turns[len(got.turns)-1].text
		if !strings.Contains(last, "no key as an argument") {
			t.Errorf("/connect %q was not refused with a reason: %q", arg, last)
		}
		if strings.Contains(last, tuiTestKey) {
			t.Errorf("the refusal echoed the key back into the transcript: %q", last)
		}
	}
}
