package main

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"mochiii/daemon/mcp"
)

// A TURN THAT KEEPS LEARNING NOTHING IS STOPPED, however it varies its calls.
//
// The loop already ends a model that makes THE SAME call and gets THE SAME
// bytes three times (maxIdenticalRepeats). That is one shape of going in
// circles and the easiest to avoid by accident: change one character of the
// path and it is a new call. REPRODUCED 2026-10-08 against this binary with a
// scripted model:
//
//	a file that is not there, a different guess each time   17 model calls
//	an edit whose SEARCH never matches, reworded each time  17 model calls
//
// Both ran to the iteration ceiling plus the wrap-up, each call re-sending the
// whole conversation. A real model does the same thing more slowly: 9 to 17
// calls to conclude "that file does not exist" (measured 2026-10-06).
//
// WHAT IS COUNTED is not sameness of the call but whether its result gave the
// model anything to work with:
//
//   - a LOOK-UP that failed (no such file, a page that would not load), or
//     came back with exactly what an earlier call this turn already returned;
//   - an EDIT that was refused -- nothing changed;
//   - a look-up that worked and found nothing (a search with no match), at
//     half weight: "nothing matches" is sometimes the very thing being
//     checked, and six of those in a row is a different thing from three
//     failed reads.
//
// Anything else resets the count: new text read, an edit taken, a command run.
// A COMMAND'S FAILURE IS NEWS -- a failing test prints why -- so sandbox_exec
// and every tool this file does not know by name counts only as the existing
// identical-repeat rule already counts it.
//
// TWO STEPS, the gentle one first. At stallSteerAt the tool result itself says
// so, in the one place a model reads between calls, and that is often all it
// takes. At stallStopAt the turn ends through the same wrap-up every other
// limit uses, so the user gets what was found and what is missing instead of
// sixteen tool lines and silence.
//
// NOT A LONG TASK'S SEGMENT: those have their own progress check
// (progressMark in longtask.go), written for work that is meant to go on.

// Weights and thresholds, in half-steps so that "found nothing" can count for
// half of "failed".
const (
	stallWeightFailed = 2
	stallWeightEmpty  = 1
	// stallSteerAt is three failures in a row (or six empty results).
	stallSteerAt = 3 * stallWeightFailed
	// stallStopAt is five failures in a row (or ten empty results).
	stallStopAt = 5 * stallWeightFailed
)

// stallSteerNote is appended to the tool result that takes a turn to
// stallSteerAt.
const stallSteerNote = "\n\n[Note from Mochiii: your last several calls gave you nothing new -- they failed, " +
	"found nothing, or returned what you already had. If you are looking for something that is not " +
	"there, stop: tell the user what is missing. Otherwise change your approach instead of trying " +
	"another variation.]"

// stallKind sorts a tool by what its failure means.
type stallKind int

const (
	stallOther  stallKind = iota // a command, a task tool, a third-party tool: only an identical repeat counts
	stallLookup                  // reads something: a failure, an empty result or a repeat of earlier content teaches nothing
	stallEdit                    // changes something: a refusal changes nothing
)

var stallKinds = map[string]stallKind{
	"read_file": stallLookup, "list_directory": stallLookup, "search_code": stallLookup,
	"grep": stallLookup, "repo_map": stallLookup, "git_history": stallLookup,
	"web_search": stallLookup, "web_fetch": stallLookup,
	"query_compiler_definition": stallLookup, "query_compiler_references": stallLookup,
	"propose_edit": stallEdit, "propose_ast_edit": stallEdit,
}

func stallKindOf(tool mcp.Tool) stallKind {
	if tool.Server != mcp.BuiltinServerName {
		return stallOther
	}
	return stallKinds[tool.Name]
}

// weighResult records what one finished call taught the model and returns the
// text to send it: the result as rendered, a short pointer when an earlier
// call already returned these bytes, and the steering note when this call is
// the one that crosses stallSteerAt.
//
// repeated is the loop's existing verdict that this is the same call with the
// same result as before; it has already replaced the payload with its own
// note, and it counts here as a failure does.
func (t *agentTurn) weighResult(tool mcp.Tool, sig string, result mcp.Result, rendered string, repeated bool) string {
	kind := stallKindOf(tool)
	weight := 0
	switch {
	case repeated:
		weight = stallWeightFailed
	case kind == stallOther:
		// News, whatever it says.
	case result.IsError:
		weight = stallWeightFailed
	case kind == stallLookup && result.Empty:
		weight = stallWeightEmpty
	case kind == stallLookup:
		// A search's line about names the index does not hold is about the
		// QUESTION (absentnames.go); what is compared is what came back, or a
		// search for one missing name after another would each look new.
		lead, body := "", rendered
		if tool.Name == "search_code" {
			lead, body = splitAbsentNamesLead(rendered)
		}
		digest := sha256.Sum256([]byte(body))
		if first, seen := t.seenResults[digest]; seen && first != sig {
			weight = stallWeightFailed
			rendered = lead + fmt.Sprintf("This is byte for byte what an earlier call this turn already returned (%s), "+
				"so it is not repeated here. Nothing new.", truncateRunes(first, 160))
		} else {
			if t.seenResults == nil {
				t.seenResults = map[[sha256.Size]byte]string{}
			}
			t.seenResults[digest] = sig
		}
	}
	if weight == 0 {
		t.stall = 0
		return rendered
	}
	before := t.stall
	t.stall += weight
	if before < stallSteerAt && t.stall >= stallSteerAt && !strings.HasSuffix(rendered, stallSteerNote) {
		rendered += stallSteerNote
	}
	return rendered
}
