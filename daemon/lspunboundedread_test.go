package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// CLASS III AS A CHAIN PROPERTY, which is the half the file-scoped guard cannot
// reach.
//
// builtinreadalloc_test.go's TestBuiltinToolSurfaceBoundsItsReads reads the FILES
// implementing model-facing tools and forbids the unbounded readers in them. It
// closed three sites and it is the right shape for three of the four. Its own
// comment records where it stops:
//
//	mcp_lsp.go  has NO read of its own. Its handlers' only outward call is
//	            srv.Call, and the unbounded read in that chain is readHeaders'
//	            ReadString in lsp_bridge.go -- one file deeper, where a guard
//	            that reads FILES cannot see it. Listing mcp_lsp.go is therefore
//	            necessary and NOT sufficient.
//
// So this walks the call graph, the way TestNoBuiltinSpawnsWithoutDeclaringIt
// walks it for exec.Command, and asks the CODE which handlers can reach an
// unbounded read rather than which files contain one.
//
// WHAT AN UNBOUNDED READ IS HERE, and the definition is the whole test. A read
// is unbounded when the number of bytes it may allocate is decided by the thing
// being read rather than by the caller:
//
//	os.ReadFile / os.ReadDir      the file's whole length; every dirent
//	io.ReadAll                    to EOF
//	bufio ReadString / ReadBytes  until the delimiter arrives, or forever
//
// io.ReadFull is NOT on that list and is not an omission: its length is the
// caller's slice. That distinction is why lsp_bridge.go's BODY read is fine and
// its HEADER read is not -- maxLSPMessageBytes bounds the body before
// io.ReadFull, and nothing bounds the header line at all.
//
// WHAT IT CANNOT SEE, stated rather than implied. The walk is within this
// package, like its template: a handler reaching an unbounded read through
// another module is invisible to it. It over-approximates in the safe direction
// -- ReadString and ReadBytes are matched on the selector alone, so an unrelated
// method sharing a name pulls a handler into the check rather than out of it.
// And it says nothing about whether a reachable read is EXPLOITABLE; reachability
// is not a threat model.

// unboundedReadSite is one direct unbounded read, named well enough to act on.
type unboundedReadSite struct {
	fn   string // the enclosing function, keyed as daemonCallGraph keys it
	call string // the call as written, e.g. "out.ReadString"
	file string
	line int
}

func (s unboundedReadSite) String() string {
	return fmt.Sprintf("%s (%s:%d, via %s)", s.fn, s.file, s.line, s.call)
}

// readerIsBounded answers the one question the AST can answer that a grep
// cannot: is this io.ReadAll wrapped in something that bounds it?
//
// webfetch.go is the reason this exists. It calls io.ReadAll TWICE and both are
// correct -- io.ReadAll(io.LimitReader(resp.Body, maxBytes)), chosen over
// Content-Length because a hostile server can lie about that. A detector that
// matched the text "io.ReadAll(" would report both as defects, and a guard whose
// first run cries wolf on the two sites that got it RIGHT is a guard people
// switch off.
func readerIsBounded(call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	inner, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	switch f := inner.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "LimitReader"
	case *ast.Ident:
		return f.Name == "LimitReader"
	}
	return false
}

// eligibilityGateName is this repository's own read-side gate. shouldSkipFile
// enforces maxFileSize (and the secret-name and binary checks), and the rule
// planmode.go states in general is that the read side must not assume the write
// side ran -- so a read preceded by a re-run of this gate IS bounded, at
// maxFileSize.
//
// NAMED, and asserted to exist. TestEligibilityGateStillExists fails if this
// function is renamed or deleted, because a recogniser keyed on a name that no
// longer resolves silently stops recognising anything and every bounded read in
// the indexer starts failing -- or, worse, the name survives on something that no
// longer bounds.
const eligibilityGateName = "shouldSkipFile"

