package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// AN AGENT THAT FITS THE MODEL (1.4.0).
//
// Whether a model on this computer can drive the agent is a property of that
// model: measured 2026-10-24, qwen2.5-coder:3b -- which Ollama lists as able to
// use tools -- made no valid tool call in 15 tries. So with agent mode on, the
// app asks the daemon to try the model in force (LocalRequest.Check), keeps
// the answer, and asks each question the way the model can take it:
//
//	chat     no tools: the code that fits, and edits written as text for review
//	compact  the compact agent -- five tools, short instructions -- for a model
//	         whose window cannot hold the full agent (CapCompactAgent)
//	full     the full agent
//
// Until a model has been checked its questions go as chat, never as an agent
// request that may not fit. A daemon too old to check is asked as in 1.3.0.

// localCheckEntry is one check's result, kept with the policy so a restart does
// not repeat it. A check is for one address, model and window: a model whose
// server was given more room is checked again.
type localCheckEntry struct {
	Way    string `json:"way"`
	Reason string `json:"reason"`
	Tried  int    `json:"tried"`
	Passed int    `json:"passed"`
}

// localCheckMsg is the answer to a check, for the key it was asked for.
type localCheckMsg struct {
	key   string
	model string
	resp  protocol.LocalResponse
	err   error
}

func localCheckKey(base, model string, window int) string {
	return base + "\x00" + model + "\x00" + strconv.Itoa(window)
}

// currentLocalCheckLocked is the check for the model in force, if there is one,
// and whether one is being made now. localState.mu is held.
func currentLocalCheckLocked() (entry localCheckEntry, ok, checking bool) {
	loadLocalPolicyLocked()
	key := localCheckKey(localState.base, localState.model, localState.window)
	entry, ok = localState.policy.Checked[key]
	return entry, ok, localState.checking == key
}

// checkedCapabilities is promptCapabilities for a daemon that checks models:
// what a question declares, from the check of the model in force.
// localState.mu is held.
func checkedCapabilitiesLocked() []string {
	entry, ok, _ := currentLocalCheckLocked()
	if !ok {
		return nil
	}
	var caps []string
	switch entry.Way {
	case protocol.LocalWayCompact:
		caps = []string{protocol.CapToolApproval, protocol.CapCompactAgent}
	case protocol.LocalWayFull:
		caps = []string{protocol.CapToolApproval}
	default:
		return nil
	}
	if !localState.policy.Web {
		caps = append(caps, protocol.CapNoNetworkTools)
	}
	return caps
}

// checkedModeLabel is localModeLabel for a daemon that checks models.
func checkedModeLabel() string {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	entry, ok, checking := currentLocalCheckLocked()
	web := "web off"
	if localState.policy.Web {
		web = "web on"
	}
	switch {
	case checking:
		return "agent: checking the model"
	case !ok:
		return "agent: not checked yet"
	case entry.Way == protocol.LocalWayFull:
		return "agent · " + web
	case entry.Way == protocol.LocalWayCompact:
		return "agent (compact) · " + web
	case entry.Passed >= 2:
		return "chat · its window is too small for agent mode"
	}
	return "chat · this model can't use tools"
}

func checkLocalCmd(clientName, key, model string) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendLocal(clientName, protocol.LocalRequest{Check: true})
		return localCheckMsg{key: key, model: model, resp: resp, err: err}
	}
}

// maybeCheckLocal starts a check of the model in force when agent mode is on
// for it and it has none for its window -- or always, when force. It says it
// is checking, because the questions asked meanwhile go as chat.
func (m chatModel) maybeCheckLocal(force bool) (chatModel, tea.Cmd, string) {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	loadLocalPolicyLocked()
	if !localState.active || !localState.checkSupported || !localState.policy.Agent || localState.model == "" || !localState.fitted {
		return m, nil, ""
	}
	_, ok, checking := currentLocalCheckLocked()
	if checking || (ok && !force) {
		return m, nil, ""
	}
	key := localCheckKey(localState.base, localState.model, localState.window)
	localState.checking = key
	note := fmt.Sprintf("Checking whether %s can use tools: a few small test questions, on this computer only. "+
		"Until it is done, questions go without tools.", sanitizeText(localState.model))
	return m, checkLocalCmd(m.clientName, key, localState.model), note
}

// handleLocalCheck keeps a check's answer and says it in one line.
func (m chatModel) handleLocalCheck(msg localCheckMsg) (tea.Model, tea.Cmd) {
	localState.mu.Lock()
	if localState.checking == msg.key {
		localState.checking = ""
	}
	localState.mu.Unlock()
	if msg.err != nil || !msg.resp.Ok || msg.resp.Check == nil {
		reason := "the background service did not answer"
		if msg.err != nil {
			reason = msg.err.Error()
		} else if msg.resp.Error != "" {
			reason = msg.resp.Error
		}
		return m.sayLocally("Could not check " + sanitizeText(msg.model) + ": " + sanitizeText(reason) +
			". Questions go without tools; /agent check tries again.")
	}
	c := msg.resp.Check
	entry := localCheckEntry{Way: c.Way, Reason: c.Reason, Tried: c.Tried, Passed: c.Passed}
	if err := keepLocalCheck(msg.key, entry); err != nil {
		return m.sayLocally("could not keep the check for next time: " + sanitizeText(err.Error()))
	}
	return m.sayLocally(localCheckText(*c))
}

// keepLocalCheck stores a check with the policy.
func keepLocalCheck(key string, entry localCheckEntry) error {
	p := currentLocalPolicy()
	checked := make(map[string]localCheckEntry, len(p.Checked)+1)
	for k, v := range p.Checked {
		checked[k] = v
	}
	checked[key] = entry
	p.Checked = checked
	return setLocalPolicy(p)
}

// localCheckText is a check's answer as the person reads it.
func localCheckText(c protocol.LocalCheck) string {
	model := sanitizeText(c.Model)
	reason := sanitizeText(c.Reason)
	switch c.Way {
	case protocol.LocalWayFull:
		return fmt.Sprintf("%s works as the full agent: %s.", model, reason)
	case protocol.LocalWayCompact:
		return fmt.Sprintf("%s works as an agent: %s.", model, reason)
	}
	s := fmt.Sprintf("%s works as chat: %s.", model, reason)
	if c.Passed >= 2 && c.Raise != "" {
		s += " To give it more room, " + sanitizeText(c.Raise) + "."
	} else {
		s += " For agent mode, use a model of 7B or more that can call tools; /agent check tries this one again."
	}
	return s
}

// localAgentReply is what /agent on adds about the model in force.
func localAgentReply() string {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	if !localState.checkSupported || !localState.active {
		return ""
	}
	entry, ok, checking := currentLocalCheckLocked()
	switch {
	case checking:
		return " " + sanitizeText(localState.model) + " is being checked now."
	case ok:
		return " " + strings.TrimSuffix(localCheckText(protocol.LocalCheck{Model: localState.model, Way: entry.Way,
			Reason: entry.Reason, Tried: entry.Tried, Passed: entry.Passed}), ".") + "."
	}
	return ""
}
