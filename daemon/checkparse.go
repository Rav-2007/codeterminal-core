package main

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// READING A CHECK, NOT TRUSTING ONE. A command's exit status says whether it
// passed; its output says what failed and where. The model reads that output
// too -- but a model can skim it, misread it, or be told by it what to do, and
// the judge that decides whether an attempt made things better or worse
// (correction.go) must depend on none of that. parseCheck reads the output the
// way a person scanning a CI log does: the tests named as failing, the compile
// errors and the lines they point at, a panic and the first frame of it that
// is in the project. Go first; pytest, cargo, jest and tsc by their own
// markers; any compiler that prints file:line:col: messages. What it cannot
// recognise it reports as "other" -- never as a pass, which only the exit
// status can say.

// Check kinds: what a check's output says, in one word.
const (
	checkPass    = "pass"
	checkBuild   = "build"
	checkVet     = "vet"
	checkTest    = "test"
	checkPanic   = "panic"
	checkTimeout = "timeout"
	checkOther   = "other"
)

// What one outcome keeps: enough to say what failed, small enough to carry in
// a ledger. The counts are of everything; the lists are the first few.
const (
	maxOutcomeNames     = 20
	maxOutcomeLocations = 10
	otherSignatureLines = 20
)

// checkOutcome is what one check's output says failed.
type checkOutcome struct {
	Kind string `json:"kind"`
	// Failing names the failing tests, sorted, at most maxOutcomeNames of
	// them; Tests counts every one. Errors counts the distinct build or vet
	// errors.
	Failing []string `json:"failing,omitempty"`
	Tests   int      `json:"tests,omitempty"`
	Errors  int      `json:"errors,omitempty"`
	// Locations are where the failures point, path:line in the project, in
	// the order the output gave them.
	Locations []string `json:"locations,omitempty"`
	// Signature names WHAT failed, not where or when: the same failure after
	// an edit moved its line, or on a slower run, has the same signature.
	Signature string `json:"signature,omitempty"`
}

var (
	goTestFail = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	// FAIL <pkg> <time> | [build failed] | [setup failed]
	goPkgFail = regexp.MustCompile(`^FAIL\s+(\S+)\s+(\[build failed\]|\[setup failed\]|[0-9.]+s)\s*$`)
	goPanic   = regexp.MustCompile(`^panic: (.+?)(?: \[recovered\])?$`)
	// path:line:col: message -- Go, gcc, clang and most linters.
	compileError = regexp.MustCompile(`^(\S+?\.[A-Za-z0-9]+):(\d+):(\d+):\s*(.+)$`)
	// a t.Error line under its --- FAIL, indented, no column
	goTestLine = regexp.MustCompile(`^\s+(\S+?_test\.go):(\d+): `)
	// one frame of a panic's stack
	goFrame = regexp.MustCompile(`^\s+(\S+\.go):(\d+)(?: \+0x[0-9a-fA-F]+)?$`)
	pyFail  = regexp.MustCompile(`^(?:FAILED|ERROR) (\S+?)(?: - .*)?$`)
	pyLine  = regexp.MustCompile(`^(\S+?\.py):(\d+): `)
	// where a module that could not be imported went wrong: E  File "x.py", line 3
	pySource = regexp.MustCompile(`^E\s+File "([^"]+\.py)", line (\d+)`)
	// ____ ERROR collecting test_m.py ____, and what stopped it: E  SyntaxError: ...
	pyCollecting = regexp.MustCompile(`^_+ ERROR collecting (\S+) _+$`)
	pyRaised     = regexp.MustCompile(`^E\s+([A-Za-z_][\w.]*(?:Error|Exception))\b`)
	rsError      = regexp.MustCompile(`^error(?:\[E\d+\])?: (.+)$`)
	rsArrow      = regexp.MustCompile(`^\s*--> (\S+?):(\d+):\d+`)
	rsTest       = regexp.MustCompile(`^---- (\S+) stdout ----$`)
	rsPanic      = regexp.MustCompile(`^thread '.*' panicked at (\S+?):(\d+):\d+`)
	jsFail       = regexp.MustCompile(`^\s*● (.+?)\s*$`)
	jsAt         = regexp.MustCompile(`\((\S+?\.[jt]sx?):(\d+):\d+\)`)
	// FAIL ./m.test.js -- the suite the lines below it belong to
	jsSuite = regexp.MustCompile(`^FAIL\s+(\S+\.[jt]sx?)\s*$`)
	// SyntaxError: /abs/m.test.js: Unexpected token (5:0)
	jsSyntax = regexp.MustCompile(`^\s*SyntaxError: (\S+?\.[jt]sx?): .*\((\d+):\d+\)\s*$`)
	tsParen  = regexp.MustCompile(`^(\S+?\.tsx?)\((\d+),\d+\): error (TS\d+): (.+)$`)
	tsDash   = regexp.MustCompile(`^(\S+?\.tsx?):(\d+):\d+ - error (TS\d+): (.+)$`)
)

