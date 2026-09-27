package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
	"mochiii/protocol"
)

// The working copy (stage.go): the agent edits, reads and tests a private copy
// of the project, and the user reviews the net diff. See stage.go.

func stageProject(t *testing.T, files map[string]string) (*Server, string) {
	t.Helper()
	dir, err := editapply.ResolveRealWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{cfg: &Config{}, workspace: dir, logger: quietLogger()}, dir
}

func stagedSink(t *testing.T, s *Server) *proposalSink {
	t.Helper()
	sink := &proposalSink{stageFrom: s.workingCopySource("auto")}
	if sink.stageFrom == "" {
		t.Fatal("an ordinary turn has no working copy")
	}
	t.Cleanup(sink.discard)
	return sink
}

func propose(t *testing.T, s *Server, sink *proposalSink, path, search, replace string) (string, bool) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"path": path, "search": search, "replace": replace})
	res, err := s.builtinProposeEdit(context.Background(), args, sink)
	if err != nil {
		t.Fatalf("propose_edit: %v", err)
	}
	return res.Content, res.IsError
}

func readTool(t *testing.T, s *Server, sink *proposalSink, path string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"path": path})
	res, err := s.builtinReadFile(sink.readCtx(context.Background()), args)
	if err != nil || res.IsError {
		t.Fatalf("read_file %s: %v %s", path, err, res.Content)
	}
	return res.Content
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An edit lands in the copy: the agent reads it back, the project does not
// change.
func TestAnEditLandsInTheWorkingCopyNotTheProject(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.txt": "one\ntwo\n"})
	sink := stagedSink(t, s)
	if out, isErr := propose(t, s, sink, "a.txt", "two", "TWO"); isErr {
		t.Fatalf("refused: %s", out)
	}
	if got := readTool(t, s, sink, "a.txt"); got != "one\nTWO\n" {
		t.Errorf("read_file shows %q; the agent cannot see its own edit", got)
	}
	if got := fileText(t, filepath.Join(dir, "a.txt")); got != "one\ntwo\n" {
		t.Errorf("the project changed before any review: %q", got)
	}
}

// The second edit can build on the first -- which it could not while edits
// were only recorded.
func TestASecondEditBuildsOnTheFirst(t *testing.T) {
	s, _ := stageProject(t, map[string]string{"a.go": "package a\n\nfunc F() int { return 1 }\n"})

	plain := &proposalSink{}
	propose(t, s, plain, "a.go", "return 1", "return helper()")
	if _, isErr := propose(t, s, plain, "a.go", "return helper()", "return helper() + 1"); !isErr {
		t.Fatal("control: without a working copy the second edit should not find the first's text")
	}

	sink := stagedSink(t, s)
	propose(t, s, sink, "a.go", "return 1", "return helper()")
	if out, isErr := propose(t, s, sink, "a.go", "return helper()", "return helper() + 1"); isErr {
		t.Fatalf("the second edit could not build on the first: %s", out)
	}
}

// The net diff, applied the way the review applies it, reproduces the copy.
func TestTheOfferedEditsReproduceTheWorkingCopy(t *testing.T) {
	// F and G far enough apart to be two blocks, not one merged hunk.
	orig := "package a\n\nimport \"fmt\"\n\n// F says hi.\nfunc F() { fmt.Println(\"hi\") }\n" +
		strings.Repeat("\n// filler\n", 6) + "\n// G says bye.\nfunc G() { fmt.Println(\"bye\") }\n"
	s, dir := stageProject(t, map[string]string{"a.go": orig})
	sink := stagedSink(t, s)
	propose(t, s, sink, "a.go", "\"hi\"", "\"hello\"")
	propose(t, s, sink, "a.go", "\"bye\"", "\"goodbye\"")
	propose(t, s, sink, "b/new.txt", "", "fresh\n")
	want := readTool(t, s, sink, "a.go")

	blocks, info, degraded := sink.finish()
	if info == nil || len(degraded) != 0 {
		t.Fatalf("finish: info=%v degraded=%v", info, degraded)
	}
	if len(blocks) < 3 {
		t.Errorf("%d block(s); two separate changes and a new file should be three, not one whole-file block", len(blocks))
	}
	backup, err := editapply.NewBackupSessionDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		p, err := editapply.PrepareEdit(dir, b)
		if err != nil {
			t.Fatalf("offered edit %s does not apply: %v", b.FilePath, err)
		}
		if err := editapply.Apply(dir, p, backup); err != nil {
			t.Fatal(err)
		}
	}
	if got := fileText(t, filepath.Join(dir, "a.go")); got != want {
		t.Errorf("after review the project is\n%s\nwant\n%s", got, want)
	}
	if got := fileText(t, filepath.Join(dir, "b", "new.txt")); got != "fresh\n" {
		t.Errorf("new file: %q", got)
	}
}

