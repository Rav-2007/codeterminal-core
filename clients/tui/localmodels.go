package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// MODELS ON THIS COMPUTER (1.3.0).
//
// Ollama, LM Studio, llama.cpp and vLLM, found and connected from inside the
// app with no key (the setup's "On this computer", or /connect local), and
// held to what a small model on a laptop does well. Measured with Ollama and
// qwen2.5-coder:3b on 2026-10-23: chat with code search answered correctly;
// agent mode did not -- with Ollama's default window the request was cut from
// 4,893 tokens to 2,050 and the model invented a tool, and with the whole
// request it still called none. So for a model on this computer:
//
//   - agent mode is OFF until /agent on: the model gets no tools, and the
//     daemon searches the code for it, which is what worked;
//   - the internet tools are OFF until /web on, even with agent mode on: a
//     setup chosen for privacy sends nothing out unless asked to (the owner's
//     decision, 2026-10-23);
//   - the code sent with a question is fitted into the model's real context
//     window, and the app says so once, with how to give the model more room.
//
// All three are what this client asks of the daemon. A client that does not
// ask -- the VS Code extension -- is served exactly as before.

// localRowID is the setup list's "On this computer" row, and the word
// /connect takes for it.
const localRowID = "local"

// localConnectWords are what /connect accepts for "a server on this computer".
var localConnectWords = map[string]bool{
	"local": true, "ollama": true, "lmstudio": true, "lm-studio": true,
	"llamacpp": true, "llama.cpp": true, "llama-server": true, "vllm": true,
}

// localPolicy is what a model on this computer may do. Both off until the
// person turns them on, and kept across restarts.
type localPolicy struct {
	Agent bool `json:"agent"`
	Web   bool `json:"web"`
}

// localState is this client's view of local models. One per process.
var localState struct {
	mu sync.Mutex
	// active: the provider in force is a server on this machine.
	active bool
	// supported: the daemon names protocol.FeatureLocalModels, so it honours
	// CapNoNetworkTools. Without it, a local model is offered no tools at all.
	supported bool
	policy    localPolicy
	loaded    bool
	// fitKey is the address and model the last fit was asked for, and noted
	// the fits already explained, so each is said once.
	fitKey  string
	lastFit *protocol.LocalContext
	noted   map[string]bool
}

// localPolicyPath is where the policy is kept. A var so tests keep theirs in a
// temporary folder; "" keeps it in memory only.
var localPolicyPath = func() string {
	dir, err := stateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "local-models.json")
}

func loadLocalPolicyLocked() {
	if localState.loaded {
		return
	}
	localState.loaded = true
	path := localPolicyPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var p localPolicy
	if json.Unmarshal(data, &p) == nil {
		localState.policy = p
	}
}

// currentLocalPolicy is the policy in force.
func currentLocalPolicy() localPolicy {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	loadLocalPolicyLocked()
	return localState.policy
}

