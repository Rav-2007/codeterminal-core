package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"mochiii/editapply"
	"mochiii/protocol"
)

// MOCHIII'S RECORD UNDER THE ANSWER (2026-10-26).
//
// In a run of real fixes from real GitHub projects, turns ended on "Now let's
// run the tests to verify the fix:" -- out of steps, never verified -- or with
// no words at all after a provider stopped them. The wrap-up call at a limit
// (wrapUpAtLimit) asks the model to say where it stands, and one model wrote
// the same promise of a test run instead. And "continue" then started nearly
// blind: the next turn gets only what the last one SAID, and after a provider
// stop that can be three bytes.
//
// So the daemon says it, from its own record and never from the model's words:
// why the turn stopped, what it changed, what was and was not checked on the
// final version, whether tests were changed, what changed in the real project
// outside review -- and, when the answer claims the tests pass and the record
// says otherwise, that. It goes under the answer as ordinary text, like the
// Sources block (websources.go), so both clients show it as it is, the TUI's
// history carries it into the next "continue", and conversation memory keeps it.
//
// It is NOT under every answer. A turn that finished, with nothing changed
// outside review, no test changed, and no claim the record contradicts, has
// the check line above its review to say what was run; a second account would
// be noise.

// maxReportedFiles bounds each list in the record.
const maxReportedFiles = 8

// turnFacts is what the record is built from.
type turnFacts struct {
	result agentResult
	// offered are the edit blocks put to the user for review; info is the
	// working copy's report on them (nil without a copy).
	offered []editapply.EditBlock
	info    *protocol.WorkingCopyInfo
	// checks are the commands run in the working copy, and editsNow the edit
	// count when the turn ended, to tell a check of the final version apart.
	checks   []ranCheck
	editsNow int
	// outside is what commands changed in the real project (runplace.go).
	outside []string
}

// turnReport is the record, or "" when there is nothing to add.
func turnReport(f turnFacts) string {
	var last *ranCheck
	if len(f.checks) > 0 {
		last = &f.checks[len(f.checks)-1]
	}
	lastFinal := lastCheckOfFinal(f.checks, f.editsNow)
	finalChecked := lastFinal != nil && lastFinal.passed
	testsChanged := testsChangedIn(f.offered)
	if f.info != nil && len(f.info.TestsChanged) > 0 {
		testsChanged = f.info.TestsChanged
	}
	claimed := claimsTestsPass(f.result.FinalText)
	// Only of changes: with nothing changed, the project as it is IS the final
	// version, and a test run in it this record does not hold still counts.
	contradicted := claimed && len(f.offered) > 0 && (!finalChecked || len(testsChanged) > 0)
	incomplete := f.result.Incomplete != nil
	if !incomplete && len(testsChanged) == 0 && len(f.outside) == 0 && !contradicted {
		return ""
	}

	var lines []string
	if incomplete {
		lines = append(lines, "Stopped: "+stopSaid(f.result.Incomplete)+".")
	}
	if files := offeredFiles(f.offered); len(files) > 0 {
		lines = append(lines, "Changed: "+listSaid(files)+" — offered for your review.")
	}
	if len(testsChanged) > 0 {
		lines = append(lines, "Existing tests changed: "+listSaid(testsChanged)+
			" — a pass after that may come from the changed test.")
	}
	if len(f.outside) > 0 {
		lines = append(lines, "Changed in your real project, outside review: "+listSaid(f.outside)+".")
	}
	switch {
	case last == nil && len(f.offered) > 0:
		lines = append(lines, "Not verified: nothing was built or tested on these changes.")
	case last != nil:
		state := "passed"
		if !last.passed {
			state = "FAILED"
		}
		when := "on the final version"
		if last.edits != f.editsNow {
			when = "before the last change, so the final version was not run"
		}
		lines = append(lines, "Last check: `"+truncateRunes(last.command, 120)+"` "+state+", "+when+".")
		switch {
		case lastFinal != nil && lastFinal != last && !lastFinal.passed:
			lines = append(lines, "The last build or test run on the final version, `"+
				truncateRunes(lastFinal.command, 120)+"`, FAILED.")
		case !finalChecked && len(f.offered) > 0 && last.passed && last.edits == f.editsNow:
			lines = append(lines, "Not verified: that command does not build or test the project.")
		}
	}
	switch {
	case contradicted && lastFinal == nil:
		lines = append(lines, "The answer says the tests pass; this record shows no passing run of the project's "+
			"tests on the final version.")
	case contradicted && !finalChecked:
		lines = append(lines, "The answer says the tests pass; the last build or test run on the final version "+
			"failed.")
	case contradicted:
		lines = append(lines, "The answer says the tests pass; they passed on the final version only after "+
			"existing tests were changed.")
	}
	if incomplete {
		if read := filesRead(f.result.ToolSignatures); len(read) > 0 {
			lines = append(lines, "Read: "+listSaid(read)+".")
		}
	}
	return "\n\n---\n**Where this stands** — Mochiii's record of this turn, not the model's words:\n- " +
		strings.Join(lines, "\n- ")
}

