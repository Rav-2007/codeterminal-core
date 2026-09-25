package main

import "testing"

// THE ONE PATH THAT RUNS EVERYWHERE, AND THE ONE THAT MATTERS MOST.
//
// withoutEcho has two outcomes: it disables terminal echo and runs fn, or it
// CANNOT disable echo and runs fn anyway, reporting false so the caller warns
// that the key was visible. Under `go test` stdin is not a terminal, so this
// exercises the second -- on Linux, and on `cross (windows-latest, daemon)`,
// which is the only place termecho_windows.go is ever executed rather than
// merely compiled.
//
// The failure it guards against is not cosmetic. If the fallback returned
// without calling fn, readKey would collect nothing and `connect` would refuse a
// key the user had just typed, with no explanation. If it returned true, the
// caller would believe the key was hidden when it was not, and the warning
// telling the user to rotate it would never print.
//
// NOT COVERED, and it needs a human at a terminal: the branch where echo really
// is turned off and restored.
func TestWithoutEchoRunsTheReaderEvenWhenEchoCannotBeDisabled(t *testing.T) {
	if stdinIsTerminal() {
		t.Skip("NOT RUN: stdin is a terminal here, so this exercises the other branch")
	}

	calls := 0
	disabled := withoutEcho(func() { calls++ })

	if calls != 1 {
		t.Errorf("the reader ran %d times, want exactly 1: a key typed at the prompt would be lost", calls)
	}
	if disabled {
		t.Error("withoutEcho claims it hid the key, but stdin is not a terminal and nothing was hidden; " +
			"the caller would skip the warning that the key was shown in the clear")
	}
}

// stdinIsTerminal must answer, not panic, on a pipe -- it is the first thing
// readKey asks, and it decides whether the key is read from stdin or prompted for.
func TestStdinIsTerminalAnswersForAPipe(t *testing.T) {
	if stdinIsTerminal() {
		t.Log("stdin is a terminal in this environment; the answer is still an answer")
	}
}