// gateGuardEnds returns the end position of every `if ... shouldSkipFile(...) ...
// { ... return ... }` in a body.
//
// THREE CONDITIONS, AND EACH ONE IS LOAD-BEARING. The call must appear in an
// if statement's init or condition, that if's body must return, and -- at the
// call site below -- it must come BEFORE the read. Presence alone is not enough:
// a function that calls the gate and ignores its result reads exactly as far as a
// function that never called it, and "the gate was mentioned" is not "the gate
// was obeyed".
func gateGuardEnds(body *ast.BlockStmt) []token.Pos {
	var ends []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		mentions := false
		for _, part := range []ast.Node{ifs.Init, ifs.Cond} {
			if part == nil {
				continue
			}
			ast.Inspect(part, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.Ident:
					if f.Name == eligibilityGateName {
						mentions = true
					}
				case *ast.SelectorExpr:
					if f.Sel.Name == eligibilityGateName {
						mentions = true
					}
				}
				return true
			})
		}
		if !mentions {
			return true
		}
		returns := false
		ast.Inspect(ifs.Body, func(m ast.Node) bool {
			if _, ok := m.(*ast.ReturnStmt); ok {
				returns = true
			}
			return true
		})
		if returns {
			ends = append(ends, ifs.End())
		}
		return true
	})
	return ends
}

// isUnboundedRead classifies one call expression. `honourBounds` is the switch
// the known-answer test flips: with it false the LimitReader exception is
// ignored, which is how that test proves the exception is what excludes
// webfetch.go rather than an accident of parsing.
func isUnboundedRead(call *ast.CallExpr, honourBounds bool) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	recv := ""
	if id, ok := sel.X.(*ast.Ident); ok {
		recv = id.Name
	}
	name := sel.Sel.Name
	text := recv + "." + name

	switch {
	// The whole resource, in one call, from a package-level helper.
	case recv == "os" && (name == "ReadFile" || name == "ReadDir"):
		return text, true

	// To EOF -- unless something bounds it.
	case (recv == "io" || recv == "ioutil") && name == "ReadAll":
		if honourBounds && readerIsBounded(call) {
			return "", false
		}
		return text, true

	// Until the delimiter, which the thing being read decides. Matched on the
	// selector alone: the safe over-approximation.
	case name == "ReadString" || name == "ReadBytes":
		return text, true

	// The count-less spelling of a listing. builtinreadalloc_test.go names this
	// because NEUTERING ITS GUARD FOUND IT MISSING: dir.ReadDir(-1) is exactly
	// os.ReadDir's semantics through a handle, and a guard that only knows the
	// convenience spelling misses it.
	case name == "ReadDir" && len(call.Args) == 1:
		if u, ok := call.Args[0].(*ast.UnaryExpr); ok && u.Op == token.SUB {
			return text + "(-1)", true
		}
	}
	return "", false
}

// daemonUnboundedReads parses this package and returns every function that
// performs an unbounded read directly, with the site.
//
// The keying scheme is daemonCallGraph's, character for character -- including
// outer.funcN for literals, in source order -- because the two maps are walked
// together and a key that does not line up is a silent miss rather than an
// error. That is the failure mode H13 names: a derivation that is wrong
// everywhere but produces right answers where nothing depends on it.
func daemonUnboundedReads(t *testing.T, honourBounds bool) map[string][]unboundedReadSite {
	t.Helper()
	out := map[string][]unboundedReadSite{}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for k, v := range unboundedReadsInSource(name, string(src), honourBounds) {
			out[k] = append(out[k], v...)
		}
	}
	return out
}

