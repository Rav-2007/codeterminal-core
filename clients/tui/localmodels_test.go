package tui

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// Tests for localmodels.go: models on this computer, from the terminal app.

// resetLocalState gives a test its own local-model state, its policy kept in a
// temporary file, and puts everything back afterwards.
func resetLocalState(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-models.json")
	savedPath := localPolicyPath
	localPolicyPath = func() string { return path }
	clear := func() {
		localState.mu.Lock()
		defer localState.mu.Unlock()
		localState.active, localState.supported, localState.loaded = false, false, false
		localState.policy = localPolicy{}
		localState.fitKey, localState.lastFit, localState.noted = "", nil, nil
		localState.checkSupported, localState.base, localState.model = false, "", ""
		localState.window, localState.fitted, localState.checking = 0, false, ""
	}
	clear()
	t.Cleanup(func() {
		clear()
		localPolicyPath = savedPath
	})
	return path
}

func setSupported(v bool) {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	localState.supported = v
}

// THE OWNER'S DEFAULTS (2026-10-23): a model on this computer gets no tools
// until /agent on, and no internet tools until /web on; a cloud provider is
// served as before.
//
// Neuter check: make promptCapabilities return []string{protocol.CapToolApproval}
// whatever is active, and every local row here fails.
func TestALocalModelGetsNoToolsAndNoInternetUntilTurnedOn(t *testing.T) {
	resetLocalState(t)
	setSupported(true)
	same := func(got, want []string) bool { return strings.Join(got, ",") == strings.Join(want, ",") }
	cases := []struct {
		name   string
		active bool
		policy localPolicy
		want   []string
	}{
		{"a cloud provider: agent mode as always", false, localPolicy{}, []string{protocol.CapToolApproval}},
		{"local, as installed: no tools", true, localPolicy{}, nil},
		{"local, /web on alone: still no tools", true, localPolicy{Web: true}, nil},
		{"local, /agent on: tools, no internet", true, localPolicy{Agent: true}, []string{protocol.CapToolApproval, protocol.CapNoNetworkTools}},
		{"local, /agent on and /web on: all tools", true, localPolicy{Agent: true, Web: true}, []string{protocol.CapToolApproval}},
	}
	for _, c := range cases {
		setLocalActive(c.active)
		if err := setLocalPolicy(c.policy); err != nil {
			t.Fatal(err)
		}
		if got := promptCapabilities(); !same(got, c.want) {
			t.Errorf("%s: capabilities %v, want %v", c.name, got, c.want)
		}
	}
}

// A daemon that does not honour "no internet tools" is not asked for tools at
// all: tools with the internet in them would break the promise /web off makes.
func TestAnOlderDaemonGetsNoToolsRatherThanTheInternet(t *testing.T) {
	resetLocalState(t)
	setLocalActive(true)
	if err := setLocalPolicy(localPolicy{Agent: true}); err != nil {
		t.Fatal(err)
	}
	setSupported(false)
	if got := promptCapabilities(); got != nil {
		t.Errorf("an older daemon was declared %v; a local model there must get no tools", got)
	}
	rememberDaemonFeatures(protocol.HandshakeResponse{Features: []string{protocol.FeatureLocalModels}})
	if got := promptCapabilities(); len(got) != 2 {
		t.Errorf("a daemon naming the feature was declared %v", got)
	}
}

// /agent and /web are kept for the next start, privately.
func TestTheLocalChoicesAreKeptAcrossRestarts(t *testing.T) {
	path := resetLocalState(t)
	if err := setLocalPolicy(localPolicy{Agent: true}); err != nil {
		t.Fatal(err)
	}
	localState.mu.Lock()
	localState.loaded, localState.policy = false, localPolicy{} // a new process
	localState.mu.Unlock()
	if p := currentLocalPolicy(); !p.Agent || p.Web {
		t.Errorf("after a restart the policy is %+v, want agent on and web off", p)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the policy file is %v (%v), want 0600", info.Mode().Perm(), err)
		}
	}
}

