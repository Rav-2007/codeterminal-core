package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// plainScreen is what is on screen, as a reader would take it in: no colour, and
// with the box's line breaks AND ITS BORDERS folded away so a sentence can be
// looked for whole. The borders matter as much as the breaks: a sentence that
// wraps inside a box has "│ │" between its two halves, and a test that looked
// for it whole then failed whenever the box changed width.
func plainScreen(m chatModel) string {
	text := strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ").Replace(ansi.Strip(m.View()))
	return strings.Join(strings.Fields(text), " ")
}

// findMsg runs a command -- and, when it is a batch, everything in it -- and
// returns the first message of type T. The setup batches a spinner tick or a
// cursor blink beside the request it sends, so the request's answer is no
// longer the command's only message.
func findMsg[T any](cmd tea.Cmd) (T, bool) {
	var zero T
	if cmd == nil {
		return zero, false
	}
	out := make(chan tea.Msg, 64) // never blocks a sender, so nothing is left parked
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range batch {
					run(sub)
				}
				return
			}
			out <- msg
		}()
	}
	run(cmd)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-out:
			if v, ok := msg.(T); ok {
				return v, true
			}
		case <-deadline:
			return zero, false
		}
	}
}

// connectAnswerOf is the daemon's answer to a connect request a command sent.
func connectAnswerOf(cmd tea.Cmd) (connectResultMsg, bool) {
	return findMsg[connectResultMsg](cmd)
}

// recordingDaemon is a daemon that answers every connection's handshake, naming
// the given features, then answers its one request with whatever answer returns
// for it -- and keeps every request it was sent.
type recordingDaemon struct {
	mu   sync.Mutex
	reqs []json.RawMessage
}

func (d *recordingDaemon) requests() []json.RawMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]json.RawMessage(nil), d.reqs...)
}

// connectRequests decodes what was recorded as connect requests.
func (d *recordingDaemon) connectRequests(t *testing.T) []protocol.ConnectRequest {
	t.Helper()
	var out []protocol.ConnectRequest
	for _, raw := range d.requests() {
		var req protocol.ConnectRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("a recorded request does not decode: %v", err)
		}
		out = append(out, req)
	}
	return out
}

func startRequestRecorder(t *testing.T, features []string, answer func(raw json.RawMessage) any) *recordingDaemon {
	t.Helper()
	d := &recordingDaemon{}
	addr := testAddress(t)
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data, err := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
				var hs protocol.HandshakeRequest
				if dec.Decode(&hs) != nil {
					return
				}
				_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true, Features: features})
				var raw json.RawMessage
				if dec.Decode(&raw) != nil {
					return
				}
				d.mu.Lock()
				d.reqs = append(d.reqs, raw)
				d.mu.Unlock()
				_ = enc.Encode(answer(raw))
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return d
}

// firstRunModel is the client as main builds it when the daemon says no key is
// in force, at the size given.
func firstRunModel(t *testing.T, width, height int) chatModel {
	t.Helper()
	m := newChatModel("test-client", "/workspace", "/workspace", nil)
	m.needsAPIKey = true
	m = m.withFirstRunSetup()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(chatModel)
}

const setupTestKey = "nvapi-0123456789abcdefSECRETsetup"

func mustProvider(t *testing.T, id string) protocol.Provider {
	t.Helper()
	p, ok := protocol.ProviderByName(id)
	if !ok {
		t.Fatalf("%s is not in the provider list", id)
	}
	return p
}

// keyStepFor walks the first-run setup to the key step of one provider, by
// typing its name and pressing Enter the way a person does.
func keyStepFor(t *testing.T, id string) chatModel {
	t.Helper()
	m := firstRunModel(t, 100, 40)
	m = typeText(m, id)
	m, _ = pressEnter(m)
	if m.state != stateConnect || m.setup.step != setupKey {
		t.Fatalf("picking %s did not open the key step (state %v, step %v)", id, m.state, m.setup.step)
	}
	return m
}

