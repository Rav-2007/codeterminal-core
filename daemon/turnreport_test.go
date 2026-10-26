package main

import (
	"strings"
	"testing"

	"mochiii/editapply"
	"mochiii/protocol"
)

// MOCHIII'S RECORD UNDER THE ANSWER (turnreport.go).

// The claims the real-repo run produced are claims; a hedge, a negation or a
// promise is not.
func TestAClaimThatTheTestsPassIsRecognised(t *testing.T) {
	for _, claim := range []string{
		"All 18 tests pass.", "All 49 existing tests pass", "All 246 tests pass, confirming the fix.",
		"All 18 existing unit tests pass", "ctest --test-dir build — **100% tests passed, 0 failed out of 19**",
		"The tests now pass.", "All tests pass and the crash is gone.",
	} {
		if !claimsTestsPass(claim) {
			t.Errorf("not read as a claim: %q", claim)
		}
	}
	for _, not := range []string{
		"Now let's run the tests to verify the fix:", "I could not run the tests in this environment.",
		"The tests should pass once the build finishes.", "The tests did not pass.", "If all tests pass, we are done.",
		"Once the tests pass I will commit.", "3 tests passed and 1 failed.", "Let me check whether the tests pass.",
		"I will run the tests next.",
	} {
		if claimsTestsPass(not) {
			t.Errorf("read as a claim: %q", not)
		}
	}
}

func TestTheRecordSaysWhereATurnStands(t *testing.T) {
	edit := []editapply.EditBlock{{FilePath: "cJSON.c", Search: "a", Replace: "b"}}
	stop := &protocol.IncompleteInfo{Reason: protocol.IncompleteAgentBudget,
		Detail: "this task reached its limit of 16 steps before finishing — what you see above is everything that was done."}

	// A turn stopped on its limit, mid-thought: where it stopped, what changed,
	// that the last check predates the last edit, and what it read.
	report := turnReport(turnFacts{
		result: agentResult{FinalText: "Now let's run the tests to verify the fix:", Incomplete: stop,
			ToolSignatures: []string{`builtin__read_file({"path":"cJSON.c"})`, `builtin__read_file({"path":"tests/misc_tests.c"})`}},
		offered: edit, checks: []ranCheck{{command: "ctest --test-dir build", passed: true, edits: 0}}, editsNow: 1,
	})
	for _, want := range []string{"Mochiii's record", "Stopped: reached its limit of 16 steps before finishing.",
		"Changed: cJSON.c", "Last check: `ctest --test-dir build` passed, before the last change",
		"Read: cJSON.c, tests/misc_tests.c."} {
		if !strings.Contains(report, want) {
			t.Errorf("the record does not say %q:\n%s", want, report)
		}
	}

	// THE cJSON CASE: a claim the record does not support, and a changed test.
	report = turnReport(turnFacts{
		result:  agentResult{FinalText: "All 18 existing unit tests pass."},
		offered: append(edit, editapply.EditBlock{FilePath: "tests/misc_tests.c", Search: "TEST_ASSERT_TRUE(x);", Replace: "TEST_ASSERT_FALSE(x);"}),
		checks:  []ranCheck{{command: "ctest --test-dir build", passed: true, edits: 2}}, editsNow: 2,
	})
	for _, want := range []string{"Existing tests changed: tests/misc_tests.c",
		"The answer says the tests pass; they passed on the final version only after existing tests were changed."} {
		if !strings.Contains(report, want) {
			t.Errorf("the record does not say %q:\n%s", want, report)
		}
	}

	// A finished turn whose claim the record supports gets no record at all:
	// the check line above the review already says it.
	if report := turnReport(turnFacts{
		result:  agentResult{FinalText: "All 19 tests pass."},
		offered: edit, checks: []ranCheck{{command: "cd build && ctest", passed: true, edits: 1}}, editsNow: 1,
	}); report != "" {
		t.Errorf("a finished, checked turn got a record:\n%s", report)
	}
	// The claim with no check on the final version is contradicted.
	if report := turnReport(turnFacts{
		result:  agentResult{FinalText: "All 19 tests pass."},
		offered: edit, checks: []ranCheck{{command: "cd build && ls", passed: true, edits: 1}}, editsNow: 1,
	}); !strings.Contains(report, "The answer says the tests pass") {
		t.Errorf("a claim backed by a command that tests nothing was not contradicted:\n%s", report)
	}
	// A build that passed, and then a test run that failed, on the final version:
	// the claim is contradicted -- the last run speaks for the version, not the
	// best one (the cJSON trial of a real-model run, 2026-10-27).
	if report := turnReport(turnFacts{
		result:  agentResult{FinalText: "All 19 tests pass."},
		offered: edit, checks: []ranCheck{{command: "cmake --build build", passed: true, edits: 1},
			{command: "ctest --test-dir build", passed: false, edits: 1}}, editsNow: 1,
	}); !strings.Contains(report, "The answer says the tests pass; the last build or test run on the final version failed.") {
		t.Errorf("a claim after a passing build and a failing test run was not contradicted:\n%s", report)
	}
	// And when something that tests nothing ran after the failing run, the
	// record still names the failing run.
	report = turnReport(turnFacts{
		result:  agentResult{FinalText: "All 19 tests pass."},
		offered: edit, checks: []ranCheck{{command: "ctest --test-dir build", passed: false, edits: 1},
			{command: "cat build/Testing/Temporary/LastTest.log", passed: true, edits: 1}}, editsNow: 1,
	})
	for _, want := range []string{"Last check: `cat build/Testing/Temporary/LastTest.log` passed, on the final version.",
		"The last build or test run on the final version, `ctest --test-dir build`, FAILED.",
		"The answer says the tests pass; the last build or test run on the final version failed."} {
		if !strings.Contains(report, want) {
			t.Errorf("the record does not say %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "does not build or test the project") {
		t.Errorf("the record calls the turn untested when its test run failed:\n%s", report)
	}
	// A pass before the last edit does not back a claim about the final version.
	if report := turnReport(turnFacts{
		result:  agentResult{FinalText: "All 19 tests pass."},
		offered: edit, checks: []ranCheck{{command: "ctest --test-dir build", passed: true, edits: 0}}, editsNow: 1,
	}); !strings.Contains(report, "The answer says the tests pass") {
		t.Errorf("a claim backed by a run before the last edit was not contradicted:\n%s", report)
	}
	// Reading a file is not checking the work, though the command has an argument.
	if report := turnReport(turnFacts{
		result:  agentResult{FinalText: "All 19 tests pass."},
		offered: edit, checks: []ranCheck{{command: "cat cJSON.c", passed: true, edits: 1}}, editsNow: 1,
	}); !strings.Contains(report, "The answer says the tests pass") {
		t.Errorf("a claim backed by reading a file was not contradicted:\n%s", report)
	}
	for line, want := range map[string]bool{"ctest --test-dir build": true, "cd build && ctest": true,
		"python -m pytest tests/": true, "./gradlew test": true, "mvn -o test -pl gson": true,
		"cat a.txt": false, "python script.py": false, "pytest --collect-only": false, "go version": false} {
		if got := isVerifyingLine(line); got != want {
			t.Errorf("isVerifyingLine(%q) = %v, want %v", line, got, want)
		}
	}
	// Changes outside review are named even on a finished turn.
	if report := turnReport(turnFacts{result: agentResult{FinalText: "Installed."},
		outside: []string{"package.json (npm install lodash)"}}); !strings.Contains(report, "outside review: package.json (npm install lodash)") {
		t.Errorf("a change outside review was not named:\n%s", report)
	}
}