func TestAgentAndWebSayWhatTheyDo(t *testing.T) {
	resetLocalState(t)
	setLocalActive(true)
	m := newTestModel()
	say := func(name, args string) string {
		t.Helper()
		next, _ := m.handleExtraSlash(name, args)
		m = next.(chatModel)
		return m.turns[len(m.turns)-1].text
	}
	if got := say("agent", ""); !strings.Contains(got, "Agent mode is off") {
		t.Errorf("/agent as installed says %q", got)
	}
	if got := say("agent", "on"); !strings.Contains(got, "Agent mode is on") || !strings.Contains(got, "/web on") {
		t.Errorf("/agent on says %q", got)
	}
	if !currentLocalPolicy().Agent {
		t.Error("/agent on did not turn it on")
	}
	if got := say("web", ""); !strings.Contains(got, "nothing is sent to the internet") {
		t.Errorf("/web as installed says %q", got)
	}
	if got := say("web", "on"); !strings.Contains(got, "sends text to the internet") || !currentLocalPolicy().Web {
		t.Errorf("/web on says %q", got)
	}
	if got := say("agent", "sometimes"); !strings.Contains(got, "takes on or off") {
		t.Errorf("/agent with a word it does not know says %q", got)
	}
	setLocalActive(false)
	if got := say("web", "off"); !strings.Contains(got, "cloud provider") {
		t.Errorf("/web off on a cloud provider does not say when it applies: %q", got)
	}
}

func TestTheLineUnderThePromptSaysWhatALocalModelMayDo(t *testing.T) {
	resetLocalState(t)
	const local = "http://127.0.0.1:11434/v1"
	if got := localModeLabel(local); got != "tools off" {
		t.Errorf("as installed: %q", got)
	}
	_ = setLocalPolicy(localPolicy{Agent: true})
	if got := localModeLabel(local); got != "tools on · web off" {
		t.Errorf("/agent on: %q", got)
	}
	if got := localModeLabel("https://openrouter.ai/api/v1"); got != "" {
		t.Errorf("a cloud provider got a local label: %q", got)
	}
}

func TestConnectNamesLocalServersWithoutAKeyStep(t *testing.T) {
	for _, word := range []string{"local", "ollama", "LM-Studio", "vllm"} {
		if base, ok := connectBaseArg(word); !ok || base != localRowID {
			t.Errorf("connectBaseArg(%q) = %q, %v; want the local list", word, base, ok)
		}
	}
	if base, ok := connectBaseArg("http://localhost:11434/v1/"); !ok || base != "http://localhost:11434/v1" {
		t.Errorf("connectBaseArg(local address) = %q, %v", base, ok)
	}
	if connectWithKey("/connect ollama") || connectWithKey("/connect local") {
		t.Error("a local server's name is refused as a probable key")
	}
}

// localDaemon answers discovery with servers and a connect with acceptance.
func localDaemon(t *testing.T, servers []protocol.LocalServer) *recordingDaemon {
	t.Helper()
	return startRequestRecorder(t, []string{protocol.FeatureLocalModels}, func(raw json.RawMessage) any {
		if strings.Contains(string(raw), `"local":true`) {
			return protocol.LocalResponse{Ok: true, Servers: servers}
		}
		var req protocol.ConnectRequest
		_ = json.Unmarshal(raw, &req)
		return protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectAccepted, InUse: true,
			APIBase: req.APIBase, Model: "qwen2.5-coder:3b", ModelCount: 1}
	})
}

// THE SETUP'S "ON THIS COMPUTER": found servers are listed with their models,
// one with none says to download one, and picking one connects it with no key.
func TestTheSetupFindsALocalServerAndConnectsWithNoKey(t *testing.T) {
	resetLocalState(t)
	const ollamaBase = "http://127.0.0.1:11434/v1"
	daemon := localDaemon(t, []protocol.LocalServer{
		{Kind: "ollama", Name: "Ollama", APIBase: ollamaBase, Models: []string{"qwen2.5-coder:3b"}},
		{Kind: "lmstudio", Name: "LM Studio", APIBase: "http://127.0.0.1:1234/v1"},
	})

	m := firstRunModel(t, 100, 40)
	if !strings.Contains(plainScreen(m), "On this computer") {
		t.Fatalf("the provider list does not offer this computer:\n%s", plainScreen(m))
	}
	m = typeText(m, "ollama")
	m, cmd := pressEnter(m)
	if m.state != stateSetup || m.setup.step != setupLocal || !m.setup.loading {
		t.Fatalf("picking this computer: state %v, step %v, loading %v", m.state, m.setup.step, m.setup.loading)
	}
	found, ok := findMsg[localServersMsg](cmd)
	if !ok {
		t.Fatal("no discovery reached the daemon")
	}
	next, _ := m.Update(found)
	m = next.(chatModel)
	screen := plainScreen(m)
	for _, want := range []string{"Ollama", "127.0.0.1:11434 · 1 model", "LM Studio", "no model downloaded yet"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the list does not say %q:\n%s", want, screen)
		}
	}

	m.setup.picker.selectID("http://127.0.0.1:1234/v1")
	m, _ = pressEnter(m)
	if m.setup.step != setupLocal || !strings.Contains(m.setup.notice, "no model yet") {
		t.Errorf("picking a server with no model: step %v, notice %q", m.setup.step, m.setup.notice)
	}

	m.setup.picker.selectID(ollamaBase)
	m, cmd = pressEnter(m)
	if m.setup.step != setupChecking || m.state == stateConnect {
		t.Fatalf("picking Ollama: state %v, step %v; want the checking screen and no key step", m.state, m.setup.step)
	}
	if !strings.Contains(plainScreen(m), "for its models") {
		t.Errorf("the checking screen does not say what it is asking:\n%s", plainScreen(m))
	}
	if _, ok := connectAnswerOf(cmd); !ok {
		t.Fatal("no connect reached the daemon")
	}
	var connects []protocol.ConnectRequest
	for _, req := range daemon.connectRequests(t) {
		if req.Connect {
			connects = append(connects, req)
		}
	}
	if len(connects) != 1 || connects[0].APIBase != ollamaBase || connects[0].APIKey != "" {
		t.Errorf("connect requests %+v, want one to %s with no key", connects, ollamaBase)
	}
}