func assertNoKeyAnywhere(t *testing.T, m chatModel, key string) {
	t.Helper()
	for i, tr := range m.turns {
		if strings.Contains(tr.text, key) {
			t.Errorf("turn %d carries the key into the transcript", i)
		}
	}
	if strings.Contains(m.View(), key) {
		t.Error("the key is on screen")
	}
	if m.input.Value() != "" {
		t.Error("the key is still in the input")
	}
	for _, p := range m.promptHistory {
		if strings.Contains(p, key) {
			t.Error("the key is in the up-arrow recall")
		}
	}
}

// THE REPORT THIS ANSWERS (OWNER, 2026-10-11): "in the very first step only it
// moves to the chat". With no key in force the client now opens on the list of
// providers, not on the splash and not on the prompt.
//
// Neuter check: drop the `if needsAPIKey { model = model.withFirstRunSetup() }`
// in main.go -- TestARealFirstRunOpensOnTheProviderList (the pty test) fails;
// this one pins what that screen is.
func TestWithNoKeyTheClientOpensOnTheProviderList(t *testing.T) {
	m := firstRunModel(t, 100, 40)
	if m.state != stateSetup || m.setup.step != setupProvider {
		t.Fatalf("state %v, step %v; want the provider list", m.state, m.setup.step)
	}
	screen := plainScreen(m)
	for _, want := range []string{brandName, setupWelcome, setupProviderTitle, "Any key", "OpenRouter", "OpenAI",
		"Anthropic", "NVIDIA", "Groq", setupProviderSearch, setupEscSkip} {
		if !strings.Contains(screen, want) {
			t.Errorf("the first screen does not show %q:\n%s", want, screen)
		}
	}
	// The lotus is drawn when there is room for it under a usable list.
	if !strings.Contains(ansi.Strip(m.View()), "( ●● )") {
		t.Error("a tall terminal's first screen has no logo")
	}
	// Every provider the daemon knows can be reached from here.
	for _, p := range protocol.Providers() {
		probe := typeText(m, p.ID)
		if got, ok := probe.setup.picker.selected(); !ok || got.ID != p.ID {
			t.Errorf("typing %q on the list selects %q, not that provider", p.ID, got.ID)
		}
	}
}

// THE DOOR IS NOT LOCKED. Esc goes to the chat with nothing sent, the client
// still knows it has no key, and the first question opens the key prompt it
// always did -- the part of the 2026-09-26 decision the owner kept.
func TestEscOnTheFirstScreenGoesToTheChatAndAQuestionStillAsksForAKey(t *testing.T) {
	m := firstRunModel(t, 100, 40)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)
	if m.state != stateIdle || m.setupActive() {
		t.Fatalf("after esc: state %v, setup step %v; want the prompt", m.state, m.setup.step)
	}
	if !m.needsAPIKey {
		t.Error("skipping the setup cleared needsAPIKey, so the first question would be sent with no credential")
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "Setup skipped") || !strings.Contains(last, "/connect") ||
		!strings.Contains(last, "nothing was sent or stored") {
		t.Errorf("the skip does not say what happens next, or that nothing left the machine:\n%s", last)
	}
	if !strings.Contains(m.View(), defaultInputPlaceholder) {
		t.Error("the ordinary prompt is not on screen after the skip")
	}

	// /help and the rest work with no key...
	helped := typeText(m, "/help")
	helped, _ = pressEnter(helped)
	if helped.state == stateConnect || helped.setupActive() {
		t.Error("/help was held behind a key prompt")
	}
	// ...and a question is held, behind the same masked prompt as before.
	asked := typeText(m, "what does this repo do")
	asked, _ = pressEnter(asked)
	if asked.state != stateConnect || asked.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("a question with no key did not open the masked prompt (state %v)", asked.state)
	}
	if asked.pendingPrompt != "what does this repo do" {
		t.Errorf("the question was not held: %q", asked.pendingPrompt)
	}
	if asked.setupActive() {
		t.Error("the held-question prompt was drawn as a setup step; it is the inline one")
	}
}

