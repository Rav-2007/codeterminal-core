package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The TUI half of the git-config execution finding. The VS Code client's mirror
// is clients/vscode/src/test/suite/safeGit.test.ts, and both exist because the
// /mcp-server hijack survived its own fix (d56e425) by living in the client
// nobody re-checked.
//
// NEUTER CHECK: remove "core.fsmonitor=" from neutralisedGitConfig and
// TestRunGitStatus_RefusesRepositoryControlledConfig FAILS by finding the
// sentinel -- measured, not asserted.

// hostileRepo builds a git repository whose own config names a script to run.
func hostileRepo(t *testing.T, sentinel string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Skipf("git init failed: %v: %s", err, out)
	}

	var hook string
	if runtime.GOOS == "windows" {
		hook = filepath.Join(dir, ".git", "fsm.cmd")
		body := "@echo off\r\necho pwned > \"" + sentinel + "\"\r\nexit 1\r\n"
		if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
			t.Fatalf("writing hook: %v", err)
		}
	} else {
		hook = filepath.Join(dir, ".git", "fsm.sh")
		body := "#!/bin/sh\necho pwned > \"" + sentinel + "\"\nexit 1\n"
		if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
			t.Fatalf("writing hook: %v", err)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "config", "core.fsmonitor", hook).CombinedOutput(); err != nil {
		t.Fatalf("setting core.fsmonitor: %v: %s", err, out)
	}
	return dir
}

func TestRunGitStatus_RefusesRepositoryControlledConfig(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "pwned.txt")
	repo := hostileRepo(t, sentinel)

	runGitStatus(repo)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("git executed a command named by the OPENED REPOSITORY's .git/config. " +
			"/git is one slash command, and a folder that arrived as an unzipped archive " +
			"carries its own .git/config")
	}
}

// The capability must survive. A refusal that breaks /git for every real
// repository is not a fix -- this project has shipped that exact shape before,
// when macOS peer credentials refused every legitimate client.
func TestRunGitStatus_StillReportsAnOrdinaryRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Skipf("git init failed: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	got := runGitStatus(dir)
	if strings.Contains(got, "failed") {
		t.Fatalf("status failed for a legitimate repository: %s", got)
	}
	if !strings.Contains(got, "a.txt") && !strings.Contains(got, "##") {
		t.Fatalf("expected the untracked file or a branch line, got: %s", got)
	}
}

// "." is whatever directory the TUI was launched from. Reporting on it is
// reporting on a repository the user never chose.
func TestRunGitStatus_RefusesRatherThanUsingTheWorkingDirectory(t *testing.T) {
	got := runGitStatus("")
	if !strings.Contains(got, "no workspace") {
		t.Fatalf("expected a refusal, got: %s", got)
	}
}

func TestSafeGitArgs_OverridesPrecedeRepositorySelection(t *testing.T) {
	args := safeGitArgs("/some/dir", "status", "-sb")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c core.fsmonitor=") {
		t.Fatalf("core.fsmonitor not neutralised: %s", joined)
	}

	lastC, dashBigC := -1, -1
	for i, a := range args {
		if a == "-c" {
			lastC = i
		}
		if a == "-C" {
			dashBigC = i
		}
	}
	if dashBigC < lastC {
		t.Fatalf("-C at %d precedes the last -c at %d: the repository is selected "+
			"before the overrides apply", dashBigC, lastC)
	}
}
