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
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if text, bad := isUnboundedRead(call, honourBounds); bad {
				out[key] = append(out[key], unboundedReadSite{
					fn:   key,
					call: text,
					file: name,
					line: fset.Position(call.Pos()).Line,
				})
			}
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

// reachesUnboundedRead is reachesSpawn's shape, with one difference that is not
// cosmetic: it returns the PATH it took. "This handler can reach an unbounded
// read" is not actionable; "it reaches it through GetServer and readLoop" is,
// and a chain property whose report names only its endpoints sends the reader
// back to do the walk by hand.
func reachesUnboundedRead(fn string, calls map[string][]string, readers map[string][]unboundedReadSite,
	seen map[string]bool, path []string) ([]string, unboundedReadSite, bool) {
	if seen[fn] {
		return nil, unboundedReadSite{}, false
	}
	seen[fn] = true
	path = append(path, fn)
	if sites := readers[fn]; len(sites) > 0 {
		return path, sites[0], true
	}
	for _, callee := range calls[fn] {
		if p, site, ok := reachesUnboundedRead(callee, calls, readers, seen, path); ok {
			return p, site, true
		}
	}
	return nil, unboundedReadSite{}, false
}

// knownAnswerFixture holds every shape the detector must classify, with the
// right answer written beside each. Synthetic on purpose: see
// unboundedReadsInSource for why a known-answer test must not be anchored on a
// live defect.
//
// Each shape here is a real spelling that occurs, or occurred, in this
// repository -- not an invented case. The two LimitReader arms are webfetch.go's
// exact form; the ReadDir(-1) arm is the spelling that NEUTERING
// builtinreadalloc_test.go's guard found missing; io.ReadFull is lsp_bridge.go's
// body read, which is bounded by its caller's slice and must never fire.
const knownAnswerFixture = `package x

func unboundedFile()    { data, _ := os.ReadFile(p); _ = data }
func unboundedDir()     { es, _ := os.ReadDir(p); _ = es }
func unboundedAll()     { b, _ := io.ReadAll(r); _ = b }
func unboundedDelim()   { line, _ := out.ReadString('\n'); _ = line }
func unboundedBytes()   { line, _ := out.ReadBytes('\n'); _ = line }
func unboundedHandle()  { es, _ := dir.ReadDir(-1); _ = es }

func boundedLimit()     { b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBytes)); _ = b }
func boundedLimitBare() { b, _ := io.ReadAll(LimitReader(r, n)); _ = b }
func boundedHandle()    { es, _ := dir.ReadDir(n); _ = es }
func boundedFull()      { _, _ = io.ReadFull(out, body) }
func boundedByte()      { c, _ := out.ReadByte(); _ = c }
func boundedScan()      { for sc.Scan() { _ = sc.Text() } }
`

