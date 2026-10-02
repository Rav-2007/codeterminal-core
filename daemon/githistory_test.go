package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// gitRepo makes a repository with the given commits, each a map of files to
// write, and returns its root. The committer is fixed so the tests do not read
// the developer's own identity.
func gitRepo(t *testing.T, commits ...map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := realTempDir(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_AUTHOR_DATE=2026-01-02T00:00:00Z",
			"GIT_COMMITTER_DATE=2026-01-02T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	for i, files := range commits {
		for name, content := range files {
			full := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", "-A")
		git("commit", "-q", "-m", "commit "+string(rune('A'+i)))
	}
	return root
}

func gitHistory(t *testing.T, s *Server, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := s.builtinGitHistory(context.Background(), raw)
	if err != nil {
		return err.Error(), false
	}
	return res.Content, !res.IsError
}

// The four subcommands read what they say, from commits.
func TestGitHistoryReadsLogShowDiffAndBlame(t *testing.T) {
	root := gitRepo(t,
		map[string]string{"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n"},
		map[string]string{"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a - b }\n"},
	)
	s := builtinTestServer(t)
	s.workspace = root

	out, ok := gitHistory(t, s, map[string]any{"command": "log"})
	if !ok || !strings.Contains(out, "commit A") || !strings.Contains(out, "commit B") || !strings.Contains(out, "calc/calc.go") {
		t.Errorf("log = %q", out)
	}
	out, ok = gitHistory(t, s, map[string]any{"command": "show"})
	if !ok || !strings.Contains(out, "-func Add(a, b int) int { return a + b }") || !strings.Contains(out, "+func Add(a, b int) int { return a - b }") {
		t.Errorf("show HEAD does not carry the regression:\n%s", out)
	}
	out, ok = gitHistory(t, s, map[string]any{"command": "diff", "rev": "HEAD~1", "rev2": "HEAD", "path": "calc"})
	if !ok || !strings.Contains(out, "return a - b") {
		t.Errorf("diff HEAD~1 HEAD -- calc = %q", out)
	}
	out, ok = gitHistory(t, s, map[string]any{"command": "blame", "path": "calc/calc.go", "lines": "3,3"})
	if !ok || !strings.Contains(out, "return a - b") || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("blame -L 3,3 = %q", out)
	}
}

// A REVISION IS NEVER AN OPTION, A FILE, OR A RANGE IN DISGUISE.
//
// Neuter check: drop validGitRev's pattern, and "--output=..." reaches git.
func TestGitHistoryRefusesOptionsAndFilesAsRevisions(t *testing.T) {
	root := gitRepo(t, map[string]string{"a.txt": "a\n"})
	s := builtinTestServer(t)
	s.workspace = root
	for _, rev := range []string{"--output=/tmp/pwned", "-p", "HEAD:.env", "HEAD..main", "a b", "$(id)"} {
		if out, ok := gitHistory(t, s, map[string]any{"command": "show", "rev": rev}); ok {
			t.Errorf("rev %q was accepted: %q", rev, out)
		}
	}
	for _, path := range []string{"../outside", "/etc/passwd", "~/.ssh/id_rsa", ".env", ".git/config"} {
		if out, ok := gitHistory(t, s, map[string]any{"command": "log", "path": path}); ok {
			t.Errorf("path %q was accepted: %q", path, out)
		}
	}
	if _, ok := gitHistory(t, s, map[string]any{"command": "diff"}); ok {
		t.Error("diff with no revision was accepted; it would compare the working tree")
	}
}

