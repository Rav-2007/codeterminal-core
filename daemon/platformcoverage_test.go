package main

import (
	"go/build"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestPlatformCoverageIsStated turns a block of silently-uncompiled test files
// into a line in the log of the platform that is missing them.
//
// WHY. `go test` prints "ok" for a package whose platform-tagged files were
// never compiled. On windows-latest this module loses test files to build
// constraints and NOTHING SAID SO -- the run looked identical to one where
// everything executed. Skips must never read as passes, and until 2026-09-09
// only clients/tui had a banner like this; daemon, editapply and protocol lost
// files on Windows in silence.
//
// DERIVED, NOT ENUMERATED. clients/tui's version keeps a hand-written map of
// linux-only suites with descriptions, which is right there -- it explains WHAT
// is lost -- but a hand list cannot see a file that was renamed or added, and
// this repository has now found that class six times. Here the excluded set is
// computed from the build constraints themselves via go/build's MatchFile, so
// adding a platform-tagged test file changes this output with no edit.
func TestPlatformCoverageIsStated(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	ctx := build.Default // this GOOS/GOARCH, which is the whole question
	var total, compiled int
	var excluded []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		total++
		ok, err := ctx.MatchFile(".", name)
		if err != nil {
			t.Fatalf("reading build constraints of %s: %v", name, err)
		}
		if ok {
			compiled++
		} else {
			excluded = append(excluded, name)
		}
	}

	// A scan that read nothing would report perfect coverage. Measured 177
	// test files here on 2026-09-09; the floor sits well below that so ordinary
	// churn does not trip it, and a collapse to zero still does.
	if total < 100 {
		t.Fatalf("found only %d _test.go files; this test cannot have checked anything", total)
	}

	sort.Strings(excluded)
	if len(excluded) == 0 {
		t.Logf("PLATFORM COVERAGE on %s/%s: all %d test files compile here.", runtime.GOOS, runtime.GOARCH, total)
		return
	}
	t.Logf("PLATFORM COVERAGE on %s/%s: %d of %d test files compiled; %d DID NOT RUN here:",
		runtime.GOOS, runtime.GOARCH, compiled, total, len(excluded))
	for _, n := range excluded {
		t.Logf("  NOT RUN  %s", n)
	}
	t.Logf("Their build constraints -- an OS/arch tag, or a custom one like `eval` -- exclude " +
		"this configuration. That is not a failure. It is the part of this package that THIS " +
		"run cannot speak for, named so a green `ok` is not read as covering it.")
}