// stopSaid is why a turn stopped, in a few words: the first clause of the
// notice the user was shown (agentloop.go), or the reason's own words.
func stopSaid(info *protocol.IncompleteInfo) string {
	detail := strings.TrimSpace(strings.SplitN(info.Detail, " — ", 2)[0])
	for _, lead := range []string{"this task ", "stopped part-way because ", "stopped part-way: "} {
		detail = strings.TrimPrefix(detail, lead)
	}
	detail = strings.TrimSuffix(detail, ".")
	switch {
	case detail != "":
		return truncateRunes(detail, 160)
	case info.Reason == protocol.IncompleteProviderError:
		return "the model provider failed part-way"
	case info.Reason == protocol.IncompleteLength:
		return "the answer reached the model's output limit"
	case info.Reason == protocol.IncompleteUserCancelled:
		return "you stopped it"
	}
	return "it did not finish"
}

// offeredFiles are the files of the offered edits, once each, in order.
func offeredFiles(blocks []editapply.EditBlock) []string {
	seen := map[string]bool{}
	var files []string
	for _, b := range blocks {
		if p := filepath.ToSlash(b.FilePath); !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	return files
}

// filesRead are the files read_file read this turn, once each, in order.
func filesRead(signatures []string) []string {
	const prefix = "builtin__read_file("
	seen := map[string]bool{}
	var files []string
	for _, sig := range signatures {
		raw, ok := strings.CutPrefix(sig, prefix)
		if !ok {
			continue
		}
		var args struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(strings.TrimSuffix(raw, ")")), &args) == nil && args.Path != "" && !seen[args.Path] {
			seen[args.Path] = true
			files = append(files, filepath.ToSlash(args.Path))
		}
	}
	return files
}

// listSaid is a list for the record: the first few, then how many more.
func listSaid(items []string) string {
	shown := items
	if len(items) > maxReportedFiles {
		shown = items[:maxReportedFiles]
	}
	out := strings.Join(shown, ", ")
	if len(items) > maxReportedFiles {
		out += ", and " + plural(len(items)-maxReportedFiles, "more")
	}
	return out
}

// maxWrapUpReserve is the most of a turn's time kept back for its last call.
const maxWrapUpReserve = 45 * time.Second

// wrapUpReserve is how long before its deadline a turn that will end with an
// answer (wrapUpAtLimit) stops taking steps, so that answer still has time:
// a deadline stop used to skip the wrap-up always, and the turn ended on the
// model's last half-sentence. A twentieth of the turn's time, at most
// maxWrapUpReserve; nothing for a phase that hands on, or a long task's segment.
func (s *Server) wrapUpReserve(bud budget) time.Duration {
	if !bud.wrapUpInside || bud.segment || bud.deadlineIsPhaseShare || s.cfg == nil {
		return 0
	}
	return min(maxWrapUpReserve, s.cfg.MCP.Budget.resolvedTurnTimeout()/20)
}
