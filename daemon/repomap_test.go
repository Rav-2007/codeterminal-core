package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mapWorkspace builds a small workspace with the shapes that matter: real
// source in more than one language, a secret, a gitignored build directory, and
// something inside .git.
func mapWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(real, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(".gitignore", "build/\n")
	write("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n\ntype Server struct {\n\tport int\n}\n\nfunc (s *Server) Start() error {\n\treturn nil\n}\n")
	write("lib/parse.py", "import os\n\n\nclass Parser:\n    def parse(self, s):\n        return s\n\n\ndef helper(x):\n    return x\n")
	write("web/app.ts", "export function mount(el) {}\n\nexport class Widget {}\n\nconst internal = 1;\n")
	write(".env", "OPENAI_API_KEY=sk-must-not-appear\n")
	write("build/generated.go", "package build\n\nfunc Generated() {}\n")
	write(".git/config", "[core]\n")
	return real
}

// THE PROPERTY THAT MATTERS MOST, and the one a second walk would get wrong.
//
// The map lists paths, and a listing is a disclosure: it goes into a prompt that
// leaves this machine. If it walked on its own rules it would be a SECOND
// exclusion surface, weaker than the indexer's and maintained by nobody -- and
// the first thing it would do is name the .env file the indexer has always
// refused to read. Sharing shouldSkipFile is what makes that impossible.
func TestTheMapNeverNamesWhatTheIndexerRefusesToRead(t *testing.T) {
	root := mapWorkspace(t)
	m, err := buildRepoMap(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	out := m.Render()

	for _, forbidden := range []string{".env", "sk-must-not-appear", ".git/", "build/generated.go"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the map discloses %q:\n%s", forbidden, out)
		}
	}
	// ANTI-VACUITY: an empty map excludes everything and proves nothing.
	if !strings.Contains(out, "main.go") {
		t.Fatalf("the map lists no source at all, so the exclusions above are meaningless:\n%s", out)
	}
}

// Declarations, across languages, at the left margin only.
func TestTheMapExtractsTopLevelDeclarations(t *testing.T) {
	root := mapWorkspace(t)
	m, err := buildRepoMap(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	// Through symbolsFor, which is how Render gets them: the walk no longer
	// extracts declarations eagerly, so reading f.Symbols directly would read
	// the unloaded zero value and pass this test by testing nothing.
	byPath := map[string][]string{}
	for i := range m.Files {
		byPath[m.Files[i].Path] = m.symbolsFor(&m.Files[i])
	}

	for _, tc := range []struct {
		path string
		want []string
		deny []string
	}{
		// A Go method's receiver is stepped over: the name is Start, not (s.
		{"main.go", []string{"func main", "type Server", "func Start"}, []string{"port"}},
		// Python: class and def at the margin; the indented method is not a
		// top-level declaration and must not be listed as one.
		{"lib/parse.py", []string{"class Parser", "def helper"}, []string{"def parse"}},
		{"web/app.ts", []string{"export function mount", "export class Widget"}, nil},
	} {
		got := strings.Join(byPath[tc.path], ", ")
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: declarations %q, want to contain %q", tc.path, got, want)
			}
		}
		for _, deny := range tc.deny {
			if strings.Contains(got, deny) {
				t.Errorf("%s: declarations %q must not contain %q", tc.path, got, deny)
			}
		}
	}
}

