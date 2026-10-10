package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a model is told when it reads a file that is not there. The old answer
// was true of one path and silent about the project; these are about the
// project.

// missingFixture is a small project with the things a wrong suggestion would
// be made of: a near name, the same name elsewhere, and names that must never
// be offered back.
func missingFixture(t *testing.T) *Server {
	t.Helper()
	s := builtinTestServer(t)
	for rel, body := range map[string]string{
		"main.go":                        "package main\n",
		"conf/settings.yml":              "a: 1\n",
		"internal/auth/login.go":         "package auth\n",
		"internal/auth/login_test.go":    "package auth\n",
		"docs/README.md":                 "# docs\n",
		".gitignore":                     "build/\n*.log\n",
		"build/settings.yaml":            "ignored: true\n",
		"debug.log":                      "ignored\n",
		".git/settings.yaml":             "protected\n",
		"node_modules/pkg/settings.yaml": "a dependency\n",
		"deploy/my_secret_settings.yaml": "refused by name\n",
		"deploy/.env":                    "KEY=1\n",
	} {
		full := filepath.Join(s.workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func readMissing(t *testing.T, s *Server, path string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"path": path})
	res, err := s.builtinReadFile(context.Background(), args)
	if err != nil {
		t.Fatalf("read_file(%q) returned a Go error: %v", path, err)
	}
	if !res.IsError {
		t.Fatalf("read_file(%q) succeeded: %q", path, res.Content)
	}
	return res.Content
}

// THE REPORTED CASE. config/settings.yaml does not exist; conf/settings.yml
// does. The answer names it, and says the search is over.
func TestAMissingFileIsAnsweredForTheWholeProject(t *testing.T) {
	s := missingFixture(t)
	got := readMissing(t, s, "config/settings.yaml")
	for _, want := range []string{
		"cannot read config/settings.yaml: no such file.",
		"No file named settings.yaml exists anywhere in this project",
		"files checked",
		"conf/settings.yml",
		"do not try more paths",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the answer does not say %q:\n%s", want, got)
		}
	}
}

// The same name in another folder is the commonest miss of all: the model had
// the name right and the folder wrong.
func TestTheSameNameElsewhereIsPointedTo(t *testing.T) {
	s := missingFixture(t)
	got := readMissing(t, s, "auth/login.go")
	if !strings.Contains(got, "A file named login.go exists at: internal/auth/login.go. Use that path.") {
		t.Errorf("the file one folder away was not pointed to:\n%s", got)
	}
	// Whatever its case: a model that wrote README.MD meant README.md.
	if got := readMissing(t, s, "README.MD"); !strings.Contains(got, "docs/README.md") {
		t.Errorf("a name that differs only in case was not pointed to:\n%s", got)
	}
}

func TestAFileThatIsNowhereIsSaidToBeNowhere(t *testing.T) {
	s := missingFixture(t)
	got := readMissing(t, s, "src/billing/invoice.rs")
	for _, want := range []string{
		"No file named invoice.rs exists anywhere in this project",
		"nothing has a similar name",
		"It does not exist: tell the user so",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the answer does not say %q:\n%s", want, got)
		}
	}
	// The number is what was looked at: the six files a listing would show.
	if !strings.Contains(got, "(6 files checked)") {
		t.Errorf("the count is not the readable files of the project:\n%s", got)
	}
}

// THE ANSWER NEVER LISTS WHAT A LISTING HIDES. Four files here are called
// settings.yaml or nearly: one git ignores, one is inside .git, one is a
// dependency, one is refused by its name. None may be offered.
func TestASuggestionIsNeverAFileTheModelCouldNotList(t *testing.T) {
	s := missingFixture(t)
	got := readMissing(t, s, "config/settings.yaml")
	for _, hidden := range []string{"build/", ".git/", "node_modules/", "my_secret_settings", "deploy/"} {
		if strings.Contains(got, hidden) {
			t.Errorf("the answer names %q, which no listing would show:\n%s", hidden, got)
		}
	}
}