// PROVIDER, THEN ITS KEY: the key step names the provider and the address the
// key is about to be sent to, the key reaches the daemon with that address, and
// it is nowhere else afterwards.
//
// Neuter check: send APIBase: "" in setupSubmitKey.
func TestPickingAProviderSendsItsKeyThereAndNowhereElse(t *testing.T) {
	groq := mustProvider(t, "groq")
	daemon := startRequestRecorder(t, nil, func(json.RawMessage) any {
		return protocol.ConnectResponse{Ok: false, Outcome: protocol.ConnectRejected, APIBase: groq.APIBase, Detail: "invalid api key"}
	})
	const key = "gsk_0123456789abcdefSECRETgroq"

	m := keyStepFor(t, "groq")
	if m.connectBase != groq.APIBase {
		t.Fatalf("the key step is for %q, want %q", m.connectBase, groq.APIBase)
	}
	if m.input.EchoMode != textinput.EchoPassword {
		t.Fatal("the key step's input is not masked")
	}
	screen := plainScreen(m)
	for _, want := range []string{"Groq", groq.APIBase, "not shown as you type", setupEscBack} {
		if !strings.Contains(screen, want) {
			t.Errorf("the key step does not say %q:\n%s", want, screen)
		}
	}

	m = typeText(m, key)
	if strings.Contains(m.View(), key) {
		t.Fatal("the key is drawn in clear while it is typed")
	}
	m, cmd := pressEnter(m)
	if m.state != stateSetup || m.setup.step != setupChecking {
		t.Fatalf("after enter: state %v, step %v; want the checking screen", m.state, m.setup.step)
	}
	if m.input.EchoMode != textinput.EchoNormal {
		t.Error("the input was left in password mode")
	}
	if !strings.Contains(plainScreen(m), "checking the key with Groq") {
		t.Errorf("the checking screen does not say who is being asked:\n%s", plainScreen(m))
	}
	assertNoKeyAnywhere(t, m, key)

	answer, ok := connectAnswerOf(cmd)
	if !ok {
		t.Fatal("no request reached the daemon")
	}
	got := daemon.connectRequests(t)
	if len(got) != 1 || got[0].APIKey != key || got[0].APIBase != groq.APIBase || !got[0].Connect {
		t.Fatalf("the daemon was sent %d request(s); want one carrying the key for %s", len(got), groq.APIBase)
	}

	// REFUSED: back on the same provider's key step, saying who refused it.
	updated, _ := m.Update(answer)
	m = updated.(chatModel)
	if m.state != stateConnect || m.setup.step != setupKey || m.connectBase != groq.APIBase {
		t.Fatalf("after a refusal: state %v, step %v, address %q; want Groq's key step again", m.state, m.setup.step, m.connectBase)
	}
	if m.input.EchoMode != textinput.EchoPassword {
		t.Error("the key step reopened unmasked")
	}
	if !m.needsAPIKey {
		t.Error("a refused key cleared needsAPIKey")
	}
	if s := plainScreen(m); !strings.Contains(s, "refused by Groq") || !strings.Contains(s, "invalid api key") || !strings.Contains(s, "nothing was stored") {
		t.Errorf("the refusal is not explained on the key step:\n%s", s)
	}
	assertNoKeyAnywhere(t, m, key)

	// LEAVING AFTER THAT MUST NOT SAY "NOTHING WAS SENT": something was, and the
	// provider's refusal of it is the line directly above (seen in the real
	// app, 2026-10-11).
	m = press(press(m, tea.KeyEsc), tea.KeyEsc)
	if m.state != stateIdle {
		t.Fatalf("two escs did not reach the chat (state %v)", m.state)
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "Setup skipped") || strings.Contains(last, "nothing was sent") {
		t.Errorf("leaving after a key was sent says:\n%s", last)
	}
}

// tiersForSetup is a provider's list as the daemon reports it after a connect:
// the tested default, two more, one that did not answer, one switched off.
func tiersForSetup() []protocol.StatusTier {
	return []protocol.StatusTier{
		{Name: "alpha/quiet-one", Slug: "alpha/quiet-one", Active: true, Quiet: true},
		{Name: "beta/second", Slug: "beta/second", Active: true},
		{Name: "gamma/tested", Slug: "gamma/tested", Active: true, Default: true},
		{Name: "omega/off", Slug: "omega/off", Active: false},
		{Name: "zeta/third", Slug: "zeta/third", Active: true},
	}
}