// A map that quietly stops is one a model reads as complete -- and concluding a
// file does not exist is the failure this whole feature exists to prevent. So
// truncation has to be stated, in the map, in words.
func TestATruncatedMapSaysSo(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// Enough files, with long enough names, to overrun the budget several times.
	// The index is in the NAME: an earlier version keyed on i%26 and wrote the
	// same twenty-six files thirty-five times, so the fixture that was meant to
	// overflow the budget fit inside it comfortably and the test passed by
	// testing nothing.
	for i := 0; i < 900; i++ {
		name := filepath.Join(real, "pkg", fmt.Sprintf("%s%03d.go", strings.Repeat("n", 40), i))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("package pkg\n\nfunc F() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m, err := buildRepoMap(context.Background(), real)
	if err != nil {
		t.Fatal(err)
	}
	out := m.Render()

	if len(out) > maxRepoMapBytes {
		t.Errorf("the map is %d bytes, over its own %d-byte budget", len(out), maxRepoMapBytes)
	}
	if !m.Truncated || m.NotShown == 0 {
		t.Fatalf("900 files fit in %d bytes? truncated=%v notShown=%d", maxRepoMapBytes, m.Truncated, m.NotShown)
	}
	if !strings.Contains(out, "omitted for space") {
		t.Errorf("the map dropped %d files and does not say so:\n%s", m.NotShown, out[:min(400, len(out))])
	}
	// The remedy it names has to be a tool that exists, or it is advice the
	// model cannot take.
	if !strings.Contains(out, "list_directory") {
		t.Error("the truncation notice does not name a tool the model can actually call")
	}
}

// The budget must not be spendable by one directory: detail is spread, so a
// project's later packages are not invisible because of where they sort.
func TestDeclarationDetailIsSpreadAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"aaa", "mmm", "zzz"} {
		for i := 0; i < 30; i++ {
			body := "package p\n\nfunc " + dir + "F() {}\n\ntype " + dir + "T struct{}\n"
			// %02d, not string(rune('a'+i)): the loop runs to 30, so the rune
			// form walked past 'z' into '{', '|', '}' and '~' -- and '|' is not
			// a legal character in a Windows filename, so this test could never
			// have run there.
			p := filepath.Join(real, dir, fmt.Sprintf("%s%02d.go", filepath.Base(dir), i))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	m, err := buildRepoMap(context.Background(), real)
	if err != nil {
		t.Fatal(err)
	}
	out := m.Render()
	detail := out[strings.Index(out, "declarations:"):]

	for _, dir := range []string{"aaa/", "mmm/", "zzz/"} {
		if !strings.Contains(detail, dir) {
			t.Errorf("no declarations from %s; the first directory ate the budget:\n%s", dir, detail)
		}
	}
}

// THE POINT OF ALL OF IT: the specialist that cannot look gets shown.
//
// The Planner has no tools by design -- planning is reasoning over the request
// -- so when it needs to name a file it has exactly two options, and MEASURED
// it took the wrong one: it invented `src/agent/agent.ts` in a Go repository
// and the phases downstream carried the invention forward as a finding. Both
// losses in the §14 A/B were attributed to exactly that.
//
// The role prompt already tells it to say when it does not know. This test is
// the difference between telling a model not to guess and giving it the answer.
func TestThePlannerIsGivenTheMapAndTheOthersAreNot(t *testing.T) {
	root := mapWorkspace(t)
	once := &repoMapOnce{root: root}

	planner := repoMapFor(context.Background(), &rolePlanner, once)
	if planner == "" {
		t.Fatal("the Planner -- the one role that cannot go and look -- was given no map")
	}
	if !strings.Contains(planner, "main.go") {
		t.Errorf("the Planner's map does not list the workspace's own source:\n%s", planner)
	}

	// A role that can search does not need a map more than it needs the budget
	// the map would spend.
	for _, role := range []*agentRole{&roleResearcher, &roleCoder, &roleTester} {
		if got := repoMapFor(context.Background(), role, once); got != "" {
			t.Errorf("%s was given the map; it has tools and can look", role.Display)
		}
	}
	// Built once for the turn, however many phases ask.
	if repoMapFor(context.Background(), &rolePlanner, once); !once.built {
		t.Error("the map was never actually built")
	}
}

// The map has to reach the model, not merely be built: it goes in the phase's
// SYSTEM message, with the sentence that makes it usable -- that a path not
// listed may not exist.
func TestTheMapReachesThePhaseAsStandingContext(t *testing.T) {
	root := mapWorkspace(t)
	once := &repoMapOnce{root: root}

	messages := buildPhaseMessages("base system", nil, "do the thing", &rolePlanner, nil, 4096,
		repoMapFor(context.Background(), &rolePlanner, once))
	if len(messages) == 0 {
		t.Fatal("no messages built")
	}
	if messages[0].Role != "system" {
		t.Fatalf("first message is %q, want the system message", messages[0].Role)
	}
	system := messages[0].Content
	for _, want := range []string{"base system", "PLANNER", "main.go", "may not exist"} {
		if !strings.Contains(system, want) {
			t.Errorf("the phase's system message is missing %q", want)
		}
	}
	// It must not also be pasted into the user turn: the request is what the
	// user asked, and duplicating the map there would make every handoff quote
	// it back.
	for _, m := range messages[1:] {
		if strings.Contains(m.Content, "repository map") {
			t.Errorf("the map was duplicated into a %s message", m.Role)
		}
	}
}

// EVERY DIRECTORY IS NAMED, whatever it costs, because a directory absent from
// the map is a directory a model concludes does not exist.
//
// This is the same bug as TestDeclarationDetailIsSpreadAcrossDirectories, one
// level up, and it survived that test for a release. MEASURED on this
// repository: daemon/ took 4,205 of the 8,192-byte path budget for its 226
// files, the listing died at "editapply/", and helper/, mcp-servers/,
// protocol/, proxy/ and testdata/ never appeared -- under a header that said
// "518 files in 45 directories". Alphabetical order was the budget policy.
//
// The fixture reproduces that shape: one enormous early directory, small ones
// after it in every direction.
func TestNoDirectoryIsMissingFromTheMap(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(dir, name string) {
		p := filepath.Join(real, dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package p\n\nfunc F() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 600; i++ {
		write("bbb", fmt.Sprintf("%s%03d.go", strings.Repeat("w", 30), i))
	}
	small := []string{"aaa", "protocol", "proxy", "helper", "zzz"}
	for _, d := range small {
		for i := 0; i < 3; i++ {
			write(d, fmt.Sprintf("f%d.go", i))
		}
	}

	m, err := buildRepoMap(context.Background(), real)
	if err != nil {
		t.Fatal(err)
	}
	out := m.Render()
	if len(out) > maxRepoMapBytes {
		t.Errorf("the map is %d bytes, over its own %d-byte budget", len(out), maxRepoMapBytes)
	}
	for _, d := range append([]string{"bbb"}, small...) {
		if !strings.Contains(out, d+"/ (") {
			t.Errorf("directory %q is absent from the map; a model reading this concludes it does not exist:\n%s",
				d, out[:min(600, len(out))])
		}
	}
	// And the count next to a directory is the TRUE count, not the shown count:
	// that is what makes "+N more" actionable rather than decorative.
	if !strings.Contains(out, "bbb/ (600)") {
		t.Errorf("the big directory does not report its real file count:\n%s", out[:min(600, len(out))])
	}
}

// Within a directory the alphabet is not a priority order either. MEASURED:
// daemon/'s visible names ran from "addrinuse_unix.go" to "gate7_scrub_test.go"
// -- forty test files shown while server.go, orchestrator.go and roles.go were
// hidden behind them.
func TestCodeIsListedBeforeTestsWithinADirectory(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		p := filepath.Join(real, "pkg", fmt.Sprintf("aaa%s%03d_test.go", strings.Repeat("t", 25), i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package p\n\nfunc TestX(t *testing.T) {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Sorts last alphabetically, and is the only file here that matters.
	if err := os.WriteFile(filepath.Join(real, "pkg", "zzz_server.go"),
		[]byte("package p\n\nfunc Serve() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := buildRepoMap(context.Background(), real)
	if err != nil {
		t.Fatal(err)
	}
	out := m.Render()
	if !strings.Contains(out, "zzz_server.go") {
		t.Errorf("the one source file in the directory is hidden behind 400 test files:\n%s",
			out[:min(600, len(out))])
	}
}

// The per-file symbol cap chooses WHICH declarations survive, and a file's
// preamble is not its API. MEASURED on repomap.go itself: the first twelve
// declarations were five consts, two types and five funcs -- and Render, the
// function the file exists for, was not among them.
func TestAFilesAPIOutranksItsPreamble(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	body.WriteString("package p\n\n")
	for i := 0; i < maxSymbolsPerFile*2; i++ {
		fmt.Fprintf(&body, "const preamble%02d = %d\n", i, i)
	}
	body.WriteString("\nfunc TheOnlyThingThatMatters() {}\n")
	if err := os.WriteFile(filepath.Join(real, "a.go"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	got := strings.Join(declarationsIn(filepath.Join(real, "a.go")), ", ")
	if !strings.Contains(got, "func TheOnlyThingThatMatters") {
		t.Errorf("24 consts crowded out the only function in the file: %q", got)
	}
	if len(declarationsIn(filepath.Join(real, "a.go"))) > maxSymbolsPerFile {
		t.Errorf("the per-file cap was not applied: %q", got)
	}
}

// The map is built inside a turn the user can stop, so it has to stop too --
// both before the walk starts and, on a large or slow tree, during it.
func TestACancelledTurnStopsTheWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildRepoMap(ctx, ".."); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context did not stop the walk: err=%v", err)
	}
}

// lateCancel reports "not cancelled" for the first n calls to Err() and
// cancelled thereafter.
//
// A wall-clock cancel from another goroutine cannot say WHERE the walk noticed,
// so removing the in-walk check and keeping only the one at the entry passes
// that test -- measured, it did. This makes the entry check spend the grace and
// the walk find the cancellation, which is the half that matters: a turn
// interrupted while the map is building on a slow tree must not leave a
// goroutine walking the filesystem for a turn that already ended.
type lateCancel struct {
	context.Context
	calls *int
	grace int
}

func (c lateCancel) Err() error {
	*c.calls++
	if *c.calls <= c.grace {
		return nil
	}
	return context.Canceled
}

func TestCancellationIsNoticedDuringTheWalkNotOnlyBeforeIt(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// Comfortably more entries than one cancellation-check interval.
	for i := 0; i < 700; i++ {
		p := filepath.Join(real, fmt.Sprintf("d%02d", i%10), fmt.Sprintf("f%03d.go", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package p\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	calls := 0
	ctx := lateCancel{Context: context.Background(), calls: &calls, grace: 1}
	if _, err := buildRepoMap(ctx, real); !errors.Is(err, context.Canceled) {
		t.Errorf("the walk ran to completion on a context that went cancelled after it started: err=%v", err)
	}
	if calls < 2 {
		t.Errorf("the walk asked about cancellation %d times; it never checked once underway", calls)
	}
}

// THROUGHPUT IS A CORRECTNESS PROPERTY HERE, because the map is built on demand
// inside a turn the user is waiting on. MEASURED before this: the walk read all
// 518 files in this repository to extract declarations, the render printed 33
// of them, and 62ms of the 77ms build was thrown away. Extraction is now driven
// by the render, so a file nobody prints is a file nobody opens.
func TestTheMapDoesNotReadFilesItWillNotShow(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for d := 0; d < 20; d++ {
		for i := 0; i < 40; i++ {
			p := filepath.Join(real, fmt.Sprintf("d%02d", d), fmt.Sprintf("f%02d.go", i))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("package p\n\nfunc F() {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	m, err := buildRepoMap(context.Background(), real)
	if err != nil {
		t.Fatal(err)
	}
	loadedAfterWalk := 0
	for i := range m.Files {
		if m.Files[i].loaded {
			loadedAfterWalk++
		}
	}
	if loadedAfterWalk != 0 {
		t.Errorf("the walk read %d files' contents before anything asked for them", loadedAfterWalk)
	}

	m.Render()
	loaded := 0
	for i := range m.Files {
		if m.Files[i].loaded {
			loaded++
		}
	}
	if loaded == 0 {
		t.Fatal("the render read nothing, so this test proves nothing about what it skipped")
	}
	// The render can print at most one file per directory per round, and stops
	// well before that on this fixture. Reading even half the tree would mean
	// extraction is running ahead of what is printed.
	if loaded > len(m.Files)/2 {
		t.Errorf("the render read %d of %d files to print a bounded map", loaded, len(m.Files))
	}
}

// A scan that stops at the file cap must say the map is incomplete. Without
// this the header asserts "N files in M directories" about a tree it never
// finished walking -- the invented-path failure, arriving from the other side.
func TestHittingTheFileCapIsStatedInTheMap(t *testing.T) {
	m := &RepoMap{
		Files:    []repoFile{{Path: "a.go", Class: FileClassCode}},
		LimitHit: true,
	}
	out := m.Render()
	if !strings.Contains(out, "INCOMPLETE") {
		t.Errorf("the map stopped at the file cap and does not say so:\n%s", out)
	}
}

// The header is written last but its space is reserved first, and every clause
// in it is conditional -- so the reserve has to cover all of them firing at
// once, not the usual one or two. Getting this wrong overruns the cap in
// exactly the case where the map is already the least trustworthy.
func TestTheHeaderFitsItsReserveInTheWorstCase(t *testing.T) {
	m := &RepoMap{
		Files:    make([]repoFile, maxFilesScanned),
		LimitHit: true,
	}
	worst := m.renderHeader(1, 99999, 9999, 99999)
	if len(worst) > headerReserve {
		t.Errorf("the worst-case header is %d bytes and only %d are reserved for it, so the map "+
			"can overrun its own %d-byte cap:\n%s", len(worst), headerReserve, maxRepoMapBytes, worst)
	}
	// ANTI-VACUITY: every clause has to actually be present, or this measures
	// a header that cannot occur.
	for _, clause := range []string{"omitted for space", "omitted entirely", "INCOMPLETE", "declarations shown"} {
		if !strings.Contains(worst, clause) {
			t.Errorf("the worst case is missing the %q clause, so it is not the worst case:\n%s", clause, worst)
		}
	}
}

// An import is not a declaration, in either spelling. ES imports never reached
// the map because "import" is not a declaration keyword; CommonJS ones did.
func TestCommonJSImportsAreNotDeclarations(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	src := "const fs = require('fs')\nconst path = require(\"path\")\n" +
		"import x from 'y'\nconst REAL_SETTING = 3\nfunction doTheWork() {}\n"
	if err := os.WriteFile(filepath.Join(real, "s.js"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(declarationsIn(filepath.Join(real, "s.js")), ", ")
	for _, deny := range []string{"const fs", "const path", "x"} {
		if strings.Contains(got, deny) {
			t.Errorf("an import is listed as a declaration (%q) in %q", deny, got)
		}
	}
	// ANTI-VACUITY: the file's real declarations still have to be there, or the
	// exclusion above is just breaking extraction.
	for _, want := range []string{"function doTheWork", "const REAL_SETTING"} {
		if !strings.Contains(got, want) {
			t.Errorf("the exclusion also dropped %q: %q", want, got)
		}
	}
}

// PROSE IS NOT CODE, and the map used to think it was.
//
// Measured on this repository before symbolsFor grew its FileClassDoc guard:
// two of the 33 files it described in detail were markdown, and what it said
// about them was "var rows, function this, function revoke, let the, record
// implied" and "class P0". Every one of those is an English sentence that
// happens to begin at column zero with a word some language uses as a keyword.
//
// The wasted bytes are the small half. The real cost is that roles.go gives the
// planner this map SPECIFICALLY so it stops citing things that do not exist --
// and a map that offers it "function revoke" from a changelog is that same
// failure wearing the fix's clothes.
func TestTheMapNeverDescribesProseAsCode(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Real prose from the shapes actually observed, at column zero.
	write("NOTES.md", strings.Join([]string{
		"var rows were dropped by the sweep.",
		"function this way round is clearer.",
		"function revoke was the one that mattered.",
		"let the reader decide.",
		"record implied a schema change.",
		"class P0 defects block the gate.",
	}, "\n")+"\n")
	// A real Go file, so the map has something legitimate to say.
	write("real.go", "package main\n\nfunc RealDeclaration() {}\n")

	m, err := buildRepoMap(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	out := m.Render()

	for _, invented := range []string{"function revoke", "let the", "record implied", "class P0", "function this", "var rows"} {
		if strings.Contains(out, invented) {
			t.Errorf("the map presents prose as a declaration (%q) -- it is describing a .md file "+
				"as if it declared code, which is exactly the invention the map exists to prevent:\n%s",
				invented, out)
		}
	}
	// ANTI-VACUITY: a map that extracted nothing at all would pass the loop
	// above while proving nothing.
	if !strings.Contains(out, "func RealDeclaration") {
		t.Fatalf("the map named no real declaration, so the assertions above are vacuous:\n%s", out)
	}
	// The doc must still be LISTED. The guard suppresses its declaration line,
	// not its existence -- a file the planner cannot see is a file it can
	// invent a replacement for.
	if !strings.Contains(out, "NOTES.md") {
		t.Errorf("the doc vanished from the map entirely; only its declaration line should be suppressed:\n%s", out)
	}
}

// One line per file, so a name printed twice spends a scarce slot to say
// nothing and reads as a bug in the map rather than a fact about the file.
// Measured: scripts/agent-cost-bench.sh rendered as "... func print, func print".
func TestTheMapNeverRepeatsASymbol(t *testing.T) {
	root := t.TempDir()
	// A shell function defined twice under a guard -- the real shape this came
	// from, not a contrived one.
	if err := os.WriteFile(filepath.Join(root, "tool.sh"), []byte(
		"print() { echo \"$1\"; }\n\nif [ -t 1 ]; then\nprint() { printf '%s' \"$1\"; }\nfi\n\nrun() { print hi; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := buildRepoMap(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var f *repoFile
	for i := range m.Files {
		if m.Files[i].Path == "tool.sh" {
			f = &m.Files[i]
		}
	}
	if f == nil {
		t.Fatal("tool.sh is not in the map at all")
	}
	syms := m.symbolsFor(f)
	// ANTI-VACUITY: the dedupe is meaningless if nothing was extracted.
	if len(syms) == 0 {
		t.Fatal("no symbols extracted from tool.sh, so the dedupe assertion below proves nothing")
	}
	seen := map[string]bool{}
	for _, s := range syms {
		if seen[s] {
			t.Errorf("symbol %q appears twice in %v", s, syms)
		}
		seen[s] = true
	}
	if !slices.Contains(syms, "func print") {
		t.Errorf("dedupe dropped the symbol entirely rather than its repeat: %v", syms)
	}
}
