package main

import (
	"fmt"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
	"mochiii/protocol"
)

// The values PromptRequest.Mode may carry.
//
// THE WIRE FIELD IS OVERLOADED, and that is the first thing to know about it.
// It carries the VS Code approval-mode picker AND plan mode in one string:
// clients/vscode/media/main.js declares `let currentMode = 'auto'` and posts
// whichever of the three the user selected. So "auto" and "manual" are not
// junk to be rejected -- they are the ordinary case, and "auto" is the DEFAULT
// every normal turn sends.
//
// This matters because the validation below is fail-CLOSED. An enum of
// {"", "plan"} would have looked correct, passed every test in this package,
// and rejected the default path of the primary client on the first real turn.
const (
	modeAuto   = "auto"
	modeManual = "manual"
	modePlan   = "plan"
)

// normalizeMode canonicalises a client-supplied mode and rejects one it does
// not recognise.
//
// WHY THIS REJECTS RATHER THAN FALLING THROUGH. The comparison used to be a
// bare `mode != "plan"`, which meant every string that was not exactly "plan"
// selected the full tool menu -- including "Plan", "planning", and a mode field
// a future client misspells. The failure was silent and open: the user asked
// for plan mode, the daemon read something it did not recognise, and handed
// back a menu containing the one built-in that executes arbitrary code. An
// unknown mode is now an error, because the alternative to understanding what
// the client asked for is not "assume the safe thing" -- it was "assume the
// permissive thing".
//
// The empty string is accepted and means the full menu: `mode` is
// `json:"mode,omitempty"`, so a client that never sets it is indistinguishable
// on the wire from one that sent "". Rejecting "" would reject every client
// that predates the field.
func normalizeMode(raw string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(raw))
	switch m {
	case "", modeAuto, modeManual, modePlan:
		return m, nil
	default:
		return "", fmt.Errorf("unknown mode %q (want one of: auto, manual, plan)", raw)
	}
}

// isPlanMode reports whether a mode string selects plan mode.
//
// Deliberately lenient where normalizeMode is strict: this is the read-side
// predicate, called from the tool-menu construction, and it must give the SAME
// answer as the wire-side validation for anything that got past it. Doing its
// own trim and fold means a caller that skipped normalizeMode -- a test, or a
// future entry point -- still gets the filter rather than silently the full
// menu, which is the failure mode this whole file exists to close.
func isPlanMode(mode string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) == modePlan
}

// planModeDenies reports whether a tool is withheld in plan mode.
//
// KEYED ON CAPABILITY, NOT ON NAME -- and on EVERY capability the type
// declares, which is the correction. The previous filter tested
// `t.ExecutesCode` alone and called itself capability-keyed. mcp.Tool declares
// two such flags, and mcp.go says of the second, verbatim: "IT EXISTS FOR THE
// SAME REASON ExecutesCode DOES". Testing one of the two left web_search and
// web_fetch registered in plan mode, so the mode that promises not to touch
// anything kept both network egress and the indirect-injection intake that
// comes back with it.
//
// A tool added later that sets any of these flags is withheld without anyone
// editing this function, which is the entire reason to branch on the flags
// rather than on a list of names.
//
// THE SET GREW ONCE ALREADY AND THAT IS THE WARNING. This function shipped
// reading ExecutesCode alone while its comment claimed to be keyed on
// capability; adding ReachesNetwork fixed that instance and left the same
// assumption in place. LaunchesSubprocess is the third, found on
// query_compiler_definition and query_compiler_references -- tools that declare
// only ReadOnlyHint and start a language server the daemon does not control.
// The flags are not the property; they are the properties someone has named so
// far. A built-in that acts in a way none of them describes is withheld by
// nothing here, which is why builtinCapabilityAudit enumerates handlers rather
// than trusting this list to be finished.
func planModeDenies(t mcp.Tool) bool {
	return t.ExecutesCode || t.ReachesNetwork || t.LaunchesSubprocess
}

