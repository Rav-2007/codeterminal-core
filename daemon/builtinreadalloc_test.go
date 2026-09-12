package main

import (
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