// setLocalPolicy puts p in force and keeps it.
func setLocalPolicy(p localPolicy) error {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	localState.loaded = true
	localState.policy = p
	path := localPolicyPath()
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func setLocalActive(active bool) {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	localState.active = active
}

func localActive() bool {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	return localState.active
}

// rememberDaemonFeatures notes whether the daemon honours what a local model's
// questions ask of it. Called on every handshake.
func rememberDaemonFeatures(hs protocol.HandshakeResponse) {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	localState.supported = hasFeature(hs, protocol.FeatureLocalModels)
}

// promptCapabilities is what a question declares in its handshake.
//
// CapToolApproval is the switch for agent mode (daemonconn.go), so leaving it
// out is how a local model gets no tools. With tools but no internet, the
// question also asks for none of the tools that reach it -- and only of a
// daemon that says it honours that; an older one would not, so then the model
// gets no tools at all rather than tools with the internet in them.
func promptCapabilities() []string {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	if !localState.active {
		return []string{protocol.CapToolApproval}
	}
	loadLocalPolicyLocked()
	switch {
	case !localState.policy.Agent:
		return nil
	case localState.policy.Web:
		return []string{protocol.CapToolApproval}
	case !localState.supported:
		return nil
	}
	return []string{protocol.CapToolApproval, protocol.CapNoNetworkTools}
}

// connectWithPolicy connects for one question declaring caps, and keeps this
// client's idea of "a model on this computer" in step with the daemon's.
//
// Capabilities are declared before the daemon answers, and the answer names
// FeatureLocalInForce when a local model is in force. When that changes what
// the question should declare -- a one-shot run that never asked, a provider
// switched by another client -- it connects again declaring that. With a
// cloud provider in force, and with a daemon too old to say, it connects once,
// exactly as before.
func connectWithPolicy(clientName string, caps []string) (*daemonSession, error) {
	sess, err := connectToDaemon(clientName, caps...)
	if err != nil || !hasFeature(sess.handshake, protocol.FeatureLocalModels) {
		return sess, err
	}
	local := hasFeature(sess.handshake, protocol.FeatureLocalInForce)
	if local == localActive() {
		return sess, nil
	}
	setLocalActive(local)
	want := promptCapabilities()
	if strings.Join(want, ",") == strings.Join(caps, ",") {
		return sess, nil
	}
	_ = sess.Close()
	return connectToDaemon(clientName, want...)
}

// connectOneShot is connectWithPolicy for `mochiii --prompt`: caps is what the
// run may declare at most (none when no person is at the terminal).
func connectOneShot(clientName string, caps []string) (*daemonSession, error) {
	if len(caps) == 0 {
		return connectToDaemon(clientName)
	}
	return connectWithPolicy(clientName, promptCapabilities())
}

// localConnectedText is the connect message for a server on this computer:
// there is no key to name, and no model was tried, on purpose.
func localConnectedText(r protocol.ConnectResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Connected to %s. It needs no key", providerLabel(r.APIBase))
	if r.InUse {
		b.WriteString(", and it is in use now.")
	} else {
		b.WriteString(", but a key in the environment is what this daemon is sending.")
	}
	if r.Model != "" {
		fmt.Fprintf(&b, "\n\nPrompts go to %s (%s on this server: /model lists them, /model <name> switches).",
			sanitizeText(r.Model), countOf(r.ModelCount, "model"))
	}
	return b.String()
}

// localModeLabel is what the line under the prompt adds for a model on this
// computer: whether it has tools, and whether they reach the internet.
func localModeLabel(base string) string {
	if !isLocalBase(base) {
		return ""
	}
	p := currentLocalPolicy()
	switch {
	case !p.Agent:
		return "tools off"
	case p.Web:
		return "tools on · web on"
	}
	return "tools on · web off"
}

// errDaemonPredatesLocal is said when the daemon serving this folder is older
// than this client.
var errDaemonPredatesLocal = errors.New("the background service for this folder is older than this Mochiii and " +
	"cannot look for models on this computer; run mochiii --stop, then mochiii again")

// sendLocal performs one LocalRequest round trip.
func sendLocal(clientName string, req protocol.LocalRequest) (protocol.LocalResponse, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return protocol.LocalResponse{}, err
	}
	defer func() { _ = sess.Close() }()
	if !hasFeature(sess.handshake, protocol.FeatureLocalModels) {
		return protocol.LocalResponse{}, errDaemonPredatesLocal
	}
	req.ProtocolVersion = protocol.ProtocolVersion
	req.Local = true
	if err := sess.enc.Encode(req); err != nil {
		return protocol.LocalResponse{}, fmt.Errorf("sending the request: %w", err)
	}
	var resp protocol.LocalResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return protocol.LocalResponse{}, fmt.Errorf("reading the answer: %w", err)
	}
	return resp, nil
}

// localServersMsg is the answer to a discovery.
type localServersMsg struct {
	servers []protocol.LocalServer
	err     error
}

// localFitMsg is the answer to a fit, for the address and model it was asked for.
type localFitMsg struct {
	key  string
	resp protocol.LocalResponse
	err  error
}

func discoverLocalCmd(clientName string) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendLocal(clientName, protocol.LocalRequest{Discover: true})
		if err == nil && !resp.Ok {
			err = errors.New(firstNonEmptyText(resp.Error, "the daemon could not look"))
		}
		return localServersMsg{servers: resp.Servers, err: err}
	}
}

func fitLocalCmd(clientName, key string) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendLocal(clientName, protocol.LocalRequest{Fit: true})
		return localFitMsg{key: key, resp: resp, err: err}
	}
}

// afterConnInfo runs when the app learns what is in force: it notes whether
// that is a model on this computer, and fits the code context into it once
// for each address and model.
func (m chatModel) afterConnInfo() (tea.Model, tea.Cmd) {
	base := m.conn.connectedBase()
	local := isLocalBase(base)
	setLocalActive(local)
	if !local {
		return m, nil
	}
	key := base + "\x00" + m.statusModel()
	localState.mu.Lock()
	if localState.fitKey == key {
		localState.mu.Unlock()
		return m, nil
	}
	localState.fitKey = key
	localState.mu.Unlock()
	return m, fitLocalCmd(m.clientName, key)
}

// handleLocalFit says, once, what the fit changed -- and nothing when it
// changed nothing.
func (m chatModel) handleLocalFit(msg localFitMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil || msg.resp.Context == nil {
		return m, nil
	}
	c := msg.resp.Context
	localState.mu.Lock()
	localState.lastFit = c
	if localState.noted == nil {
		localState.noted = map[string]bool{}
	}
	noteKey := fmt.Sprintf("%s\x00%d\x00%d", msg.key, c.Window, c.BudgetChars)
	seen := localState.noted[noteKey]
	localState.noted[noteKey] = true
	localState.mu.Unlock()
	if seen {
		return m, nil
	}
	if note := localFitNote(*c, currentLocalPolicy()); note != "" {
		return m.sayLocally(note)
	}
	return m, nil
}

