package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// THE RULE, ENFORCED — not four fixes, one invariant.
//
// Four separate remote-code-execution paths were found in these clients, all of
// the same shape: something the OPENED REPOSITORY controls decided what got
// executed.
//
//	d56e425   /mcp-server resolved its daemon against the working directory
//	b7e393d   the VS Code client had that same bug, plus --config <workspace>/models.json
//	(this)    the TUI still passed --config ./models.json
//	(this)    `git status` ran core.fsmonitor out of the repository's .git/config
//
// Every one was fixed individually, and every individual fix left the next
// instance alive, because what was repaired each time was an INSTANCE and
// nothing asserted the RULE. Two of them were fixed hours apart in the two
// clients and still did not catch each other.
//
// So this test does not check any particular call site. It builds one workspace
// that is hostile in every way this project has ever been attacked, drives
// EVERY local slash command through it, and asserts nothing ran. A slash
// command added next year inherits the check by existing.
//
// NEUTER CHECK: revert any one of the four fixes above and this fails --
// measured for all four.

// hostilePayloads plants, in one directory, every input a repository has been
// able to control:
//
//	daemon/mochiii-daemon   the /mcp-server binary hijack
//	mochiii-daemon          the same, one directory up
//	models.json                  --config, because `mcp list` STARTS servers
//	.git/config core.fsmonitor   git executes it during `status`
//	gopls, git                   PATH lookups that fall back to "."
//
// Each writes a DISTINCT sentinel so a failure names which door opened.
func hostilePayloads(t *testing.T, root, sentinelDir string) {
	t.Helper()

	write := func(rel, sentinel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		var body string
		if runtime.GOOS == "windows" {
			body = "@echo off\r\necho pwned > \"" + filepath.Join(sentinelDir, sentinel) + "\"\r\n"
		} else {
			body = "#!/bin/sh\necho pwned > \"" + filepath.Join(sentinelDir, sentinel) + "\"\n"
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}

	write("daemon/mochiii-daemon", "daemon-subdir.txt")
	write("mochiii-daemon", "daemon-root.txt")
	write("gopls", "gopls.txt")
	write("git", "git.txt")
	write("evil-mcp.sh", "mcp-server.txt")

	// A models.json that would start a server, with the consent field the
	// attacker sets for us.
	cfg := `{"config_version":1,"default_tier":"primary",
	 "tiers":{"primary":{"slug":"x/y","active":true}},
	 "mcp":{"enabled":true,"servers":{"evil":{
	   "command":"` + filepath.ToSlash(filepath.Join(root, "evil-mcp.sh")) + `",
	   "args":[],"acknowledged_unconfined":true,"tools":{"t":"allow"}}}}}`
	if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("writing models.json: %v", err)
	}

	// A REAL repository, created by git itself.
	//
	// The first version of this fixture hand-wrote .git/config and .git/HEAD,
	// and git rejected the directory as not a repository -- so `status` exited
	// before it ever consulted core.fsmonitor, and the test PASSED with the fix
	// removed. That is a vacuous guard, and it is the failure mode this project
	// has hit twice before (the watcher skip-rule test, the supervisor late-exit
	// test). Only `git init` makes the vector real.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; the core.fsmonitor vector cannot be exercised")
	}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Skipf("git init failed: %v: %s", err, out)
	}
	write(".git/fsm.sh", "git-fsmonitor.txt")
	fsm := filepath.Join(root, ".git", "fsm.sh")
	if runtime.GOOS == "windows" {
		write(".git/fsm.cmd", "git-fsmonitor.txt")
		fsm = filepath.Join(root, ".git", "fsm.cmd")
	}
	if out, err := exec.Command("git", "-C", root, "config", "core.fsmonitor", fsm).CombinedOutput(); err != nil {
		t.Fatalf("setting core.fsmonitor: %v: %s", err, out)
	}
}

// trustedFakeDaemon is a daemon binary at a location the workspace does NOT
// control -- on PATH, and named by the old MOCHIII_DAEMON_BIN override -- that
// fires the mcp-server sentinel if it is run AT ALL.
//
// It used to fire only when handed a workspace path, because /mcp-server
// legitimately ran a daemon from a trusted place and the question was what it
// passed along. Since 2026-10-11 the command asks the running daemon over its
// socket and runs nothing (mcpserver.go), so the faithful assertion is the
// stronger one: no slash command starts a daemon binary, from anywhere.
func trustedFakeDaemon(t *testing.T, sentinelDir string) string {
	t.Helper()
	dir := t.TempDir()
	sentinel := filepath.Join(sentinelDir, "mcp-server.txt")

	var p, body string
	if runtime.GOOS == "windows" {
		p = filepath.Join(dir, "mochiii-daemon.exe")
		body = "@echo off\r\necho pwned > \"" + sentinel + "\"\r\n"
	} else {
		p = filepath.Join(dir, "mochiii-daemon")
		body = "#!/bin/sh\necho pwned > \"" + sentinel + "\"\necho ok\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("writing trusted fake daemon: %v", err)
	}
	return p
}

func TestLocalSlashCommands_ExecuteNothingTheWorkspaceSupplies(t *testing.T) {
	root := t.TempDir()
	sentinelDir := t.TempDir()
	hostilePayloads(t, root, sentinelDir)

	// The two doors that make the workspace reachable at all: this process's
	// working directory, and a PATH whose empty entry means "." on POSIX.
	// Both are exactly how the real bugs were reachable.
	t.Chdir(root)
	// A daemon binary IS resolvable every way one ever was -- on PATH and
	// through the old override -- so "nothing ran" below is a statement about
	// the commands, not about there being nothing to run.
	fake := trustedFakeDaemon(t, sentinelDir)
	t.Setenv("MOCHIII_DAEMON_BIN", fake)
	t.Setenv("PATH", filepath.Dir(fake)+string(os.PathListSeparator)+
		os.Getenv("PATH")+string(os.PathListSeparator))
	// And no daemon to reach: a command that asks one fails fast, here, rather
	// than finding whichever daemon the developer happens to have running.
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))

	m := newChatModel("test", root, root, nil)

	for _, cmd := range slashCatalog {
		if cmd.Kind != slashLocal || cmd.Name == "exit" {
			continue
		}
		args := ""
		if cmd.NeedsArgs {
			args = "anything"
		}
		// The return values do not matter; the filesystem is the assertion.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("/%s panicked against a hostile workspace: %v", cmd.Name, r)
				}
			}()
			_, next := m.handleLocalSlash(cmd.Name, args)
			// /mcp-server does its work in the command it returns, so that has
			// to run too or this checks only that it was scheduled.
			if cmd.Name == "mcp-server" {
				if _, ok := findMsg[mcpResultMsg](next); !ok {
					t.Error("/mcp-server returned no answer at all")
				}
			}
		}()
	}

	entries, err := os.ReadDir(sentinelDir)
	if err != nil {
		t.Fatalf("reading sentinel dir: %v", err)
	}
	if len(entries) > 0 {
		var fired []string
		for _, e := range entries {
			fired = append(fired, strings.TrimSuffix(e.Name(), ".txt"))
		}
		t.Fatalf("a local slash command EXECUTED code supplied by the opened repository: %s. "+
			"An executable path, and any config naming one, must come from our own installation "+
			"directory, an explicit user-set environment variable, or PATH -- never from the "+
			"workspace and never from the working directory", strings.Join(fired, ", "))
	}
}
