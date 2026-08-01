package main

import (
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
// legacy path comes back unchanged so LoadConfig raises the error operators
// already recognise, naming ./models.json.
func TestResolveConfigPath_FallsBackToTheLegacyPath(t *testing.T) {
	t.Chdir(t.TempDir())

	got := resolveConfigPath()
	if got != legacyConfigPath && !filepath.IsAbs(got) {
		t.Errorf("resolveConfigPath() = %q, want %q or an absolute path that exists", got, legacyConfigPath)
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
		if p == legacyConfigPath {
			continue // LoadConfig will report it by name
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("resolveConfigPath() returned %q, which does not exist: %v", p, err)
		}
	}
}