// localFitNote explains a fit in a sentence or three, or is "" when there is
// nothing to say.
func localFitNote(c protocol.LocalContext, p localPolicy) string {
	if c.Window <= 0 {
		return ""
	}
	who := sanitizeText(c.Model)
	var b strings.Builder
	switch {
	case c.Fitted && c.BudgetChars == 0:
		fmt.Fprintf(&b, "%s has room for only %s tokens here, too little for any of your code next to a question, "+
			"so answers will not see your project.", who, groupDigits(c.Window))
	case c.Fitted:
		fmt.Fprintf(&b, "%s has room for %s tokens here, so Mochiii sends at most %s characters of your code with "+
			"each question, so that nothing is cut off.", who, groupDigits(c.Window), groupDigits(c.BudgetChars))
	}
	if p.Agent && !c.AgentFits {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "Agent mode needs at least %s tokens and has %s, so it will not work well.",
			groupDigits(8192), groupDigits(c.Window))
	}
	if b.Len() == 0 {
		return ""
	}
	if c.Raise != "" {
		b.WriteString(" To give it more room, " + c.Raise + ".")
	}
	return b.String()
}

// groupDigits writes 32768 as 32,768.
func groupDigits(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// localProviderRow is the setup list's entry for a server on this computer.
func localProviderRow(currentBase string) pickerRow {
	return pickerRow{
		ID: localRowID, Label: "On this computer",
		Hint:    "Ollama, LM Studio, llama.cpp or vLLM — no key",
		Match:   "local ollama lm studio lmstudio llama.cpp llamacpp vllm offline private",
		Current: isLocalBase(currentBase),
	}
}

// beginLocalSetup opens the setup at "On this computer", looking for servers.
// The caller has set viaList: Esc goes back to the provider list from there.
func (m chatModel) beginLocalSetup() (tea.Model, tea.Cmd) {
	m.state = stateSetup
	m.setup.step = setupLocal
	m.setup.loading = true
	m.setup.notice = ""
	m.setup.localServers = nil
	return m, tea.Batch(m.spinner.Tick, discoverLocalCmd(m.clientName))
}

// localNoneFound is said when no server answered.
const localNoneFound = "No model server is running on this computer. Start one and pick this again — " +
	"for Ollama: ollama serve, then ollama pull qwen2.5-coder:7b. LM Studio: start its server. " +
	"Another address: /connect http://localhost:<port>/v1"

// handleLocalServers fills the list with what discovery found.
func (m chatModel) handleLocalServers(msg localServersMsg) (tea.Model, tea.Cmd) {
	if m.state != stateSetup || m.setup.step != setupLocal {
		return m, nil // the person left before the answer came
	}
	m.setup.loading = false
	m.setup.localServers = msg.servers
	m.setup.picker = newListPicker("Model servers on this computer", localServerRows(msg.servers))
	m.setup.picker.placeholder = "search"
	switch {
	case msg.err != nil:
		m.setup.notice = sanitizeText(msg.err.Error())
	case len(msg.servers) == 0:
		m.setup.notice = localNoneFound
	}
	return m, nil
}

// localServerRows is one row per server found.
func localServerRows(servers []protocol.LocalServer) []pickerRow {
	var rows []pickerRow
	for _, s := range servers {
		host := s.APIBase
		if u, err := url.Parse(s.APIBase); err == nil && u.Host != "" {
			host = u.Host
		}
		hint := host + " · " + countOf(len(s.Models), "model")
		if len(s.Models) == 0 {
			hint = host + " · no model downloaded yet"
		}
		rows = append(rows, pickerRow{ID: s.APIBase, Label: sanitizeText(s.Name), Hint: sanitizeText(hint),
			Match: s.Kind + " " + s.Name})
	}
	return rows
}

// countOf writes 1 model, 2 models.
func countOf(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// setupLocalKey is the keys of the list of servers.
func (m chatModel) setupLocalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.setup.viaList {
			m.setup.step = setupProvider
			m.setup.loading = false
			m.setup.notice = ""
			m.setup.picker = m.providerPicker()
			return m, nil
		}
		cmd := m.leaveSetup(m.setupLeavingNote())
		return m, cmd
	case "enter":
		if m.setup.loading {
			return m, nil
		}
		row, ok := m.setup.picker.selected()
		if !ok {
			return m, nil
		}
		for _, s := range m.setup.localServers {
			if s.APIBase == row.ID && len(s.Models) == 0 {
				m.setup.notice = sanitizeText(s.Name) + " has no model yet. Download one first" + localPullHint(s.Kind) +
					", then pick it again."
				return m, nil
			}
		}
		return m.setupConnectLocal(row.ID)
	}
	if m.setup.loading {
		return m, nil
	}
	return m.setupListKey(msg), nil
}