// A request that itself names a protected or secret path gets the resolver's
// answer and nothing more -- "there is no .env here, but there is one in
// deploy/" would be this feature listing what the listing hides.
func TestAHiddenNameIsNotSearchedFor(t *testing.T) {
	s := missingFixture(t)
	for _, path := range []string{".env", "config/.env", ".git/config2", "keys/server_secret.go", "../outside.txt"} {
		got := readMissing(t, s, path)
		if strings.Contains(got, "exists at") || strings.Contains(got, "closest names") || strings.Contains(got, "files checked") {
			t.Errorf("read_file(%q) searched the project for a name it must not look for:\n%s", path, got)
		}
		if strings.Contains(got, "deploy") {
			t.Errorf("read_file(%q) revealed where a hidden file is:\n%s", path, got)
		}
	}
}

// Outside the project nothing is searched: an outside read is approved one
// exact path at a time, and "not there" is all there is to say about it.
func TestAPathOutsideTheProjectIsNotSearchedFor(t *testing.T) {
	s := missingFixture(t)
	if _, ok := s.missingPathAnswer(context.Background(), "read", filepath.Join(os.TempDir(), "no-such-dir-xyz", "settings.yaml"), false); ok {
		t.Error("a path outside the project was answered from the project's file list")
	}
	// And a file that EXISTS is not this function's business.
	if _, ok := s.missingPathAnswer(context.Background(), "read", "main.go", false); ok {
		t.Error("an existing file was reported missing")
	}
}

func TestAMissingFolderIsAnsweredTheSameWay(t *testing.T) {
	s := missingFixture(t)
	args, _ := json.Marshal(map[string]string{"path": "auth"})
	res, err := s.builtinListDirectory(context.Background(), args)
	if err != nil || !res.IsError {
		t.Fatalf("list_directory(auth) = %+v, %v; want a readable error", res, err)
	}
	if !strings.Contains(res.Content, "A folder named auth exists at: internal/auth. Use that path.") {
		t.Errorf("the folder one level down was not pointed to:\n%s", res.Content)
	}
	args, _ = json.Marshal(map[string]string{"path": "services"})
	res, _ = s.builtinListDirectory(context.Background(), args)
	if !strings.Contains(res.Content, "No folder named services exists anywhere in this project") {
		t.Errorf("a folder that is nowhere was not said to be nowhere:\n%s", res.Content)
	}
	if strings.Contains(res.Content, ".git") || strings.Contains(res.Content, "node_modules") || strings.Contains(res.Content, "build") {
		t.Errorf("the answer names a folder no listing would show:\n%s", res.Content)
	}
}

// A walk that stops early must not say "nowhere": it says how far it looked.
func TestAnswerStopsShortOfNowhereWhenTheWalkWasCut(t *testing.T) {
	s := missingFixture(t)
	missingScanLimit = 2
	t.Cleanup(func() { missingScanLimit = maxMissingScanFiles })

	got := readMissing(t, s, "src/billing/invoice.rs")
	if strings.Contains(got, "anywhere in this project") || strings.Contains(got, "does not exist") {
		t.Errorf("two files were looked at and the answer speaks for the whole project:\n%s", got)
	}
	if !strings.Contains(got, "in the first 2 files checked") {
		t.Errorf("the answer does not say how far it looked:\n%s", got)
	}
}

// Which names are worth offering back.
func TestWhichNamesCountAsClose(t *testing.T) {
	same, close := similarNames([]string{"a/one.go", "b/two.go"}, "x/three.go", "three.go")
	if len(same) != 0 || len(close) != 0 {
		t.Fatalf("similarNames found %v / %v for a name that is not there", same, close)
	}
	// One- and two-letter stems are in every name and say nothing.
	if _, close := similarNames([]string{"cmd/main.go", "pkg/a.go", "pkg/ab.go"}, "x/a.rs", "a.rs"); len(close) != 1 || close[0] != "pkg/a.go" {
		t.Errorf("a one-letter stem should match only its own name with another ending; got %v", close)
	}
	// The same name with another ending comes before a name that merely holds it.
	_, close = similarNames([]string{"z/app_settings_old.json", "conf/settings.yml"}, "config/settings.yaml", "settings.yaml")
	if len(close) != 2 || close[0] != "conf/settings.yml" {
		t.Errorf("order of closeness = %v; want the same name with another ending first", close)
	}
	// And never more than a handful.
	var many []string
	for _, d := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		many = append(many, d+"/settings.yaml")
	}
	if same, _ := similarNames(many, "config/settings.yaml", "settings.yaml"); len(same) != maxMissingSuggestions {
		t.Errorf("%d same-name paths offered, want %d", len(same), maxMissingSuggestions)
	}
}
