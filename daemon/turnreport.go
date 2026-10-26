package main

import (
	"encoding/json"
	"path/filepath"
	"regexp"
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
	finalChecked, last := false, (*ranCheck)(nil)
	for i := range f.checks {
		c := &f.checks[i]
		last = c
		if c.passed && c.edits == f.editsNow && isVerifyingLine(c.command) {
			finalChecked = true
		}
	}
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
		if !finalChecked && len(f.offered) > 0 && (last.passed && last.edits == f.editsNow) {
			lines = append(lines, "Not verified: that command does not build or test the project.")
		}
	}
	switch {
	case contradicted && !finalChecked:
		lines = append(lines, "The answer says the tests pass; this record shows no passing run of the project's "+
			"tests on the final version.")
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

// testsPassClaims are the shapes of "the tests pass" as models write it:
// "All 18 existing unit tests pass", "All tests now pass", "100% tests passed",
// "the tests pass".
var testsPassClaims = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\ball\s+(?:\d+\s+)?(?:of\s+the\s+)?(?:[a-z-]+\s+){0,3}?tests?\s+(?:now\s+|still\s+)?(?:pass|passes|passed|succeed|succeeded|are\s+passing)\b`),
	regexp.MustCompile(`(?i)\b100%\s+(?:of\s+)?(?:the\s+)?tests\s+passed\b`),
	regexp.MustCompile(`(?i)(?:^|[^0-9]\s)(?:the\s+)(?:[a-z-]+\s+){0,2}?tests?\s+(?:now\s+|all\s+)?(?:pass|passes|passed|are\s+passing)\b`),
}

// hedges make a sentence not a claim: "the tests should pass", "I could not
// run the tests", "once all tests pass".
var hedges = regexp.MustCompile(`(?i)\b(?:not|never|cannot|unable|should|would|might|may|if|once|until|expect|will|need|needs|try|let's|let\s+me|whether)\b|n't\b`)

// claimsTestsPass reports whether text says, without a hedge, that the tests
// pass.
func claimsTestsPass(text string) bool {
	for _, sentence := range regexp.MustCompile(`[.!?\n]+`).Split(text, -1) {
		if hedges.MatchString(sentence) {
			continue
		}
		for _, claim := range testsPassClaims {
			if claim.MatchString(sentence) {
				return true
			}
		}
	}
	return false
}

// isVerifyingLine is isVerifyingCommand (taskmodes.go) for a shell line, as
// run_command runs: true when one of its commands builds or tests the project.
// `cd build && ctest` does; `cd build && ls`, `cat a.txt` and a script do not --
// isVerifyingCommand was written for sandbox_exec, whose programs are all build
// tools, and alone it would count any command with an argument.
func isVerifyingLine(line string) bool {
	for _, words := range shellCommandWords(line) {
		core := commandCore(words)
		if len(core) == 0 {
			continue
		}
		program := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(core[0]), "./"), ".exe")
		switch {
		case strings.HasPrefix(program, "python") || program == "py":
			if len(core) >= 3 && core[1] == "-m" && pythonTestModules[core[2]] && isVerifyingCommand(strings.Join(core, " ")) {
				return true
			}
		case buildPrograms[program] && isVerifyingCommand(strings.Join(append([]string{program}, core[1:]...), " ")):
			return true
		}
	}
	return false
}

// buildPrograms build or test a project: the programs a check is run with.
var buildPrograms = map[string]bool{"go": true, "npm": true, "npx": true, "yarn": true, "pnpm": true, "bun": true,
	"deno": true, "make": true, "cargo": true, "cmake": true, "ctest": true, "mvn": true, "mvnw": true, "gradle": true,
	"gradlew": true, "sbt": true, "pytest": true, "tox": true, "nox": true, "dotnet": true, "mix": true, "rspec": true,
	"rake": true, "jest": true, "vitest": true, "phpunit": true, "swift": true, "bazel": true, "meson": true,
	"ninja": true, "zig": true, "stack": true, "cabal": true, "dune": true}

// pythonTestModules are what `python -m` runs a project's tests with.
var pythonTestModules = map[string]bool{"pytest": true, "unittest": true, "tox": true, "nox": true, "nose2": true}

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