// modelStep is the setup at its last step, as an accepted key leaves it.
func modelStep(t *testing.T) chatModel {
	t.Helper()
	m := keyStepFor(t, "nvidia")
	m.setup.step = setupChecking
	m.state = stateSetup
	nvidia := mustProvider(t, "nvidia")
	updated, cmd := m.Update(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, APIBase: nvidia.APIBase, InUse: true,
		MaskedKey: "...etup", Detail: "NVIDIA answered a test request", Model: "gamma/tested", ModelCount: 4, ModelTested: true,
	}})
	m = updated.(chatModel)
	if m.setup.step != setupModel || !m.setup.loading || cmd == nil {
		t.Fatalf("an accepted key did not ask for the model list (step %v, loading %v)", m.setup.step, m.setup.loading)
	}
	if m.needsAPIKey {
		t.Fatal("an accepted key that is in use left needsAPIKey set")
	}
	updated, _ = m.Update(setupTiersMsg{tiers: tiersForSetup()})
	return updated.(chatModel)
}

// THE THIRD STEP: the provider's models where their names put them, the one in
// use marked and highlighted, the ones that did not answer last, the ones
// switched off not offered.
func TestAnAcceptedKeyOffersTheProvidersModels(t *testing.T) {
	m := modelStep(t)
	if m.state != stateSetup || m.setup.step != setupModel || m.setup.loading {
		t.Fatalf("state %v, step %v, loading %v; want the model list", m.state, m.setup.step, m.setup.loading)
	}
	var order []string
	for _, row := range m.setup.picker.rows {
		order = append(order, row.ID)
	}
	if want := "beta/second gamma/tested zeta/third alpha/quiet-one"; strings.Join(order, " ") != want {
		t.Errorf("the models are offered as %v, want %s", order, want)
	}
	if got, _ := m.setup.picker.selected(); got.ID != "gamma/tested" {
		t.Errorf("the highlight opens on %q, want the model in use", got.ID)
	}
	screen := plainScreen(m)
	for _, want := range []string{setupModelTitle, setupModelSearch, "Connected to NVIDIA", "gamma/tested ● (current)",
		"did not answer when the key was connected", setupEscKeepModel} {
		if !strings.Contains(screen, want) {
			t.Errorf("the model step does not say %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "omega/off") {
		t.Error("a model that is switched off is offered")
	}
	// The connect result is in the transcript for when the setup closes.
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "Connected to NVIDIA") {
		t.Errorf("the transcript has no record of the connect:\n%s", last)
	}
}

// PICKING A MODEL MAKES IT THE MODEL, here and in the daemon: this client's
// prompts name it, and the daemon is asked to remember it (FeatureModelChoice).
//
// Neuter check: return `m, focus` without calling chooseModel in handleSetupKey.
func TestPickingAModelSetsItAndAsksTheDaemonToRememberIt(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureModelChoice}, func(json.RawMessage) any {
		return protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectModelChosen, Model: "beta/second", ModelSaved: true}
	})
	m := modelStep(t)
	m = typeText(m, "beta")
	m, cmd := pressEnter(m)
	if m.state != stateIdle || m.setupActive() {
		t.Fatalf("after choosing: state %v, step %v; want the prompt", m.state, m.setup.step)
	}
	if m.preferredTier != "beta/second" {
		t.Errorf("this client's prompts go to %q, want beta/second", m.preferredTier)
	}
	answer, ok := findMsg[modelChoiceMsg](cmd)
	if !ok {
		t.Fatal("the daemon was not asked to remember the model")
	}
	got := daemon.connectRequests(t)
	if len(got) != 1 || got[0].ChooseModel != "beta/second" || got[0].APIKey != "" || got[0].ClearModel {
		t.Fatalf("the daemon was sent %+v; want one request choosing beta/second and carrying no key", len(got))
	}
	updated, _ := m.Update(answer)
	m = updated.(chatModel)
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "Remembered") || !strings.Contains(last, "after a restart") {
		t.Errorf("a saved choice is not reported as remembered:\n%s", last)
	}
}

