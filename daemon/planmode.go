package main

import (
	"fmt"
	"strings"

	"codeterminal/daemon/mcp"
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
// A tool added later that sets either flag is withheld without anyone editing
// this function, which is the entire reason to branch on the flags rather than
// on a list of names.
func planModeDenies(t mcp.Tool) bool {
	return t.ExecutesCode || t.ReachesNetwork
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
