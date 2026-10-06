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

// errTestConnect stands in for a failed round trip to the daemon.
var errTestConnect = errors.New("daemon unreachable")

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

// THE MESSAGE MUST NOT CONTRADICT ITSELF.
//
// The daemon reports InUse, and it is false whenever a key in the daemon's
// ENVIRONMENT beats the one just stored. The renderer used to say "is in use now
// — no restart needed" unconditionally, so under an environment override the
// same message told the user their key was in use and, two lines later, that it
// was "NOT what it is sending". Both sentences cannot be true, and the one that
// was wrong is the one a user reads first and acts on.
//
// This is the same class of defect as claiming a sandbox confines something it
// does not: the state is already on the wire, and the text ignored it.
func TestTheRenderNeverClaimsAKeyIsInUseWhenTheDaemonSaysItIsNot(t *testing.T) {
	accepted := func(inUse, envOverride bool) protocol.ConnectResponse {
		return protocol.ConnectResponse{
			Ok: true, Outcome: protocol.ConnectAccepted,
			Detail:    "the provider accepted it",
			MaskedKey: "...ijkl (24 characters)",
			InUse:     inUse, EnvOverride: envOverride,
		}
	}

	overridden := formatConnectResult(connectResultMsg{resp: accepted(false, true)})
	if strings.Contains(overridden, "is in use now") {
		t.Errorf("claimed the key is in use while the daemon reported in_use=false:\n%s", overridden)
	}
	if !strings.Contains(overridden, "NOT what this daemon is sending") {
		t.Errorf("did not say the stored key is not the one being sent:\n%s", overridden)
	}

	// The ordinary case must still read as plainly as it did before.
	inUse := formatConnectResult(connectResultMsg{resp: accepted(true, false)})
	if !strings.Contains(inUse, "is in use now — no restart needed") {
		t.Errorf("the normal success message lost its point:\n%s", inUse)
	}

	// Unverified carries the same claim and needs the same restraint.
	unverified := formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectUnverified,
		Detail: "the provider did not answer", MaskedKey: "...ijkl (24 characters)",
		InUse: false, EnvOverride: true,
	}})
	if strings.Contains(unverified, "It is in use now") {
		t.Errorf("unverified claimed in-use against in_use=false:\n%s", unverified)
	}
}

// THE KEY IS ASKED FOR WHEN A QUESTION NEEDS IT, NOT WHEN THE CLIENT OPENS.
//
// Someone opening Mochiii for the first time should meet the prompt, not a
// credential form. So nothing asks at startup; the first submitted QUESTION opens
// the masked key prompt, and the question is held rather than spent earning a
// provider's 401. These tests hold that whole contract, including the part that
// matters most to a user: the question they typed is never lost.
func TestAQuestionWithNoKeyOpensTheKeyPromptAndIsHeld(t *testing.T) {
	m := newTestChatModel(t)
	m.needsAPIKey = true

	m.input.SetValue("what does this repo do?")
	out, cmd := m.startTurn()
	got := out.(chatModel)

	if got.state != stateConnect {
		t.Fatalf("a question with no key did not open the key prompt (state %v)", got.state)
	}
	if cmd == nil {
		t.Error("the key prompt opened without focusing the input, so typing would go nowhere")
	}
	if got.pendingPrompt != "what does this repo do?" {
		t.Errorf("the question was not held: %q", got.pendingPrompt)
	}
	// It must NOT have been sent, and must not sit in the transcript as if it had.
	for _, turn := range got.turns {
		if turn.role == roleUser {
			t.Errorf("the question was added as a sent user turn: %q", turn.text)
		}
	}
	if got.input.EchoMode != textinput.EchoPassword {
		t.Error("the key prompt is not masked; the key would be typed in the clear")
	}
	last := got.turns[len(got.turns)-1].text
	if !strings.Contains(last, "provider API key") {
		t.Errorf("nothing explained why the prompt appeared: %q", last)
	}
}