// The model already in use needs no request, and Esc changes nothing either.
func TestKeepingTheModelInUseSendsNothing(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureModelChoice}, func(json.RawMessage) any {
		return protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectModelChosen}
	})
	for name, key := range map[string]tea.KeyType{"enter on the one in use": tea.KeyEnter, "esc": tea.KeyEsc} {
		m := press(modelStep(t), key)
		if m.state != stateIdle || m.setupActive() {
			t.Errorf("%s: state %v, step %v; want the prompt", name, m.state, m.setup.step)
		}
		if m.preferredTier != "" {
			t.Errorf("%s: set a model (%q) nobody chose", name, m.preferredTier)
		}
		for _, tr := range m.turns {
			if strings.Contains(tr.text, "model set to") {
				t.Errorf("%s: announced a model change", name)
			}
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(daemon.requests()); n != 0 {
		t.Errorf("%d request(s) were sent for a choice that changed nothing", n)
	}
}

// One model, or a list that never arrived, is nothing to choose between: the
// setup closes instead of showing a list with a single possible answer.
func TestTheModelStepIsSkippedWhenThereIsNothingToChoose(t *testing.T) {
	for name, msg := range map[string]setupTiersMsg{
		"one model":       {tiers: []protocol.StatusTier{{Name: "only", Slug: "only", Active: true, Default: true}}},
		"list unreadable": {err: errors.New("daemon unreachable")},
	} {
		m := modelStep(t)
		m.setup.loading = true
		updated, _ := m.Update(msg)
		m = updated.(chatModel)
		if m.state != stateIdle || m.setupActive() {
			t.Errorf("%s: state %v, step %v; want the prompt", name, m.state, m.setup.step)
		}
	}
	// And a list that arrives after the setup was left is ignored.
	m := newTestModel()
	updated, _ := m.Update(setupTiersMsg{tiers: tiersForSetup()})
	if updated.(chatModel).setupActive() {
		t.Error("a late model list reopened the setup")
	}
}

// A PASTE ON THE PROVIDER LIST IS, AS LIKELY AS NOT, THE KEY. It must not be
// shown, and the screen says where the key goes.
func TestAKeyPastedOnTheProviderListIsNotShown(t *testing.T) {
	m := firstRunModel(t, 100, 40)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(setupTestKey), Paste: true})
	m = updated.(chatModel)
	if m.setup.picker.filter != "" {
		t.Errorf("the search text took %d pasted characters", len(m.setup.picker.filter))
	}
	if strings.Contains(m.View(), "SECRET") {
		t.Fatal("the pasted key is on screen")
	}
	if !strings.Contains(plainScreen(m), "pick a provider first") {
		t.Errorf("the screen does not say where a key goes:\n%s", plainScreen(m))
	}
	// The next real keystroke clears the note and searches as usual.
	m = typeText(m, "g")
	if strings.Contains(plainScreen(m), "pick a provider first") || m.setup.picker.filter != "g" {
		t.Error("typing after the paste did not go back to searching")
	}
}

// A KEY IS NEVER SENT TO A COMPANY THAT DID NOT ISSUE IT. Groq picked, an NVIDIA
// key pasted: it goes to NVIDIA and the transcript says so. An address typed by
// hand is left alone -- a gateway may take keys that look like someone else's.
//
// Neuter check: drop the ProvidersForKey block in setupSubmitKey.
func TestAKeyForAnotherProviderGoesToItsIssuer(t *testing.T) {
	nvidia := mustProvider(t, "nvidia")
	daemon := startRequestRecorder(t, nil, func(json.RawMessage) any {
		return protocol.ConnectResponse{Ok: false, Outcome: protocol.ConnectRejected, APIBase: nvidia.APIBase, Detail: "Authorization failed"}
	})

	m := typeText(keyStepFor(t, "groq"), setupTestKey)
	m, cmd := pressEnter(m)
	if _, ok := connectAnswerOf(cmd); !ok {
		t.Fatal("no request reached the daemon")
	}
	got := daemon.connectRequests(t)
	if len(got) != 1 || got[0].APIBase != nvidia.APIBase {
		t.Fatalf("an NVIDIA key typed on Groq's key step was sent to %q", got[0].APIBase)
	}
	if !strings.Contains(plainScreen(m), "checking the key with NVIDIA") {
		t.Errorf("the checking screen names the wrong provider:\n%s", plainScreen(m))
	}
	var said bool
	for _, tr := range m.turns {
		if strings.Contains(tr.text, "NVIDIA key") && strings.Contains(tr.text, "not with Groq") {
			said = true
		}
	}
	if !said {
		t.Error("nothing says the key went to a different provider than the one picked")
	}
	assertNoKeyAnywhere(t, m, setupTestKey)

	// By hand: the address given is the address used.
	const gateway = "https://gateway.example/v1"
	m = newTestModel()
	m = typeText(m, "/connect "+gateway)
	m, _ = pressEnter(m)
	m = typeText(m, setupTestKey)
	_, cmd = pressEnter(m)
	if _, ok := connectAnswerOf(cmd); !ok {
		t.Fatal("no request reached the daemon for the typed address")
	}
	got = daemon.connectRequests(t)
	if last := got[len(got)-1]; last.APIBase != gateway {
		t.Errorf("a key for a hand-typed address was redirected to %q", last.APIBase)
	}
}

