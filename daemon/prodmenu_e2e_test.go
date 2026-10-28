//go:build unix

package main

// THE PRODUCTION-MENU BUILDER, HELD TO THE REAL DAEMON.
//
// prodMenuFirstRequest claims to build what a turn's first model call sends
// by calling the turn's own functions in the turn's order. That claim decays
// the day someone adds a step to the turn and not to the builder -- and then
// the eval is a fourth hand-built menu that passes for the wrong reason, the
// defect it exists to fix. So this starts the real binary with the shipped
// models.json's mcp section, sends one prompt, and requires the builder's
// request to match the one that reached the scripted model: the model, every
// tool byte for byte, and every message, the date sentence aside.
//
// Neuter check: drop withMachineReach (or any step) from prodMenuFirstRequest
// and this fails naming the system message.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
)

var todaySentence = regexp.MustCompile(`Today's date is [^.]+\.`)

// firstRequestDiffs lists every way built differs from the request body the
// daemon sent, or nothing when they match.
func firstRequestDiffs(raw []byte, built prodMenuRequest) []string {
	var sent struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Tools    []toolSpec    `json:"tools"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		return []string{"the daemon's request is not JSON: " + err.Error()}
	}
	var diffs []string
	if sent.Model != built.model {
		diffs = append(diffs, fmt.Sprintf("model: daemon %q, builder %q", sent.Model, built.model))
	}
	a, _ := json.Marshal(sent.Tools)
	b, _ := json.Marshal(built.tools)
	if string(a) != string(b) {
		diffs = append(diffs, fmt.Sprintf("tools differ (daemon %d tools, %d bytes; builder %d tools, %d bytes)",
			len(sent.Tools), len(a), len(built.tools), len(b)))
		for i := 0; i < max(len(sent.Tools), len(built.tools)); i++ {
			var x, y toolSpec
			if i < len(sent.Tools) {
				x = sent.Tools[i]
			}
			if i < len(built.tools) {
				y = built.tools[i]
			}
			if xa, ya := toolSpecString(x), toolSpecString(y); xa != ya {
				diffs = append(diffs, fmt.Sprintf("  tool %d: daemon %s\n           builder %s", i, xa, ya))
			}
		}
	}
	if len(sent.Messages) != len(built.messages) {
		diffs = append(diffs, fmt.Sprintf("messages: daemon %d, builder %d", len(sent.Messages), len(built.messages)))
		return diffs
	}
	for i := range sent.Messages {
		x, y := sent.Messages[i], built.messages[i]
		if x.Role != y.Role {
			diffs = append(diffs, fmt.Sprintf("message %d role: daemon %s, builder %s", i, x.Role, y.Role))
		}
		xc, yc := todaySentence.ReplaceAllString(x.Content, "<date>"), todaySentence.ReplaceAllString(y.Content, "<date>")
		if xc != yc {
			diffs = append(diffs, fmt.Sprintf("message %d (%s) content differs:\n--- daemon (%d bytes)\n%s\n--- builder (%d bytes)\n%s",
				i, x.Role, len(xc), xc, len(yc), yc))
		}
	}
	return diffs
}

func toolSpecString(t toolSpec) string {
	b, _ := json.Marshal(t)
	return string(b)
}

// shippedMCPPatch makes the e2e daemon's mcp section the shipped models.json's.
func shippedMCPPatch(t *testing.T) (func(map[string]any), map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("../models.json")
	if err != nil {
		t.Fatal(err)
	}
	var shipped map[string]any
	if err := json.Unmarshal(raw, &shipped); err != nil {
		t.Fatal(err)
	}
	section, _ := shipped["mcp"].(map[string]any)
	if section == nil {
		t.Fatal("models.json has no mcp section")
	}
	return func(m map[string]any) {
		for k := range m {
			delete(m, k)
		}
		for k, v := range section {
			m[k] = v
		}
	}, section
}

// e2eConfigWithShippedMCP is the config startE2E writes when given that patch:
// models.agent.json's tiers with the shipped mcp section.
func e2eConfigWithShippedMCP(t *testing.T, section map[string]any) *Config {
	t.Helper()
	raw, err := os.ReadFile("../models.agent.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["mcp"] = section
	out, _ := json.MarshalIndent(cfg, "", "  ")
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestTheProdMenuBuilderMatchesTheRealDaemonsFirstRequest(t *testing.T) {
	model := newE2EModel(t, byToolResults(textSSE("ok")))
	patch, section := shippedMCPPatch(t)
	d := startE2E(t, model, prodMenuWorkspace, patch)

	var pip prodMenuCase
	for _, c := range prodMenuCases {
		if c.control {
			pip = c
		}
	}
	turn := d.prompt(t, pip.prompt, e2eApproveAll)
	if turn.done.Error != "" {
		t.Fatalf("the turn failed: %s", turn.done.Error)
	}
	reqs := model.requests()
	if len(reqs) == 0 {
		t.Fatal("the daemon never called the model")
	}

	// The two host probes the two sides cannot share. First, sandbox_exec's
	// description says whether a memory and process cap applies, from mcp.LimiterUsable,
	// which reaches the user's systemd through XDG_RUNTIME_DIR. startE2E gives
	// the daemon a temporary XDG_RUNTIME_DIR, so on a host that exports no
	// DBUS_SESSION_BUS_ADDRESS (a CI runner) the daemon finds no limiter, while
	// this process probed once under the real one. The builder takes the
	// daemon's answer, so every other byte is still compared.
	// TestTheDescriptionDoesNotPromiseLimitsTheHostCannotApply covers the
	// sentence following the probe, both ways.
	daemonLimited := !strings.Contains(reqs[0].raw, "NO memory or process limit on this host")
	restoreLimiter := mcp.LimiterUsable
	t.Cleanup(func() { mcp.LimiterUsable = restoreLimiter })
	mcp.LimiterUsable = func() bool { return daemonLimited }

	// Second, it says whether the egress firewall blocks the metadata endpoint,
	// from a probe that runs the real helper once per process with a 15s timeout
	// (mcp.BwrapEgressUsable / EgressFilterUsable). Each side runs its own, and on
	// a loaded runner one can pass while the other times out: main 24c9274 went
	// red with the daemon saying "blocked" and this process "can also reach". The
	// builder takes the daemon's answer here too.
	// TestAConfinedPromptDisclosesNetworkReach covers both wordings, and
	// TestMetadataIsRefusedThroughTheRealTool the firewall itself.
	useDaemonsEgressAnswer(t, strings.Contains(reqs[0].raw, "which are blocked"))

	setup := prodMenuTestSetup(t, e2eConfigWithShippedMCP(t, section), model.srv.URL+"/api/v1")
	setup.workspace = d.workspace
	built, err := newProdMenuServer(setup, menuUnindexed).prodMenuFirstRequest(context.Background(), pip.prompt, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Positive control for the request itself: the shipped menu, not an empty
	// one that matches an empty one.
	if len(built.tools) < 10 || !toolOnMenu(built.tools, "run_command") {
		t.Fatalf("the builder's menu has %d tools; the shipped menu offers run_command among more than ten", len(built.tools))
	}
	if diffs := firstRequestDiffs([]byte(reqs[0].raw), built); len(diffs) > 0 {
		for _, d := range diffs {
			t.Error(d)
		}
	}

	// Positive controls for the comparison: one changed description, and one
	// changed system message, are each reported.
	degraded, err := degradeMenu(built.tools, pip.degrade)
	if err != nil {
		t.Fatal(err)
	}
	altered := built
	altered.tools = degraded
	if len(firstRequestDiffs([]byte(reqs[0].raw), altered)) == 0 {
		t.Error("a changed tool description went unnoticed by the comparison")
	}
	altered = built
	altered.messages = append([]chatMessage(nil), built.messages...)
	altered.messages[0].Content += " "
	if len(firstRequestDiffs([]byte(reqs[0].raw), altered)) == 0 {
		t.Error("a changed system message went unnoticed by the comparison")
	}
}