// unboundedReadsInSource is the scan itself, over one file's text. Factored out
// so the known-answer test can run the detector against a SYNTHETIC fixture
// holding every shape it must classify, rather than against whichever live
// defect happens to exist.
//
// THAT SPLIT IS THE POINT, and the first version of this file got it wrong. Its
// known-answer positive asserted that readHeaders IS a site -- so the test would
// have started FAILING the moment the defect it names was fixed, which makes the
// guard an argument against its own remedy. A known-answer test must be anchored
// on inputs that do not change when the code under audit improves.
func unboundedReadsInSource(name, src string, honourBounds bool) map[string][]unboundedReadSite {
	out := map[string][]unboundedReadSite{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return out // a build-tagged file this configuration does not compile
	}
	scan := func(key string, body *ast.BlockStmt) {
		gates := gateGuardEnds(body)
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			text, bad := isUnboundedRead(call, honourBounds)
			if !bad {
				return true
			}
			// BOUNDED BY THE GATE, if one of them closed before this read.
			// Order matters and is checked: a gate AFTER the read bounds
			// nothing, and the AST is where that is visible.
			if honourBounds {
				for _, end := range gates {
					if end < call.Pos() {
						return true
					}
				}
			}
			out[key] = append(out[key], unboundedReadSite{
				fn:   key,
				call: text,
				file: name,
				line: fset.Position(call.Pos()).Line,
			})
			return true
		})
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		scan(fd.Name.Name, fd.Body)
		lit := 0
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			fl, ok := n.(*ast.FuncLit)
			if !ok {
				return true
			}
			lit++
			scan(fd.Name.Name+".func"+itoa(lit), fl.Body)
			return true
		})
	}
	return out
}

// reachesFunc answers "is fn2 reachable from fn1 in this graph", with no
// reference to what fn2 does. The class guard uses it as its anti-vacuity floor:
// once both live sites are bounded, "no handler reaches an unbounded read" is
// the correct answer AND the answer a broken walk gives for free, and the two
// have to be told apart.
func reachesFunc(from, want string, calls map[string][]string, seen map[string]bool) bool {
	if seen[from] {
		return false
	}
	seen[from] = true
	if from == want {
		return true
	}
	for _, callee := range calls[from] {
		if reachesFunc(callee, want, calls, seen) {
			return true
		}
	}
	return false
}

// reachedSite is one unbounded read a handler can reach, with the path taken to
// it. The path is not decoration: "this handler can reach an unbounded read" is
// not actionable, "it reaches it through GetServer and readLoop" is, and a chain
// property whose report names only its endpoints sends the reader back to do the
// walk by hand.
type reachedSite struct {
	site unboundedReadSite
	path []string
}

// reachesUnboundedRead is reachesSpawn's shape, with one difference that matters:
// it collects EVERY site reachable, not the first.
//
// THE FIRST VERSION RETURNED ONE, AND THAT WAS A HOLE. Paired with an exemption
// keyed on the handler alone, one recorded decision silenced every other site
// under the same tool -- and it did: search_code's exemption for
// parseGitignoreLayer hid readReferencedSpan completely, and that site only
// appeared once the first was fixed and the exemption removed. A gate that stops
// looking after one hit is a gate whose coverage shrinks every time you use it.
func reachesUnboundedRead(fn string, calls map[string][]string, readers map[string][]unboundedReadSite,
	seen map[string]bool, path []string, out map[string]reachedSite) {
	if seen[fn] {
		return
	}
	seen[fn] = true
	path = append(append([]string{}, path...), fn)
	for _, site := range readers[fn] {
		if _, dup := out[site.fn]; !dup {
			out[site.fn] = reachedSite{site: site, path: path}
		}
	}
	for _, callee := range calls[fn] {
		reachesUnboundedRead(callee, calls, readers, seen, path, out)
	}
}

