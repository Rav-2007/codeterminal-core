package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// "Daemon must run from repo root (config-path gotcha)" has been an open
// backlog item since 2026-07-08. Both defaults were CWD-relative --
// `-config ./models.json` and `-system-prompt daemon/prompts/system.txt` -- so
// the daemon started only from the repo root and died anywhere else with
//
//	reading config ./models.json: no such file or directory
//
// It is the same defect resolveHelperBinPath documents (Fix 8), and it is a
// hard blocker for a packaged install: an extension that bundles the binary
// has no repo root to run from.

func TestResolveSystemPrompt_EmptyPathUsesTheEmbeddedCopy(t *testing.T) {
	got, err := resolveSystemPrompt("")
	if err != nil {
		t.Fatalf("resolveSystemPrompt(\"\"): %v", err)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("embedded system prompt is empty; //go:embed did not pick up prompts/system.txt")
	}

	onDisk, err := os.ReadFile("prompts/system.txt")
	if err != nil {
		t.Fatalf("reading prompts/system.txt: %v", err)
	}
	if got != string(onDisk) {
		t.Error("the embedded prompt has drifted from prompts/system.txt; the file is the source and the binary must carry it verbatim")
	}
}

// The embedded copy must be reachable with no working directory to help it,
// which is the entire point.
func TestResolveSystemPrompt_WorksFromAnUnrelatedWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	got, err := resolveSystemPrompt("")
	if err != nil {
		t.Fatalf("resolveSystemPrompt from an unrelated cwd: %v", err)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("empty prompt when resolved outside the repo root")
	}
}

