package main

import (
	"strings"
	"testing"
)

// THE CONFIG THAT SHIPS IS THE PRODUCT, and until 2026-10-12 nothing asserted
// anything about it.
//
// models.json is copied verbatim into the .vsix by
// clients/vscode/scripts/stage-runtime.js, so whatever it says is what every
// installed extension does. It said nothing about MCP at all -- no `mcp` key --
// and agentModeEngaged is `s.cfg.MCP.Enabled && hs.HasCapability(...)`, so
// agent mode was off for every user of 0.1.0 and 0.1.1 and no built-in tool was
// ever advertised.
//
// WHAT THAT COST. The VS Code client was otherwise ready: chatPanel.ts passes
// an onToolApproval handler, daemonClient.ts therefore declares
// CAP_TOOL_APPROVAL, and the webview renders the approval UI. Every piece was
// in place except one key in one JSON file, and the symptom reaching users was
// not "tools are off" -- it was the model answering questions about their code
// without being able to read any of it, and inventing the answer. A user
// reported exactly that on 2026-10-11; "Retrieved context 0" was on screen and
// correct.
//
// A unit test cannot catch a missing key in a config file nobody reads. This
// reads it.
func TestShippedConfigEnablesTheReadOnlyToolSet(t *testing.T) {
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("LoadConfig(../models.json): %v -- this is the file that ships in the .vsix", err)
	}

	if !cfg.MCP.Enabled {
		t.Fatal("mcp.enabled is false in the SHIPPED models.json, so agentModeEngaged can never be true " +
			"and no built-in tool is advertised to any user. The model is then asked about code it " +
			"cannot read, and answers anyway.")
	}

	// The read-only set. Each is classConfined (see builtinToolClasses): it runs
	// inside this daemon's own code, behind the same gates as everything else,
	// and reaches neither the network nor a subprocess. These are the tools a
	// question about the open file needs, and allowing them is what makes the
	// first turn work without a prompt the user cannot act on.
	for _, tool := range []string{"read_file", "list_directory", "search_code", "repo_map", "grep"} {
		if got := cfg.MCP.Builtin.policyFor(tool); got != PolicyAllow {
			t.Errorf("built-in %q resolves to %q, want %q -- an unlisted tool is %q, which asks the "+
				"user to approve every single read and makes the product unusable for its main job",
				tool, got, PolicyAllow, PolicyAsk)
		}
	}
}

// NOTHING THAT LEAVES THE MACHINE OR RUNS CODE MAY BE ALLOWED UNATTENDED, and
// this is the half of the previous test that matters more.
//
// The easy mistake, once someone is editing this block, is to widen it by
// copying models.agent.json -- which sets web_search and web_fetch to "allow".
// That file is a DEVELOPMENT config reached through mochiii.configPath, chosen
// deliberately by someone who read what they were turning on. The shipped
// default is not chosen by anyone: it is what a user gets for installing an
// extension, and "allow" there means a tool runs with no prompt at all.
//
// So this does not test a list of names. It tests the PROPERTY -- allowed
// implies classConfined -- which keeps holding when a tool is added, renamed,
// or reclassified, and fails the moment someone allows a network, subprocess or
// code-execution tool in the file that ships.
//
// Same shape as planModeDenies, and for the same reason recorded there: branch
// on the capability, not on a list of names that is only as finished as the
// last person to edit it.
func TestShippedConfigAllowsNothingBeyondConfinedTools(t *testing.T) {
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("LoadConfig(../models.json): %v", err)
	}

	for tool, policy := range cfg.MCP.Builtin.Tools {
		if policy != PolicyAllow {
			continue
		}
		if class := builtinToolClass(tool); class != classConfined {
			t.Errorf("the SHIPPED models.json sets %q to %q, but it is not a confined tool "+
				"(class %v). A tool that reaches the network, starts a subprocess or runs commands "+
				"must not run without asking in the default config. Set it to %q, or leave it "+
				"unlisted, which means %q.",
				tool, PolicyAllow, class, PolicyAsk, PolicyAsk)
		}
	}
}

// THE SHIPPED CONFIG MAY DISCLOSE, BUT MUST NOT ALARM.
//
// This test was first written asserting that models.json loads with NO
// warnings at all, and it failed immediately -- correctly. Allowing a confined
// tool warns on purpose:
//
//	"mcp.builtin: 5 built-in tool(s) will run WITHOUT asking you: grep,
//	 list_directory, read_file, repo_map, search_code (these are confined and
//	 do not write to your files)"
//
// That line is the design working. The user is told, in the daemon log, exactly
// which tools run unattended and what bounds them. Demanding silence there
// would have meant either leaving the tools on "ask" -- a prompt before every
// single file read -- or suppressing the one sentence that discloses them.
//
// So the property is not "no warnings". It is: the shipped default discloses
// the confined set and raises NONE of the warnings that mean something
// dangerous was switched on. Those four sentences are the validator's own
// words for network egress, subprocess launch, command execution, and a tool
// name that matches nothing.
func TestShippedConfigWarnsOnlyAboutConfinedTools(t *testing.T) {
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("LoadConfig(../models.json): %v", err)
	}

	// Lifted from validateMCP's own warnf calls, so a reworded warning fails
	// here rather than silently stopping being matched.
	forbidden := map[string]string{
		"REACH THE INTERNET": "a network tool is allowed unattended in the config that ships",
		"RUN COMMANDS":       "a code-execution tool is allowed unattended in the config that ships",
		"language server":    "a subprocess-launching tool is allowed unattended in the config that ships",
		"does nothing":       "a tool name in the shipped config matches no built-in -- almost certainly a typo",
	}
	for _, w := range cfg.Warnings() {
		for needle, why := range forbidden {
			if strings.Contains(w, needle) {
				t.Errorf("%s.\nThe warning was: %s", why, w)
			}
		}
	}

	// And the disclosure must actually be made. If this stops appearing, either
	// the read-only set was turned off -- which breaks the product's main job --
	// or the sentence that tells the user what runs unattended was dropped.
	disclosed := false
	for _, w := range cfg.Warnings() {
		if strings.Contains(w, "WITHOUT asking you") {
			disclosed = true
		}
	}
	if !disclosed {
		t.Error("the shipped config no longer discloses which built-in tools run without asking. " +
			"Either the read-only set is off, or the disclosure was lost.")
	}
}