// knownAnswerFixtureFile holds every shape the detector must classify, with the
// right answer asserted below. Synthetic on purpose: see unboundedReadsInSource
// for why a known-answer test must not be anchored on a live defect.
//
// Each shape in it is a real spelling that occurs, or occurred, in this
// repository -- not an invented case. The two LimitReader arms are webfetch.go's
// exact form; the ReadDir(-1) arm is the spelling that NEUTERING
// builtinreadalloc_test.go's guard found missing; io.ReadFull is lsp_bridge.go's
// body read, which is bounded by its caller's slice and must never fire; the four
// gate arms are readReferencedSpan's and regionsOnDisk's pattern and three ways
// of getting it wrong.
//
// IN testdata/ AND NOT IN A RAW STRING IN THIS FILE, and that is not tidiness.
// It WAS a raw string here, and it broke two unrelated guards:
// TestTheHeuristicAgreesWithTheCompiler reported "the heuristic invents 16
// boundaries the compiler does not recognise" and
// TestConstructExtentsNeverStopShortOfTheCompiler reported its first
// stop-short construct since 2026-08-28. chunkcontext's heuristic finds top-level
// declarations by scanning lines, so `func unboundedDir() {...}` inside a string
// literal reads to it as a real declaration while go/parser correctly sees string
// contents. A .gotxt file is not a Go file to either of them.
//
// THAT IS ALSO A MEASURED LIMITATION OF THE HEURISTIC, worth recording rather
// than just routing around: any file embedding Go source in a raw string
// over-counts its declarations and can make constructExtents over-extend. Rare,
// low impact, and now demonstrated.
const knownAnswerFixtureFile = "testdata/unboundedread_fixture.gotxt"

func knownAnswerFixture(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(knownAnswerFixtureFile)
	if err != nil {
		t.Fatalf("reading the known-answer fixture: %v", err)
	}
	// Vacuity floor: an empty or truncated fixture makes every "must not fire"
	// assertion below pass for free.
	if len(src) < 400 {
		t.Fatalf("known-answer fixture is %d bytes; it holds sixteen declarations and cannot be "+
			"that small. A truncated fixture would pass every negative assertion.", len(src))
	}
	return string(src)
}

// TestUnboundedReadDetectorKnowsTheAnswers is the HARD GATE, and nothing
// downstream is reportable if it fails (H2: a detector is untested until it has
// been shown to find the thing it is for and to leave alone the things it is
// not).
func TestUnboundedReadDetectorKnowsTheAnswers(t *testing.T) {
	sites := unboundedReadsInSource("fixture.go", knownAnswerFixture(t), true)

	// Vacuity floor: a fixture that failed to parse yields an empty map, and
	// every "must not fire" assertion below then passes for free.
	if len(sites) == 0 {
		t.Fatal("vacuity floor: the detector found nothing in a fixture written to contain six " +
			"unbounded reads. The parse failed, so nothing below means anything.")
	}

	mustFire := map[string]string{
		"gateResultIgnored":     "os.ReadFile",
		"gateBodyDoesNotReturn": "os.ReadFile",
		"gateAfterTheRead":      "os.ReadFile",
		"unboundedFile":         "os.ReadFile",
		"unboundedDir":          "os.ReadDir",
		"unboundedAll":          "io.ReadAll",
		"unboundedDelim":        "out.ReadString",
		"unboundedBytes":        "out.ReadBytes",
		"unboundedHandle":       "dir.ReadDir(-1)",
	}
	for fn, want := range mustFire {
		got, ok := sites[fn]
		if !ok {
			t.Errorf("known-answer FAILURE: %s performs %s and the detector did not see it", fn, want)
			continue
		}
		if got[0].call != want {
			t.Errorf("known-answer FAILURE: %s fired as %q, wanted %q", fn, got[0].call, want)
		}
	}

	mustNotFire := map[string]string{
		"boundedLimit":     "io.ReadAll(io.LimitReader(...)) -- webfetch.go's exact form, chosen over Content-Length because a hostile server can lie about that",
		"boundedLimitBare": "the same, with LimitReader dot-imported or shadowed",
		"boundedHandle":    "dir.ReadDir(n) reads at most n; that is the antidote, not the defect",
		"boundedFull":      "io.ReadFull's length is the CALLER's slice -- lsp_bridge.go's body read",
		"boundedByte":      "one byte is bounded by construction; this is what a bounded line reader is built from",
		"boundedScan":      "bufio.Scanner is bounded by MaxScanTokenSize and errors rather than growing",
		"boundedByGate": "shouldSkipFile enforces maxFileSize and is re-run, with its result " +
			"obeyed, one statement before the read -- the antidote readReferencedSpan and " +
			"regionsOnDisk both apply and builtinreadalloc_test.go names as the correct pattern",
	}
	for fn, why := range mustNotFire {
		if got, ok := sites[fn]; ok {
			t.Errorf("known-answer FAILURE: the detector flagged %s (%s), and it is bounded: %s",
				fn, got[0].call, why)
		}
	}

	// --- THE EXCEPTION MUST BE WHAT EXCLUDES THEM. With the LimitReader
	// exception ignored, both bounded-ReadAll arms MUST appear -- otherwise they
	// are excluded for some other reason and the assertions above are evidence of
	// nothing. Modelled on detectorSeesTheAccessor in noscrubaccessor_test.go.
	strict := unboundedReadsInSource("fixture.go", knownAnswerFixture(t), false)
	for _, fn := range []string{"boundedLimit", "boundedLimitBare"} {
		if _, ok := strict[fn]; !ok {
			t.Errorf("the LimitReader exception is not what excludes %s: with the exception "+
				"disabled it STILL does not fire, so it is excluded for some other reason and "+
				"the negative above proves nothing.", fn)
		}
	}
	if _, ok := strict["boundedFull"]; ok {
		t.Error("io.ReadFull fired under strict mode, so it is on the reader list rather than " +
			"excluded by the bound check. Its length is the caller's slice; it must never be on " +
			"that list in either mode.")
	}
}

