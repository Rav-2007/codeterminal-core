package main

import "strings"

// planModeToolNames are the tools the plan directive tells the model it may
// use. Declared as data rather than embedded in the sentence so a test can
// check every one against the menu plan mode actually registers.
//
// WHY THAT TEST EXISTS. The directive shipped naming `view_file` and
// `grep_search`. Neither has ever existed in this daemon -- the tools are
// read_file and search_code -- so every plan-mode turn spent tokens telling the
// model to call two things that were not in its menu, and nothing failed,
// because a prompt string is not compiled against anything.
var planModeToolNames = []string{"read_file", "search_code"}

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
	"definitions. You may use read_file and search_code to explore the workspace " +
	"before planning. Tools that execute code are not available in this mode."

// planModeSystemPrompt returns the system prompt for a turn, with the plan
// directive appended when the client asked for plan mode.
//
// Returns base unchanged for every other mode, so no non-plan turn changes.
func planModeSystemPrompt(base, mode string) string {
	if mode != "plan" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return planModeDirective
	}
	return base + "\n\n" + planModeDirective
}