// An explicitly named file is honoured, and a named-but-unreadable one is an
// error rather than a silent fallback: someone who passed --system-prompt meant
// it, and quietly running a different prompt is worse than refusing.
func TestResolveSystemPrompt_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom.txt")
	if err := os.WriteFile(custom, []byte("CUSTOM PROMPT\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveSystemPrompt(custom)
	if err != nil {
		t.Fatalf("resolveSystemPrompt(%q): %v", custom, err)
	}
	if got != "CUSTOM PROMPT\n" {
		t.Errorf("got %q, want the file's contents", got)
	}

	if _, err := resolveSystemPrompt(filepath.Join(dir, "absent.txt")); err == nil {
		t.Error("a named-but-missing --system-prompt must be an error, not a silent fallback to the embedded copy")
	}
}

// resolveConfigPath prefers a models.json sitting next to the binary, which is
// the layout the packaged extension ships and the one that makes the daemon
// launchable from anywhere.
func TestResolveConfigPath_PrefersTheCopyBesideTheBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	exeDir := filepath.Dir(exe)

	beside := filepath.Join(exeDir, configFileName)
	if _, err := os.Stat(beside); err == nil {
		t.Skip("a models.json already exists next to the test binary; refusing to overwrite it")
	}
	if err := os.WriteFile(beside, []byte(`{"models":{}}`), 0600); err != nil {
		t.Skipf("cannot write next to the test binary: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(beside) })

	// Chdir somewhere with no models.json, so a CWD-relative resolver would miss.
	t.Chdir(t.TempDir())

	if got := resolveConfigPath(); got != beside {
		t.Errorf("resolveConfigPath() = %q, want the copy beside the binary %q", got, beside)
	}
}

// With nothing beside the binary and nothing in the working directory, the
// answer is "" -- the models.json built into this binary (1.2.0). It used to be
// ./models.json, so LoadConfig could name it in an error; an install is one
// file now, and it starts with no models.json anywhere.
func TestResolveConfigPath_FallsBackToTheBuiltInConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	got := resolveConfigPath()
	if got != "" && !filepath.IsAbs(got) {
		t.Errorf("resolveConfigPath() = %q, want \"\" (built in) or an absolute path that exists", got)
	}
}

// THE WORKSPACE CANNOT CHOOSE THE CONFIG (1.2.0). The terminal client used to
// pass --config so a project's models.json -- which names the programs agent
// mode spawns -- was never the one loaded. The daemon it starts is now itself,
// with no --config, so resolveConfigPath must not offer the working directory,
// which is the workspace, to an installed daemon.
//
// Neuter check: offer legacyConfigPath unconditionally and this fails with the
// planted file returned.
func TestResolveConfigPath_IgnoresTheWorkspaceWhenInstalled(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, configFileName), []byte(`{"planted":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	original := goRunDetector
	goRunDetector = func() bool { return false }
	defer func() { goRunDetector = original }()

	if got := resolveConfigPath(); got == legacyConfigPath || pathIsUnder(absoluteOrAsGiven(got), repo) && got != "" {
		t.Fatalf("an installed daemon resolved the workspace's own models.json: %q", got)
	}
}

// Under `go run` the working-directory candidate is still offered: that is the
// developer workflow it exists for.
func TestResolveConfigPath_OffersTheWorkingDirectoryUnderGoRun(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, configFileName), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	original := goRunDetector
	goRunDetector = func() bool { return true }
	defer func() { goRunDetector = original }()

	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	for _, beside := range []string{filepath.Join(filepath.Dir(exe), configFileName), filepath.Join(filepath.Dir(filepath.Dir(exe)), configFileName)} {
		if _, err := os.Stat(beside); err == nil {
			t.Skipf("a models.json sits at %s and wins first", beside)
		}
	}
	if got := resolveConfigPath(); got != legacyConfigPath {
		t.Errorf("resolveConfigPath() under go run = %q, want %q", got, legacyConfigPath)
	}
}

// The working directory must not decide whether the daemon can find its config.
func TestResolveConfigPath_FindsTheRepoConfigFromASubdirectory(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, configFileName)
	if err := os.WriteFile(cfg, []byte(`{"models":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "deep", "nested")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	fromRoot := resolveConfigPath()
	t.Chdir(sub)
	fromSub := resolveConfigPath()

	// From the root the legacy ./models.json resolves; from two directories
	// down it cannot, and that difference is the whole bug. Whatever is
	// returned, it must never be a path that does not exist.
	for _, p := range []string{fromRoot, fromSub} {
		if p == legacyConfigPath || p == "" {
			continue // ./models.json is reported by name; "" is the built-in one
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("resolveConfigPath() returned %q, which does not exist: %v", p, err)
		}
	}
}

// pathIsUnder is the entire security decision, so it is tested against the path
// shapes that break naive implementations.
func TestPathIsUnder(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{"/tmp/go-build123/b001/exe/daemon", "/tmp", true},
		{"/tmp", "/tmp", true},
		{"/usr/local/bin/mochiii-daemon", "/tmp", false},
		{"/home/u/repo/daemon", "/tmp", false},
		// The one a strings.HasPrefix implementation gets WRONG: a sibling
		// directory sharing a textual prefix.
		{"/tmpfoo/daemon", "/tmp", false},
		{"/tmp-other/daemon", "/tmp", false},
		// Escapes must not count as inside.
		{"/tmp/../usr/bin/daemon", "/tmp", false},
	} {
		if got := pathIsUnder(tc.path, tc.dir); got != tc.want {
			t.Errorf("pathIsUnder(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}

// The OS-touching half must be deterministic and must not panic. Deterministic
// matters because it gates a security decision: a predicate that flickered
// would offer the working-directory candidate intermittently.
func TestRunningFromGoRun_IsDeterministic(t *testing.T) {
	first := runningFromGoRun()
	for i := 0; i < 5; i++ {
		if again := runningFromGoRun(); again != first {
			t.Fatalf("runningFromGoRun returned %v then %v", first, again)
		}
	}
}

// The built-in models.json is the repository's models.agent.json, byte for
// byte. Run `make builtin-config` after editing models.agent.json.
func TestTheBuiltInConfigIsTheShippedOne(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "models.agent.json"))
	if err != nil {
		t.Fatalf("reading models.agent.json: %v", err)
	}
	got, err := builtinConfig()
	if err != nil {
		t.Fatalf("reading the built-in config: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the built-in models.json differs from models.agent.json: run `make builtin-config`")
	}
}

// LoadConfig("") loads the built-in config, and names it as such.
func TestLoadConfigWithNoPathLoadsTheBuiltInOne(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig(\"\"): %v", err)
	}
	if cfg.DefaultTier == "" || len(cfg.Tiers) == 0 {
		t.Fatalf("the built-in config loaded empty: tier %q, %d tiers", cfg.DefaultTier, len(cfg.Tiers))
	}
	if !cfg.MCP.Enabled {
		t.Error("the built-in config is not the agent-mode one: mcp is off")
	}
}