func TestNoServerFoundSaysHowToStartOne(t *testing.T) {
	resetLocalState(t)
	localDaemon(t, nil)
	m := newTestModel()
	next, cmd := m.handleLocalSlash("connect", "local")
	m = next.(chatModel)
	found, ok := findMsg[localServersMsg](cmd)
	if !ok {
		t.Fatal("/connect local did not look")
	}
	next, _ = m.Update(found)
	m = next.(chatModel)
	if !strings.Contains(m.setup.notice, "ollama serve") {
		t.Errorf("no server found says %q, want how to start one", m.setup.notice)
	}
}

// /connect with a local address goes straight to connecting, with no key, and
// a refusal comes back as the reason.
func TestALocalAddressConnectsWithNoKeyStep(t *testing.T) {
	resetLocalState(t)
	daemon := localDaemon(t, nil)
	m := newTestModel()
	next, cmd := m.handleLocalSlash("connect", "http://localhost:8080/v1")
	m = next.(chatModel)
	if m.setup.step != setupChecking || m.state == stateConnect {
		t.Fatalf("/connect <local address>: state %v, step %v; want no key step", m.state, m.setup.step)
	}
	if _, ok := connectAnswerOf(cmd); !ok {
		t.Fatal("no connect reached the daemon")
	}
	if got := daemon.connectRequests(t); len(got) != 1 || got[0].APIKey != "" || got[0].APIBase != "http://localhost:8080/v1" {
		t.Errorf("sent %+v", got)
	}
	next, _ = m.Update(connectResultMsg{resp: protocol.ConnectResponse{Error: "nothing answered at http://localhost:8080/v1"}})
	m = next.(chatModel)
	if m.state == stateSetup {
		t.Error("a refused /connect <address> left the setup open with no list to go back to")
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, "nothing answered") {
		t.Errorf("the reason is not in the transcript: %q", last)
	}
}

func TestAFitIsExplainedOnceAndOnlyWhenItChangedSomething(t *testing.T) {
	resetLocalState(t)
	fitted := protocol.LocalContext{Model: "qwen2.5-coder:3b", Window: 4096, BudgetChars: 3072, Fitted: true,
		Raise: "start Ollama with a bigger context, for example: OLLAMA_CONTEXT_LENGTH=16384 ollama serve"}
	note := localFitNote(fitted, localPolicy{})
	for _, want := range []string{"4,096 tokens", "3,072 characters", "nothing is cut off", "OLLAMA_CONTEXT_LENGTH=16384"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say %q: %q", want, note)
		}
	}
	if got := localFitNote(protocol.LocalContext{Window: 32768, BudgetChars: 32000, AgentFits: true}, localPolicy{Agent: true}); got != "" {
		t.Errorf("a fit that changed nothing was explained: %q", got)
	}
	if got := localFitNote(protocol.LocalContext{Window: 4096, BudgetChars: 32000}, localPolicy{Agent: true}); !strings.Contains(got, "Agent mode needs at least 8,192") {
		t.Errorf("agent mode on in a small window is not warned about: %q", got)
	}

	m := newTestModel()
	msg := localFitMsg{key: "k", resp: protocol.LocalResponse{Ok: true, Context: &fitted}}
	next, _ := m.Update(msg)
	m = next.(chatModel)
	before := len(m.turns)
	next, _ = m.Update(msg)
	m = next.(chatModel)
	if len(m.turns) != before {
		t.Error("the same fit was explained twice")
	}
	if !strings.Contains(m.turns[before-1].text, "3,072 characters") {
		t.Errorf("the fit was not explained: %q", m.turns[before-1].text)
	}
}