// stagedEditBlocks, over awkward shapes: each result replays to exactly after.
func TestStagedEditBlocksReplay(t *testing.T) {
	cases := map[string][2]string{
		"insert at top":        {"b\nc\n", "a\nb\nc\n"},
		"append":               {"a\nb\n", "a\nb\nc\n"},
		"delete middle":        {"a\nb\nc\nd\n", "a\nd\n"},
		"no trailing newline":  {"a\nb", "a\nB"},
		"repeated lines":       {"x\nx\nx\ny\nx\nx\nx\n", "x\nx\nx\nY\nx\nx\nx\n"},
		"two far changes":      {strings.Repeat("l\n", 3) + "A\n" + strings.Repeat("m\n", 20) + "B\n" + strings.Repeat("n\n", 3), strings.Repeat("l\n", 3) + "A2\n" + strings.Repeat("m\n", 20) + "B2\n" + strings.Repeat("n\n", 3)},
		"all repeated":         {"x\nx\nx\n", "x\ny\nx\n"},
		"replace everything":   {"a\nb\n", "c\nd\n"},
		"emptied":              {"a\nb\n", ""},
		"identical duplicates": {"f()\nf()\n", "f()\ng()\n"},
	}
	for name, c := range cases {
		blocks := stagedEditBlocks("x.txt", c[0], c[1])
		cur := c[0]
		for _, b := range blocks {
			if strings.Count(cur, b.Search) != 1 {
				t.Errorf("%s: block search %q is not unique", name, b.Search)
				break
			}
			cur = strings.Replace(cur, b.Search, b.Replace, 1)
		}
		if cur != c[1] {
			t.Errorf("%s: replay gives %q, want %q", name, cur, c[1])
		}
	}
	if n := len(stagedEditBlocks("x", cases["two far changes"][0], cases["two far changes"][1])); n != 2 {
		t.Errorf("two far-apart changes gave %d block(s), want 2 small ones", n)
	}
	// Two lines of context around the change also occur elsewhere, so the
	// block must WIDEN until unique -- not fall back to the whole file, which
	// would show the user every line as changed.
	before := "A\nx\nx\n1\nx\nx\nB\nx\nx\n1\nx\nx\nC\n"
	after := strings.Replace(before, "1", "2", 1)
	blocks := stagedEditBlocks("x", before, after)
	if len(blocks) != 1 || blocks[0].Search == before || strings.Count(before, blocks[0].Search) != 1 {
		t.Errorf("ambiguous context gave %+v; want one block widened just enough to be unique", blocks)
	}
}

