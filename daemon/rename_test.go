package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mochiii/editapply"
)

func lspEdit(startLine, startChar, endLine, endChar int, text string) lspTextEdit {
	var e lspTextEdit
	e.Range.Start.Line, e.Range.Start.Character = startLine, startChar
	e.Range.End.Line, e.Range.End.Character = endLine, endChar
	e.NewText = text
	return e
}

// POSITIONS ARE UTF-16, EXACTLY. An emoji before the name on the same line is
// two UTF-16 units and four bytes; a splice computed in runes or bytes would
// land inside the wrong character.
//
// Neuter check: count every rune as one unit in utf16RuneLen.
func TestApplyTextEditsIsExactInUTF16(t *testing.T) {
	content := "package a\n\nvar s = \"😀\" + Total(1)\nfunc Total(x int) int { return x }\n"
	// "Total" on line 2 starts after `var s = "😀" + ` -- 14 units (the emoji is 2).
	got, err := applyTextEdits(content, []lspTextEdit{
		lspEdit(2, 15, 2, 20, "Sum"),
		lspEdit(3, 5, 3, 10, "Sum"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "package a\n\nvar s = \"😀\" + Sum(1)\nfunc Sum(x int) int { return x }\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	for name, edits := range map[string][]lspTextEdit{
		"overlapping":             {lspEdit(3, 5, 3, 10, "A"), lspEdit(3, 8, 3, 12, "B")},
		"past the line":           {lspEdit(3, 90, 3, 95, "A")},
		"past the file":           {lspEdit(40, 0, 40, 1, "A")},
		"inside a character":      {lspEdit(2, 10, 2, 11, "A")},
		"ending before it starts": {lspEdit(3, 9, 3, 5, "A")},
	} {
		if out, err := applyTextEdits(content, edits); err == nil {
			t.Errorf("%s edits were applied: %q", name, out)
		}
	}
}

func renameFixture(t *testing.T) (*Server, *proposalSink, string) {
	t.Helper()
	s := builtinTestServer(t)
	writeFiles(t, s.workspace, map[string]string{
		"a.go": "package a\n\nfunc Total(x int) int { return x }\n",
		"b.go": "package a\n\nvar t = Total(1) + Total(2)\n",
	})
	root, _ := editapply.ResolveRealWorkspaceRoot(s.workspace)
	p := &proposalSink{stageFrom: root}
	if _, err := p.workingCopy(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.discard)
	return s, p, root
}

func renameEdits(root string) map[string][]lspTextEdit {
	return map[string][]lspTextEdit{
		fileURI(filepath.Join(root, "a.go")): {lspEdit(2, 5, 2, 10, "Sum")},
		fileURI(filepath.Join(root, "b.go")): {lspEdit(2, 8, 2, 13, "Sum"), lspEdit(2, 19, 2, 24, "Sum")},
	}
}

// A RENAME IS ALL OR NONE, and only on files exactly as the project has them.
//
// Neuter check: drop the st.touched check in applyRename, and the rename lands
// on a file the task already changed.
func TestARenameIsAllOrNoneAndOnlyOnUntouchedFiles(t *testing.T) {
	_, p, root := renameFixture(t)
	st := p.stage
	res, err := applyRename(st, root, renameEdits(root), "Total", "Sum")
	if err != nil || res.IsError {
		t.Fatalf("the rename failed: %v %s", err, res.Content)
	}
	for _, rel := range []string{"a.go", "b.go"} {
		if got, _ := stageFile(t, st, rel); strings.Contains(got, "Total") || !strings.Contains(got, "Sum") {
			t.Errorf("%s after the rename: %q", rel, got)
		}
	}
	if !strings.Contains(res.Content, "3 edit(s) in 2 file(s)") {
		t.Errorf("result = %q", res.Content)
	}

	// The same rename again: both files are now changed in the copy.
	_, p2, root2 := renameFixture(t)
	if _, err := p2.stage.apply(editapply.EditBlock{FilePath: "b.go", Search: "var t", Replace: "var u"}); err != nil {
		t.Fatal(err)
	}
	res, _ = applyRename(p2.stage, root2, renameEdits(root2), "Total", "Sum")
	if !res.IsError || !strings.Contains(res.Content, "already changed") {
		t.Errorf("a rename over a changed file was not refused: %q", res.Content)
	}
	if got, _ := stageFile(t, p2.stage, "a.go"); !strings.Contains(got, "Total") {
		t.Errorf("a refused rename still changed a.go: %q", got)
	}

	// A URI outside the project, and a file a command changed in the copy.
	_, p3, root3 := renameFixture(t)
	outside := map[string][]lspTextEdit{fileURI(filepath.Join(t.TempDir(), "x.go")): {lspEdit(0, 0, 0, 1, "y")}}
	if res, _ := applyRename(p3.stage, root3, outside, "x", "y"); !res.IsError || !strings.Contains(res.Content, "outside the project") {
		t.Errorf("a rename outside the project was not refused: %q", res.Content)
	}
	if err := os.WriteFile(filepath.Join(p3.stage.root, "b.go"), []byte("package a\n// a command wrote this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, _ := applyRename(p3.stage, root3, renameEdits(root3), "Total", "Sum"); !res.IsError || !strings.Contains(res.Content, "differs in the working copy") {
		t.Errorf("a rename over a file a command changed was not refused: %q", res.Content)
	}
}

// ALL OR NONE, ON THE APPLY PATH TOO: when the second file refuses its write,
// the first is put back.
//
// Neuter check: drop the undo loop in applyRename, and a.go keeps "Sum".
func TestARenameThatFailsHalfwayUndoesItself(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	s := builtinTestServer(t)
	writeFiles(t, s.workspace, map[string]string{
		"a.go":     "package a\n\nfunc Total(x int) int { return x }\n",
		"sub/b.go": "package a\n\nvar t = Total(1) + Total(2)\n",
	})
	root, _ := editapply.ResolveRealWorkspaceRoot(s.workspace)
	p := &proposalSink{stageFrom: root}
	st, err := p.workingCopy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.discard)
	sub := filepath.Join(st.root, "sub")
	if err := os.Chmod(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	edits := map[string][]lspTextEdit{
		fileURI(filepath.Join(root, "a.go")):     {lspEdit(2, 5, 2, 10, "Sum")},
		fileURI(filepath.Join(root, "sub/b.go")): {lspEdit(2, 8, 2, 13, "Sum"), lspEdit(2, 19, 2, 24, "Sum")},
	}
	res, _ := applyRename(st, root, edits, "Total", "Sum")
	if !res.IsError {
		t.Fatalf("the rename succeeded into an unwritable folder: %q", res.Content)
	}
	if got, _ := stageFile(t, st, "a.go"); !strings.Contains(got, "Total") || strings.Contains(got, "Sum") {
		t.Errorf("a.go was left renamed after the rename failed: %q", got)
	}
}

// THE BRIDGE ASKS FOR HIERARCHICAL SYMBOLS -- the shape with each name's own
// range -- and READS THE FLAT ONE TOO, for a server that answers with it.
// Two layers, each pinned here, because either alone makes real gopls work.
//
// Neuter checks: drop the capability from lspInitializeParams; drop the
// Location case from parseDocumentSymbols.
func TestTheBridgeAsksForAndReadsBothSymbolShapes(t *testing.T) {
	raw, _ := json.Marshal(lspInitializeParams("/w"))
	if !strings.Contains(string(raw), `"hierarchicalDocumentSymbolSupport":true`) {
		t.Errorf("the handshake does not ask for hierarchical symbols: %s", raw)
	}
	flat := `[{"location":{"uri":"file:///w/a.go","range":{"start":{"line":3,"character":0},"end":{"line":3,"character":38}}},"name":"Total","kind":12}]`
	syms, err := parseDocumentSymbols([]byte(flat))
	if err != nil || len(syms) != 1 || syms[0].Range.Start.Line != 3 || syms[0].Range.End.Character != 38 {
		t.Errorf("the flat shape parsed to %+v, %v", syms, err)
	}
	tree := `[{"name":"T","range":{"start":{"line":1,"character":0},"end":{"line":9,"character":1}},"selectionRange":{"start":{"line":1,"character":5},"end":{"line":1,"character":6}},"children":[{"name":"M","range":{"start":{"line":4,"character":0},"end":{"line":6,"character":1}},"selectionRange":{"start":{"line":4,"character":12},"end":{"line":4,"character":13}}}]}]`
	syms, err = parseDocumentSymbols([]byte(tree))
	if err != nil || len(syms) != 1 || len(syms[0].Children) != 1 || syms[0].Children[0].SelectionRange.Start.Character != 12 {
		t.Errorf("the hierarchical shape parsed to %+v, %v", syms, err)
	}
}

// propose_ast_edit WORKS AGAINST THE REAL LANGUAGE SERVER -- it was refused on
// every call until the symbol shape was read right. Skipped without gopls.
func TestProposeASTEditWithTheRealLanguageServer(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not on PATH")
	}
	s := builtinTestServer(t)
	writeFiles(t, s.workspace, map[string]string{
		"go.mod": "module example.com/a\n\ngo 1.21\n",
		"a.go":   "package a\n\n// Total adds one.\nfunc Total(x int) int { return x + 1 }\n",
	})
	root, _ := editapply.ResolveRealWorkspaceRoot(s.workspace)
	s.lspBridge = NewLSPBridge(root)
	t.Cleanup(s.lspBridge.Close)
	p := &proposalSink{}
	raw, _ := json.Marshal(map[string]string{"path": "a.go", "symbol": "Total", "replace": "func Total(x int) int { return x + 2 }"})
	res, err := s.builtinProposeASTEdit(withApprovedLaunch(context.Background(), "go"), raw, p)
	if err != nil || res.IsError || len(p.blocks) != 1 {
		t.Fatalf("propose_ast_edit against gopls: %v %q (%d block(s))", err, res.Content, len(p.blocks))
	}
	if b := p.blocks[0]; !strings.Contains(b.Search, "func Total(x int) int { return x + 1 }") || !strings.Contains(b.Replace, "x + 2") {
		t.Errorf("the edit is %+v", b)
	}
}

// THE REAL LANGUAGE SERVER, end to end: gopls renames a function declared in
// one file and called in another, in the working copy, and the result builds.
// Skipped where gopls is not installed.
func TestRenameSymbolWithTheRealLanguageServer(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not on PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	s := builtinTestServer(t)
	writeFiles(t, s.workspace, map[string]string{
		"go.mod":   "module example.com/a\n\ngo 1.21\n",
		"a.go":     "package a\n\n// Total adds one.\nfunc Total(x int) int { return x + 1 }\n",
		"b.go":     "package a\n\nvar t = Total(1) + Total(2)\n",
		"c/c.go":   "package c\n\nimport \"example.com/a\"\n\nvar C = a.Total(3)\n",
		"README":   "Total is documented here and must not change.\n",
		"notes.go": "package a\n\n// Totalizer is a different name.\nvar Totalizer = 1\n",
	})
	root, _ := editapply.ResolveRealWorkspaceRoot(s.workspace)
	s.lspBridge = NewLSPBridge(root)
	t.Cleanup(s.lspBridge.Close)
	p := &proposalSink{stageFrom: root}
	t.Cleanup(p.discard)

	raw, _ := json.Marshal(map[string]string{"path": "a.go", "symbol": "Total", "new_name": "Sum"})
	res, err := s.builtinRenameSymbol(withApprovedLaunch(context.Background(), "go"), raw, p)
	if err != nil || res.IsError {
		t.Fatalf("rename_symbol failed: %v %s", err, res.Content)
	}
	st := p.stage
	for rel, want := range map[string]string{"a.go": "func Sum(", "b.go": "Sum(1) + Sum(2)", "c/c.go": "a.Sum(3)"} {
		if got, _ := stageFile(t, st, rel); !strings.Contains(got, want) {
			t.Errorf("%s after the rename lacks %q:\n%s", rel, want, got)
		}
	}
	if got, _ := stageFile(t, st, "notes.go"); !strings.Contains(got, "Totalizer") {
		t.Errorf("a different name that starts the same was renamed: %q", got)
	}
	if got, _ := stageFile(t, st, "README"); !strings.Contains(got, "Total is documented") {
		t.Errorf("prose was renamed: %q", got)
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = st.root
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Errorf("the renamed copy does not build: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.go")); !strings.Contains(string(got), "func Total(") {
		t.Error("the rename reached the project itself")
	}
}

// A WINDOWS FILE URI KEEPS ITS DRIVE: file:///C:/x is C:/x, as fileURI writes
// it. Pure, so it is checked here on every platform.
//
// Neuter check: return p unchanged from trimDriveSlash.
func TestTrimDriveSlashGivesWindowsPathsTheirDrive(t *testing.T) {
	for in, want := range map[string]string{
		"/C:/Users/me/app/main.go": "C:/Users/me/app/main.go",
		"/c:/x":                    "c:/x",
		"/home/me/app/main.go":     "/home/me/app/main.go",
		"/1:/x":                    "/1:/x",
		"/C":                       "/C",
	} {
		if got := trimDriveSlash(in); got != want {
			t.Errorf("trimDriveSlash(%q) = %q, want %q", in, got, want)
		}
	}
}