// TestUnboundedReadDetectorLeavesTheCleanSitesAlone is the other half, and it
// runs against the REAL package rather than a fixture.
//
// Three sites C3 traced to the end of their allocation chains and found clean,
// each clean for a DIFFERENT reason -- which is why all three are here: a
// detector that gets them right for one reason is not shown to get them right at
// all. Unlike the positives, these assertions stay true when the live defects
// are fixed, so they belong against the real code.
func TestUnboundedReadDetectorLeavesTheCleanSitesAlone(t *testing.T) {
	readers := daemonUnboundedReads(t, true)
	if len(readers) == 0 {
		t.Fatal("vacuity floor: no unbounded read found anywhere in this package. Not a clean " +
			"result -- a broken parse.")
	}

	clean := map[string]string{
		"mcp_exec.go": "caps AT SOURCE: execMaxOutputBytes feeds a tailBuffer used as " +
			"cmd.Stdout/Stderr, so the bytes are never all resident. tailBuffer is a WRITER; " +
			"there is no read here to bound",
		"mcp_lsp.go": "has no read of its own at all -- its handlers' only outward call is " +
			"srv.Call. It must be reached by the WALK, never flagged as a site",
		"webtools.go": "bounds before the allocation, in webfetch.go",
	}
	for fn, ss := range readers {
		for _, s := range ss {
			if why, ok := clean[s.file]; ok {
				t.Errorf("the detector flagged %s as an unbounded read site (function %s), and it "+
					"is not one: %s", s, fn, why)
			}
			if s.file == "webfetch.go" && strings.HasSuffix(s.call, ".ReadAll") {
				t.Errorf("%s flagged, but webfetch.go's io.ReadAll calls are wrapped in "+
					"io.LimitReader and are the two sites that got this RIGHT (function %s)", s, fn)
			}
		}
	}
}

