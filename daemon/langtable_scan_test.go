package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EVERY PLACE THIS PRODUCT DECIDES WHAT KIND OF FILE IT IS LOOKING AT IS
// CLASSIFIED HERE.
//
// It was maintained by nobody. daemon/mcp_lsp.go and daemon/mcp_ast_edit.go each
// held their own eight-line extension switch -- byte for byte identical, down to
// the whitespace -- and both opened with
//
//	lang := "go"
//
// A .rs file matched neither case, kept "go", and went to gopls. gopls answered
// honestly that it had no symbols for it, and propose_ast_edit reported that to
// the user as `symbol "main" not found in lib.rs`: a confident statement that
// their code was wrong, when the truth was that we had asked the wrong compiler.
// Two copies meant two chances to write that default, and no mechanism that
// noticed either.
//
// editapply.LanguageOf is now the single authority, and serverCommand's typed
// parameter means nothing can spell a language any more, so nothing can misspell
// one. This test covers the gap that leaves: something can still look at a
// filename and build its own private notion of what the file is.
//
// IT MATCHES THE CONSTRUCT, NOT THE WORD, and does it on the AST rather than the
// text. That is not fastidiousness -- the first version of this test grepped for
// "filepath.Ext(" and would have missed the most natural way to write the very
// bug it exists to prevent:
//
//	if strings.HasSuffix(path, ".rs") { ... }
//
// which contains no Ext at all. The AST walk sees both forms, and sees neither
// inside a comment. Same discipline as socketauthcoverage_test.go, which pins
// every place this daemon admits a local peer.

// fileKindUse says why one file is allowed to look at a filename's shape.
type fileKindUse int

const (
	// langtableAuthority: the one table that maps an extension to a language.
	// Exactly one file may hold this, and the test asserts it.
	langtableAuthority fileKindUse = iota

	// displayOnly: the extension goes into a MESSAGE and routes nothing. Safe,
	// because a wrong word in a sentence is a typo and a wrong language is a
	// wrong compiler.
	displayOnly

	// separateAxis: a classification that is deliberately NOT a language.
	// Retrieval ranking buckets .md and .txt and lockfiles and is intentionally
	// broader and coarser than any language table; merging the two would make a
	// ranking tweak a compiler-routing change. The two are allowed to disagree,
	// and this classification is the record that the disagreement is intended.
	separateAxis

	// nonSourceFormat: the shape of a container this product downloads, not the
	// language of a source file. A .tgz is not a language and never will be.
	nonSourceFormat
)

// fileKindSites classifies every non-test file that inspects a filename's shape,
// with the number of times it does so. THE COUNT IS PART OF THE CLASSIFICATION:
// a new inspection inside an already-listed file is exactly as unreviewed as one
// in a new file, and without the count it would slip through.
var fileKindSites = map[string]struct {
	use   fileKindUse
	sites int
}{
	// LanguageOf itself.
	"editapply/langtable.go": {langtableAuthority, 1},

	// describeExt, feeding "no syntax check applied (unsupported for .rs)".
	"editapply/apply.go": {displayOnly, 1},

	// describeFileExt, feeding "no language server is configured for .rs files".
	"daemon/lsp_bridge.go": {displayOnly, 1},

	// classifyFile (code/test/doc/config/other) and isTestFile, for rerank.go's
	// class weights.
	"daemon/fileclass.go": {separateAxis, 2},

	// .tgz / .tar.gz / .zip, picking how to unpack a downloaded runtime.
	"daemon/onnxruntimefetch.go": {nonSourceFormat, 3},
}

// scanRoots are the module directories whose production code could route on a
// file's kind. Fixed rather than discovered, for the same reason scripts/fuzz.sh
// lists its targets: a directory that stops being scanned should be visible as a
// deletion in a diff.
var scanRoots = []string{".", "../editapply", "../clients/tui", "../protocol"}

// extensionLiteral matches a string literal that is being used as a file
// extension or a filename suffix -- ".go", ".tar.gz", "_test.go" -- and not the
// many other dotted strings a program contains (hosts, versions, URLs). Keeping
// it tight is what stops this test from crying wolf; keeping it anchored is what
// stops it from missing ".rs".
var extensionLiteral = regexp.MustCompile(`^\.[A-Za-z0-9]+(\.[A-Za-z0-9]+)*$|^_[A-Za-z0-9]+\.[A-Za-z0-9]+$`)

