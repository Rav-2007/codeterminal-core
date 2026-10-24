package tui

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// Tests for the agent that fits the model (localagent.go).

// localModelInForce puts a model on this computer in force, fitted to window,
// with a daemon that checks models or one too old to.
func localModelInForce(t *testing.T, checks bool, window int) string {
	t.Helper()
	path := resetLocalState(t)
	localState.mu.Lock()
	defer localState.mu.Unlock()
	localState.active, localState.supported, localState.checkSupported = true, true, checks
	localState.base, localState.model, localState.window, localState.fitted = "http://127.0.0.1:11434/v1", "m", window, true
	localState.fitKey = localState.base + "\x00m"
	return path
}

func withPolicy(t *testing.T, p localPolicy) {
	t.Helper()
	if err := setLocalPolicy(p); err != nil {
		t.Fatal(err)
	}
}

func checkedAs(way string, window int) map[string]localCheckEntry {
	return map[string]localCheckEntry{localCheckKey("http://127.0.0.1:11434/v1", "m", window): {Way: way, Reason: "a reason", Tried: 2, Passed: 2}}
}

// A QUESTION DECLARES WHAT THE MODEL'S CHECK FOUND: no tools until it has been
// checked, then the compact or the full agent, or none for a chat model; and a
// check made for another window does not count.
//
// Neuter check: make checkedCapabilitiesLocked return []string{CapToolApproval}
// whatever the check said, and the unchecked and chat models get tools.
func TestAQuestionDeclaresWhatTheModelsCheckFound(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy localPolicy
		want   []string
	}{
		{"agent mode off", localPolicy{}, nil},
		{"not checked yet", localPolicy{Agent: true}, nil},
		{"compact", localPolicy{Agent: true, Checked: checkedAs(protocol.LocalWayCompact, 8192)},
			[]string{protocol.CapToolApproval, protocol.CapCompactAgent, protocol.CapNoNetworkTools}},
		{"compact, web on", localPolicy{Agent: true, Web: true, Checked: checkedAs(protocol.LocalWayCompact, 8192)},
			[]string{protocol.CapToolApproval, protocol.CapCompactAgent}},
		{"full", localPolicy{Agent: true, Checked: checkedAs(protocol.LocalWayFull, 8192)},
			[]string{protocol.CapToolApproval, protocol.CapNoNetworkTools}},
		{"chat", localPolicy{Agent: true, Checked: checkedAs(protocol.LocalWayChat, 8192)}, nil},
		{"checked for another window", localPolicy{Agent: true, Checked: checkedAs(protocol.LocalWayFull, 4096)}, nil},
	} {
		localModelInForce(t, true, 8192)
		withPolicy(t, tc.policy)
		if got := promptCapabilities(); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: declares %v, want %v", tc.name, got, tc.want)
		}
	}
	localModelInForce(t, false, 8192)
	withPolicy(t, localPolicy{Agent: true})
	if got := promptCapabilities(); fmt.Sprint(got) != fmt.Sprint([]string{protocol.CapToolApproval, protocol.CapNoNetworkTools}) {
		t.Errorf("a daemon too old to check: declares %v, want 1.3.0's", got)
	}
}

