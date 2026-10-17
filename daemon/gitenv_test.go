package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// NO TEST HERE FINDS A REPOSITORY THROUGH THE ENVIRONMENT.
//
// Tests in this package start git in temp directories, and git obeys GIT_DIR
// and its siblings over the directory it runs in. git also EXPORTS them to every
// hook -- absolute ones, in a linked worktree -- so on 2026-10-16 the
// `go test -race` in .githooks/pre-push ran these tests against the real
// repository: core.bare=true, core.fsmonitor and user=test landed in the shared
// .git/config, and test commits landed on the branch being pushed. The hook now
// clears them itself; this is the half that holds when anything else (a
// `git rebase -x`, an alias, a CI step) runs the tests the same way.
//
// The names come from git, so the list cannot fall behind it. No git, nothing
// to clear. Mirrored in clients/tui/gitenv_test.go and editapply/gitenv_test.go:
// each module's tests run in their own process.
func init() {
	out, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		_ = os.Unsetenv(name)
	}
}

// gitEnvChild marks the child run below, so it does not start another.
const gitEnvChild = "MOCHIII_GITENV_CHILD"

// TestGitInTestsCannotReachTheRepositoryThatRanThem re-runs this package's
// git-writing tests the way a hook in a linked worktree runs them -- GIT_DIR
// exported and absolute -- and requires the repository it names to come out
// byte-identical. Delete the init above and the child writes into it.
func TestGitInTestsCannotReachTheRepositoryThatRanThem(t *testing.T) {
	checkGitEnvIsolation(t,
		"TestGitHistoryReadsLogShowDiffAndBlame",
		"TestGitHistoryCannotReachAKeyStore",
		"TestGitHubConnectChecksTheTokenThenKeepsItPrivately")
}

func checkGitEnvIsolation(t *testing.T, tests ...string) {
	if os.Getenv(gitEnvChild) != "" {
		t.Skip("this is the child run")
	}
	if runtime.GOOS == "windows" {
		t.Skip("git hooks here are bash; the re-run tests skip their git work on this platform")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sentinel := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=sentinel", "-c", "user.email=s@s", "commit", "-q", "--allow-empty", "-m", "sentinel"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", sentinel}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitDir := filepath.Join(sentinel, ".git")
	before := gitDirFiles(t, gitDir)

	child := exec.Command(os.Args[0], "-test.v", "-test.count=1", "-test.run", "^("+strings.Join(tests, "|")+")$")
	child.Env = append(os.Environ(), "GIT_DIR="+gitDir, gitEnvChild+"=1")
	out, runErr := child.CombinedOutput()

	after := gitDirFiles(t, gitDir)
	for name, was := range before {
		if now, ok := after[name]; !ok {
			t.Errorf("the child run deleted %s from the repository GIT_DIR named", name)
		} else if !bytes.Equal(was, now) && name == "config" {
			t.Errorf("the child run rewrote the config of the repository GIT_DIR named:\nwas:\n%s\nnow:\n%s", was, now)
		} else if !bytes.Equal(was, now) {
			t.Errorf("the child run changed %s in the repository GIT_DIR named", name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("the child run added %s to the repository GIT_DIR named", name)
		}
	}
	if runErr != nil {
		t.Fatalf("the child run failed: %v\n%s", runErr, out)
	}
	// A renamed test would match nothing and this would pass by running
	// nothing, so each one must be seen to pass.
	for _, name := range tests {
		if !bytes.Contains(out, []byte("--- PASS: "+name+" ")) {
			t.Errorf("%s did not pass in the child run, so it proved nothing:\n%s", name, out)
		}
	}
}

// gitDirFiles reads every file in a git directory except the object store,
// which only grows and is reached through the refs that are compared.
func gitDirFiles(t *testing.T, gitDir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(gitDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(gitDir, path)
		if d.IsDir() {
			if rel == "objects" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		files[rel] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
