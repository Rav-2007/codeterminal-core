package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// CLASS III -- THE CAP IS APPLIED TO THE RESULT, AFTER THE WHOLE RESOURCE HAS
// BEEN MATERIALISED.
//
// TestBuiltinReadFileAnnouncesTruncation (mcpruntime_test.go) already covers
// read_file with an oversized file and asserts `len(res.Content)` is small. It
// passes, it has always passed, and it CANNOT detect what is tested here -- its
// failure message even says "the read cap did not apply" while measuring the
// result. That distinction is the entire class: maxBuiltinReadBytes bounds the
// RESULT; nothing bounds the READ.
//
// So this measures ALLOCATION, not result size. builtinReadFile calls
// os.ReadFile on a path the MODEL chose and truncates afterwards, so the peak
// allocation is the file's full size. os.Stat is already called one branch
// above (to reject directories) and its FileInfo carries Size(), which is not
// consulted.
//
// THE SIGNAL IS DELIBERATELY ~500x THE CAP so the assertion cannot be flaky.
// TotalAlloc is noisy -- GC and unrelated allocation move it -- but a 32 MiB
// read against a 64 KiB cap is not a margin any noise closes.
//
// The antidote already exists in this repository and is not applied here:
// fileref.go's readReferencedSpan re-runs shouldSkipFile (which enforces
// maxFileSize) before its read and says the indexer's gate applies "here too",
// and chunker.go's readEligibleFile re-runs it immediately before reading.
// planmode.go states the general rule: the read side must not assume the write
// side ran.
func TestBuiltinReadFile_DoesNotMaterialiseTheWholeFile(t *testing.T) {
	s := builtinTestServer(t)

	const fileSize = 32 << 20 // 32 MiB, ~512x maxBuiltinReadBytes
	path := filepath.Join(s.workspace, "big.bin")
	if err := os.WriteFile(path, make([]byte, fileSize), 0600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	// Vacuity floor: the fixture must actually dwarf the cap, or a passing
	// assertion would mean nothing.
	if fileSize <= maxBuiltinReadBytes*8 {
		t.Fatalf("vacuity floor: fixture %d bytes is not decisively larger than the %d-byte cap",
			fileSize, maxBuiltinReadBytes)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res := call(t, s.builtinReadFile, `{"path":"big.bin"}`)
	runtime.ReadMemStats(&after)

	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	// Vacuity floor, second direction: the call must have done the work. A
	// handler that refused early would allocate nothing and pass for the wrong
	// reason.
	if len(res.Content) == 0 {
		t.Fatal("vacuity floor: the handler returned nothing, so this measures nothing")
	}

	allocated := after.TotalAlloc - before.TotalAlloc
	const budget = 4 << 20 // 8x headroom over the cap, 8x below the unfixed read
	if allocated > budget {
		t.Errorf("builtinReadFile allocated %d bytes to return %d.\n"+
			"The file is %d bytes and maxBuiltinReadBytes is %d, so the whole file was "+
			"materialised and then truncated. Bound the READ (os.Stat's Size() is already "+
			"in hand at the directory check), not just the result.",
			allocated, len(res.Content), fileSize, maxBuiltinReadBytes)
	}
}

// CLASS III, instance 2: list_directory. Same shape, different resource.
//
// builtinListDirectory calls os.ReadDir, which reads and sorts EVERY entry,
// then slices to maxBuiltinListEntries. os.File.ReadDir(n) exists precisely to
// read at most n, and is not used.
//
// The absolute numbers here are smaller than read_file's -- a dirent costs
// ~100 bytes, not a file's whole length -- so this is the same defect with a
// lower ceiling, and it is filed that way rather than inflated to match.
func TestBuiltinListDirectory_DoesNotMaterialiseEveryEntry(t *testing.T) {
	s := builtinTestServer(t)

	const entries = 40000 // 80x maxBuiltinListEntries
	dir := filepath.Join(s.workspace, "many")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := range entries {
		f, err := os.Create(filepath.Join(dir, "f"+itoa(i)))
		if err != nil {
			t.Fatalf("creating fixture %d: %v", i, err)
		}
		_ = f.Close()
	}
	if entries <= maxBuiltinListEntries*8 {
		t.Fatalf("vacuity floor: %d entries is not decisively more than the %d cap",
			entries, maxBuiltinListEntries)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res := call(t, s.builtinListDirectory, `{"path":"many"}`)
	runtime.ReadMemStats(&after)

	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("vacuity floor: the handler returned nothing, so this measures nothing")
	}

	allocated := after.TotalAlloc - before.TotalAlloc
	// The budget is tied to the SCAN cap, not to a round number: the property
	// is that allocation scales with maxBuiltinListScan and not with the size
	// of the directory. At ~200 bytes per materialised entry, 512 each is
	// generous headroom and still an order of magnitude below what an
	// unbounded scan of a large directory costs.
	//
	// Measured before the fix, on this fixture: 9,694,832 bytes.
	// Measured after:                           2,072,048 bytes.
	budget := uint64(maxBuiltinListScan) * 512
	if allocated > budget {
		t.Errorf("builtinListDirectory allocated %d bytes (budget %d) to list %d of %d entries.\n"+
			"The scan must stop at maxBuiltinListScan=%d rather than materialising every "+
			"entry: os.ReadDir reads them all, dir.ReadDir(n) reads at most n.",
			allocated, budget, maxBuiltinListEntries, entries, maxBuiltinListScan)
	}
}

// unboundedReaders are the convenience functions that read a whole resource in
// one call. They are not wrong in general -- they are wrong on the model-facing
// tool surface, where the argument naming the resource comes from the model.
// ReadDir(-1) is included because NEUTERING THIS GUARD FOUND IT MISSING. When
// the list_directory fix was reverted to dir.ReadDir(-1) -- which is exactly
// os.ReadDir's semantics through a handle -- the allocation test failed and
// this guard passed. A gate that only recognises the convenience spelling of
// the thing it forbids is the shape this whole pass exists to find, so the
// count-less spelling is named too.
var unboundedReaders = []string{"os.ReadFile(", "os.ReadDir(", "ReadDir(-1)"}

// builtinToolSurface is the set of files implementing tools a model can call
// with a path of its choosing. Listed rather than globbed so that adding a new
// tool file is a decision someone makes, not something a pattern silently
// absorbs -- and TestBuiltinToolSurfaceFilesAllExist below is what stops this
// list from rotting into a list of files that are gone.
var builtinToolSurface = []string{
	"mcpbuiltin.go",
	"mcp_ast_edit.go",
	// Added 2026-09-15, after TestBuiltinToolSurfaceListIsComplete derived the
	// set from the registry and found these three absent. Each was traced to the
	// end of its allocation chain before being added, and each is clean -- but
	// clean for a DIFFERENT reason, and one of them only at file level:
	//
	//   mcp_exec.go   caps AT SOURCE. execMaxOutputBytes feeds a tailBuffer used
	//                 as cmd.Stdout/Stderr, so the bytes are never all resident;
	//                 its own comment distinguishes that from the downstream
	//                 egress cap, which is the Class III distinction exactly.
	//                 builtinRepoMap takes no model-chosen path, and repomap.go
	//                 re-runs shouldSkipFile before reading.
	//   webtools.go   bounds BEFORE the allocation -- webfetch.go's
	//                 io.ReadAll(io.LimitReader(resp.Body, maxBytes)), chosen
	//                 over Content-Length because a hostile server can lie.
	//   mcp_lsp.go    has NO read of its own. Its handlers' only outward call is
	//                 srv.Call, and the unbounded read in that chain is
	//                 readHeaders' ReadString in lsp_bridge.go -- one file
	//                 deeper, where a guard that reads FILES cannot see it.
	//                 Listing mcp_lsp.go is therefore necessary and NOT
	//                 sufficient: the risk it actually carries is R1.24/TB7,
	//                 which no file-scoped guard can reach.
	"mcp_exec.go",
	"mcp_lsp.go",
	"webtools.go",
}

// TestBuiltinToolSurfaceBoundsItsReads is the CLASS guard, and it exists
// because fixing three sites does not stop a fourth.
//
// The prediction it is written against: a new tool handler repeats this defect
// because a cap constant with a comment beside it looks sufficient. It does
// look sufficient. maxBuiltinReadBytes looked sufficient for months, and the
// test that covered read_file asserted len(res.Content) and passed throughout
// -- its failure message even called that "the read cap".
//
// So this does not check for a cap. It checks that the unbounded READERS are
// absent, which is a property a reviewer cannot satisfy by adding a constant.
//
// Comments are stripped first (stripGoComments, socketauthcoverage_test.go),
// for the reason that file gives: without it, prose mentioning os.ReadFile --
// including the explanatory comments the fix added -- would trip a guard that
// is supposed to be reading code.
func TestBuiltinToolSurfaceBoundsItsReads(t *testing.T) {
	for _, name := range builtinToolSurface {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		code := stripGoComments(string(raw))

		// Vacuity floor: stripping must not have eaten the file. A guard over
		// an empty string passes for every input.
		if len(code) < len(raw)/4 {
			t.Fatalf("vacuity floor: stripping comments left %d of %d bytes of %s; "+
				"the guard below would be inspecting almost nothing", len(code), len(raw), name)
		}

		for _, reader := range unboundedReaders {
			if strings.Contains(code, reader) {
				t.Errorf("%s calls %s.\n"+
					"This file implements tools a model calls with a path it chooses, so an "+
					"unbounded read is allocation the model controls. Use readBoundedFile "+
					"(mcpbuiltin.go), or dir.ReadDir(n) for a listing.\n"+
					"If this call is genuinely bounded some other way, say how in a comment "+
					"AND take it out of this file -- the guard reads code, not intentions.",
					name, reader)
			}
		}
	}
}

// TestBuiltinToolSurfaceFilesAllExist is the anti-R1.16 half: a guard whose
// expected set is a hand-written list can be satisfied by deleting entries from
// the list. Modelled on TestAcceptSiteClassificationsAllPointAtRealFiles.
func TestBuiltinToolSurfaceFilesAllExist(t *testing.T) {
	if len(builtinToolSurface) == 0 {
		t.Fatal("vacuity floor: the tool surface list is empty, so the guard checks nothing")
	}
	for _, name := range builtinToolSurface {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("builtinToolSurface lists %s, which does not exist: %v\n"+
				"A tool file was renamed or removed and this list was not updated, which "+
				"is how a guard quietly stops covering the thing it names.", name, err)
		}
	}
}

// daemonFuncDeclFiles maps every function and method declared in this package to
// the file declaring it. Same parse as daemonCallGraph (builtincapability_test.go),
// which deliberately records call edges and not locations.
func daemonFuncDeclFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			continue // a build-tagged file this configuration does not compile
		}
		for _, decl := range file.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
				out[fd.Name.Name] = name
			}
		}
	}
	return out
}