// Slash commands must still work with no key -- /connect above all, or the client
// would be unable to accept the very thing it is asking for.
func TestSlashCommandsStillWorkWithNoKey(t *testing.T) {
	for _, cmd := range []string{"/connect", "/help"} {
		m := newTestChatModel(t)
		m.needsAPIKey = true
		m.input.SetValue(cmd)
		out, _ := m.startTurn()
		got := out.(chatModel)
		if got.pendingPrompt != "" {
			t.Errorf("%s was held as if it were a question: %q", cmd, got.pendingPrompt)
		}
		if cmd == "/help" && got.state == stateConnect {
			t.Error("/help opened the key prompt instead of printing help")
		}
	}
}

// Every path that does NOT send the question must give it back.
func TestAHeldQuestionIsGivenBackWheneverItIsNotSent(t *testing.T) {
	const question = "explain the sandbox"

	held := func(t *testing.T) chatModel {
		t.Helper()
		m := newTestChatModel(t)
		m.needsAPIKey = true
		m.input.SetValue(question)
		out, _ := m.startTurn()
		return out.(chatModel)
	}

	t.Run("esc cancels", func(t *testing.T) {
		out, _ := held(t).handleConnectKey(tea.KeyMsg{Type: tea.KeyEsc})
		got := out.(chatModel)
		if got.input.Value() != question {
			t.Errorf("cancelling lost the question: input is %q", got.input.Value())
		}
		if got.pendingPrompt != "" {
			t.Error("the question is still held after being returned, so it could be sent twice")
		}
	})

	t.Run("empty entry", func(t *testing.T) {
		out, _ := held(t).handleConnectKey(tea.KeyMsg{Type: tea.KeyEnter})
		got := out.(chatModel)
		if got.input.Value() != question {
			t.Errorf("submitting nothing lost the question: input is %q", got.input.Value())
		}
	})

	t.Run("the provider refuses the key", func(t *testing.T) {
		out, _ := held(t).handleConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
			Ok: false, Outcome: protocol.ConnectRejected, Detail: "the provider refused it (HTTP 401)",
		}})
		got := out.(chatModel)
		if got.input.Value() != question {
			t.Errorf("a refused key lost the question: input is %q", got.input.Value())
		}
		if !got.needsAPIKey {
			t.Error("a refused key cleared needsAPIKey, so the next question would be sent with no credential")
		}
	})

	t.Run("stored but overridden by the environment", func(t *testing.T) {
		out, _ := held(t).handleConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
			Ok: true, Outcome: protocol.ConnectAccepted, MaskedKey: "...ijkl (24 characters)",
			InUse: false, EnvOverride: true,
		}})
		got := out.(chatModel)
		if got.input.Value() != question {
			t.Errorf("a key that did not take effect lost the question: input is %q", got.input.Value())
		}
		if !got.needsAPIKey {
			t.Error("a key the daemon is NOT using cleared needsAPIKey")
		}
	})

	t.Run("the round trip fails", func(t *testing.T) {
		out, _ := held(t).handleConnectResult(connectResultMsg{err: errTestConnect})
		got := out.(chatModel)
		if got.input.Value() != question {
			t.Errorf("a failed round trip lost the question: input is %q", got.input.Value())
		}
	})
}

// And the path that DOES send it: an accepted key the daemon is actually using
// releases the question without the user retyping it.
func TestAnAcceptedKeyReleasesTheHeldQuestion(t *testing.T) {
	m := newTestChatModel(t)
	m.needsAPIKey = true
	m.input.SetValue("why is this slow?")
	out, _ := m.startTurn()
	held := out.(chatModel)

	out, cmd := held.handleConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, MaskedKey: "...ijkl (24 characters)", InUse: true,
	}})
	got := out.(chatModel)

	if got.needsAPIKey {
		t.Error("the daemon is using the key but the client still thinks it needs one")
	}
	if got.pendingPrompt != "" {
		t.Errorf("the question is still held after being released: %q", got.pendingPrompt)
	}
	if cmd == nil {
		t.Fatal("the held question was not sent after the key was accepted")
	}
	// startTurn appends the user's turn as it sends it.
	var sent bool
	for _, turn := range got.turns {
		if turn.role == roleUser && strings.Contains(turn.text, "why is this slow?") {
			sent = true
		}
	}
	if !sent {
		t.Error("the released question never reached the transcript as a sent turn")
	}
}

