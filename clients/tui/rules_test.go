package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"mochiii/protocol"
)

// rulesDaemon is a fake daemon that keeps rules as the real one does, as far as
// a client can tell: it answers every RulesRequest with the rules as they stand.
func rulesDaemon(t *testing.T) *recordingDaemon {
	t.Helper()
	var mu sync.Mutex
	rules := []string{}
	return startRequestRecorder(t, []string{protocol.FeatureRules}, func(raw json.RawMessage) any {
		var req protocol.RulesRequest
		_ = json.Unmarshal(raw, &req)
		mu.Lock()
		defer mu.Unlock()
		resp := protocol.RulesResponse{ProtocolVersion: protocol.ProtocolVersion, Path: "/home/u/.local/state/mochiii/rules/0123abcd.md"}
		switch req.Action {
		case protocol.RulesActionAdd:
			rules = append(rules, req.Text)
		case protocol.RulesActionRemove:
			if req.Number < 1 || req.Number > len(rules) {
				resp.Error = fmt.Sprintf("there is no rule %d: this project has %d", req.Number, len(rules))
			} else {
				resp.Removed = rules[req.Number-1]
				rules = append(append([]string{}, rules[:req.Number-1]...), rules[req.Number:]...)
			}
		}
		resp.Rules = append([]string{}, rules...)
		return resp
	})
}

func rulesRequests(t *testing.T, d *recordingDaemon) []protocol.RulesRequest {
	t.Helper()
	var out []protocol.RulesRequest
	for _, raw := range d.requests() {
		var req protocol.RulesRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("a recorded request does not decode: %v", err)
		}
		out = append(out, req)
	}
	return out
}

// runRules types a command, presses Enter, and hands the daemon's answer back.
func runRules(t *testing.T, m chatModel, line string) chatModel {
	t.Helper()
	m = typeText(m, line)
	m, cmd := pressEnter(m)
	if answer, ok := findMsgWithin[rulesResultMsg](cmd, 3e9); ok {
		updated, _ := m.Update(answer)
		m = updated.(chatModel)
	}
	return m
}

// THE REPORT THIS ANSWERS (OWNER, 2026-10-13): a correction made in one chat
// was gone in the next. /remember hands the daemon one line to keep, in the
// user's own words, and says what was kept and how to undo it.
//
// Neuter check: answer "remember" with the usage in handleExtraSlash -- nothing
// reaches the daemon and this fails on its first assertion.
func TestRememberSavesARuleWithTheDaemonAndSaysSo(t *testing.T) {
	daemon := rulesDaemon(t)
	m := runRules(t, newTestModel(), "/remember  tests here are run with make check ")

	got := rulesRequests(t, daemon)
	if len(got) != 1 || !got[0].Rules || got[0].Action != protocol.RulesActionAdd || got[0].Text != "tests here are run with make check" {
		t.Fatalf("the daemon was sent %+v", got)
	}
	last := m.turns[len(m.turns)-1]
	for _, want := range []string{"1. tests here are run with make check", "every chat in this project", "/forget 1"} {
		if !strings.Contains(last.text, want) {
			t.Errorf("the reply does not say %q:\n%s", want, last.text)
		}
	}
	// What a command prints is not part of the conversation: the rule reaches
	// the model because the daemon adds it, not because this line is replayed.
	if !last.local {
		t.Error("the reply is not marked local, so it would be sent back as the assistant's own words")
	}
}

func TestRulesListsThemNumberedAndSaysWhereTheyAreKept(t *testing.T) {
	rulesDaemon(t)
	m := runRules(t, newTestModel(), "/rules")
	if last := lastTurn(t, m); !strings.Contains(last, "No rules") || !strings.Contains(last, "/remember ") {
		t.Errorf("with nothing saved the list does not say so and how to save one:\n%s", last)
	}

	m = runRules(t, m, "/remember tests here are run with make check")
	m = runRules(t, m, "/remember never edit the files under api/gen")
	m = runRules(t, m, "/rules")
	last := lastTurn(t, m)
	for _, want := range []string{"1. tests here are run with make check", "2. never edit the files under api/gen",
		"/home/u/.local/state/mochiii/rules/0123abcd.md", "/forget <number>"} {
		if !strings.Contains(last, want) {
			t.Errorf("the list does not show %q:\n%s", want, last)
		}
	}
}