// WHAT IT SHOWS IS WHAT read_file COULD SHOW: a commit that added a secret file
// does not hand its contents over.
//
// Neuter check: return the patch unfiltered from withholdSecretFileSections.
func TestGitHistoryWithholdsSecretFiles(t *testing.T) {
	root := gitRepo(t, map[string]string{".env": "API_TOKEN=hunter2-not-a-real-token\n", "app.go": "package app\n"})
	s := builtinTestServer(t)
	s.workspace = root
	out, ok := gitHistory(t, s, map[string]any{"command": "show"})
	if !ok {
		t.Fatalf("show failed: %s", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("a committed .env reached the output:\n%s", out)
	}
	if !strings.Contains(out, "package app") || !strings.Contains(out, "withheld") {
		t.Errorf("the ordinary file is missing or the withholding was not said:\n%s", out)
	}
}

// IT RUNS NOTHING A HOSTILE REPOSITORY PLANTS, CONFIRMED BY EXECUTION in both
// directions: each planted command DOES run under plain git (or the test would
// prove nothing), and none runs through git_history.
//
// Neuter check: drop "--no-textconv" from the show arguments, or the
// core.fsmonitor override, and the marker appears.
func TestGitHistoryRunsNothingAHostileRepositoryPlants(t *testing.T) {
	root := gitRepo(t,
		map[string]string{"a.txt": "one\n", ".gitattributes": "*.txt diff=evil\n"},
		map[string]string{"a.txt": "two\n"},
	)
	markers := realTempDir(t)
	// FORWARD SLASHES, in the script and in the config: a git config value
	// reads a backslash as an escape, so a Windows path planted there was mangled and
	// nothing could run (FOUND 2026-10-01 on the Windows runner). Git for
	// Windows runs these through its own sh, which reads C:/... paths.
	plant := func(name string) string {
		script := filepath.ToSlash(filepath.Join(markers, name+".sh"))
		body := "#!/bin/sh\ntouch " + filepath.ToSlash(filepath.Join(markers, name+".ran")) + "\ncat \"$1\" 2>/dev/null\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return script
	}
	cfg := filepath.Join(root, ".git", "config")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("[core]\n\tfsmonitor = " + plant("fsmonitor") + "\n\tpager = " + plant("pager") +
		"\n[diff]\n\texternal = " + plant("external") +
		"\n[diff \"evil\"]\n\ttextconv = " + plant("textconv") + "\n")
	_ = f.Close()
	ran := func() []string {
		var out []string
		for _, n := range []string{"fsmonitor", "pager", "external", "textconv"} {
			if _, err := os.Stat(filepath.Join(markers, n+".ran")); err == nil {
				out = append(out, n)
			}
		}
		return out
	}

	// ANTI-VACUITY: plain git runs the planted textconv and external diff.
	plain := exec.Command("git", "-C", root, "-c", "core.pager=cat", "show", "HEAD")
	plain.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	_, _ = plain.CombinedOutput()
	if got := ran(); len(got) == 0 {
		if runtime.GOOS == "windows" {
			t.Skip("NOT RUN: the planted shell scripts do not run under this platform's git, so nothing here " +
				"could show git_history stopping them; the overrides are the same command line everywhere")
		}
		t.Fatalf("the planted commands never ran under plain git; this test would prove nothing")
	}
	for _, n := range []string{"fsmonitor", "pager", "external", "textconv"} {
		_ = os.Remove(filepath.Join(markers, n+".ran"))
	}

	s := builtinTestServer(t)
	s.workspace = root
	for _, args := range []map[string]any{
		{"command": "log", "max": 5},
		{"command": "show"},
		{"command": "diff", "rev": "HEAD"},
		{"command": "diff", "rev": "HEAD~1", "rev2": "HEAD"},
		{"command": "blame", "path": "a.txt"},
	} {
		if out, ok := gitHistory(t, s, args); !ok {
			t.Errorf("%v failed: %s", args, out)
		}
	}
	if got := ran(); len(got) > 0 {
		t.Errorf("git_history ran planted commands: %v", got)
	}
}

// THE OVERRIDES ARE THE TUI'S, AND MORE. clients/tui/slash.go neutralises the
// keys `git status` executes; git_history runs log, show, diff and blame, and
// must cover at least the same keys.
func TestGitHistoryNeutralisesWhatTheTUIDoes(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "clients", "tui", "slash.go"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)var neutralisedGitConfig = \[\]string\{(.*?)\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("could not find neutralisedGitConfig in the TUI's slash.go")
	}
	tui := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(string(block[1]), -1)
	if len(tui) < 5 {
		t.Fatalf("parsed only %d keys from the TUI's list; the comparison would be vacuous", len(tui))
	}
	have := map[string]bool{}
	for _, kv := range gitHistoryNeutralised {
		have[kv] = true
	}
	for _, m := range tui {
		if !have[m[1]] {
			t.Errorf("the TUI neutralises %q and git_history does not", m[1])
		}
	}
}