func localPullHint(kind string) string {
	if kind == "ollama" {
		return " — for example: ollama pull qwen2.5-coder:7b"
	}
	return ""
}

// setupConnectLocal connects to the server at base with no key; the daemon's
// answer goes to handleSetupConnectResult, and on to the list of its models.
func (m chatModel) setupConnectLocal(base string) (tea.Model, tea.Cmd) {
	m.state = stateSetup
	m.setup.step = setupChecking
	m.setup.sent = true
	m.setup.notice = ""
	m.setup.base = base
	m.setup.provider = ""
	m.setup.checking = providerLabel(base)
	return m, tea.Batch(m.spinner.Tick, submitConnect(m.clientName, protocol.ConnectRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Connect:         true,
		APIBase:         base,
	}))
}

// localConnectFailed goes back to the list of servers with the daemon's reason.
func (m chatModel) localConnectFailed(reason string) (tea.Model, tea.Cmd) {
	m.state = stateSetup
	m.setup.step = setupLocal
	m.setup.notice = sanitizeText(reason)
	if len(m.setup.localServers) == 0 {
		// Reached by /connect <address>: there is no list to go back to, and
		// the reason is already in the transcript (handleSetupConnectResult).
		cmd := m.leaveSetup("")
		return m, cmd
	}
	return m, nil
}

// renderSetupLocal draws the list of servers.
func (m chatModel) renderSetupLocal(picker listPicker, inner, listRows, detail int) (help, body string) {
	esc := setupEscCancel
	if m.setup.viaList {
		esc = setupEscBack
	}
	if m.setup.loading {
		return esc, m.spinner.View() + " " + wrapText(setupTextStyle,
			"looking for Ollama, LM Studio, llama.cpp and vLLM on this computer", inner-2)
	}
	if detail == setupFull {
		body = wrapText(pickerHintStyle, "Models here run on this computer: no key, and nothing you ask leaves it. "+
			"Mochiii gives them your code to read, and no tools until /agent on.", inner) + "\n\n"
	}
	return setupListHelp + esc, body + picker.view(inner, listRows)
}

// handleLocalModeCommand is /agent and /web.
func (m chatModel) handleLocalModeCommand(name, args string) (tea.Model, tea.Cmd) {
	p := currentLocalPolicy()
	var on bool
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "", "status":
		return m.sayLocally(localModeStatus(name, p, localActive()))
	case "on":
		on = true
	case "off":
		on = false
	default:
		return m.sayLocally(fmt.Sprintf("/%s takes on or off: /%s on, /%s off; /%s on its own says which it is.", name, name, name, name))
	}
	if name == "web" {
		p.Web = on
	} else {
		p.Agent = on
	}
	if err := setLocalPolicy(p); err != nil {
		return m.sayLocally("could not keep that for next time: " + sanitizeText(err.Error()) + " (it applies until you quit)")
	}
	reply := localModeStatus(name, p, localActive())
	localState.mu.Lock()
	last := localState.lastFit
	localState.mu.Unlock()
	if name == "agent" && on && last != nil && last.Window > 0 && !last.AgentFits {
		reply += fmt.Sprintf(" The model in use has room for %s tokens, and agent mode needs at least %s.",
			groupDigits(last.Window), groupDigits(8192))
		if last.Raise != "" {
			reply += " To give it more room, " + last.Raise + "."
		}
	}
	return m.sayLocally(reply)
}

// localModeStatus says what /agent or /web is set to and what that means.
func localModeStatus(name string, p localPolicy, active bool) string {
	var s string
	switch {
	case name == "agent" && p.Agent:
		s = "Agent mode is on for models on this computer: they get tools to read files, search and propose edits. " +
			"It needs a model that can use tools. /agent off turns it off."
		if !p.Web {
			s += " The internet tools stay off: /web on allows them."
		}
	case name == "agent":
		s = "Agent mode is off for models on this computer: Mochiii searches your code and sends what matters with " +
			"each question, and the model gets no tools. /agent on gives it tools — it needs a model that can use them."
	case p.Web:
		s = "Web tools are on for models on this computer: in agent mode they can search the web and fetch pages, " +
			"which sends text to the internet. /web off turns them off."
		if !p.Agent {
			s += " Agent mode is off, so nothing uses them until /agent on."
		}
	default:
		s = "Web tools are off for models on this computer: nothing is sent to the internet. " +
			"/web on lets agent mode search the web and fetch pages."
	}
	if !active {
		s += " (You are using a cloud provider now; this applies when you use a model on this computer, " +
			"from /provider or /connect local.)"
	}
	return s
}