// jsSuiteFailed is how jest says a test file could not even be loaded.
const jsSuiteFailed = "Test suite failed to run"

// Summary lines a Rust build prints after the errors it already listed.
var rsSummary = []string{"could not compile", "aborting due to", "test failed", "build failed"}

// parseCheck reads one finished check. root is the project's real root, so a
// location the output gives as an absolute path comes back relative to it.
func parseCheck(command string, out execOutcome, output, root string) checkOutcome {
	switch {
	case out.timedOut:
		return checkOutcome{Kind: checkTimeout, Signature: signatureOf(checkTimeout, []string{commandKey(command)})}
	case out.ran && out.exit == 0:
		return checkOutcome{Kind: checkPass}
	}

	var (
		failing   = map[string]bool{}
		errs      = map[string]bool{}
		pkgs      []string
		buildFail bool
		panicMsg  string
		inPanic   bool
		o         checkOutcome
		seenLoc   = map[string]bool{}
	)
	addLoc := func(path, line string) {
		rel := projectPath(path, root)
		loc := rel + ":" + line
		if seenLoc[loc] || len(o.Locations) >= maxOutcomeLocations || outsideProject(rel) {
			return
		}
		seenLoc[loc] = true
		o.Locations = append(o.Locations, loc)
	}

	// A tool told to colour its output (tsc --pretty, cargo --color=always,
	// pytest --color=yes) wraps every name in escape codes; read the text.
	lines := strings.Split(ciANSI.ReplaceAllString(output, ""), "\n")
	suite, collecting, raised := "", "", map[string]string{}
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if m := goTestFail.FindStringSubmatch(line); m != nil {
			failing[m[1]] = true
			continue
		}
		if m := goPkgFail.FindStringSubmatch(line); m != nil {
			if strings.HasPrefix(m[2], "[") {
				buildFail = true
			} else {
				pkgs = append(pkgs, m[1])
			}
			continue
		}
		if m := goPanic.FindStringSubmatch(line); m != nil {
			if panicMsg == "" {
				panicMsg = m[1]
			}
			inPanic = true
			continue
		}
		if m := tsParen.FindStringSubmatch(line); m != nil {
			errs[failureKey(projectPath(m[1], root), m[3]+" "+m[4])] = true
			addLoc(m[1], m[2])
			continue
		}
		if m := tsDash.FindStringSubmatch(line); m != nil {
			errs[failureKey(projectPath(m[1], root), m[3]+" "+m[4])] = true
			addLoc(m[1], m[2])
			continue
		}
		if m := compileError.FindStringSubmatch(line); m != nil {
			errs[failureKey(projectPath(m[1], root), m[4])] = true
			addLoc(m[1], m[2])
			continue
		}
		if m := goTestLine.FindStringSubmatch(line); m != nil {
			addLoc(m[1], m[2])
			continue
		}
		if m := goFrame.FindStringSubmatch(line); m != nil {
			// A panic's stack runs through the runtime and the test framework;
			// the frame worth naming is the first one in the project.
			if inPanic && root != "" && strings.HasPrefix(filepath.ToSlash(m[1]), filepath.ToSlash(root)+"/") {
				addLoc(m[1], m[2])
				inPanic = false
			}
			continue
		}
		if m := pyFail.FindStringSubmatch(line); m != nil {
			// "ERROR test_m.py", a module and no test in it: the file could
			// not be imported, so nothing in it ran -- a broken build, worse
			// than any of its tests failing. MEASURED 2026-10-16 on pytest
			// 9.1: read as one failing test, 2 failing -> a syntax error
			// scored as an improvement.
			// What stopped it is part of the failure: a syntax error mended
			// into an import error is a different failure, not the same one.
			if strings.HasPrefix(line, "ERROR") && !strings.Contains(m[1], "::") {
				errs[failureKey(projectPath(m[1], root), strings.TrimSpace("could not be imported "+raised[m[1]]))] = true
			} else {
				failing[m[1]] = true
			}
			continue
		}
		if m := pySource.FindStringSubmatch(line); m != nil {
			addLoc(m[1], m[2])
			continue
		}
		if m := pyCollecting.FindStringSubmatch(line); m != nil {
			collecting = m[1]
			continue
		}
		if m := pyRaised.FindStringSubmatch(line); m != nil {
			if collecting != "" && raised[collecting] == "" {
				raised[collecting] = m[1]
			}
			continue
		}
		if m := pyLine.FindStringSubmatch(line); m != nil {
			addLoc(m[1], m[2])
			continue
		}
		if m := rsError.FindStringSubmatch(line); m != nil {
			if summaryLine(m[1]) {
				continue
			}
			where := ""
			// Its location is on one of the next few lines: "  --> src/x.rs:3:5".
			for j := i + 1; j < len(lines) && j <= i+3; j++ {
				if a := rsArrow.FindStringSubmatch(lines[j]); a != nil {
					where = projectPath(a[1], root)
					addLoc(a[1], a[2])
					break
				}
			}
			errs[failureKey(where, m[1])] = true
			continue
		}
		if m := rsTest.FindStringSubmatch(line); m != nil {
			failing[m[1]] = true
			continue
		}
		if m := rsPanic.FindStringSubmatch(line); m != nil {
			addLoc(m[1], m[2])
			continue
		}
		if m := jsSuite.FindStringSubmatch(line); m != nil {
			suite = m[1]
			continue
		}
		if m := jsFail.FindStringSubmatch(line); m != nil {
			// The same for jest: a suite that failed to run ran no tests.
			if m[1] == jsSuiteFailed {
				errs[failureKey(projectPath(suite, root), m[1])] = true
			} else {
				failing[m[1]] = true
			}
			continue
		}
		if m := jsSyntax.FindStringSubmatch(line); m != nil {
			addLoc(m[1], m[2])
			continue
		}
		if m := jsAt.FindStringSubmatch(line); m != nil {
			addLoc(m[1], m[2])
		}
	}

	names := leafNames(failing)
	o.Tests = len(names)
	if o.Tests == 0 {
		o.Tests = len(pkgs)
	}
	if len(names) > maxOutcomeNames {
		o.Failing = names[:maxOutcomeNames]
	} else {
		o.Failing = names
	}
	o.Errors = len(errs)

	var items []string
	switch {
	case len(errs) > 0 || buildFail:
		o.Kind = checkBuild
		if isLintCommand(command) {
			o.Kind = checkVet
		}
		if o.Errors == 0 {
			o.Errors = 1
		}
		o.Tests, o.Failing = 0, nil // a build that fails runs no tests
		items = sortedSet(errs)
	case panicMsg != "":
		o.Kind = checkPanic
		items = append([]string{normalizeFailure(panicMsg)}, normalizeAll(names)...)
	case o.Tests > 0:
		o.Kind = checkTest
		items = append(normalizeAll(names), pkgs...)
	default:
		o.Kind = checkOther
		items = tailLines(lines, otherSignatureLines)
	}
	o.Signature = signatureOf(o.Kind, items)
	return o
}