// TestUnboundedReadDetectorKnowsTheAnswers is the HARD GATE, and nothing
// downstream is reportable if it fails (H2: a detector is untested until it has
// been shown to find the thing it is for and to leave alone the things it is
// not).
func TestUnboundedReadDetectorKnowsTheAnswers(t *testing.T) {
	sites := unboundedReadsInSource("fixture.go", knownAnswerFixture, true)

	// Vacuity floor: a fixture that failed to parse yields an empty map, and
	// every "must not fire" assertion below then passes for free.
	if len(sites) == 0 {
		t.Fatal("vacuity floor: the detector found nothing in a fixture written to contain six " +
			"unbounded reads. The parse failed, so nothing below means anything.")
	}

	mustFire := map[string]string{
		"unboundedFile":   "os.ReadFile",
		"unboundedDir":    "os.ReadDir",
		"unboundedAll":    "io.ReadAll",
		"unboundedDelim":  "out.ReadString",
		"unboundedBytes":  "out.ReadBytes",
		"unboundedHandle": "dir.ReadDir(-1)",
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
	strict := unboundedReadsInSource("fixture.go", knownAnswerFixture, false)
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
var unboundedReadExemptions = map[string]struct{ trigger, reason string }{
	// --- SITE 1: readHeaders' ReadString, lsp_bridge.go. TB7 / R1.24.
	//
	// THREE handlers, not the two C4 row 4 named. propose_ast_edit reaches
	// lspServerForFile directly, without going through handleLSPQuery, so the
	// row's enumeration of entry points was short by one -- found by the walk,
	// which is the difference between a chain property and a traced example.
	"query_compiler_definition": {
		trigger: "readHeaders bounds its header line length AND its header count",
		reason: "Chain: builtinLSPDefinition -> handleLSPQuery -> lspServerForFile -> GetServer " +
			"-> readLoop -> readHeaders, whose out.ReadString('\n') accumulates until a newline " +
			"the language server may never send. MEASURED: 135,962,168 bytes allocated over a " +
			"64 MiB newline-free stream. Reachability is low -- serverCommand returns one of " +
			"three hardcoded names on an inherited PATH -- but low is not bounded.",
	},
	"query_compiler_references": {
		trigger: "readHeaders bounds its header line length AND its header count",
		reason:  "Same chain as query_compiler_definition, through builtinLSPReferences.",
	},
	"propose_ast_edit": {
		trigger: "readHeaders bounds its header line length AND its header count",
		reason: "Same site, a DIFFERENT chain: builtinTools.func3 -> builtinProposeASTEdit -> " +
			"lspServerForFile, skipping handleLSPQuery. This entry point was not named in C4 " +
			"row 4 and was found by this walk.",
	},

	// --- SITE 2: parseGitignoreLayer's os.ReadFile, chunker.go. NEW, found here.
	//
	// The one read in chunker.go that does NOT re-run the gate. readEligibleFile
	// in the same file re-runs shouldSkipFile immediately before reading, and
	// planmode.go states the rule: the read side must not assume the write side
	// ran. Lower severity than site 1 and filed that way rather than inflated to
	// match -- the path comes from a directory walk, not from the model -- but
	// the SIZE is decided by a file in the workspace.
	"search_code": {
		trigger: "parseGitignoreLayer bounds its read",
		reason: "Chain: builtinSearchCode -> gatherContext -> resolveFileLineRefs -> resolveRefs " +
			"-> findFilesBySuffix -> matchDir -> matches -> layerFor -> parseGitignoreLayer. " +
			"MEASURED: 90,333,064 bytes allocated for a 33.5 MB .gitignore, against maxFileSize " +
			"of 1 MiB.",
	},
	"repo_map": {
		trigger: "parseGitignoreLayer bounds its read",
		reason: "Same site: builtinTools.func1 -> builtinRepoMap -> buildRepoMap -> matchDir -> " +
			"matches -> layerFor -> parseGitignoreLayer.",
	},
}

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
		path, site, ok := reachesUnboundedRead(fn, calls, readers, map[string]bool{}, nil)
		if !ok {
			continue
		}
		if ex, allowed := unboundedReadExemptions[b.Tool.Name]; allowed {
			if ex.trigger == "" || ex.reason == "" {
				t.Errorf("exemption for %q is missing a %s. An exemption without both is a place "+
					"to hide things; reach.sh and gate-parity.sh refuse these for the same cause.",
					b.Tool.Name, map[bool]string{true: "trigger", false: "reason"}[ex.trigger == ""])
			}
			t.Logf("ALLOWED: %q reaches %s -- retires when: %s", b.Tool.Name, site, ex.trigger)
			continue
		}
		t.Errorf("built-in %q can reach an unbounded read.\n"+
			"  site:  %s\n"+
			"  chain: %s\n"+
			"The number of bytes that read may allocate is decided by the thing being read, not "+
			"by this process. A cap applied to the RESULT does not help: that is Class III, and "+
			"it is the distinction maxBuiltinReadBytes got wrong for months.\n"+
			"Bound it at the source, or add an entry to unboundedReadExemptions with a reason AND "+
			"the event that retires it.",
			b.Tool.Name, site, strings.Join(path, " -> "))
	}
}