// A SECRET FILE IS WITHHELD HOWEVER GIT WRITES ITS NAME, ON EITHER SIDE. FOUND
// 2026-10-01, both reproduced against a real repository before the fix: git
// quotes a path holding a non-ASCII letter ("a/conf \303\251/server.pem"),
// and that header went unrecognised; and only the a/ side was checked, so a
// commit renaming notes.txt to .env showed the new file.
//
// Neuter checks: stop unquoteGitPath unquoting a quoted side, or check only
// the first path diffSectionPaths returns.
func TestGitHistoryWithholdsQuotedAndRenamedSecrets(t *testing.T) {
	root := gitRepo(t, map[string]string{"conf é/server.pem": "MATERIAL-QUOTED-PATH\n", "notes.txt": "one\ntwo\nthree\nfour\nfive\nsix\n"})
	s := builtinTestServer(t)
	s.workspace = root
	out, ok := gitHistory(t, s, map[string]any{"command": "show"})
	if !ok || strings.Contains(out, "MATERIAL-QUOTED-PATH") || !strings.Contains(out, "withheld") {
		t.Errorf("a secret file under a quoted path was not withheld:\n%s", out)
	}

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.Remove(filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("one\ntwo\nthree\nfour\nfive\nTOKEN=RENAMED-SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "rename to a secret name")
	for _, args := range []map[string]any{{"command": "show"}, {"command": "diff", "rev": "HEAD"}} {
		out, ok := gitHistory(t, s, args)
		if !ok || strings.Contains(out, "RENAMED-SECRET") {
			t.Errorf("%v showed a file renamed TO a secret name:\n%s", args["command"], out)
		}
		if !strings.Contains(out, "notes.txt") {
			t.Errorf("%v: the commit itself should still show (its stat names the files):\n%s", args["command"], out)
		}
	}
}

// The header parser, case by case: every name a section header can carry.
func TestDiffSectionPathsReadsEveryHeaderShape(t *testing.T) {
	cases := []struct {
		line string
		want []string // all must be among the paths found
	}{
		{"diff --git a/app.go b/app.go\n", []string{"app.go"}},
		{"diff --git a/notes.txt b/.env\n", []string{"notes.txt", ".env"}},
		{"diff --git \"a/conf \\303\\251/server.pem\" \"b/conf \\303\\251/server.pem\"\n", []string{"conf é/server.pem"}},
		{"diff --git a/x.txt \"b/caf\\303\\251/.env\"\n", []string{"x.txt", "café/.env"}},
		{"diff --git a/a b/c.txt b/a b/c.txt\n", []string{"a b/c.txt"}},
		{"diff --cc \"conf \\303\\251/k.pem\"\n", []string{"conf é/k.pem"}},
		{"diff --combined main.go\n", []string{"main.go"}},
	}
	for _, c := range cases {
		got, ok := diffSectionPaths(c.line)
		if !ok {
			t.Errorf("%q was not read as a header", c.line)
			continue
		}
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%q: paths %q lack %q", c.line, got, w)
			}
		}
	}
	if _, ok := diffSectionPaths("index 1234567..89abcde 100644\n"); ok {
		t.Error("an index line was read as a header")
	}
}
