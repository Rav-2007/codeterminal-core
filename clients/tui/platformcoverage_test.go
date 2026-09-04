package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// WHAT THIS PLATFORM DOES NOT RUN, SAID OUT LOUD.
//
// Five of this package's test files are `//go:build linux`, for one honest
// reason: allocating a pty is per-kernel (Linux uses TIOCSPTLCK/TIOCGPTN, the
// BSDs use TIOCPTYUNLK/TIOCPTYGNAME) and a portable second implementation is one
// nobody runs. That decision is fine. What is NOT fine is its consequence today:
// on macOS and Windows those files are not compiled, `go test ./...` prints a
// cheerful `ok`, and NOTHING anywhere says that the terminal-restore path, the
// signal handling, the real-binary sanitizer wiring and the render-determinism
// matrix were not exercised at all.
//
// That is the same fail-open shape the gate audit spent a day removing from this
// repo's scripts: a check that inspects nothing reports success. A build tag is
// a silent skip, and a silent skip is indistinguishable from a pass.
//
// So this test runs EVERYWHERE and does opposite jobs on either side:
//
//   - On Linux it is a GATE. Every file below must exist and must carry the tag,
//     and no linux-tagged file may exist that is not listed -- so a sixth
//     pty-backed suite cannot be added without appearing in the per-platform
//     status this list feeds.
//   - Everywhere else it is a REPORT. It names, in the test output of the
//     platform that is missing them, exactly which suites did not run.
//
// It asserts on the COUNT OF FILES INSPECTED, so a version that reads no files
// fails instead of passing.

// linuxOnlySuites is the per-platform status, in the one place that can be
// checked against the tree rather than drifting from it in prose.
var linuxOnlySuites = map[string]string{
	"ptysmoke_test.go":          "the real binary in a real terminal on a real socket",
	"exitsignals_pty_test.go":   "terminal restored on every exit path, and the SIGHUP gap (R1.1)",
	"brokenpipe_pty_test.go":    "an early reader closing the pipe under it",
	"sanitize_pty_test.go":      "escape filtering measured at an actual terminal",
	"renderprofile_pty_test.go": "the 14-environment x 4-profile determinism matrix (2.4)",
}

func TestPlatformCoverageIsStated(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	found := map[string]bool{}
	inspected := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		inspected++
		// The constraint is in the first few lines, above the package clause.
		head := string(b)
		if i := strings.Index(head, "\npackage "); i > 0 {
			head = head[:i]
		}
		for _, line := range strings.Split(head, "\n") {
			if strings.TrimSpace(line) == "//go:build linux" {
				found[e.Name()] = true
			}
		}
	}

	// A version of this test that read nothing would pass every assertion below
	// by vacuum. This is the floor that stops it.
	if inspected < 20 {
		t.Fatalf("inspected only %d .go files in %s; this test cannot have checked anything",
			inspected, mustAbs(t))
	}

	names := make([]string, 0, len(linuxOnlySuites))
	for n := range linuxOnlySuites {
		names = append(names, n)
	}
	sort.Strings(names)

	if runtime.GOOS != "linux" {
		t.Logf("PLATFORM COVERAGE on %s/%s: %d of %d .go files inspected; the following "+
			"suites are LINUX-ONLY and DID NOT RUN here:", runtime.GOOS, runtime.GOARCH, inspected, inspected)
		for _, n := range names {
			t.Logf("  NOT RUN  %-26s %s", n, linuxOnlySuites[n])
		}
		t.Logf("Everything else in this package DID run on %s. What is unverified here is "+
			"THE TERMINAL, not the signals: pty allocation, and the restore sequence that "+
			"exit paths are supposed to emit. Signal DELIVERY is covered here -- "+
			"exitsignals_test.go is //go:build !windows, so its six tests ran. It is the "+
			"six restore tests beside them, in exitsignals_pty_test.go, that did not.",
			runtime.GOOS)
		return
	}

	// On Linux the list is a gate rather than a note.
	for _, n := range names {
		if !found[n] {
			t.Errorf("%s is listed as a linux-only suite but is not tagged `//go:build linux` "+
				"(or no longer exists). The per-platform status in "+
				"docs/TUI_PRODUCTION_READINESS_2026-09-04.md is derived from this list, so a "+
				"stale entry there means the document is wrong about what macOS and Windows run.", n)
		}
	}
	for n := range found {
		if _, ok := linuxOnlySuites[n]; !ok {
			t.Errorf("%s is `//go:build linux` but is not in linuxOnlySuites. A suite that "+
				"silently does not run on two of three platforms has to be named somewhere a "+
				"person will read it; add it here with one line on what it covers.", n)
		}
	}
	t.Logf("PLATFORM COVERAGE: %d .go files inspected, %d linux-only suites, all declared",
		inspected, len(found))
}

func mustAbs(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(".")
	if err != nil {
		return "."
	}
	return p
}
