package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RUN FROM A SUBFOLDER, NOT THROUGH cd (execdir.go, 2026-10-26). The model's
// `cd gson && mvn test` was refused only as a program off the list, and it went
// to run_command -- which cannot see its edits -- for the rest of the task.

func TestExecDirStaysInsideTheProject(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// An etc of the project's own, so "/etc" could not be refused merely for
	// not existing: an absolute dir is refused as one.
	for _, d := range []string{filepath.Join("gson", "src"), "etc"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "pom.xml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dir, want string
	}{
		{"", root},
		{".", root},
		{"gson", filepath.Join(root, "gson")},
		{"gson/src/..", filepath.Join(root, "gson")},
		{"..", ""},
		{"../x", ""},
		{"gson/../../x", ""},
		{"/etc", ""},
		{"missing", ""},
		{"pom.xml", ""},   // a file, not a folder
		{"elsewhere", ""}, // a link out of the project
	} {
		got, why := execDirIn(root, tc.dir)
		if tc.want != "" && (why != "" || got != tc.want) {
			t.Errorf("execDirIn(%q) = %q, %q; want %q", tc.dir, got, why, tc.want)
		}
		if tc.want == "" && why == "" {
			t.Errorf("execDirIn(%q) = %q, not refused", tc.dir, got)
		}
	}
}

// Shell syntax is refused by name, with the way to say it -- not run with the
// syntax handed to the program as arguments, and not refused as "cd is not one
// of go, npm, make or cargo".
func TestShellSyntaxIsRefusedByName(t *testing.T) {
	s := execToolFixture(t)
	for _, tc := range []struct{ command, want string }{
		{"cd gson", `"dir"`},
		{"make && make test", `"&&"`},
		{"make test > out.txt", `">"`},
		{"make test | tail", `"|"`},
		{"make test ; make lint", `";"`},
		{"make test 2>/dev/null", `"2>/dev/null"`},
		{"make test >out.txt", `">out.txt"`},
		{"cat <<EOF", "here-document"},
		{"go test $(go list ./...)", `"$(go"`},
		{"go test `go list`", "is not replaced"},
		{"CTEST_OUTPUT_ON_FAILURE=1 ctest", "sets no variables"},
		{"make test\nmake lint", "line break"},
		{`mvn test -Dtest="A,B`, "never closed"},
	} {
		out := runExecTool(t, s, tc.command)
		if !strings.Contains(out, "not a shell") || !strings.Contains(out, tc.want) {
			t.Errorf("sandbox_exec(%q) said %q; want it refused by name, naming %s", tc.command, out, tc.want)
		}
	}
	// A word that only CONTAINS such a character is an argument, not syntax.
	if why := s.execCommandRefusal(mustExecWords(t, "go test -run A|B ./...")); why != "" {
		t.Errorf("a -run pattern with | in it was refused: %q", why)
	}
	// And a program off the list is still refused as one.
	if out := runExecTool(t, s, "curl https://example.com"); !strings.Contains(out, "not one of") {
		t.Errorf("a program off the list was not refused as one: %q", out)
	}
}

// dir is where the command really runs, inside the sandbox; one outside the
// project is refused before anything runs.
func TestSandboxExecRunsInTheFolderItIsGiven(t *testing.T) {
	requireWorkingSandbox(t)
	s := execToolFixture(t)
	module := filepath.Join(s.workspace, "module")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	makefile := "where:\n\t@echo RAN-IN-$$(basename $$(pwd))\n"
	if err := os.WriteFile(filepath.Join(module, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args map[string]string) string {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.builtinSandboxExec(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.Content
	}
	if out := run(map[string]string{"command": "make where", "dir": "module"}); !strings.Contains(out, "RAN-IN-module") {
		t.Errorf("the command did not run in module/: %s", out)
	}
	if out := run(map[string]string{"command": "make where", "dir": "../"}); !strings.Contains(out, "inside the project") {
		t.Errorf("a dir outside the project was not refused: %s", out)
	}
}

// QUOTES KEEP A WORD WHOLE, and nothing else happens to it (execWords). The
// fixed agent's jsoup trial spent about twenty calls on `-Dtest="A,B"`, whose
// quotes reached Maven as part of the pattern.
func TestExecWordsKeepQuotedWordsWhole(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{`mvn test -Dtest="JsoupTest,DocumentTest" -q`, []string{"mvn", "test", "-Dtest=JsoupTest,DocumentTest", "-q"}},
		{`python -m pytest -k 'numeric and reversed'`, []string{"python", "-m", "pytest", "-k", "numeric and reversed"}},
		{"go\ttest  ./...", []string{"go", "test", "./..."}},
		{`go test -run "" ./...`, []string{"go", "test", "-run", "", "./..."}},
		{`echo $HOME "*.go"`, []string{"echo", "$HOME", "*.go"}}, // no variables, no globs
	} {
		got := wordTexts(mustExecWords(t, tc.command))
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("execWords(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
	// A quoted word that looks like shell syntax is the program's argument.
	s := execToolFixture(t)
	for _, command := range []string{`go test -run ">x" ./...`, `go test -run "a && b"`, `make "<<x"`} {
		if why := s.execCommandRefusal(mustExecWords(t, command)); why != "" {
			t.Errorf("a quoted argument in %q was refused as shell syntax: %q", command, why)
		}
	}
}

// The words a quoted argument becomes are what the program really receives.
func TestSandboxExecPassesAQuotedArgumentAsOneWord(t *testing.T) {
	requireWorkingSandbox(t)
	s := execToolFixture(t)
	makefile := "show:\n\t@echo [$(ARGS)]\n"
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runExecTool(t, s, `make show "ARGS=two words"`); !strings.Contains(out, "[two words]") {
		t.Errorf("the quoted argument did not reach make as one word: %s", out)
	}
}

func mustExecWords(t *testing.T, command string) []execWord {
	t.Helper()
	words, why := execWords(command)
	if why != "" {
		t.Fatalf("execWords(%q) refused: %s", command, why)
	}
	return words
}
