package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	plant := func(name string) string {
		script := filepath.Join(markers, name+".sh")
		body := "#!/bin/sh\ntouch " + filepath.Join(markers, name+".ran") + "\ncat \"$1\" 2>/dev/null\n"
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
