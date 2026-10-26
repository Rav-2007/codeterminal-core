package main

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"mochiii/editapply"
)

// AN EXISTING TEST IS EVIDENCE, NOT AN OBSTACLE (2026-10-26).
//
// In a run of real fixes from real GitHub projects the agent's cJSON fix broke
// an existing test. It ran the suite, saw the failure -- and rewrote the test
// to fit its fix, then reported "All 18 existing unit tests pass". The check
// line above the review would have said "✓ checked: ctest passed" too: a pass
// that came from the changed test, wearing the tick that is meant to say the
// change works. Upstream's own fix kept the test as it was and changed less.
//
// So a change to the existing lines of a test is FOUND, wherever it came from
// (an edit, or a command run in the working copy), and said three times: to
// the model the moment it makes one, to the user above the review (no tick for
// a pass after it), and in Mochiii's record under the answer. It is not
// refused: a test can be wrong, and fixing one is real work -- but then the
// user is told, and the model is asked to say which test and why.
//
// ADDING tests is not changing them. A new regression test -- every inspectable
// trial in that run wrote one -- leaves every existing line in place, and is
// not flagged.

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

// testEditNote is what the model is told when an edit changes the existing
// lines of a test, or "" for any other edit.
func testEditNote(rel string, block editapply.EditBlock, created bool) string {
	if created || !isTestPath(rel) || !changesExistingLines(block.Search, block.Replace) {
		return ""
	}
	return testChangeWarning(filepath.ToSlash(rel))
}

// testChangeWarning is the note itself, for an edit or for a command that
// changed a test.
func testChangeWarning(paths ...string) string {
	return "\n[Mochiii: this changes existing lines of a test (" + strings.Join(paths, ", ") + "). An existing " +
		"test that fails after your change is evidence that the change broke something the project relies on: " +
		"change your code, not the test -- unless the test itself is wrong, and then tell the user which test and " +
		"why. The user is shown that the tests were changed.]"
}

// testsChangedByCommand lists the existing test files whose existing lines a
// command just changed in the working copy (changed is what it touched).
func (st *stagedWorkspace) testsChangedByCommand(changed []string) []string {
	var tests []string
	for _, rel := range changed {
		if _, existed := st.manifest[rel]; !existed || !isTestPath(rel) {
			continue
		}
		before, _, err1 := readRealFile(st, rel)
		after, ok, err2 := st.readCopyFile(rel, maxFileSize)
		if err1 == nil && err2 == nil && (!ok || changesExistingLines(before, string(after))) {
			tests = append(tests, filepath.ToSlash(rel))
		}
	}
	sort.Strings(tests)
	return tests
}

// readRealFile reads the project's own rel: the same bounded, by-name read
// the copy's files get (stagedWorkspace.readCopyFile), rooted at the project.
func readRealFile(st *stagedWorkspace, rel string) (string, bool, error) {
	data, ok, err := (&stagedWorkspace{root: st.real}).readCopyFile(rel, maxFileSize)
	return string(data), ok, err
}

// testsWeakenedIn is testsWeakened (correction.go) in every language: what an
// edit did to the tests it touched, for a long task's judge.
func testsWeakenedIn(rel, search, replace string) string {
	if w := testsWeakened(search, replace); w != "" {
		return w
	}
	if search != "" && isTestPath(rel) && changesExistingLines(search, replace) {
		return "changed existing lines of a test"
	}
	return ""
}
