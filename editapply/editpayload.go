package editapply

import "strings"

// PayloadFormat names what a model response turned out to carry.
type PayloadFormat int

const (
	// FormatNone: a genuine plain-text answer proposing no edit. The common,
	// correct case for a question.
	FormatNone PayloadFormat = iota
	// FormatSearchReplace: the format prompts/system.txt asks for.
	FormatSearchReplace
	// FormatUnifiedDiff: a git or diff(1) patch, translated into EditBlocks.
	FormatUnifiedDiff
	// FormatUnrecognised: edit-shaped, and could not be read. This is the
	// distinction the whole type exists for -- see EditPayload.
	FormatUnrecognised
)

func (f PayloadFormat) String() string {
	switch f {
	case FormatSearchReplace:
		return "search_replace"
	case FormatUnifiedDiff:
		return "unified_diff"
	case FormatUnrecognised:
		return "unrecognised"
	default:
		return "none"
	}
}

// EditPayload is everything a caller needs to know about what a response asked
// to change on disk.
//
// It exists because the fact that went missing -- "this looked like an edit
// payload and I could not read it" -- CANNOT BE EXPRESSED in the
// ([]EditBlock, []BlockError) pair ParseEditBlocks returns. That pair has one
// spelling for "a plain-text answer" and no spelling at all for "a patch I do
// not understand", so callers rendered both as silence: `edits apply` printed
// "no edit blocks found in input" and exited 0 on a real patch, and the TUI
// returned to an idle prompt saying nothing. Adding a third state to the type
// is what makes the two cases impossible to confuse again.
type EditPayload struct {
	Format   PayloadFormat
	Blocks   []EditBlock
	Rejected []BlockError
	// Hint is set only for FormatUnrecognised, and carries a sentence meant to
	// be shown to the user verbatim.
	Hint PayloadHint
}

// ParseEditPayload reads whichever supported edit format the response carries.
//
// SEARCH/REPLACE is tried first and wins outright, including when it produces
// only REJECTIONS. That ordering is deliberate twice over: it is the format the
// system prompt asks for, so it is the likelier reading; and a rejection is a
// RECOGNISED block that failed, which must keep its own line-numbered reason
// rather than be re-diagnosed as something else. Only the exact
// (no blocks, no rejections) case -- the branch that used to print the shrug --
// falls through to the diff reader.
//
// ParseEditBlocks itself is left untouched. Widening it in place would make one
// fuzz target responsible for two grammars, and would put a format sniffer
// inside the function three callers rely on to mean "no edit here" -- where a
// false positive would silently convert prose ABOUT a patch into an edit.
func ParseEditPayload(response string) EditPayload {
	blocks, rejected := ParseEditBlocks(response)
	if len(blocks) > 0 || len(rejected) > 0 {
		return EditPayload{Format: FormatSearchReplace, Blocks: blocks, Rejected: rejected}
	}

	hint, edgy := LooksLikeEditPayload(response)
	if !edgy {
		return EditPayload{Format: FormatNone}
	}

	// A hunk header is REQUIRED before anything is parsed as a diff. A
	// `diff --git` line alone, or a "---"/"+++" pair alone, is recognised as
	// edit-shaped but never read: that is what keeps a markdown rule or a YAML
	// document separator from becoming a patch, since a rule is followed by
	// prose and never by "@@ -n,m +n,m @@".
	if hint.Kind == PayloadUnifiedDiff && carriesHunk(response) {
		dblocks, drejected := ParseUnifiedDiff(response)
		if len(dblocks) > 0 || len(drejected) > 0 {
			return EditPayload{Format: FormatUnifiedDiff, Blocks: dblocks, Rejected: drejected}
		}
	}

	return EditPayload{Format: FormatUnrecognised, Hint: hint}
}

// carriesHunk reports whether the response contains a hunk header the diff
// reader should be given a chance at.
//
// Combined ("@@@") headers count, even though the reader only ever refuses
// them. Routing them in is what turns a merge diff from a silent
// FormatUnrecognised into a refusal that says WHY -- a three-way hunk has no
// single before-text to search for. Neither shape can be confused with a
// markdown rule, which is what this guard exists to keep out.
func carriesHunk(response string) bool {
	for _, line := range strings.Split(response, "\n") {
		trimmed := strings.TrimSpace(line)
		if isHunkHeader(trimmed) || strings.HasPrefix(trimmed, "@@@ ") {
			return true
		}
	}
	return false
}