func sortedSiteKeys(m map[string]reachedSite) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedReaderKeys(m map[string][]unboundedReadSite) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// unboundedReadExemptions records model-facing handlers that MAY reach an
// unbounded read, with a reason and the event that retires the exemption.
//
// Both fields are mandatory, and the trigger is the half that keeps this honest:
// a reason explains why the reach is acceptable today, a trigger says what makes
// it unacceptable. Without one, an exemption written for a week-long situation
// becomes permanent by default -- which is how every stale record in this
// repository started. Same rule as scripts/reach.sh and scripts/gate-parity.sh.
//
// Keyed on the HANDLER, not on the site: one handler reaching one class of read
// is one decision. A handler reaching a NEW site still fails, because the
// message names the site and the reader has to come back here.
// KEYED "<tool>|<site function>", NOT ON THE TOOL. One handler reaching one site
// is one decision; the same handler reaching a DIFFERENT site is a different one
// and must still fail. The first version of this map was keyed on the tool alone
// and search_code's entry for parseGitignoreLayer silenced readReferencedSpan
// entirely -- which is the two-states-one-phrase error this repository keeps
// finding, committed here by the person writing the guard against it.
//
// EMPTY, and that is the point: this guard is green on facts, not on silence.
// Both live sites were BOUNDED rather than exempted, and readReferencedSpan and
// regionsOnDisk are recognised by gateGuardEnds, because "the read re-runs the
// indexer's gate and obeys it" is a property and an exemption is not.
var unboundedReadExemptions = map[string]struct{ trigger, reason string }{}

// TestNoBuiltinReachesAnUnboundedRead is the class guard: not "are these four
// sites fixed" but "can a model-facing tool reach a read whose size the thing
// being read decides".
func TestNoBuiltinReachesAnUnboundedRead(t *testing.T) {
	calls, _ := daemonCallGraph(t)
	readers := daemonUnboundedReads(t, true)

	// Anti-vacuity, before any loop that passes trivially on an empty parse.
	if len(calls) < 50 {
		t.Fatalf("vacuity floor: parsed only %d functions from the daemon package; the walk "+
			"found almost nothing, so its verdict is meaningless", len(calls))
	}
	if len(readers) == 0 {
		t.Fatal("vacuity floor: no unbounded read found anywhere in this package; a clean " +
			"result here would be a false one")
	}

	s := builtinTestServer(t)
	tools := s.builtinTools(&proposalSink{}, "")
	if len(tools) == 0 {
		t.Fatal("vacuity floor: no built-ins registered, so nothing was walked")
	}

	// THE FLOOR THAT SURVIVES THE FIX, and it is the one this test actually
	// needs. Once both live sites are bounded, "no built-in reaches an unbounded
	// read" is the correct answer -- and it is also exactly what a broken walk
	// returns for free. So the walk is asserted separately, on a chain that is a
	// property of the ARCHITECTURE rather than of the defect:
	//
	//   builtinLSPDefinition -> handleLSPQuery -> lspServerForFile -> GetServer
	//                        -> readLoop -> readHeaders
	//
	// That chain exists because the LSP tools talk to a subprocess through a
	// demultiplexing goroutine, which is true whether or not readHeaders is
	// bounded. If it ever stops resolving, the handler names and the call graph
	// have drifted apart and every "no reach" verdict above is meaningless.
	anchored := false
	for _, b := range tools {
		fn := handlerFuncName(b.Handler)
		if fn == "" {
			continue
		}
		if reachesFunc(fn, "readHeaders", calls, map[string]bool{}) {
			anchored = true
			break
		}
	}
	if !anchored {
		t.Fatal("vacuity floor: no built-in reaches readHeaders in the call graph. The LSP tools " +
			"do -- through handleLSPQuery, lspServerForFile, GetServer and readLoop -- so a run " +
			"where none does means the handler names and the graph stopped lining up, not that " +
			"the tools stopped talking to language servers. Every verdict below would be a false " +
			"negative.")
	}

	for _, b := range tools {
		fn := handlerFuncName(b.Handler)
		if fn == "" {
			t.Errorf("could not resolve a handler function name for %q, so it was not checked",
				b.Tool.Name)
			continue
		}
		reached := map[string]reachedSite{}
		reachesUnboundedRead(fn, calls, readers, map[string]bool{}, nil, reached)
		for _, siteFn := range sortedSiteKeys(reached) {
			r := reached[siteFn]
			key := b.Tool.Name + "|" + siteFn
			if ex, allowed := unboundedReadExemptions[key]; allowed {
				if ex.trigger == "" || ex.reason == "" {
					t.Errorf("exemption %q is missing a %s. An exemption without both is a place "+
						"to hide things; reach.sh and gate-parity.sh refuse these for the same "+
						"cause.", key,
						map[bool]string{true: "trigger", false: "reason"}[ex.trigger == ""])
				}
				t.Logf("ALLOWED: %q reaches %s -- retires when: %s", b.Tool.Name, r.site, ex.trigger)
				continue
			}
			t.Errorf("built-in %q can reach an unbounded read.\n"+
				"  site:  %s\n"+
				"  chain: %s\n"+
				"The number of bytes that read may allocate is decided by the thing being read, "+
				"not by this process. A cap applied to the RESULT does not help: that is Class "+
				"III, and it is the distinction maxBuiltinReadBytes got wrong for months.\n"+
				"Bound it at the source, or add an entry to unboundedReadExemptions keyed %q "+
				"with a reason AND the event that retires it.",
				b.Tool.Name, r.site, strings.Join(r.path, " -> "), key)
		}
	}
}

