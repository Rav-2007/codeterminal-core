package main

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"mochiii/editapply"
)

// WHAT COUNTS AS EVIDENCE in Mochiii's record of a turn (turnreport.go): an
// answer saying the tests pass, a command that builds or tests the project, a
// change to the existing lines of a test.
//
// A file of its own, needing nothing of one view's but the shell-line reader
// (shellline.go), so that the real-repo eval measures the agent as it was
// before one view by the very same definitions: that arm is main as it was,
// plus these two files and the eval.

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

// isTestPath reports whether rel, a workspace-relative path, is a test file or
// test data by a common convention of any language: retrieval's two (Go's
// _test.go, and isTestPathBeyondGo's Python, JavaScript, Java and test folders)
// and C, C++, Kotlin, Swift and Ruby's. A Markdown file is never one.
func isTestPath(rel string) bool {
	slash := filepath.ToSlash(rel)
	base := strings.ToLower(path.Base(slash))
	ext := path.Ext(base)
	if ext == ".md" {
		return false
	}
	if isTestFile(base) || isTestPathBeyondGo(slash) {
		return true
	}
	stem := strings.TrimSuffix(base, ext)
	// CamelCase as written (FooTest.cpp, FooTests.swift): "contest" is not one.
	camel := strings.TrimSuffix(path.Base(slash), path.Ext(slash))
	switch ext {
	case ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".kt", ".swift", ".rb", ".rs", ".m", ".mm":
		for _, affix := range []string{"_test", "_tests", "-test", "-tests", "_spec"} {
			if strings.HasSuffix(stem, affix) {
				return true
			}
		}
		if strings.HasPrefix(stem, "test_") || strings.HasSuffix(camel, "Test") || strings.HasSuffix(camel, "Tests") {
			return true
		}
	}
	for _, dir := range strings.Split(path.Dir(slash), "/") {
		switch strings.ToLower(dir) {
		case "spec", "testing", "testdata", "unittest", "unittests", "unit_tests", "test-suite", "testsuite":
			return true
		}
	}
	return false
}

// changesExistingLines reports whether after drops or alters a line of before:
// false when every line of before is still in after, in order -- an addition,
// such as a new test case -- and true otherwise. Line ends are not compared.
func changesExistingLines(before, after string) bool {
	if before == "" {
		return false
	}
	have := strings.Split(strings.ReplaceAll(after, "\r\n", "\n"), "\n")
	j := 0
	for _, line := range strings.Split(strings.ReplaceAll(before, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		for j < len(have) && strings.TrimRight(have[j], " \t") != line {
			j++
		}
		if j == len(have) {
			return true
		}
		j++
	}
	return false
}

// testsChangedIn lists, sorted, the existing test files whose existing lines
// the offered edit blocks change. A block with no SEARCH creates a file, and a
// new file is a new test, not a changed one.
func testsChangedIn(blocks []editapply.EditBlock) []string {
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.Search != "" && isTestPath(b.FilePath) && changesExistingLines(b.Search, b.Replace) {
			seen[filepath.ToSlash(b.FilePath)] = true
		}
	}
	return sortedSet(seen)
}

// lastCheckOfFinal is the last build or test run on the final version of the
// changes -- the one made with editsNow edits in place -- or nil. It is what
// speaks for that version: in a real-model run (2026-10-27) a build passed and
// then the test run after it failed, and "tested on the final version" was
// said of it because one run had passed.
func lastCheckOfFinal(checks []ranCheck, editsNow int) *ranCheck {
	var last *ranCheck
	for i := range checks {
		if c := &checks[i]; c.edits == editsNow && isVerifyingLine(c.command) {
			last = c
		}
	}
	return last
}
