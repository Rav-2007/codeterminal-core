package editapply

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reported request (2026-10-15): "write a simple login page using html in
// the new_file", where ~/Desktop/new_file is an existing, empty FOLDER. The
// edit is refused as a directory -- by the ~/ path the model sent and by the
// absolute one alike -- with the next step in it and no absolute path.
//
// Neuter check: drop the folder check in PrepareEditAnywhere and the message
// names "Desktop/new_file" (a project-looking path) and the absolute path.
func TestAnEditAimedAtAFolderInTheHomeFolderSaysToNameAFileInside(t *testing.T) {
	home, project := fakeHome(t)
	if err := os.MkdirAll(filepath.Join(home, "Desktop", "new_file"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"~/Desktop/new_file", filepath.Join(home, "Desktop", "new_file")} {
		_, err := PrepareEditAnywhere(project, create(path, "<html></html>\n"))
		if err == nil {
			t.Fatalf("%s: an edit to a folder was prepared", path)
		}
		if !errors.Is(err, ErrIsDirectory) {
			t.Errorf("%s: %v is not an ErrIsDirectory", path, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "is a directory") || !strings.Contains(msg, "~/Desktop/new_file/<name>") {
			t.Errorf("%s: %q does not say it is a folder and what to send instead", path, msg)
		}
		if strings.Contains(msg, home) {
			t.Errorf("%s: %q names the absolute home path", path, msg)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "Desktop", "new_file")); len(entries) != 0 {
		t.Errorf("a refused edit wrote into the folder: %v", entries)
	}
}

// The next step works: a file inside that folder is a create, shown by its ~/
// path, and nothing is written until it is applied.
func TestAFileInsideTheFolderIsCreated(t *testing.T) {
	home, project := fakeHome(t)
	dir := filepath.Join(home, "Desktop", "new_file")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := PrepareEditAnywhere(project, create("~/Desktop/new_file/index.html", "<html></html>\n"))
	if err != nil {
		t.Fatalf("PrepareEditAnywhere: %v", err)
	}
	if !p.Creates || p.OutsideRoot != home || p.Block.FilePath != "~/Desktop/new_file/index.html" {
		t.Errorf("prepared = creates %v, root %q, path %q", p.Creates, p.OutsideRoot, p.Block.FilePath)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); !os.IsNotExist(err) {
		t.Errorf("preparing an edit wrote the file (stat: %v)", err)
	}
}

// A folder in the project is refused the same way, by its project path.
func TestAnEditAimedAtAProjectFolderSaysToNameAFileInside(t *testing.T) {
	_, project := fakeHome(t)
	if err := os.Mkdir(filepath.Join(project, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareEditAnywhere(project, create("adir", "x\n"))
	if !errors.Is(err, ErrIsDirectory) {
		t.Fatalf("err = %v, want ErrIsDirectory", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "adir/<name>") || strings.Contains(msg, project) {
		t.Errorf("%q: want the next step by project path and no absolute path", msg)
	}
}

// A path no edit may ever write is marked so, whatever form it takes.
func TestARefusedPathIsAnErrPathRefused(t *testing.T) {
	_, project := fakeHome(t)
	outside := filepath.Join(filepath.Dir(t.TempDir()), "elsewhere.txt")
	for _, path := range []string{"~/.bashrc", "~/bin/tool", "~/Desktop/app.desktop", outside} {
		_, err := PrepareEditAnywhere(project, create(path, "x\n"))
		if !errors.Is(err, ErrPathRefused) {
			t.Errorf("%s: err = %v, want ErrPathRefused", path, err)
		}
		if errors.Is(err, ErrIsDirectory) {
			t.Errorf("%s: a refused path was also called a folder", path)
		}
	}
}