// A bare "sk-" key on the "Any key" row: the daemon sent it nowhere and names
// who it could be from. The setup goes back to the list, cut down to those.
func TestAKeyThatCouldBeSeveralProvidersReturnsToTheListNarrowed(t *testing.T) {
	m := firstRunModel(t, 100, 40)
	m, _ = pressEnter(m) // "Any key"
	if m.state != stateConnect || m.connectBase != "" {
		t.Fatalf("the first row did not open a key step with no address (state %v, %q)", m.state, m.connectBase)
	}
	m = typeText(m, "sk-0123456789abcdefSECRETbare")
	m, _ = pressEnter(m)
	updated, _ := m.Update(connectResultMsg{resp: protocol.ConnectResponse{
		Outcome: protocol.ConnectNeedsProvider, Candidates: []string{"openai", "deepseek"},
		Detail: `keys that start with "sk-" are issued by OpenAI, DeepSeek, and nothing in this one says which`,
	}})
	m = updated.(chatModel)
	if m.state != stateSetup || m.setup.step != setupProvider {
		t.Fatalf("state %v, step %v; want the provider list again", m.state, m.setup.step)
	}
	if n := len(m.setup.picker.rows); n != 2 {
		t.Errorf("the list holds %d rows, want the 2 candidates", n)
	}
	if s := plainScreen(m); !strings.Contains(s, "sent nowhere") || !strings.Contains(s, "OpenAI") || !strings.Contains(s, "DeepSeek") {
		t.Errorf("the list does not say why it is back:\n%s", s)
	}
	if !m.needsAPIKey {
		t.Error("a key that was never checked cleared needsAPIKey")
	}
	// Picking one opens ITS key step, to paste the key again.
	m, _ = pressEnter(m)
	if m.state != stateConnect || m.connectBase != mustProvider(t, "openai").APIBase {
		t.Errorf("picking a candidate opened a key step for %q", m.connectBase)
	}
}

// Esc on the key step goes back one screen when a list came before it, and an
// empty Enter is a slip that stays put rather than a way out.
func TestTheKeyStepsEscAndEmptyEnter(t *testing.T) {
	m := keyStepFor(t, "nvidia")
	stayed, cmd := pressEnter(m)
	if cmd != nil || stayed.state != stateConnect || stayed.setup.step != setupKey {
		t.Fatalf("an empty enter left the key step (state %v) or sent something", stayed.state)
	}
	if !strings.Contains(plainScreen(stayed), "no key entered") {
		t.Error("an empty enter says nothing")
	}

	m = typeText(m, setupTestKey)
	back := press(m, tea.KeyEsc)
	if back.state != stateSetup || back.setup.step != setupProvider {
		t.Fatalf("esc on the key step: state %v, step %v; want the provider list", back.state, back.setup.step)
	}
	if back.connectBase != "" || back.input.EchoMode != textinput.EchoNormal {
		t.Error("esc left the address or the masking behind")
	}
	assertNoKeyAnywhere(t, back, setupTestKey)
	if !back.needsAPIKey {
		t.Error("going back cleared needsAPIKey")
	}
}

