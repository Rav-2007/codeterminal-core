//go:build unix

package main

// THE WEB RULES, THROUGH THE REAL DAEMON: left out of what reaches the model
// when the shipped models.json denies both web tools, and sent when a config
// offers them (models.agent.json allows both).
//
// Neuter check: make turnContext skip withoutWebRules, and the shipped case
// fails on the first rule's opening.

import (
	"encoding/json"
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
		wantWeb bool
	}{
		{"shipped config, no web tool", true, false},
		{"web tools allowed", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := newE2EModel(t, byToolResults(textSSE("ok")))
			var patch func(map[string]any)
			if tc.shipped {
				patch, _ = shippedMCPPatch(t)
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
			for _, opening := range webRuleOpenings {
				if got := strings.Contains(system, opening); got != tc.wantWeb {
					t.Errorf("the rule %q sent = %v, want %v", opening, got, tc.wantWeb)
				}
			}
			// The no-tool paragraph is sent either way.
			if !strings.Contains(system, "If no such tool is available to you") {
				t.Error("the paragraph for a turn with no web tool is missing")
			}
		})
	}
}