// Commands run in the copy, against the agent's edits; the copy's path never
// reaches the model; the result is reported.
func TestSandboxExecTestsTheWorkingCopy(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	s, dir := stageProject(t, map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.22\n",
		"m/m.go":      "package m\n\nfunc Two() int { return 3 }\n",
		"m/m_test.go": "package m\n\nimport \"testing\"\n\nfunc TestTwo(t *testing.T) {\n\tif Two() != 2 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n",
	})
	sink := stagedSink(t, s)
	run := func() string {
		res, err := s.builtinSandboxExecStaged(context.Background(), json.RawMessage(`{"command":"go test ./..."}`), sink)
		if err != nil || res.IsError {
			t.Fatalf("sandbox_exec: %v %s", err, res.Content)
		}
		return res.Content
	}
	if out := run(); !strings.HasPrefix(out, "Command exited with error") {
		t.Fatalf("the failing test passed before the fix:\n%s", out)
	}
	// go env GOMOD prints an absolute path: the copy's, rewritten to the
	// project's.
	res, _ := s.builtinSandboxExecStaged(context.Background(), json.RawMessage(`{"command":"go env GOMOD"}`), sink)
	if strings.Contains(res.Content, sink.stage.root) || !strings.Contains(res.Content, filepath.Join(dir, "go.mod")) {
		t.Errorf("go env GOMOD printed %q; want the project's path, never the working copy's", res.Content)
	}
	propose(t, s, sink, "m/m.go", "return 3", "return 2")
	if out := run(); strings.HasPrefix(out, "Command exited with error") {
		t.Fatalf("the test still fails after the fix -- the command did not run against the edit:\n%s", out)
	}
	if got := fileText(t, filepath.Join(dir, "m", "m.go")); !strings.Contains(got, "return 3") {
		t.Error("the project changed before review")
	}
	_, info, _ := sink.finish()
	if info == nil || info.Checked != "go test ./..." || !info.Passed {
		t.Errorf("working copy report = %+v, want the passing go test", info)
	}
}

