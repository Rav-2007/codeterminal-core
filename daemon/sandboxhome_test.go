package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// REGISTER ITEM 37, the production half: sandbox homes are reclaimed.
//
// reclaimSandboxHomes deletes directories, so each of its rules is pinned here,
// and the dangerous ones -- what it must never delete -- are the ones with
// sentinels. Everything runs under t.TempDir(); a rule that broke would delete
// a fixture, not the developer's cache (which TestMain also redirects).

const (
	tagA   = "0123456789abcdef"
	tagB   = "fedcba9876543210"
	tagOwn = "aaaaaaaaaaaaaaaa"
)

// sandboxRoot makes a root that passes the root check.
func sandboxRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "mochiii", "sandbox-home")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// mkHome creates root/tag with one file in it, last modified at mtime.
func mkHome(t *testing.T, root, tag string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, tag)
	if err := os.MkdirAll(filepath.Join(dir, ".cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cache", "blob"), []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mkRecord writes root/tag.json naming workspace, last modified at mtime.
func mkRecord(t *testing.T, root, tag, workspace string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(root, tag+".json")
	data, _ := json.Marshal(sandboxHomeRecord{Workspace: workspace})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

var reclaimNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// The age rule. A folder with no record (every folder from before this change)
// is judged by its own mtime. Neuter check: make the age comparison never true,
// and the old folder survives.
func TestAnUnusedSandboxHomeIsReclaimedAndAFreshOneKept(t *testing.T) {
	root := sandboxRoot(t)
	old := mkHome(t, root, tagA, reclaimNow.Add(-31*24*time.Hour))
	fresh := mkHome(t, root, tagB, reclaimNow.Add(-24*time.Hour))

	rep := reclaimSandboxHomes(root, tagOwn, reclaimNow, sandboxHomeMaxAge)

	if exists(old) {
		t.Error("a folder unused for 31 days was kept")
	}
	if !exists(fresh) {
		t.Error("a folder used yesterday was reclaimed")
	}
	if rep.Reclaimed != 1 || rep.Kept != 1 || rep.Bytes < 4096 || len(rep.Errors) != 0 {
		t.Errorf("report = %+v, want 1 reclaimed (>= 4096 bytes), 1 kept, no errors", rep)
	}
}

// The project-gone rule, and its boundary. Neuter check: treat any stat error
// as "gone" and the folder whose project is merely unreadable is reclaimed.
func TestAHomeWhoseProjectIsGoneIsReclaimedEvenWhenFresh(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through permission bits, so the unreadable-project case cannot be built; NOT RUN as root")
	}
	root := sandboxRoot(t)
	recent := reclaimNow.Add(-time.Hour)

	gone := mkHome(t, root, tagA, recent)
	mkRecord(t, root, tagA, filepath.Join(t.TempDir(), "deleted-project"), recent)

	live := t.TempDir()
	kept := mkHome(t, root, tagB, recent)
	mkRecord(t, root, tagB, live, recent)

	// A project behind a directory this user cannot search: os.Stat fails with
	// EACCES, which says nothing about whether the project exists.
	locked := t.TempDir()
	if err := os.Mkdir(filepath.Join(locked, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	unreadable := mkHome(t, root, "1111111111111111", recent)
	mkRecord(t, root, "1111111111111111", filepath.Join(locked, "project"), recent)

	reclaimSandboxHomes(root, tagOwn, reclaimNow, sandboxHomeMaxAge)

	if exists(gone) || exists(gone+".json") {
		t.Error("the folder and record of a deleted project were kept")
	}
	if !exists(kept) {
		t.Error("a fresh folder whose project exists was reclaimed")
	}
	if !exists(unreadable) {
		t.Error("a folder was reclaimed because its project was UNREADABLE, not gone")
	}
}

// NEVER THROUGH A LINK. A symlink named exactly like a tag, pointing at a real
// directory holding a file that must survive -- and DUE by every other rule:
// its record names a project that no longer exists, so the only thing standing
// between it and removal is the symlink check.
//
// An earlier version of this test was vacuous, and a neuter caught it. It made
// the entry "old" through the TARGET's mtime, but the age rule reads the link's
// own mtime, which was fresh because the test had just created it. The link
// survived because it was not due, not because it was a link, so removing the
// symlink check changed nothing.
//
// Neuter check: judge candidates with os.Stat instead of Lstat, and the link is
// removed. (RemoveAll does not follow a top-level link, so the sentinel survives
// even then -- which is why the link itself is asserted too: both layers are
// checked, not just the one that held.)
func TestASymlinkNamedLikeATagIsNeverFollowedOrRemoved(t *testing.T) {
	root := sandboxRoot(t)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "do-not-delete")
	if err := os.WriteFile(sentinel, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	ancient := reclaimNow.Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(outside, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, tagA)
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	// Due by the project-gone rule, whatever any mtime says.
	mkRecord(t, root, tagA, filepath.Join(t.TempDir(), "deleted-project"), reclaimNow)

	reclaimSandboxHomes(root, tagOwn, reclaimNow, sandboxHomeMaxAge)

	if !exists(sentinel) {
		t.Fatal("a file OUTSIDE the sandbox root was deleted through a symlink")
	}
	if !exists(link) {
		t.Error("a symlink under the root was removed; links are never candidates")
	}
}

// Scope. This daemon's own folder is never reclaimed, however old; nothing not
// named exactly like a tag is touched. Neuter check: drop the keepTag test.
func TestOwnHomeAndForeignNamesAreNeverTouched(t *testing.T) {
	root := sandboxRoot(t)
	ancient := reclaimNow.Add(-365 * 24 * time.Hour)
	own := mkHome(t, root, tagOwn, ancient)

	var foreign []string
	for _, name := range []string{"notes", "ABCDEF0123456789", "0123456789abcde", "0123456789abcdef0", "0123456789abcdef.bak"} {
		foreign = append(foreign, mkHome(t, root, name, ancient))
	}

	reclaimSandboxHomes(root, tagOwn, reclaimNow, sandboxHomeMaxAge)

	if !exists(own) {
		t.Error("this daemon's own sandbox home was reclaimed")
	}
	for _, f := range foreign {
		if !exists(f) {
			t.Errorf("%s is not named like a sandbox home and was deleted", filepath.Base(f))
		}
	}
}

// THE ROOT CHECK. Pointed anywhere but a mochiii/sandbox-home directory, the
// pass refuses and deletes nothing -- a mis-resolved cache directory must not
// become a mass delete. Neuter check: drop the root check.
func TestARootThatIsNotTheSandboxRootDeletesNothing(t *testing.T) {
	ancient := reclaimNow.Add(-365 * 24 * time.Hour)
	for name, root := range map[string]string{
		"wrong name":   filepath.Join(t.TempDir(), "mochiii", "elsewhere"),
		"wrong parent": filepath.Join(t.TempDir(), "other", "sandbox-home"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			victim := mkHome(t, root, tagA, ancient)
			rep := reclaimSandboxHomes(root, tagOwn, reclaimNow, sandboxHomeMaxAge)
			if rep.Refused == "" {
				t.Errorf("the pass did not refuse root %q", root)
			}
			if !exists(victim) {
				t.Fatalf("a folder under a root that is not the sandbox root was deleted")
			}
		})
	}
	if rep := reclaimSandboxHomes("mochiii/sandbox-home", tagOwn, reclaimNow, sandboxHomeMaxAge); rep.Refused == "" {
		t.Error("a relative root was not refused")
	}
}

// Housekeeping of the pass's own debris: a folder left mid-reclaim by an
// interrupted run, and a record whose folder is already gone.
func TestInterruptedReclaimsAndOrphanRecordsAreCleared(t *testing.T) {
	root := sandboxRoot(t)
	leftover := mkHome(t, root, tagA+".reclaiming-42-1", reclaimNow)
	mkRecord(t, root, tagB, "/nonexistent", reclaimNow)

	reclaimSandboxHomes(root, tagOwn, reclaimNow, sandboxHomeMaxAge)

	if exists(leftover) {
		t.Error("a folder left by an interrupted reclaim was not finished off")
	}
	if exists(filepath.Join(root, tagB+".json")) {
		t.Error("a record with no folder was kept")
	}
}

// Nothing ever created: not an error, and nothing to report.
func TestAMissingRootIsNothingToDo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mochiii", "sandbox-home")
	rep := reclaimSandboxHomes(root, tagOwn, reclaimNow, sandboxHomeMaxAge)
	if rep.Refused != "" || len(rep.Errors) != 0 || rep.Reclaimed != 0 {
		t.Errorf("a root that does not exist yet produced %+v", rep)
	}
}

// THE RECORD IS WRITTEN BY sandbox_exec ITSELF, beside the folder rather than
// inside the HOME a sandboxed command can write to. Driven through the real
// handler: a record written by a helper nobody calls would prove nothing.
func TestSandboxExecRecordsWhoseHomeItIs(t *testing.T) {
	s := execToolFixture(t)
	home := s.sandboxExecHome()
	if home == "" {
		t.Skip("no user cache directory on this host; NOT RUN")
	}
	// SAFETY BEFORE COVERAGE. TestMain redirects XDG_CACHE_HOME, which
	// os.UserCacheDir honours on Linux and ignores on Windows and macOS; there
	// the home would be in the developer's (or the runner's) real cache. That is
	// a reason not to run, not a failure -- the rule is only that this test
	// never writes outside its own temp tree.
	if cache := os.Getenv("XDG_CACHE_HOME"); cache == "" || !strings.HasPrefix(home, cache) {
		t.Skipf("os.UserCacheDir ignores XDG_CACHE_HOME on %s, so %q is a real cache; NOT RUN", runtime.GOOS, home)
	}

	_ = runExecTool(t, s, "make leak") // whether the command runs depends on the host's sandbox

	if _, err := os.Stat(home); err != nil {
		t.Skipf("sandbox_exec did not prepare a home on this host (%v); NOT RUN", err)
	}
	ws, ok := readSandboxRecord(home + ".json")
	if !ok {
		t.Fatalf("sandbox_exec prepared %s but wrote no readable record beside it", home)
	}
	if ws != s.workspace {
		t.Errorf("the record names %q, want the workspace %q", ws, s.workspace)
	}
	if exists(filepath.Join(home, ".json")) || exists(filepath.Join(home, filepath.Base(home)+".json")) {
		t.Error("a record was written INSIDE the sandboxed HOME, where the command can rewrite it")
	}
}

// THE STARTUP PASS IS WIRED, not just written. main.go calls the Server method,
// which resolves the real root and this daemon's own tag; a function that is
// correct and a wrapper that aims it at the wrong directory would pass every
// test above. This drives the wrapper against the root sandbox_exec actually
// uses -- under the test's redirected cache, or not at all.
//
// Neuter check: have the wrapper pass anything but sandboxHomeRoot()'s root and
// the stale folder survives.
func TestTheStartupPassReclaimsUnderTheRealRoot(t *testing.T) {
	// ITS OWN CACHE, not the package's. The package-wide redirect is shared, and
	// the first run of this test found another test's leftovers in it -- a
	// sandbox home whose t.TempDir() workspace had been deleted, which the pass
	// correctly reclaimed. Right behaviour, wrong fixture for counting.
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	root, err := sandboxHomeRoot()
	if err != nil {
		t.Skipf("no user cache directory on this host (%v); NOT RUN", err)
	}
	if !strings.HasPrefix(root, cache) {
		t.Skipf("os.UserCacheDir ignores XDG_CACHE_HOME on %s, so %q is a real cache; NOT RUN", runtime.GOOS, root)
	}

	ancient := time.Now().Add(-365 * 24 * time.Hour)
	ws := t.TempDir()
	own := mkHome(t, root, protocol.WorkspaceTag(ws), ancient)
	stale := mkHome(t, root, protocol.WorkspaceTag(t.TempDir()), ancient)
	t.Cleanup(func() { _ = os.RemoveAll(own); _ = os.RemoveAll(stale) })

	var logs bytes.Buffer
	s := &Server{logger: log.New(&logs, "", 0), workspace: ws}
	s.reclaimSandboxHomes()

	if exists(stale) {
		t.Error("the startup pass left a year-old sandbox home in place")
	}
	if !exists(own) {
		t.Error("the startup pass reclaimed this daemon's own sandbox home")
	}
	if !strings.Contains(logs.String(), "sandbox-home: reclaimed 1 folder(s), 4.0 KB") {
		t.Errorf("the reclaim was not logged: %q", logs.String())
	}
}

func TestHumanBytesPicksAReadableUnit(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 512: "512 B", 4096: "4.0 KB", 3 << 20: "3.0 MB", 232 << 20: "232.0 MB", 5 << 30: "5.0 GB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
