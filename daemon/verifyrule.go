package main

// testYourChanges ends workingCopyDirective (stage.go): how a turn with a
// working copy tests its own edits, and what it may say about them afterwards.
//
// "ALL 18 TESTS PASS" -- WITHOUT THE PROJECT'S TESTS (2026-10-26). In a run of
// eight real fixes from real GitHub projects (C, C++, Java, Python) most of the
// agent's "testing" ran where its edits were not -- run_command, in the real
// project -- or was a program it wrote itself in /tmp. On cJSON its fix broke an
// existing test while it told the user "All 18 tests pass". The sentence this
// replaced named the right tool, but not the project's OWN tests.
//
// A RULE IS NOT WHAT FIXED THE PLACE. A stronger version of this text said that
// run_command could not see the edits, and the model made more run_command
// calls, not fewer (59 -> 107). What fixed it is that run_command now runs in
// this same working copy (runplace.go); the text only says how to use that.
const testYourChanges = "So work the way an engineer does. After your last edit, run the PROJECT'S OWN tests " +
	"for the code you changed -- its existing test suite, through its usual build and test commands -- read what " +
	"they say, and fix what fails before you finish. Every command you run inside the project, with sandbox_exec " +
	"or run_command, runs in this working copy and sees your edits; prefer sandbox_exec, which is confined. A build " +
	"in several steps is one call per step, and dir (cwd for run_command) runs a step from a subfolder. A program " +
	"you write yourself is not the project's tests, and a file a command creates is not offered for review: create " +
	"files with propose_edit. When you finish, say exactly which commands you ran and what they reported -- and if " +
	"you could not run the project's tests, say so plainly instead of saying they pass. An existing test that " +
	"starts failing after your change is evidence against the change: change your code, not the test -- unless the " +
	"test itself is wrong, and then tell the user which test and why. Make the smallest change that fixes the " +
	"reported problem and keeps what the existing tests check, and run the whole suite, not only a new test."