// .git, protected and ignored folders are not copied; dependency folders are
// linked, not copied.
func TestWhatTheWorkingCopyContains(t *testing.T) {
	s, _ := stageProject(t, map[string]string{
		".gitignore":          "out/\n",
		".git/HEAD":           "ref: refs/heads/main\n",
		"out/big.bin":         "build output",
		"node_modules/x/i.js": "module.exports = 1\n",
		"src/a.go":            "package src\n",
	})
	sink := stagedSink(t, s)
	st, err := sink.workingCopy()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".git", "out"} {
		if _, err := os.Lstat(filepath.Join(st.root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s was copied", rel)
		}
	}
	if fi, err := os.Lstat(filepath.Join(st.root, "node_modules")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("node_modules is not a link (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(st.root, "src", "a.go")); err != nil {
		t.Errorf("source was not copied: %v", err)
	}
	if got := readTool(t, s, sink, "node_modules/x/i.js"); !strings.Contains(got, "exports") {
		t.Errorf("a file in a linked dependency folder cannot be read: %q", got)
	}
}

// Too large to copy: the turn works as it always did, and says so.
func TestATooLargeProjectFallsBackAndSaysSo(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	prev := stageMaxFiles
	stageMaxFiles = 1
	t.Cleanup(func() { stageMaxFiles = prev })
	sink := stagedSink(t, s)
	if out, isErr := propose(t, s, sink, "a.txt", "one", "ONE"); isErr {
		t.Fatalf("the edit was refused instead of falling back to a proposal: %s", out)
	}
	blocks, info, degraded := sink.finish()
	if info != nil || len(blocks) != 1 || blocks[0].Search != "one" {
		t.Errorf("fallback: blocks=%+v info=%+v", blocks, info)
	}
	if len(degraded) != 1 || degraded[0].Component != protocol.DegradedWorkingCopy {
		t.Errorf("the user was not told: %+v", degraded)
	}
	if got := fileText(t, filepath.Join(dir, "a.txt")); got != "one\n" {
		t.Error("the project changed")
	}
}

// What is in the copy but not offered, and why: a command's output, and a file
// the user changed while the agent worked (offering it would undo them).
func TestWhatIsNotOffered(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	sink := stagedSink(t, s)
	propose(t, s, sink, "a.txt", "one", "ONE")
	st := sink.stage
	if err := os.WriteFile(filepath.Join(st.root, "build.log"), []byte("made by a command"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The user edits a.txt in their editor meanwhile.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("user's own change, longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocks, info, _ := sink.finish()
	if len(blocks) != 0 {
		t.Errorf("offered %+v; a.txt would undo the user's change and build.log is build output", blocks)
	}
	joined := strings.Join(info.NotOffered, "\n")
	for _, want := range []string{"build.log: created by a command", "a.txt: changed on disk"} {
		if !strings.Contains(joined, want) {
			t.Errorf("not-offered list lacks %q:\n%s", want, joined)
		}
	}
}

// An edit outside the project is not copied: it stays a proposal against the
// real file, reviewed with its banner, as before.
func TestAnOutsideEditStaysAProposal(t *testing.T) {
	home, project := homeAndProject(t)
	s := &Server{cfg: &Config{}, workspace: project, logger: quietLogger()}
	sink := stagedSink(t, s)
	if out, isErr := propose(t, s, sink, "~/Desktop/notes.md", "", "hi\n"); isErr {
		t.Fatalf("refused: %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "Desktop", "notes.md")); !os.IsNotExist(err) {
		t.Error("an outside edit was written before review")
	}
	blocks, _, _ := sink.finish()
	if len(blocks) != 1 || blocks[0].FilePath != "~/Desktop/notes.md" {
		t.Errorf("blocks = %+v", blocks)
	}
}

// Plan mode, and a config that switched it off, have no working copy.
func TestWhoGetsAWorkingCopy(t *testing.T) {
	s, _ := stageProject(t, map[string]string{"a.txt": "x\n"})
	if s.workingCopySource("plan") != "" {
		t.Error("plan mode got a working copy")
	}
	s.cfg.MCP.NoWorkingCopy = true
	if s.workingCopySource("auto") != "" {
		t.Error("no_working_copy was ignored")
	}
}

// The copy is removed at the end of the turn, and copies a killed daemon left
// behind are swept at startup.
func TestWorkingCopiesAreRemoved(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.txt": "x\n"})
	sink := stagedSink(t, s)
	propose(t, s, sink, "a.txt", "x", "y")
	root := sink.stage.root
	sink.finish()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the working copy survived the turn (err=%v)", err)
	}

	leftover, err := newStagedWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	sweepStaleStages(dir)
	if _, err := os.Stat(leftover.root); !os.IsNotExist(err) {
		t.Errorf("a leftover working copy was not swept (err=%v)", err)
	}
}

// MEASURED: the model made a change with propose_edit AND wrote the same
// change as a text block in its answer; offered on top of the copy's diff it
// applied twice ("IsPalindrome redeclared"). Answer-text edits now go into the
// copy first: a duplicate is dropped, a new change joins the one diff, and one
// that collides with the agent's own edits is not offered, and said so.
func TestAnswerTextEditsJoinTheWorkingCopyOnce(t *testing.T) {
	s, dir := stageProject(t, map[string]string{
		"a.go": "package a\n\nfunc F() int { return 1 }\n",
		"b.go": "package a\n\nfunc G() int { return 2 }\n",
	})
	sink := stagedSink(t, s)
	propose(t, s, sink, "a.go", "return 1", "return 10")

	rest := sink.absorbText([]editapply.EditBlock{
		{FilePath: "a.go", Search: "return 1", Replace: "return 10"},     // the duplicate
		{FilePath: "b.go", Search: "return 2", Replace: "return 20"},     // a new change
		{FilePath: "a.go", Search: "return 1 }", Replace: "return 99 }"}, // collides: 1 is now 10
	})
	if len(rest) != 0 {
		t.Errorf("%d block(s) were left to offer on top of the working copy: %+v", len(rest), rest)
	}
	blocks, info, _ := sink.finish()
	after := map[string]string{"a.go": fileText(t, filepath.Join(dir, "a.go")), "b.go": fileText(t, filepath.Join(dir, "b.go"))}
	for _, b := range blocks {
		if strings.Count(after[b.FilePath], b.Search) != 1 {
			t.Fatalf("offered block does not apply once: %+v", b)
		}
		after[b.FilePath] = strings.Replace(after[b.FilePath], b.Search, b.Replace, 1)
	}
	if !strings.Contains(after["a.go"], "return 10 }") || strings.Contains(after["a.go"], "return 99") ||
		strings.Count(after["a.go"], "func F") != 1 {
		t.Errorf("a.go after review:\n%s", after["a.go"])
	}
	if !strings.Contains(after["b.go"], "return 20") {
		t.Errorf("the new change in the answer was lost:\n%s", after["b.go"])
	}
	if info == nil || !strings.Contains(strings.Join(info.NotOffered, "\n"), "a.go: an edit written in the answer did not apply") {
		t.Errorf("the colliding edit was dropped without a word: %+v", info)
	}
}
