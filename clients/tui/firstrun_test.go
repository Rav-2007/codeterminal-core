package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexableFolderNeverOffersHomeOrARoot(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	root := filepath.VolumeName(home) + string(filepath.Separator)
	for _, c := range []struct {
		dir  string
		want bool
	}{
		{project, true},
		{home, false},
		{home + string(filepath.Separator), false},
		{root, false},
	} {
		if got := indexableFolder(c.dir, home); got != c.want {
			t.Errorf("indexableFolder(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}

// Started in the home folder or at the top of the file system, the terminal
// app says what that folder is and where to start instead; anywhere else it
// says nothing. Windows matches the home folder whatever its letter case.
func TestTheAppSaysWhenItStartsOutsideAProject(t *testing.T) {
	home := t.TempDir()
	root := filepath.VolumeName(home) + string(filepath.Separator)
	for _, c := range []struct {
		dir, goos, want string
	}{
		{home, "linux", "your home folder"},
		{home + string(filepath.Separator), "linux", "your home folder"},
		{root, "linux", "the root of the file system"},
		{root, "windows", "the top of a drive"},
		{strings.ToUpper(home), "windows", "your home folder"},
		{strings.ToUpper(home), "linux", ""},
		{filepath.Join(home, "project"), "linux", ""},
	} {
		note := notAProjectNote(c.dir, home, c.goos)
		if c.want == "" {
			if note != "" {
				t.Errorf("%s on %s: said %q; a project folder needs no note", c.dir, c.goos, note)
			}
			continue
		}
		if !strings.Contains(note, c.want) || !strings.Contains(note, "cd into one") {
			t.Errorf("%s on %s: said %q, want it named as %q with where to start instead", c.dir, c.goos, note, c.want)
		}
	}
	if note := notAProjectNote(filepath.Join(home, "project"), "", "linux"); note != "" {
		t.Errorf("with no home folder known, a project got a note: %q", note)
	}
}

// The home folder is compared in the form a workspace root takes, with its
// symlinks resolved: a home reached through a link is still the home folder.
func TestTheHomeFolderIsComparedWithItsSymlinksResolved(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	t.Setenv("HOME", link)
	t.Setenv("USERPROFILE", link)
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if got := homeDir(); got != want {
		t.Errorf("homeDir() = %q, want the resolved %q", got, want)
	}
}

// An index directory is not an index until the run that built it finished and
// wrote its stamp; an interrupted run leaves the directory without one.
func TestAFolderIsIndexedOnlyWhenItsIndexFinished(t *testing.T) {
	root := t.TempDir()
	if folderIndexed(root) {
		t.Fatal("an empty folder reads as indexed")
	}
	if err := os.MkdirAll(filepath.Join(root, ".mochiii", "index"), 0o755); err != nil {
		t.Fatal(err)
	}
	if folderIndexed(root) {
		t.Error("an index directory with no stamp reads as indexed")
	}
	writeFile(t, filepath.Join(root, ".mochiii", "index", indexStampFile), "{}")
	if !folderIndexed(root) {
		t.Error("a finished index reads as not indexed")
	}
}

// fakeSetup is a folderSetup whose daemon commands are recorded, not run.
type fakeSetup struct {
	calls   []string
	cached  bool
	running bool
	stopped bool
}

func (f *fakeSetup) setup(t *testing.T, root, answer string) folderSetup {
	t.Helper()
	return folderSetup{
		root: root,
		home: filepath.Dir(root),
		in:   bufio.NewReader(strings.NewReader(answer)),
		out:  io.Discard,
		runner: func(args []string, stdout, _ io.Writer) error {
			call := strings.Join(args, " ")
			f.calls = append(f.calls, call)
			if call == "download-model --check" {
				fmt.Fprintf(stdout, "{\"cached\":%t,\"download_bytes\":43000000}\n", f.cached)
				if !f.cached {
					return fmt.Errorf("exit status 1")
				}
			}
			return nil
		},
		declined: &declinedFolders{path: filepath.Join(t.TempDir(), "no-index")},
		running:  func() bool { return f.running },
		stop:     func() error { f.stopped = true; f.running = false; return nil },
	}
}

func TestTheIndexOfferDoesWhatTheAnswerSays(t *testing.T) {
	for _, c := range []struct {
		name    string
		answer  string
		cached  bool
		running bool
		want    []string
		stopped bool
	}{
		{"enter means yes", "\n", true, false, []string{"download-model --check", "index ROOT"}, false},
		{"yes", "y\n", true, false, []string{"download-model --check", "index ROOT"}, false},
		{"no asks nothing more", "n\n", true, false, nil, false},
		{"a missing model is downloaded first", "\n", false, false, []string{"download-model --check", "download-model", "index ROOT"}, false},
		{"a running daemon is stopped before indexing", "\n", true, true, []string{"download-model --check", "index ROOT"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "project")
			f := &fakeSetup{cached: c.cached, running: c.running}
			f.setup(t, root, c.answer).offer(false)
			var want []string
			for _, w := range c.want {
				want = append(want, strings.Replace(w, "ROOT", root, 1))
			}
			if strings.Join(f.calls, " | ") != strings.Join(want, " | ") {
				t.Errorf("ran %q, want %q", f.calls, want)
			}
			if f.stopped != c.stopped {
				t.Errorf("stopped = %v, want %v", f.stopped, c.stopped)
			}
		})
	}
}

// "never" is remembered for that folder, and only that folder.
func TestNeverIsRememberedPerFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	f := &fakeSetup{cached: true}
	s := f.setup(t, root, "never\n")
	s.offer(false)
	if len(f.calls) != 0 || !s.declined.has(root) {
		t.Fatalf("after never: calls %q, remembered %v", f.calls, s.declined.has(root))
	}
	s.in = bufio.NewReader(strings.NewReader("y\n"))
	s.offer(false)
	if len(f.calls) != 0 {
		t.Errorf("asked again after never: ran %q", f.calls)
	}
	if s.declined.has(filepath.Join(filepath.Dir(root), "other")) {
		t.Error("never for one folder was taken as never for another")
	}
}

func TestNothingIsOfferedForAFinishedIndexOrTheHomeFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	writeFile(t, filepath.Join(root, ".mochiii", "index", indexStampFile), "{}")
	f := &fakeSetup{cached: true}
	f.setup(t, root, "y\n").offer(false)
	if len(f.calls) != 0 {
		t.Errorf("an indexed folder was offered again: ran %q", f.calls)
	}

	home := t.TempDir()
	g := &fakeSetup{cached: true}
	s := g.setup(t, home, "y\n")
	s.home = home
	s.offer(false)
	if len(g.calls) != 0 {
		t.Errorf("the home folder was offered: ran %q", g.calls)
	}
}

// --index rebuilds without asking, even an index that already exists.
func TestIndexFlagRebuildsWithoutAsking(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	writeFile(t, filepath.Join(root, ".mochiii", "index", indexStampFile), "{}")
	f := &fakeSetup{cached: true, running: true}
	f.setup(t, root, "").offer(true)
	if got := strings.Join(f.calls, " | "); got != "download-model --check | index "+root {
		t.Errorf("ran %q", got)
	}
	if !f.stopped {
		t.Error("the running daemon was not stopped before the rebuild")
	}
}