func TestForgetRemovesTheRuleWithThatNumber(t *testing.T) {
	daemon := rulesDaemon(t)
	m := runRules(t, newTestModel(), "/remember one")
	m = runRules(t, m, "/remember two")
	m = runRules(t, m, "/forget 1")
	if last := lastTurn(t, m); !strings.Contains(last, "Removed") || !strings.Contains(last, "one") || !strings.Contains(last, "1 rule left") {
		t.Errorf("the reply does not say what was removed and what is left:\n%s", last)
	}
	got := rulesRequests(t, daemon)
	if n := len(got); n != 3 || got[2].Action != protocol.RulesActionRemove || got[2].Number != 1 {
		t.Fatalf("the daemon was sent %+v", got)
	}

	// The daemon's own refusal is shown as it wrote it.
	m = runRules(t, m, "/forget 7")
	if last := lastTurn(t, m); !strings.Contains(last, "there is no rule 7: this project has 1") {
		t.Errorf("the daemon's refusal is not shown:\n%s", last)
	}
}

// What cannot be read as a request is answered here, with how to say it, and
// never sent. A command typed with nothing after it gets the answer every
// command that needs something gets (slash.go); a number that is not one gets
// this command's own.
func TestARulesCommandThatSaysNothingIsAnsweredWithItsUsage(t *testing.T) {
	daemon := rulesDaemon(t)
	for line, want := range map[string]string{
		"/remember":     "usage: /remember",
		"/remember    ": "usage: /remember",
		"/forget":       "usage: /forget",
		"/forget two":   "/forget <number>",
		"/forget 0":     "/forget <number>",
		"/forget 1 2":   "/forget <number>",
	} {
		m := runRules(t, newTestModel(), line)
		if n := len(daemon.requests()); n != 0 {
			t.Fatalf("%q was sent to the daemon", line)
		}
		if last := lastTurn(t, m); !strings.Contains(last, want) {
			t.Errorf("%q is not answered with %q:\n%s", line, want, last)
		}
	}
}

// A daemon started before it could keep rules is not sent the request -- it
// would read it as an empty prompt -- and the line says to restart it.
func TestAnOlderDaemonIsToldApartForRules(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureSavedChats}, func(json.RawMessage) any {
		return protocol.TokenResponse{Error: "prompt is empty"}
	})
	for _, line := range []string{"/remember tabs, not spaces", "/rules", "/forget 1"} {
		m := runRules(t, newTestModel(), line)
		if n := len(daemon.requests()); n != 0 {
			t.Errorf("%q: %d request(s) reached a daemon that cannot read one", line, n)
		}
		if last := lastTurn(t, m); !strings.Contains(last, "older than this client") || !strings.Contains(last, "restart it") {
			t.Errorf("%q: the line does not say the daemon is older:\n%s", line, last)
		}
	}
}

// A rule is the user's own text, but it comes back from a file a person can
// edit, and the path and any error are the daemon's. None of it reaches the
// screen with an escape sequence in it.
func TestRulesRepliesAreSanitized(t *testing.T) {
	const esc = "\x1b[2J\x1b]0;owned\x07"
	for name, msg := range map[string]rulesResultMsg{
		"list":    {resp: protocol.RulesResponse{Rules: []string{"a rule" + esc}, Path: "/state" + esc}},
		"added":   {req: protocol.RulesRequest{Action: protocol.RulesActionAdd}, resp: protocol.RulesResponse{Rules: []string{"new" + esc}}},
		"removed": {req: protocol.RulesRequest{Action: protocol.RulesActionRemove}, resp: protocol.RulesResponse{Removed: "old" + esc}},
		"refused": {resp: protocol.RulesResponse{Error: "no" + esc}},
		"failed":  {err: errors.New("dial" + esc)},
	} {
		if got := formatRulesResult(msg); strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") {
			t.Errorf("%s: a control sequence reached the text: %q", name, got)
		}
	}
	// Lines of the file that are over its limits are said, not hidden.
	got := formatRulesResult(rulesResultMsg{resp: protocol.RulesResponse{Rules: []string{"kept"}, Unused: 3, Path: "/state/rules.md"}})
	if !strings.Contains(got, "3 line") || !strings.Contains(got, "not in use") {
		t.Errorf("the list hides that part of the file is not in use:\n%s", got)
	}
}

// The three commands are in the catalog the popup and /help are drawn from.
func TestTheRulesCommandsAreInTheCatalog(t *testing.T) {
	for _, name := range []string{"remember", "rules", "forget"} {
		found := false
		for _, def := range slashCatalog {
			if def.Name == name {
				found = def.Kind == slashLocal && def.Summary != ""
			}
		}
		if !found {
			t.Errorf("/%s is not a local command with a summary in the catalog", name)
		}
	}
}