// commandKey is a command as the judge compares it: the same words, however
// they were spaced.
func commandKey(command string) string {
	return strings.Join(strings.Fields(command), " ")
}

// isLintCommand reports a command whose failures are findings about code that
// builds, not a build that broke: go vet, staticcheck, any linter.
func isLintCommand(command string) bool {
	for _, f := range strings.Fields(command) {
		base := filepath.Base(f)
		if base == "vet" || strings.HasSuffix(base, "lint") || base == "staticcheck" {
			return true
		}
	}
	return false
}

// projectPath is path as the project names it: relative to root when it is
// under it, without a leading ./, with forward slashes.
func projectPath(path, root string) string {
	p := filepath.ToSlash(path)
	if r := filepath.ToSlash(root); r != "" && strings.HasPrefix(p, r+"/") {
		p = strings.TrimPrefix(p, r+"/")
	}
	return strings.TrimPrefix(p, "./")
}

// outsideProject reports a location that is not the project's own code: a
// path out of it, or into the packages it depends on. A test framework's
// frames say where a failure surfaced, not where to repair it -- pytest's
// import error named six of its own lines and none of the project's.
func outsideProject(rel string) bool {
	if strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return true
	}
	for _, dep := range []string{"site-packages", "dist-packages", "node_modules"} {
		if strings.Contains("/"+rel, "/"+dep+"/") {
			return true
		}
	}
	return false
}