// planModeWithholdEdits drops edit blocks parsed out of the model's prose in
// plan mode, and reports each one it dropped.
//
// THE TOOL FILTER WAS NEVER THE WHOLE WRITE SURFACE. builtinTools withholds
// propose_edit and sandbox_exec, and that closed the path a tool takes. It does
// nothing about the other one: the model can emit a SEARCH/REPLACE block in
// ordinary text, parseAndLogEditBlocks lifts it out of the reply, and it arrives
// at the client as an EditProposal like any other. In VS Code auto-apply is
// derived from the mode PICKER rather than from the wire mode, so a turn sent as
// `{mode:"plan", autoApply:true}` -- which is what typing /plan with the picker
// on Auto produces -- could write that block to disk.
//
// The summary shipped for /plan says "read-only: no edits, no commands, no
// network". Two of those three were structural and the first was not, which is
// item 23's shape exactly: a control that holds on the path someone checked and
// is an instruction on the path they did not.
//
// REPORTED, NOT SILENTLY DROPPED. editapply.BlockError exists because "a refused
// block is a block the user asked for and did not get, and dropping it silently
// is the failure mode this type exists to make impossible". Withholding one
// because of the mode is the same event, so it travels the same channel: the
// user sees that the model proposed an edit and that plan mode declined it,
// rather than watching a plan quietly lose a paragraph.
func planModeWithholdEdits(blocks []editapply.EditBlock, rejections []protocol.EditRejectionWire) ([]editapply.EditBlock, []protocol.EditRejectionWire) {
	for _, b := range blocks {
		rejections = append(rejections, protocol.EditRejectionWire{
			Reason: fmt.Sprintf("plan mode: withheld a proposed edit to %s. This mode does not "+
				"change files; re-send without /plan to propose it.", b.FilePath),
		})
	}
	return nil, rejections
}

// eligibleLaneBServers counts the third-party servers a NON-plan turn would
// actually have connected.
//
// Not len(cfg.MCP.Servers): the connect loop skips a server that is disabled or
// whose unconfined nature has not been acknowledged, so the raw map size
// overstates what plan mode withheld. It is the number a user is told, and a
// count that says "5 withheld" when four were switched off sends them to debug
// a config that is behaving correctly.
func eligibleLaneBServers(cfg *Config) int {
	n := 0
	for _, srv := range cfg.MCP.Servers {
		if srv.Disabled || !srv.AcknowledgedUnconfined {
			continue
		}
		n++
	}
	return n
}

// planModeToolNames are the tools the plan directive tells the model it may
// use. Declared as data rather than embedded in the sentence so a test can
// check every one against the menu plan mode actually registers.
//
// WHY THAT TEST EXISTS. The directive shipped naming `view_file` and
// `grep_search`. Neither has ever existed in this daemon -- the tools are
// read_file and search_code -- so every plan-mode turn spent tokens telling the
// model to call two things that were not in its menu, and nothing failed,
// because a prompt string is not compiled against anything.
var planModeToolNames = []string{"read_file", "search_code", "repo_map"}

// planModeDirective is what plan mode adds to the system prompt.
//
// IT GOES IN THE SYSTEM PROMPT, and that is the point of this function rather
// than a string concatenation at the call site.
//
// It used to be appended to the USER's message, prefixed with a literal
// "[SYSTEM]: " that the model was expected to read as authority. Two things
// were wrong with that. The narrow one: a real system role exists here --
// buildChatMessages takes one, and provider.go documents the invariant that
// user content never reaches it -- so the marker was forging something the
// architecture already provided honestly.
//
// The broad one is worse. A product that trains its own model to obey
// "[SYSTEM]:" inside user-role text is manufacturing the exact primitive an
// indirect prompt injection needs: retrieved file content, tool output and web
// results all arrive as user-role text, and any of them can contain that string.
// The delimiter defence on retrieved chunks exists because this project already
// takes that threat seriously; this directive was undermining it from the
// inside.
const planModeDirective = "The user has requested an implementation plan. DO NOT propose " +
	"edits directly. Instead, output a structured Markdown artifact " +
	"(`implementation_plan.md`) utilizing Mermaid diagrams and sequential step " +
	"definitions. You may use read_file, search_code and repo_map to explore the " +
	"workspace before planning. Tools that execute code or reach the network are " +
	"not available in this mode."

// planModeSystemPrompt returns the system prompt for a turn, with the plan
// directive appended when the client asked for plan mode.
//
// Returns base unchanged for every other mode, so no non-plan turn changes.
func planModeSystemPrompt(base, mode string) string {
	if !isPlanMode(mode) {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return planModeDirective
	}
	return base + "\n\n" + planModeDirective
}