// The reported gap: "Connected." never said which platform the key was for.
func TestConnectSaysWhichProvider(t *testing.T) {
	got := formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, APIBase: "https://openrouter.ai/api/v1",
		Detail: "the provider accepted it: credit limit 1.00", MaskedKey: "...901c (73 characters)", InUse: true,
	}})
	if !strings.Contains(got, "Connected to OpenRouter (https://openrouter.ai/api/v1).") {
		t.Errorf("connect result does not name the provider:\n%s", got)
	}
	if !strings.Contains(got, "The provider accepted it") {
		t.Errorf("the detail sentence is not capitalized on its own line:\n%s", got)
	}

	for base, want := range map[string]string{
		"https://api.openai.com/v1":  "OpenAI (https://api.openai.com/v1)",
		"http://localhost:11434/v1":  "a model server on this machine (http://localhost:11434/v1)",
		"https://llm.example.com/v1": "llm.example.com (https://llm.example.com/v1)",
		"":                           "the model provider",
	} {
		if got := providerLabel(base); got != want {
			t.Errorf("providerLabel(%q) = %q, want %q", base, got, want)
		}
	}
}

// "/connect show" answers "which platform am I on?" in the same words.
func TestConnectShowNamesTheProvider(t *testing.T) {
	got := formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectShown, APIBase: "https://openrouter.ai/api/v1",
		MaskedKey: "...901c (73 characters)", Detail: "stored, and the provider accepted it when it was saved",
	}})
	if !strings.Contains(got, "Connected to OpenRouter (https://openrouter.ai/api/v1) with key ...901c") {
		t.Errorf("/connect show does not name the provider:\n%s", got)
	}
}

// A KEY TYPED AFTER /connect NEVER COMES BACK ON THE UP ARROW. The command
// refuses it (a key on the command line), but the line had already been
// recorded for recall, so one press of up showed the key in clear (FOUND
// 2026-10-01). The subcommands are still recalled.
//
// Neuter check: call rememberPrompt for every line in startTurn again.
func TestAKeyTypedAfterConnectIsNotRecalled(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "/connect "+tuiTestKey)
	m, _ = pressEnter(m)
	for _, tr := range m.turns {
		if strings.Contains(tr.text, tuiTestKey) {
			t.Fatal("the refused key was written into the transcript")
		}
	}
	m = press(m, tea.KeyUp)
	if strings.Contains(m.input.Value(), tuiTestKey) {
		t.Errorf("up recalled the refused key in clear: %q", m.input.Value())
	}

	for raw, want := range map[string]bool{
		"/connect " + tuiTestKey: true, "/connect\t" + tuiTestKey: true,
		"/connect": false, "/connect show": false, "/connect forget": false,
		"/connected to the db?": false, "how do I /connect": false,
	} {
		if got := connectWithKey(raw); got != want {
			t.Errorf("connectWithKey(%q) = %v, want %v", raw, got, want)
		}
	}
}

// THE REPORTED BUG (2026-10-05): a key from one provider, pasted while the
// daemon was on another, came back as "The provider refused that key" -- twice
// -- and nothing on screen said which provider had been asked, or that a
// different one could be named. The refusal now says both.
//
// Neuter check: drop providerLabel(r.APIBase) from the ConnectRejected branch.
func TestARefusedKeySaysWhoRefusedItAndHowToUseAnotherProvider(t *testing.T) {
	got := formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: false, Outcome: protocol.ConnectRejected, APIBase: "https://openrouter.ai/api/v1",
		Detail: "the provider refused it (HTTP 401)",
	}})
	for _, want := range []string{
		"refused by OpenRouter (https://openrouter.ai/api/v1)",
		"HTTP 401",
		"Nothing was stored",
		"name its provider first",
		connectBaseExample,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, got)
		}
	}

	// The prompt that opens when the key IN USE is refused is the other moment
	// someone is holding a key from elsewhere -- it is where this was reported.
	out, _ := newTestModel().beginConnectForRefusedKey("auth", false)
	m := out.(chatModel)
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, connectBaseExample) {
		t.Errorf("the refused-key prompt does not say how to bring a key from another provider:\n%s", last)
	}
}

