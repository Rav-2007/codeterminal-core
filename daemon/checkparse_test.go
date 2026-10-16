package main

import (
	"reflect"
	"strings"
	"testing"
)

// failed is a finished command that exited 1, as parseCheck receives it.
func exitedOne() execOutcome { return execOutcome{ran: true, exit: 1} }

func outputLines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

// WHAT A CHECK'S OUTPUT SAYS FAILED, for the build and test tools a task runs.
// Each case is the shape a real tool prints; the kind decides how the judge
// scores it, the names and locations are what a repair aims at.
//
// Neuter checks: drop the goTestFail case (the go test row loses its tests);
// drop leafNames (TestB is counted beside TestB/sub, Tests 3); drop the
// rsSummary filter (cargo's "could not compile" becomes a second error); read
// pytest's module ERROR as a test, or jest's suite failure; drop the colour
// strip; drop outsideProject (pytest's own frames become locations).
func TestParseCheckReadsEachTool(t *testing.T) {
	root := "/home/u/proj"
	cases := []struct {
		name, command, output string
		want                  checkOutcome
	}{
		{"go test failures", "go test ./...", outputLines(
			"--- FAIL: TestA (0.00s)",
			"    size_test.go:15: ParseSize(\"1KB\") = 0, want 1024",
			"--- FAIL: TestB (0.01s)",
			"    --- FAIL: TestB/sub (0.00s)",
			"        size_test.go:30: wrong",
			"FAIL",
			"FAIL\texample.com/m/units\t0.003s",
			"FAIL",
		), checkOutcome{Kind: checkTest, Failing: []string{"TestA", "TestB/sub"}, Tests: 2,
			Locations: []string{"size_test.go:15", "size_test.go:30"}}},
		{"go build errors", "go build ./...", outputLines(
			"# example.com/m/units",
			"units/size.go:12:9: undefined: foo",
			"./units/size.go:20:2: missing return",
		), checkOutcome{Kind: checkBuild, Errors: 2, Locations: []string{"units/size.go:12", "units/size.go:20"}}},
		{"go test that does not build", "go test ./units", outputLines(
			"# example.com/m/units [example.com/m/units.test]",
			"units/size_test.go:8:5: undefined: ParseSize",
			"FAIL\texample.com/m/units [build failed]",
			"FAIL",
		), checkOutcome{Kind: checkBuild, Errors: 1, Locations: []string{"units/size_test.go:8"}}},
		{"go vet", "go vet ./...", outputLines(
			"# example.com/m/units",
			"units/size.go:10:2: fmt.Printf format %d has arg s of wrong type string",
		), checkOutcome{Kind: checkVet, Errors: 1, Locations: []string{"units/size.go:10"}}},
		{"go panic", "go test ./units", outputLines(
			"--- FAIL: TestParse (0.00s)",
			"panic: runtime error: index out of range [3] with length 3 [recovered]",
			"\tpanic: runtime error: index out of range [3] with length 3",
			"",
			"goroutine 7 [running]:",
			"testing.tRunner.func1.2({0x5191a0, 0xc000014120})",
			"\t/usr/local/go/src/testing/testing.go:1631 +0x24a",
			"example.com/m/units.ParseSize({0x0, 0x0})",
			"\t/home/u/proj/units/size.go:14 +0x1d",
			"example.com/m/units.TestParse(0xc0000a4000)",
			"\t/home/u/proj/units/size_test.go:9 +0x25",
			"FAIL\texample.com/m/units\t0.004s",
		), checkOutcome{Kind: checkPanic, Failing: []string{"TestParse"}, Tests: 1,
			Locations: []string{"units/size.go:14"}}},
		{"pytest", "pytest -q", outputLines(
			"tests/test_size.py:12: AssertionError",
			"FAILED tests/test_size.py::test_parse - AssertionError: assert 0 == 1024",
			"FAILED tests/test_size.py::test_units - KeyError: 'kb'",
			"2 failed, 3 passed in 0.12s",
		), checkOutcome{Kind: checkTest, Failing: []string{"tests/test_size.py::test_parse", "tests/test_size.py::test_units"},
			Tests: 2, Locations: []string{"tests/test_size.py:12"}}},
		{"cargo build", "cargo build", outputLines(
			"error[E0425]: cannot find value `x` in this scope",
			" --> src/main.rs:3:5",
			"  |",
			"error: could not compile `demo` (bin \"demo\") due to 1 previous error",
		), checkOutcome{Kind: checkBuild, Errors: 1, Locations: []string{"src/main.rs:3"}}},
		{"cargo test", "cargo test", outputLines(
			"---- tests::it_works stdout ----",
			"thread 'tests::it_works' panicked at src/lib.rs:10:9:",
			"assertion `left == right` failed",
			"failures:",
			"    tests::it_works",
			"test result: FAILED. 0 passed; 1 failed; 0 ignored",
		), checkOutcome{Kind: checkTest, Failing: []string{"tests::it_works"}, Tests: 1,
			Locations: []string{"src/lib.rs:10"}}},
		{"jest", "npx jest", outputLines(
			" FAIL  src/sum.test.js",
			"  ● sum › adds 1 + 2 to equal 3",
			"    expect(received).toBe(expected)",
			"      at Object.<anonymous> (src/sum.test.js:3:20)",
		), checkOutcome{Kind: checkTest, Failing: []string{"sum › adds 1 + 2 to equal 3"}, Tests: 1,
			Locations: []string{"src/sum.test.js:3"}}},
		{"tsc, both layouts", "npx tsc --noEmit", outputLines(
			"src/index.ts(3,5): error TS2304: Cannot find name 'foo'.",
			"src/util.ts:7:1 - error TS2322: Type 'string' is not assignable to type 'number'.",
		), checkOutcome{Kind: checkBuild, Errors: 2, Locations: []string{"src/index.ts:3", "src/util.ts:7"}}},
		{"nothing recognisable", "make check", outputLines("something went wrong", "make: *** [check] Error 2"),
			checkOutcome{Kind: checkOther}},
		// The three below are cut from real runs (pytest 9.1, jest 29.7, tsc
		// 5.4, 2026-10-16): a file that does not load ran none of its tests.
		{"pytest module that does not import", "pytest", outputLines(
			"collected 0 items / 1 error",
			"__________________________ ERROR collecting test_m.py __________________________",
			"../venv/lib/python3.12/site-packages/_pytest/python.py:508: in importtestmodule",
			".venv/lib/python3.12/site-packages/_pytest/pathlib.py:596: in import_path",
			"/usr/lib/python3.12/importlib/__init__.py:90: in import_module",
			"E     File \"/home/u/proj/test_m.py\", line 3",
			"E       def c(: return 3",
			"E   SyntaxError: invalid syntax",
			"=========================== short test summary info ============================",
			"ERROR test_m.py",
			"!!!!!!!!!!!!!!!!!!!! Interrupted: 1 error during collection !!!!!!!!!!!!!!!!!!!!",
		), checkOutcome{Kind: checkBuild, Errors: 1, Locations: []string{"test_m.py:3"}}},
		{"jest suite that does not load", "npx jest", outputLines(
			"FAIL ./m.test.js",
			"  ● Test suite failed to run",
			"    Jest encountered an unexpected token",
			"    SyntaxError: /home/u/proj/m.test.js: Unexpected token (5:0)",
			"Test Suites: 1 failed, 1 total",
		), checkOutcome{Kind: checkBuild, Errors: 1, Locations: []string{"m.test.js:5"}}},
		{"tsc --pretty, in colour", "npx tsc --pretty", outputLines(
			"\x1b[96msrc/a.ts\x1b[0m:\x1b[93m1\x1b[0m:\x1b[93m14\x1b[0m - \x1b[91merror\x1b[0m\x1b[90m TS2322: \x1b[0mType 'string' is not assignable to type 'number'.",
		), checkOutcome{Kind: checkBuild, Errors: 1, Locations: []string{"src/a.ts:1"}}},
	}
	for _, c := range cases {
		got := parseCheck(c.command, exitedOne(), c.output, root)
		if got.Signature == "" {
			t.Errorf("%s: no signature", c.name)
		}
		got.Signature = ""
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

// ONLY THE EXIT STATUS SAYS PASS, and a timeout is its own kind. A program that
// exits 0 passes whatever it printed; one that timed out is not scored as
// better or worse than anything (correction.go reads it as unknown).
func TestParseCheckPassAndTimeout(t *testing.T) {
	scary := outputLines("--- FAIL: TestA (0.00s)", "panic: boom")
	if got := parseCheck("go test ./...", execOutcome{ran: true, exit: 0}, scary, ""); got.Kind != checkPass || got.Signature != "" {
		t.Errorf("exit 0 read as %+v, want a plain pass", got)
	}
	got := parseCheck("go test ./...", execOutcome{ran: true, exit: -1, timedOut: true}, scary, "")
	if got.Kind != checkTimeout || got.Tests != 0 {
		t.Errorf("a timeout read as %+v", got)
	}
}

// THE SIGNATURE NAMES WHAT FAILED, NOT WHERE OR WHEN. The same failure after an
// edit moved its line, on a slower run, from another temporary directory, at
// another address, in colour, is the same failure -- or the judge would see a
// "new" one on every run and never a recurrence. A different failing test is
// different, and so is a module that fails to import for a different reason.
//
// Neuter checks: drop sigLineCol (a line moved inside a message changes it);
// drop sigDuration (a timing inside a message changes it); drop sigTemp (the
// other temp dir changes it).
func TestASignatureIsStableAcrossRuns(t *testing.T) {
	sig := func(out string) string { return parseCheck("go test ./...", exitedOne(), out, "").Signature }
	panicRun := func(addr, line, took, tmp string) string {
		return outputLines("--- FAIL: TestA ("+took+")",
			"panic: runtime error: invalid memory address "+addr+" at "+tmp+"/b001/x.go:"+line,
			"FAIL\texample.com/m\t"+took)
	}
	base := sig(panicRun("0xc000012345", "12", "0.01s", "/tmp/go-build1234"))
	for name, other := range map[string]string{
		"a moved line":       panicRun("0xc000012345", "40", "0.01s", "/tmp/go-build1234"),
		"a slower run":       panicRun("0xc000012345", "12", "2.5s", "/tmp/go-build1234"),
		"another temp dir":   panicRun("0xc000012345", "12", "0.01s", "/tmp/go-build9876"),
		"another address":    panicRun("0xc0000abcde", "12", "0.01s", "/tmp/go-build1234"),
		"everything at once": panicRun("0xc0000abcde", "77", "9.1s", "/tmp/go-build4242"),
	} {
		if got := sig(other); got != base {
			t.Errorf("%s changed the signature: %s vs %s", name, got, base)
		}
	}
	// A line or a timing inside the message itself, not only around it.
	for name, pair := range map[string][2]string{
		"a moved line in a panic": {
			"panic: bad size at units/size.go:14",
			"panic: bad size at units/size.go:40"},
		"a Go duration in a panic": {
			"panic: test timed out after 10m0s",
			"panic: test timed out after 1m30s"},
		"a moved line in output nothing recognised": {
			"crashed in src/app.js:12:5\nexit 1",
			"crashed in src/app.js:47:9\nexit 1"},
		"a timing in output nothing recognised": {
			"gave up after 1.234s\nexit 1",
			"gave up after 9.9s\nexit 1"},
	} {
		if a, b := sig(outputLines(pair[0])), sig(outputLines(pair[1])); a != b {
			t.Errorf("%s changed the signature: %s vs %s", name, a, b)
		}
	}
	build := func(line string) string { return sig(outputLines("x/a.go:" + line + ":3: undefined: foo")) }
	if build("10") != build("99") {
		t.Error("a compile error that moved changed its signature")
	}
	if sig(outputLines("--- FAIL: TestA (0.00s)")) == sig(outputLines("--- FAIL: TestB (0.00s)")) {
		t.Error("two different failing tests share a signature")
	}
	// A module mended from a syntax error into an import error has moved on.
	notImported := func(raised string) string {
		return sig(outputLines("____ ERROR collecting test_m.py ____", "E   "+raised+": x", "ERROR test_m.py"))
	}
	if notImported("SyntaxError") == notImported("ModuleNotFoundError") {
		t.Error("a syntax error and an import error in one module share a signature")
	}
	// Colour is not part of a failure.
	plain := parseCheck("tsc", exitedOne(), outputLines("src/a.ts(1,14): error TS2322: Type 'string' is not assignable to type 'number'."), "")
	coloured := parseCheck("tsc", exitedOne(), outputLines("\x1b[96msrc/a.ts\x1b[0m:\x1b[93m1\x1b[0m:\x1b[93m14\x1b[0m - "+
		"\x1b[91merror\x1b[0m\x1b[90m TS2322: \x1b[0mType 'string' is not assignable to type 'number'."), "")
	if plain.Signature != coloured.Signature {
		t.Errorf("colour changed the signature: %+v vs %+v", plain, coloured)
	}
}

// A LONG OUTPUT IS COUNTED, NOT ONLY LISTED. Fifty failing tests are fifty in
// the score, while the ledger keeps the first twenty names.
func TestParseCheckBoundsWhatItKeeps(t *testing.T) {
	var ls []string
	for i := 0; i < 50; i++ {
		ls = append(ls, "--- FAIL: TestN"+strings.Repeat("x", i)+" (0.00s)",
			"    a_test.go:"+strings.Repeat("1", i+1)+": bad")
	}
	got := parseCheck("go test ./...", exitedOne(), outputLines(ls...), "")
	if got.Tests != 50 || len(got.Failing) != maxOutcomeNames || len(got.Locations) != maxOutcomeLocations {
		t.Errorf("kept %d names of %d tests and %d locations; want %d of 50 and %d",
			len(got.Failing), got.Tests, len(got.Locations), maxOutcomeNames, maxOutcomeLocations)
	}
}