// What the app learns about the provider decides the capabilities, and a
// local one is fitted once per address and model.
func TestLearningTheProviderInForceSetsLocalAndAsksForOneFit(t *testing.T) {
	resetLocalState(t)
	m := newTestModel()
	local := connInfoMsg{resp: protocol.ConnectResponse{Ok: true, Model: "qwen2.5-coder:3b",
		ConnectInForce: protocol.ConnectInForce{KeySource: protocol.KeySourceNone, ActiveAPIBase: "http://127.0.0.1:11434/v1"}}}
	next, cmd := m.Update(local)
	m = next.(chatModel)
	if !localActive() {
		t.Fatal("a local provider in force was not noted")
	}
	if cmd == nil {
		t.Fatal("no fit was asked for")
	}
	if _, cmd = m.Update(local); cmd != nil {
		t.Error("the same address and model was fitted twice")
	}
	next, _ = m.Update(connInfoMsg{resp: protocol.ConnectResponse{Ok: true, InUse: true,
		ConnectInForce: protocol.ConnectInForce{KeySource: protocol.KeySourceStored,
			ActiveAPIBase: "https://openrouter.ai/api/v1", ActiveMaskedKey: "sk-or-…abcd"}}})
	_ = next.(chatModel)
	if localActive() {
		t.Error("moving to a cloud provider left local mode on")
	}
	_ = tea.Quit
}

// handshakeRecorder is a daemon that names the given features and keeps the
// capabilities each connection declared.
func handshakeRecorder(t *testing.T, features []string) func() [][]string {
	t.Helper()
	var mu sync.Mutex
	var seen [][]string
	addr := testAddress(t)
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
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
				var hs protocol.HandshakeRequest
				if json.NewDecoder(conn).Decode(&hs) != nil {
					return
				}
				mu.Lock()
				seen = append(seen, append([]string{}, hs.Capabilities...))
				mu.Unlock()
				_ = json.NewEncoder(conn).Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true, Features: features})
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), seen...)
	}
}

// A one-shot run never asked what is in force: the handshake tells it, and
// with a local model in force it connects again declaring no tools. With a
// cloud provider, or a daemon too old to say, it connects once as before.
//
// Neuter check: make connectWithPolicy return its first session whatever the
// handshake says, and the local case keeps its tools.
func TestAQuestionConnectsAgainOnlyWhenALocalModelChangesWhatItDeclares(t *testing.T) {
	resetLocalState(t)
	seen := handshakeRecorder(t, []string{protocol.FeatureLocalModels, protocol.FeatureLocalInForce})
	sess, err := connectOneShot("test-client", []string{protocol.CapToolApproval})
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()
	got := seen()
	if len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 0 {
		t.Fatalf("handshakes declared %v; want tool approval, then none for a local model", got)
	}
	if !localActive() {
		t.Error("the handshake's word that a local model is in force was not kept")
	}
}

func TestACloudProviderStillConnectsOnce(t *testing.T) {
	for _, features := range [][]string{{protocol.FeatureLocalModels}, nil} {
		resetLocalState(t)
		seen := handshakeRecorder(t, features)
		sess, err := connectOneShot("test-client", []string{protocol.CapToolApproval})
		if err != nil {
			t.Fatal(err)
		}
		_ = sess.Close()
		if got := seen(); len(got) != 1 || len(got[0]) != 1 || got[0][0] != protocol.CapToolApproval {
			t.Errorf("features %v: handshakes declared %v; want one, with tool approval", features, got)
		}
	}
}

func TestTheLocalConnectMessageNamesNoKeyAndNoTest(t *testing.T) {
	got := formatConnectResult(connectResultMsg{resp: protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectAccepted,
		InUse: true, APIBase: "http://127.0.0.1:11434/v1", Model: "qwen2.5-coder:3b", ModelCount: 1}}, "")
	for _, want := range []string{"needs no key", "in use now", "Prompts go to qwen2.5-coder:3b", "1 model on this server"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message does not say %q: %q", want, got)
		}
	}
	for _, bad := range []string{"Key  is", "NOT answered", "1 models"} {
		if strings.Contains(got, bad) {
			t.Errorf("the message says %q: %q", bad, got)
		}
	}
}