// /connect <address> IS HOW A DIFFERENT PROVIDER IS NAMED, end to end: the
// address is said back before the key is asked for, and it reaches the daemon
// with the key -- which is what makes the daemon check the key against the
// provider that issued it instead of the one it was leaving.
//
// Neuter check: drop APIBase from the request handleConnectKey sends.
func TestConnectToAnAddressSendsTheKeyToThatProvider(t *testing.T) {
	const base = "https://integrate.api.nvidia.com/v1"
	lockPath, got, cleanup := fakeDaemonForConnect(t, protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectUnverified, Detail: "nothing proves it", MaskedKey: "...CRET",
		APIBase: base, InUse: true,
	})
	defer cleanup()
	original := lockPathFunc
	lockPathFunc = func() string { return lockPath }
	defer func() { lockPathFunc = original }()

	m := newTestModel()
	m = typeText(m, "/connect "+base+"/")
	m, _ = pressEnter(m)
	if m.state != stateConnect {
		t.Fatalf("state = %v after /connect <address>, want the masked key prompt", m.state)
	}
	if m.input.EchoMode != textinput.EchoPassword {
		t.Fatal("the key prompt opened for an address is not masked")
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "Switching to NVIDIA ("+base+")") {
		t.Errorf("the address the key is about to be sent to was not said back:\n%s", last)
	}

	m = typeText(m, tuiTestKey)
	m, cmd := pressEnter(m)
	if cmd == nil {
		t.Fatal("submitting the key sent nothing")
	}
	if m.connectBase != "" {
		t.Errorf("the address outlived the prompt (%q), so a later bare /connect would reuse it", m.connectBase)
	}
	if _, ok := cmd().(connectResultMsg); !ok {
		t.Fatal("the command did not come back with the daemon's answer")
	}
	if got.APIBase != base {
		t.Errorf("the daemon was sent api_base %q, want %q", got.APIBase, base)
	}
	if got.APIKey != tuiTestKey {
		t.Errorf("the daemon received %q, not the key that was typed", maskForTest(got.APIKey))
	}
	for _, tr := range m.turns {
		if strings.Contains(tr.text, tuiTestKey) {
			t.Fatal("the key was written into the transcript")
		}
	}
}

// ESC LEAVES NOTHING BEHIND. An address given and then abandoned must not ride
// along on the next bare /connect, which means "the provider already in use".
func TestAnAbandonedAddressIsNotReused(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "/connect https://integrate.api.nvidia.com/v1")
	m, _ = pressEnter(m)
	if m.connectBase == "" {
		t.Fatal("the address was not held while the key prompt was open")
	}
	m = press(m, tea.KeyEsc)
	if m.state == stateConnect || m.connectBase != "" {
		t.Errorf("after esc: state %v, address %q; want the prompt closed and the address gone", m.state, m.connectBase)
	}
}