func TestTheLineUnderThePromptSaysHowTheModelWorks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy localPolicy
		passed int
		want   string
	}{
		{"agent off", localPolicy{}, 0, "tools off"},
		{"unchecked", localPolicy{Agent: true}, 0, "agent: not checked yet"},
		{"full", localPolicy{Agent: true, Checked: checkedAs(protocol.LocalWayFull, 8192)}, 2, "agent · web off"},
		{"compact, web on", localPolicy{Agent: true, Web: true, Checked: checkedAs(protocol.LocalWayCompact, 8192)}, 2, "agent (compact) · web on"},
		{"chat: no tools", localPolicy{Agent: true, Checked: map[string]localCheckEntry{
			localCheckKey("http://127.0.0.1:11434/v1", "m", 8192): {Way: protocol.LocalWayChat, Tried: 2}}}, 0, "chat · this model can't use tools"},
		{"chat: too small", localPolicy{Agent: true, Checked: map[string]localCheckEntry{
			localCheckKey("http://127.0.0.1:11434/v1", "m", 8192): {Way: protocol.LocalWayChat, Tried: 2, Passed: 2}}}, 2, "chat · its window is too small for agent mode"},
	} {
		localModelInForce(t, true, 8192)
		withPolicy(t, tc.policy)
		if got := localModeLabel("http://127.0.0.1:11434/v1"); got != tc.want {
			t.Errorf("%s: the line says %q, want %q", tc.name, got, tc.want)
		}
	}
}

// localCheckDaemon is a daemon that checks models: each check is answered with
// check, and counted.
func localCheckDaemon(t *testing.T, check protocol.LocalCheck) *atomic.Int64 {
	t.Helper()
	var checks atomic.Int64
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
				dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
				var hs protocol.HandshakeRequest
				if dec.Decode(&hs) != nil {
					return
				}
				_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true,
					Features: []string{protocol.FeatureLocalModels, protocol.FeatureLocalAgentCheck}})
				var req protocol.LocalRequest
				if dec.Decode(&req) != nil || !req.Check {
					return
				}
				checks.Add(1)
				c := check
				_ = enc.Encode(protocol.LocalResponse{Ok: true, Check: &c})
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return &checks
}

// messagesOf runs cmd, and every command a batch of it holds, and returns the
// messages of type T they produced.
func messagesOf[T any](cmd tea.Cmd) []T {
	var out []T
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, sub := range msg {
				walk(sub)
			}
		case T:
			out = append(out, msg)
		}
	}
	walk(cmd)
	return out
}

func latestTurnText(m tea.Model) string {
	turns := m.(chatModel).turns
	if len(turns) == 0 {
		return ""
	}
	return turns[len(turns)-1].text
}