// failureKey is one error as the signature counts it: its file and its
// message, without the line it was on.
func failureKey(path, message string) string {
	return normalizeFailure(path + ": " + message)
}

func summaryLine(message string) bool {
	for _, s := range rsSummary {
		if strings.HasPrefix(message, s) {
			return true
		}
	}
	return false
}

// leafNames drops a Go test whose subtests are listed too: TestX fails because
// TestX/case fails, and counting both would count one failure twice.
func leafNames(set map[string]bool) []string {
	var out []string
	for name := range set {
		parent := false
		for other := range set {
			if strings.HasPrefix(other, name+"/") {
				parent = true
				break
			}
		}
		if !parent {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

var (
	sigTemp      = regexp.MustCompile(`\S*(?:go-build\d+|/tmp/|/var/folders/|\\Temp\\)\S*`)
	sigLineCol   = regexp.MustCompile(`(\.[A-Za-z0-9]+):\d+(?::\d+)?`)
	sigParenLC   = regexp.MustCompile(`(\.[jt]sx?)\(\d+,\d+\)`)
	sigHex       = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	sigGoroutine = regexp.MustCompile(`goroutine \d+`)
	// 1.5s, 300ms, and Go's own 1m30s or 2h0m0s, whole
	sigDuration = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)(?:\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h))*\b`)
	sigSpace    = regexp.MustCompile(`\s+`)
)

// normalizeFailure strips what changes from run to run but not from failure to
// failure: temporary paths, line and column numbers, addresses, goroutine
// numbers, timings.
func normalizeFailure(s string) string {
	s = sigTemp.ReplaceAllString(s, "<tmp>")
	s = sigLineCol.ReplaceAllString(s, "$1:#")
	s = sigParenLC.ReplaceAllString(s, "$1(#)")
	s = sigHex.ReplaceAllString(s, "0x#")
	s = sigGoroutine.ReplaceAllString(s, "goroutine #")
	s = sigDuration.ReplaceAllString(s, "#t")
	return strings.TrimSpace(sigSpace.ReplaceAllString(s, " "))
}

func normalizeAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, normalizeFailure(s))
	}
	return out
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tailLines is the last n non-blank lines of output, normalized: what an
// output nothing recognised is known by.
func tailLines(lines []string, n int) []string {
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if l := normalizeFailure(lines[i]); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// signatureOf hashes a kind and its items into a short name.
func signatureOf(kind string, items []string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, it := range items {
		h.Write([]byte{0})
		h.Write([]byte(it))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