// AN ADDRESS IS THE ONLY NEW ARGUMENT, AND IT IS NEVER A KEY. Everything
// connectBaseArg turns away is refused by the command and kept out of the
// up-arrow recall, so each refusal here is a place a secret cannot hide.
func TestConnectBaseArgAcceptsAnAddressAndNothingThatCouldCarryASecret(t *testing.T) {
	for arg, want := range map[string]string{
		"https://integrate.api.nvidia.com/v1":  "https://integrate.api.nvidia.com/v1",
		"https://integrate.api.nvidia.com/v1/": "https://integrate.api.nvidia.com/v1",
		"http://localhost:11434/v1":            "http://localhost:11434/v1",
		"HTTPS://Api.Example.com/v1":           "HTTPS://Api.Example.com/v1",
	} {
		if got, ok := connectBaseArg(arg); !ok || got != want {
			t.Errorf("connectBaseArg(%q) = %q, %v; want %q, true", arg, got, ok, want)
		}
	}
	for _, arg := range []string{
		"", tuiTestKey, "integrate.api.nvidia.com/v1", "https://", "ftp://host/v1",
		"https://user:" + tuiTestKey + "@host/v1",
		"https://host/v1?key=" + tuiTestKey,
		"https://host/v1#" + tuiTestKey,
		"https://host/v1 " + tuiTestKey,
	} {
		if got, ok := connectBaseArg(arg); ok {
			t.Errorf("connectBaseArg(%q) accepted it as %q", arg, got)
		}
	}

	// And the recall rule follows it: an address comes back on the up arrow, a
	// line that could hold a key does not.
	for raw, want := range map[string]bool{
		"/connect https://integrate.api.nvidia.com/v1":               false,
		"/connect https://integrate.api.nvidia.com/v1 " + tuiTestKey: true,
		"/connect https://host/v1?key=" + tuiTestKey:                 true,
		"/connect https://user:" + tuiTestKey + "@host/v1":           true,
	} {
		if got := connectWithKey(raw); got != want {
			t.Errorf("connectWithKey(%q) = %v, want %v", raw, got, want)
		}
	}
}

// What the daemon says a switch leaves behind is shown, each as its own note.
func TestConnectResultShowsTheDaemonsNotes(t *testing.T) {
	got := formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectUnverified, Detail: "nothing proves it", MaskedKey: "...CRET",
		APIBase: "https://integrate.api.nvidia.com/v1", InUse: true,
		Notes: []string{"model names differ", "the environment wins at the next start"},
	}})
	for _, want := range []string{"NOTE: model names differ", "NOTE: the environment wins at the next start"} {
		if !strings.Contains(got, want) {
			t.Errorf("the result does not show %q:\n%s", want, got)
		}
	}
}

// "/connect, PASTE THE KEY" IS THE WHOLE INSTRUCTION, so the prompt it opens
// has to say the key need not be from any one provider -- nothing used to, and
// a user holding a good key from elsewhere concluded the product could not use
// it.
func TestBareConnectSaysAnyProvidersKeyWorks(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "/connect")
	m, _ = pressEnter(m)
	if m.state != stateConnect || m.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("/connect did not open the masked key prompt (state %v)", m.state)
	}
	if m.connectBase != "" {
		t.Errorf("a bare /connect named an address (%q); the daemon is meant to tell from the key", m.connectBase)
	}
	last := m.turns[len(m.turns)-1].text
	for _, want := range []string{"Paste your API key", "OpenRouter", "NVIDIA", "recognised from the key"} {
		if !strings.Contains(last, want) {
			t.Errorf("the key prompt does not say %q:\n%s", want, last)
		}
	}
}

// A provider can be named instead of addressed: /connect nvidia. The name
// comes from the fixed list the daemon shares, so nothing typed here that is
// not on that list is taken for one.
func TestConnectByProviderName(t *testing.T) {
	nvidia, ok := protocol.ProviderByName("nvidia")
	if !ok {
		t.Fatal("nvidia is not in the provider list")
	}
	m := newTestModel()
	m = typeText(m, "/connect nvidia")
	m, _ = pressEnter(m)
	if m.state != stateConnect || m.connectBase != nvidia.APIBase {
		t.Fatalf("state %v, address %q; want the key prompt open for %q", m.state, m.connectBase, nvidia.APIBase)
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "Switching to NVIDIA ("+nvidia.APIBase+")") {
		t.Errorf("the provider and the address the key goes to were not said back:\n%s", last)
	}

	for arg, want := range map[string]string{"NVIDIA": nvidia.APIBase, "google": "https://generativelanguage.googleapis.com/v1beta/openai"} {
		if got, ok := connectBaseArg(arg); !ok || got != want {
			t.Errorf("connectBaseArg(%q) = %q, %v; want %q", arg, got, ok, want)
		}
	}
	// Close to a name is not a name: it is refused like any other probable key.
	for _, arg := range []string{"nvidiaa", "open-ai", "nvapi-" + tuiTestKey} {
		if got, ok := connectBaseArg(arg); ok {
			t.Errorf("connectBaseArg(%q) accepted it as %q", arg, got)
		}
		if !connectWithKey("/connect " + arg) {
			t.Errorf("/connect %s would be kept for the up arrow, though it may be a key", arg)
		}
	}
}