// /AGENT ON CHECKS THE MODEL ONCE AND KEEPS THE ANSWER: across a restart, and
// until /agent check asks again.
//
// Neuter checks: make maybeCheckLocal start no check, and agent mode never
// gets past "not checked yet"; drop keepLocalCheck's setLocalPolicy, and the
// check is made again after a restart.
func TestAgentOnChecksTheModelOnceAndKeepsTheAnswer(t *testing.T) {
	path := localModelInForce(t, true, 8192)
	checks := localCheckDaemon(t, protocol.LocalCheck{APIBase: "http://127.0.0.1:11434/v1", Model: "m", Window: 8192,
		Tried: 2, Passed: 2, Way: protocol.LocalWayCompact, Reason: "it called the tool in 2 of 2 tries"})
	m := newTestModel()

	next, cmd := m.handleLocalModeCommand("agent", "on")
	if !strings.Contains(latestTurnText(next), "Checking whether m can use tools") {
		t.Errorf("/agent on did not say it is checking: %q", latestTurnText(next))
	}
	answers := messagesOf[localCheckMsg](cmd)
	if len(answers) != 1 {
		t.Fatalf("/agent on made %d checks, want 1", len(answers))
	}
	next, _ = next.(chatModel).Update(answers[0])
	if !strings.Contains(latestTurnText(next), "m works as an agent: it called the tool in 2 of 2 tries") {
		t.Errorf("the answer was said as %q", latestTurnText(next))
	}
	if got := promptCapabilities(); !strings.Contains(fmt.Sprint(got), protocol.CapCompactAgent) {
		t.Errorf("after the check a question declares %v, want the compact agent", got)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"checked"`) {
		t.Errorf("the check was not kept: %s", data)
	}

	if _, cmd = next.(chatModel).handleLocalModeCommand("agent", "on"); len(messagesOf[localCheckMsg](cmd)) != 0 {
		t.Error("/agent on checked a model that was already checked")
	}

	// A restart: the state is read from the file again.
	localState.mu.Lock()
	localState.loaded, localState.policy = false, localPolicy{}
	localState.mu.Unlock()
	if got := promptCapabilities(); !strings.Contains(fmt.Sprint(got), protocol.CapCompactAgent) {
		t.Errorf("after a restart a question declares %v, want the kept check's compact agent", got)
	}

	if _, cmd = next.(chatModel).handleLocalModeCommand("agent", "check"); len(messagesOf[localCheckMsg](cmd)) != 1 {
		t.Error("/agent check did not check again")
	}
	if checks.Load() != 2 {
		t.Errorf("the daemon was asked to check %d times, want 2", checks.Load())
	}
}

// THE FIT STARTS THE CHECK when agent mode is already on: a new model, or the
// same model given a bigger window, is checked without being asked.
func TestAFitStartsTheCheckWhenAgentModeIsOn(t *testing.T) {
	localModelInForce(t, true, 0)
	localState.mu.Lock()
	localState.fitted = false
	key := localState.fitKey
	localState.mu.Unlock()
	withPolicy(t, localPolicy{Agent: true})
	checks := localCheckDaemon(t, protocol.LocalCheck{Model: "m", Window: 16384, Tried: 2, Way: protocol.LocalWayChat,
		Reason: "it did not call the tool it was given (0 of 2 tries)"})
	m := newTestModel()
	next, cmd := m.handleLocalFit(localFitMsg{key: key, resp: protocol.LocalResponse{Ok: true,
		Context: &protocol.LocalContext{Model: "m", Window: 16384, BudgetChars: 32000}}})
	if !strings.Contains(latestTurnText(next), "Checking whether m can use tools") {
		t.Errorf("the fit did not say a check started: %q", latestTurnText(next))
	}
	if answers := messagesOf[localCheckMsg](cmd); len(answers) != 1 || checks.Load() != 1 {
		t.Fatalf("%d checks after the fit, want 1", len(answers))
	}
}

func TestACheckSaysWhatToDoNext(t *testing.T) {
	chat := localCheckText(protocol.LocalCheck{Model: "qwen2.5-coder:3b", Way: protocol.LocalWayChat, Tried: 2,
		Reason: "it did not call the tool it was given (0 of 2 tries), so it answers with your code"})
	if !strings.Contains(chat, "7B") || !strings.Contains(chat, "/agent check") {
		t.Errorf("a model that cannot call tools is not told what would: %q", chat)
	}
	small := localCheckText(protocol.LocalCheck{Model: "m", Way: protocol.LocalWayChat, Tried: 2, Passed: 2,
		Reason: "its window is too small", Raise: "start Ollama with OLLAMA_CONTEXT_LENGTH=16384"})
	if !strings.Contains(small, "OLLAMA_CONTEXT_LENGTH") {
		t.Errorf("a model with too small a window is not told how to give it more: %q", small)
	}
}

// The daemon's handshake says whether it checks models; a daemon that does not
// is asked as in 1.3.0.
//
// Neuter check: drop the checkSupported line from rememberDaemonFeatures.
func TestTheDaemonSaysWhetherItChecksModels(t *testing.T) {
	resetLocalState(t)
	rememberDaemonFeatures(protocol.HandshakeResponse{Features: []string{protocol.FeatureLocalModels, protocol.FeatureLocalAgentCheck}})
	localState.mu.Lock()
	checks := localState.checkSupported
	localState.mu.Unlock()
	if !checks {
		t.Error("a daemon that checks models was not noted as one")
	}
	rememberDaemonFeatures(protocol.HandshakeResponse{Features: []string{protocol.FeatureLocalModels}})
	localState.mu.Lock()
	checks = localState.checkSupported
	localState.mu.Unlock()
	if checks {
		t.Error("a 1.3 daemon was taken for one that checks models")
	}
}