// TestBuiltinToolSurfaceListIsComplete is the OTHER direction of the anti-rot
// guard, and the one that was missing.
//
// TestBuiltinToolSurfaceFilesAllExist stops builtinToolSurface naming files that
// are gone. NOTHING stopped a file that holds a live handler from never being
// added -- and the comment above the list says the hand-list is safe because
// "adding a new tool file is a decision someone makes, not something a pattern
// silently absorbs." True, and unenforced: three files already hold registered
// handlers and are absent from it.
//
// DERIVED FROM THE REGISTRY, NOT FROM FILENAMES, and not by globbing. The shape
// is gate-parity.sh's: derive both sides mechanically and compare them, so a new
// arrival is red until someone decides about it.
//
// THE TRAP THAT MAKES THE NAIVE DERIVATION WRONG. Deriving from `Handler: s.X`
// lines alone MISSES mcp_ast_edit.go, which is already on the list. Three
// handlers are registered through an inline closure rather than a method value
// -- builtinRepoMap, builtinProposeEdit and builtinProposeASTEdit -- so a
// registration-shaped grep would have DROPPED a file the hand-list got right.
// Closures are resolved here through daemonCallGraph, which keys func literals
// the way the runtime names them.
func TestBuiltinToolSurfaceListIsComplete(t *testing.T) {
	calls, _ := daemonCallGraph(t)
	declFile := daemonFuncDeclFiles(t)

	// Anti-vacuity, before any loop that passes trivially on an empty parse.
	if len(declFile) < 50 {
		t.Fatalf("vacuity floor: parsed only %d function declarations from this package; "+
			"the walk found almost nothing, so its verdict is meaningless", len(declFile))
	}
	if len(builtinToolSurface) == 0 {
		t.Fatal("vacuity floor: builtinToolSurface is empty")
	}

	s := builtinTestServer(t)
	tools := s.builtinTools(&proposalSink{}, "")
	if len(tools) == 0 {
		t.Fatal("vacuity floor: no built-ins registered, so nothing was derived")
	}

	derived := map[string]bool{}
	for _, b := range tools {
		fn := handlerFuncName(b.Handler)
		if fn == "" {
			t.Errorf("could not resolve a handler function name for %q, so its file was not derived",
				b.Tool.Name)
			continue
		}
		hit := false
		if strings.Contains(fn, ".") {
			// A func literal, named outer.funcN. Resolve to the builtin* method
			// it calls -- the literal itself lives in the registration file and
			// would otherwise hide the implementation's real home.
			for _, callee := range calls[fn] {
				if !strings.HasPrefix(callee, "builtin") {
					continue
				}
				if f, ok := declFile[callee]; ok {
					derived[f] = true
					hit = true
				}
			}
		} else if f, ok := declFile[fn]; ok {
			derived[f] = true
			hit = true
		}
		if !hit {
			t.Errorf("built-in %q resolved to handler %q, which maps to no declaring file in "+
				"this package.\nThe derivation is broken, which means this guard is reporting on "+
				"fewer tools than exist -- fix the resolution before trusting a pass.",
				b.Tool.Name, fn)
		}
	}
	if len(derived) == 0 {
		t.Fatal("vacuity floor: derived no handler files at all; the comparison below would pass " +
			"against an empty set")
	}

	listed := map[string]bool{}
	for _, n := range builtinToolSurface {
		listed[n] = true
	}

	for f := range derived {
		if !listed[f] {
			t.Errorf("%s implements a registered built-in handler and is NOT in builtinToolSurface.\n"+
				"TestBuiltinToolSurfaceBoundsItsReads therefore does not read it, so an unbounded "+
				"os.ReadFile/os.ReadDir added there is not caught by the class guard.\n"+
				"Add it to the list deliberately, or move the handler.", f)
		}
	}
	for f := range listed {
		if !derived[f] {
			t.Errorf("builtinToolSurface lists %s, but no registered built-in handler resolves to "+
				"it.\nEither the file no longer implements a tool the model can call -- in which "+
				"case drop it, and say so -- or the derivation stopped seeing it, which is worse "+
				"because the guard would then be silently narrower than its list.", f)
		}
	}
}