// Leaving while the key is being checked cannot recall the request, so the
// answer is still shown when it comes -- in the chat, by the ordinary path.
func TestLeavingDuringTheCheckStillShowsTheAnswer(t *testing.T) {
	m := keyStepFor(t, "nvidia")
	m.state, m.setup.step = stateSetup, setupChecking
	m = press(m, tea.KeyEsc)
	if m.state != stateIdle || m.setupActive() {
		t.Fatalf("esc while checking: state %v, step %v", m.state, m.setup.step)
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "check carries on") {
		t.Errorf("leaving does not say the request is still out:\n%s", last)
	}
	updated, _ := m.Update(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, APIBase: mustProvider(t, "nvidia").APIBase, InUse: true, MaskedKey: "...etup",
	}})
	m = updated.(chatModel)
	if m.needsAPIKey || m.setupActive() {
		t.Errorf("the late answer was not taken in (needsAPIKey %v, setup step %v)", m.needsAPIKey, m.setup.step)
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "Connected to NVIDIA") {
		t.Errorf("the late answer is not in the transcript:\n%s", last)
	}
}

// A key stored but NOT in use (one in the environment wins) has no model step:
// the transcript line explains, and the client still knows prompts lack that key.
func TestAKeyThatIsNotInUseEndsTheSetupWithoutAModelStep(t *testing.T) {
	m := keyStepFor(t, "nvidia")
	m.state, m.setup.step = stateSetup, setupChecking
	updated, cmd := m.Update(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, APIBase: mustProvider(t, "nvidia").APIBase,
		InUse: false, EnvOverride: true, MaskedKey: "...etup",
	}})
	m = updated.(chatModel)
	if m.setupActive() || m.state != stateIdle {
		t.Fatalf("state %v, step %v; want the prompt", m.state, m.setup.step)
	}
	if _, asked := findMsgWithin[setupTiersMsg](cmd, 200*time.Millisecond); asked {
		t.Error("a model list was asked for, for a key the daemon is not sending")
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "NOT what this daemon is sending") {
		t.Errorf("the transcript does not say the key is not in use:\n%s", last)
	}
}

// findMsgWithin is findMsg with a short wait, for asserting something was NOT
// sent without holding the test for the full timeout.
func findMsgWithin[T any](cmd tea.Cmd, wait time.Duration) (T, bool) {
	var zero T
	if cmd == nil {
		return zero, false
	}
	done := make(chan T, 1)
	go func() {
		if v, ok := findMsg[T](cmd); ok {
			done <- v
		}
	}()
	select {
	case v := <-done:
		return v, true
	case <-time.After(wait):
		return zero, false
	}
}

// ONE PRESS OF CTRL+C NEVER ENDS THE SESSION, on the setup as anywhere else: the
// first arms the quit and the screen says so, any other key disarms it.
func TestCtrlCOnTheSetupAsksBeforeQuitting(t *testing.T) {
	m := firstRunModel(t, 100, 40)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(chatModel)
	if cmd != nil {
		t.Fatal("one ctrl+c on the setup quit the program")
	}
	if !m.quitArmed || !strings.Contains(plainScreen(m), quitConfirmHelpText) {
		t.Errorf("the first ctrl+c did not ask (armed %v):\n%s", m.quitArmed, plainScreen(m))
	}
	disarmed := press(m, tea.KeyDown)
	if disarmed.quitArmed {
		t.Error("another key did not disarm the quit")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("a second ctrl+c did not quit")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Error("the second ctrl+c produced something other than a quit")
	}
}