func TestLanguageKnowledgeIsClassified(t *testing.T) {
	found := map[string]int{}
	filesScanned := 0

	for _, root := range scanRoots {
		abs, err := filepath.Abs(root)
		if err != nil {
			t.Fatalf("resolving scan root %s: %v", root, err)
		}
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("scan root %s is not there (%v); this test would silently cover less "+
				"than it claims", root, err)
		}
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" || name == ".git" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			filesScanned++
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if perr != nil {
				// A file this test cannot parse is one it cannot vouch for, and
				// skipping it silently is how a scanner starts covering less than
				// it says. The tree compiles, so this is a bug in the walk.
				t.Errorf("parsing %s: %v", path, perr)
				return nil
			}
			if n := countFileKindSites(file); n > 0 {
				found[shortName(path)] += n
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	// ANTI-VACUITY. A scanner that finds nothing passes, and would keep passing
	// after somebody moved every site somewhere it does not look. 131 production
	// Go files across the four roots when this was written; floored at 100 so
	// ordinary deletion cannot trip it but a lost module can.
	if filesScanned < 100 {
		t.Fatalf("scanned only %d Go files; the roots are wrong and this test proves nothing", filesScanned)
	}
	if len(found) == 0 {
		t.Fatal("found no filename inspections at all. Either the product stopped looking at " +
			"file shapes -- in which case delete this test and the table -- or the walk is broken.")
	}

	for _, file := range sortedFileKindKeys(found) {
		want, classified := fileKindSites[file]
		if !classified {
			t.Errorf("%s inspects a filename's shape %d time(s) and is NOT classified.\n\n"+
				"Something new is deciding what kind of file it is looking at. Two "+
				"byte-identical copies of that decision, each defaulting to Go, are why this "+
				"test exists: a .rs file went to gopls and the user was told their symbol did "+
				"not exist.\n\n"+
				"If it is about a LANGUAGE, use editapply.LanguageOf. If it genuinely is not "+
				"-- a message, a coarser classification like fileclass.go's, or a container "+
				"format like onnxruntimefetch.go's -- add it to fileKindSites with the reason.",
				file, found[file])
			continue
		}
		if found[file] != want.sites {
			t.Errorf("%s now inspects a filename %d time(s), was classified for %d.\n\n"+
				"A new inspection in an already-classified file is exactly as unreviewed as "+
				"one in a new file. Check that it is still %s, then update the count.",
				file, found[file], want.sites, useName(want.use))
		}
	}

	for file := range fileKindSites {
		if _, ok := found[file]; !ok {
			t.Errorf("%s is classified here but no longer inspects a filename; drop the entry, "+
				"or this list drifts into fiction", file)
		}
	}

	// Exactly one authority. Two were the bug.
	authorities := 0
	for _, s := range fileKindSites {
		if s.use == langtableAuthority {
			authorities++
		}
	}
	if authorities != 1 {
		t.Errorf("%d files are classified as the language-table authority; there must be exactly "+
			"one. Two copies of this decision is the defect this whole file is about.", authorities)
	}
}

// countFileKindSites reports how many times file inspects a filename's shape:
// a filepath.Ext / path.Ext call, or a strings.HasSuffix / HasPrefix against a
// literal that looks like an extension.
func countFileKindSites(file *ast.File) int {
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case sel.Sel.Name == "Ext" && (pkg.Name == "filepath" || pkg.Name == "path"):
			n++
		case pkg.Name == "strings" && (sel.Sel.Name == "HasSuffix" || sel.Sel.Name == "HasPrefix"):
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err == nil && extensionLiteral.MatchString(v) {
					n++
				}
			}
		}
		return true
	})
	return n
}

func useName(u fileKindUse) string {
	switch u {
	case langtableAuthority:
		return "the language table's own lookup"
	case displayOnly:
		return "display only"
	case separateAxis:
		return "a deliberately separate classification axis"
	case nonSourceFormat:
		return "a container format, not a language"
	default:
		return "unclassified"
	}
}

// shortName renders an absolute path as <dir>/<file>, the form fileKindSites is
// keyed by, so the classification list reads the way a person refers to these
// files rather than as a machine-local path.
func shortName(path string) string {
	dir, file := filepath.Split(filepath.ToSlash(path))
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	if len(parts) == 0 {
		return file
	}
	return parts[len(parts)-1] + "/" + file
}

func sortedFileKindKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
