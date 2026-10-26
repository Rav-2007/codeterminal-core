//go:build unix

package main

// THE WEB RULES, THROUGH THE REAL DAEMON: left out of what reaches the model
// when the shipped models.json denies both web tools, and sent when a config
// offers one -- the shipped section with web_search alone turned on, as a user
// would in their own models.json -- or both (models.agent.json).
//
// Neuter checks: make turnContext skip withoutWebRules, and the shipped case
// fails on the first rule's opening; invert its webAvailable condition, and
// the two cases with a web tool fail too.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func firstSystemMessage(t *testing.T, model *e2eModel) (system string, tools []string) {
	t.Helper()
	reqs := model.requests()
	if len(reqs) == 0 {
		t.Fatal("the daemon never called the model")
	}
	var sent struct {
		Messages []chatMessage `json:"messages"`
		Tools    []toolSpec    `json:"tools"`
	}
	if err := json.Unmarshal([]byte(reqs[0].raw), &sent); err != nil || len(sent.Messages) == 0 || sent.Messages[0].Role != "system" {
		t.Fatalf("the first request has no system message (err %v)", err)
	}
	for _, tool := range sent.Tools {
		tools = append(tools, bareToolName(tool.Function.Name))
	}
	return sent.Messages[0].Content, tools
}

func TestTheWebRulesReachTheModelOnlyWithAWebTool(t *testing.T) {
	const prompt = "what does main.go do?"
	for _, tc := range []struct {
		name    string
		shipped bool // the shipped mcp section, which denies both web tools
		// enable names the web tools set to "allow" on top of the shipped
		// section: what a user does in their own models.json.
		enable  []string
		wantWeb bool
	}{
		{"shipped config, no web tool", true, nil, false},
		// THE DIRECTION THAT FAILS SILENTLY: a user who turns on web_search
		// alone must get the rules for it. One web tool is enough. Turning it
		// on is two edits to the shipped section, because it switches web off
		// twice: mcp.web.disabled (the tools are not registered) and a deny
		// policy for each (they are filtered from the menu).
		{"shipped config with web_search turned on", true, []string{"web_search"}, true},
		{"web tools allowed", false, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := newE2EModel(t, byToolResults(textSSE("ok")))
			var patch func(map[string]any)
			if tc.shipped {
				shipped, _ := shippedMCPPatch(t)
				patch = func(m map[string]any) {
					shipped(m)
					if len(tc.enable) == 0 {
						return
					}
					// A copy of the tools map: the shipped section is shared
					// with the other cases.
					builtin, _ := m["builtin"].(map[string]any)
					tools := map[string]any{}
					if old, ok := builtin["tools"].(map[string]any); ok {
						for k, v := range old {
							tools[k] = v
						}
					}
					for _, name := range tc.enable {
						tools[name] = "allow"
					}
					m["web"] = map[string]any{"disabled": false}
					copied := map[string]any{}
					for k, v := range builtin {
						copied[k] = v
					}
					copied["tools"] = tools
					m["builtin"] = copied
				}
			}
			d := startE2E(t, model, prodMenuWorkspace, patch)
			if turn := d.prompt(t, prompt, e2eApproveAll); turn.done.Error != "" {
				t.Fatalf("the turn failed: %s", turn.done.Error)
			}
			system, tools := firstSystemMessage(t, model)

			offered := false
			for _, name := range tools {
				offered = offered || name == "web_search" || name == "web_fetch"
			}
			if offered != tc.wantWeb {
				t.Fatalf("web tool on the menu = %v, want %v (menu: %v)", offered, tc.wantWeb, tools)
			}
			// The single-tool case really is single: what was not turned on
			// is still off.
			if len(tc.enable) > 0 {
				for _, name := range tools {
					if (name == "web_search" || name == "web_fetch") && !slices.Contains(tc.enable, name) {
						t.Fatalf("%s is on the menu though only %v was turned on", name, tc.enable)
					}
				}
			}
			for _, opening := range webRuleOpenings {
				if got := strings.Contains(system, opening); got != tc.wantWeb {
					t.Errorf("the rule %q sent = %v, want %v", opening, got, tc.wantWeb)
				}
			}
			// The paragraphs that hold with no web tool are sent either way.
			for _, kept := range answerRuleOpenings {
				if !strings.Contains(system, kept) {
					t.Errorf("%q is missing", kept)
				}
			}
		})
	}
}

// THE PLAIN CHAT PATH offers no tool at all, so the web rules never go with
// it: the shipped config with agent mode off, which reaches the same lines as
// a client that cannot answer an approval and a local model that refused
// tools. The paragraphs that hold with no web tool are still sent, and a
// greeting keeps its own one line.
//
// Neuter check: drop the withoutUnofferedWebRules call from serveConn's chat
// path and this fails on every rule's opening.
func TestTheChatPathSendsNoWebRules(t *testing.T) {
	model := newE2EModel(t, byToolResults(textSSE("ok")))
	shipped, _ := shippedMCPPatch(t)
	d := startE2E(t, model, prodMenuWorkspace, func(m map[string]any) {
		shipped(m)
		m["enabled"] = false
	})
	if turn := d.prompt(t, "what does main.go do?", nil); turn.done.Error != "" {
		t.Fatalf("the turn failed: %s", turn.done.Error)
	}
	system, tools := firstSystemMessage(t, model)
	// Positive control: this is the chat path, not an agent loop.
	if len(tools) != 0 {
		t.Fatalf("the request offered tools %v; the chat path offers none", tools)
	}
	for _, opening := range webRuleOpenings {
		if strings.Contains(system, opening) {
			t.Errorf("the chat path sent the web rule %q", opening)
		}
	}
	for _, kept := range answerRuleOpenings {
		if !strings.Contains(system, kept) {
			t.Errorf("the chat path left out %q, which holds with no web tool", kept)
		}
	}

	before := len(model.requests())
	if turn := d.prompt(t, "thanks!", nil); turn.done.Error != "" {
		t.Fatalf("the greeting failed: %s", turn.done.Error)
	}
	reqs := model.requests()
	if len(reqs) != before+1 {
		t.Fatalf("the greeting made %d model calls, want 1", len(reqs)-before)
	}
	var sent struct {
		Messages []chatMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(reqs[before].raw), &sent); err != nil || len(sent.Messages) == 0 {
		t.Fatalf("the greeting's request has no messages (err %v)", err)
	}
	if got := sent.Messages[0].Content; got != greetingSystemPrompt {
		t.Errorf("a greeting's system message is %q, want its own one line", got)
	}
}