// EVERY STEP FITS THE TERMINAL IT IS DRAWN IN. A screen taller or wider than
// the window scrolls the top off, and on the list that is the search line; and
// a size nobody should report must not crash the client (bubbles' input panics
// on a negative width -- see inputWidthFor).
func TestTheSetupFitsTheTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{{120, 50}, {100, 40}, {80, 24}, {60, 20}, {44, 16}}
	steps := map[string]func(chatModel) chatModel{
		"provider list": func(m chatModel) chatModel { return m },
		"key step":      func(m chatModel) chatModel { m, _ = pressEnter(typeText(m, "nvidia")); return m },
		"checking": func(m chatModel) chatModel {
			m, _ = pressEnter(typeText(m, "nvidia"))
			m, _ = pressEnter(typeText(m, setupTestKey))
			return m
		},
		"refused": func(m chatModel) chatModel {
			m, _ = pressEnter(typeText(m, "nvidia"))
			m, _ = pressEnter(typeText(m, setupTestKey))
			updated, _ := m.Update(connectResultMsg{resp: protocol.ConnectResponse{
				Outcome: protocol.ConnectRejected, APIBase: "https://integrate.api.nvidia.com/v1",
				Detail: "the provider said the key is not valid, at some length, so that this line has to wrap",
			}})
			return updated.(chatModel)
		},
		"model list": func(m chatModel) chatModel {
			m, _ = pressEnter(typeText(m, "nvidia"))
			m.state, m.setup.step, m.setup.loading = stateSetup, setupModel, true
			var tiers []protocol.StatusTier
			for i := 0; i < 40; i++ {
				tiers = append(tiers, protocol.StatusTier{Name: "vendor/a-rather-long-model-name-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Active: true, Default: i == 0})
			}
			updated, _ := m.Update(setupTiersMsg{tiers: tiers})
			return updated.(chatModel)
		},
	}
	for _, size := range sizes {
		for name, reach := range steps {
			m := reach(firstRunModel(t, size.w, size.h))
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) > size.h {
				t.Errorf("%s at %dx%d is %d lines tall", name, size.w, size.h, len(lines))
			}
			for _, line := range lines {
				if w := lipgloss.Width(line); w > size.w {
					t.Errorf("%s at %dx%d has a line %d columns wide: %q", name, size.w, size.h, w, ansi.Strip(line))
					break
				}
			}
		}
	}

	// The logo goes first: a 24-row terminal keeps its list rows instead.
	short := firstRunModel(t, 80, 24)
	if strings.Contains(ansi.Strip(short.View()), "( ●● )") {
		t.Error("a 24-row terminal spends its rows on the logo")
	}
	if rows, _, _ := short.setupFit(); rows < 8 {
		t.Errorf("a 24-row terminal shows only %d providers at a time", rows)
	}

	// And sizes that are not sizes.
	for _, size := range []struct{ w, h int }{{0, 0}, {1, 1}, {10, 3}, {20, 5}} {
		for name, reach := range steps {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s at %dx%d panicked: %v", name, size.w, size.h, r)
					}
				}()
				_ = reach(firstRunModel(t, size.w, size.h)).View()
			}()
		}
	}
	// Before any size has been reported at all.
	unsized := newChatModel("test-client", "/workspace", "/workspace", nil).withFirstRunSetup()
	if !strings.Contains(ansi.Strip(unsized.View()), setupProviderTitle) {
		t.Error("the setup draws nothing before the terminal reports its size")
	}
}

// /connect <provider> enters the setup at the key step and the model step
// follows it, the same as from the list.
func TestConnectByNameRunsTheSameStepsFromTheKeyOnward(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "/connect nvidia")
	m, _ = pressEnter(m)
	if m.state != stateConnect || m.setup.step != setupKey || m.setup.viaList {
		t.Fatalf("state %v, step %v, viaList %v; want the key step with no list behind it", m.state, m.setup.step, m.setup.viaList)
	}
	if s := plainScreen(m); !strings.Contains(s, "NVIDIA") || !strings.Contains(s, setupEscCancel) {
		t.Errorf("the key step does not name the provider or say esc cancels:\n%s", s)
	}
	m = typeText(m, setupTestKey)
	m, _ = pressEnter(m)
	updated, cmd := m.Update(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, APIBase: mustProvider(t, "nvidia").APIBase, InUse: true, MaskedKey: "...etup",
	}})
	m = updated.(chatModel)
	if m.setup.step != setupModel || cmd == nil {
		t.Errorf("an accepted key from /connect <name> did not go on to the model step (step %v)", m.setup.step)
	}
}

// A model named for the provider that was left names nothing at the new one.
func TestConnectingToAProviderDropsTheOldModelPreference(t *testing.T) {
	m := newTestModel()
	m.preferredTier = "deepseek_v4_pro"
	m = typeText(m, "/connect nvidia")
	m, _ = pressEnter(m)
	m = typeText(m, setupTestKey)
	m, _ = pressEnter(m)
	updated, _ := m.Update(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, APIBase: mustProvider(t, "nvidia").APIBase, InUse: true,
	}})
	if got := updated.(chatModel).preferredTier; got != "" {
		t.Errorf("prompts would still name %q, a model of the provider that was left", got)
	}
}