// A BARE sk- KEY: the daemon sent it nowhere and asks whose it is. The client
// says so first, offers each candidate as a command to run, and gives a held
// question back rather than sending it into a daemon that still has no key.
func TestAKeyThatCouldBeSeveralProvidersIsAskedAbout(t *testing.T) {
	resp := protocol.ConnectResponse{
		Ok: false, Outcome: protocol.ConnectNeedsProvider, Candidates: []string{"openai", "deepseek"},
		Detail: `keys that start with "sk-" are issued by OpenAI, DeepSeek, and nothing in this one says which`,
	}
	got := formatConnectResult(connectResultMsg{resp: resp})
	for _, want := range []string{"sent nowhere", "nothing was stored", "/connect openai", "/connect deepseek", "paste it again"} {
		if !strings.Contains(got, want) {
			t.Errorf("the answer does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "refused") || strings.Contains(got, "Connected") {
		t.Errorf("a key that was never checked is described as checked:\n%s", got)
	}

	m := newTestModel()
	m.needsAPIKey = true
	m.pendingPrompt = "who is charles babbage"
	out, _ := m.handleConnectResult(connectResultMsg{resp: resp})
	after := out.(chatModel)
	if after.input.Value() != "who is charles babbage" || !after.needsAPIKey {
		t.Errorf("the held question was lost or sent: input %q, needsAPIKey %v", after.input.Value(), after.needsAPIKey)
	}
}

// "READY" IS SAID ONLY FOR A MODEL THAT ANSWERED. The daemon reports whether the
// model it chose took a real request with this key; without that the model is
// named as what will be tried, never as something that works.
//
// Neuter check: ignore r.ModelTested in connectModelLine.
func TestConnectSaysReadyOnlyForAModelThatAnswered(t *testing.T) {
	base := protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, APIBase: "https://integrate.api.nvidia.com/v1",
		Detail:    "NVIDIA answered a test request to deepseek-ai/deepseek-v4.1-flash with this key",
		MaskedKey: "...CRET (70 characters)", InUse: true, Model: "deepseek-ai/deepseek-v4.1-flash", ModelCount: 53,
	}

	tested := base
	tested.ModelTested = true
	got := formatConnectResult(connectResultMsg{resp: tested})
	for _, want := range []string{"Connected to NVIDIA (https://integrate.api.nvidia.com/v1).", "in use now",
		"Ready — prompts go to deepseek-ai/deepseek-v4.1-flash", "53 models", "/model"} {
		if !strings.Contains(got, want) {
			t.Errorf("a tested connect does not say %q:\n%s", want, got)
		}
	}

	untested := base
	untested.Outcome, untested.Detail = protocol.ConnectUnverified, "NVIDIA recognised the key but would not run a test request"
	got = formatConnectResult(connectResultMsg{resp: untested})
	if strings.Contains(got, "Ready") {
		t.Errorf("an untested model is called ready:\n%s", got)
	}
	for _, want := range []string{"NOT verified", "NOT answered a test request", "deepseek-ai/deepseek-v4.1-flash", "NVIDIA"} {
		if !strings.Contains(got, want) {
			t.Errorf("an untested connect does not say %q:\n%s", want, got)
		}
	}

	// OpenRouter chooses no model here -- models.json's tiers stay -- and the
	// line is simply absent rather than naming nothing.
	plain := base
	plain.Model, plain.ModelCount = "", 0
	if got := formatConnectResult(connectResultMsg{resp: plain}); strings.Contains(got, "prompts go to") {
		t.Errorf("a connect that chose no model talks about one:\n%s", got)
	}
}