// TestEligibilityGateStillExists is the anti-rot half, and it is the failure mode
// a name-keyed recogniser has: gateGuardEnds looks for calls to
// eligibilityGateName, so if that function is renamed or deleted the recogniser
// stops recognising anything and every correctly-gated read in the indexer starts
// reading as a defect. Worse in the other direction: the name could survive on
// something that no longer enforces a size cap, and the recogniser would keep
// excusing reads on the strength of a function that stopped bounding them.
//
// The first half is mechanical and is here. The second is not: that
// shouldSkipFile still enforces maxFileSize is asserted by
// TestParseGitignoreLayer_BoundIsTheBoundary's sibling coverage in chunker_test
// and by shouldSkipFile's own tests, not by this one, and this comment says so
// rather than implying it.
func TestEligibilityGateStillExists(t *testing.T) {
	declFile := daemonFuncDeclFiles(t)
	if len(declFile) < 50 {
		t.Fatalf("vacuity floor: parsed only %d declarations", len(declFile))
	}
	if _, ok := declFile[eligibilityGateName]; !ok {
		t.Fatalf("gateGuardEnds recognises reads bounded by %q, and no function of that name is "+
			"declared in this package. The recogniser is keyed on a name that does not resolve, "+
			"so it excuses nothing and every gated read in the indexer now reads as a defect. "+
			"Point eligibilityGateName at the gate's new name.", eligibilityGateName)
	}
	// And the recogniser must actually fire on a real use of it, or the constant
	// is right and the matcher is broken -- two different failures.
	src, err := os.ReadFile("chunkexpand.go")
	if err != nil {
		t.Fatalf("reading chunkexpand.go: %v", err)
	}
	if sites := unboundedReadsInSource("chunkexpand.go", string(src), true); len(sites) > 0 {
		t.Errorf("regionsOnDisk re-runs %s and obeys it one statement before its os.ReadFile, so "+
			"chunkexpand.go should hold no unbounded read site. Got: %v",
			eligibilityGateName, sites)
	}
	if strict := unboundedReadsInSource("chunkexpand.go", string(src), false); len(strict) == 0 {
		t.Errorf("with bound recognition disabled, chunkexpand.go's os.ReadFile STILL does not " +
			"fire -- so the gate recogniser is not what excludes it and the assertion above is " +
			"evidence of nothing.")
	}
}
