package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the before/after evidence for Fix 8, which had two halves that
// compounded into one bad experience:
//
//  1. The helper binary was a bare relative path ("helper/…"), so it resolved
//     against the CURRENT WORKING DIRECTORY. Launch the daemon from anywhere
//     other than the repo root -- an IDE extension, a service manager, a user
//     in a subdirectory -- and the embedder never started, so retrieval
//     silently switched itself off.
//  2. The client was then told "retrieval disabled (no embedder/index
//     configured for this daemon)", which blamed the index. The index was
//     fine. The real cause only ever reached stderr.
//
// So the failure was invisible in the place it mattered and misattributed in
// the place it was visible.

// chdirTemp moves the process into a fresh temp directory for the duration of
// one test, restoring the original working directory afterwards.
func chdirTemp(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

// fakeHelperAt creates an executable-looking file at path so resolution can
// find it, and returns the directory it was created in.
func fakeHelperAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestResolveHelperBinPath_HonoursExplicitOverride pins the deployment escape
// hatch: a layout none of the candidates describe can still be pointed at.
func TestResolveHelperBinPath_HonoursExplicitOverride(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "somewhere", "my-helper")
	fakeHelperAt(t, custom)
	t.Setenv(helperBinEnvVar, custom)

	got, err := resolveHelperBinPath()
	if err != nil {
		t.Fatalf("resolveHelperBinPath: %v", err)
	}
	if got != custom {
		t.Errorf("got %q, want the override %q", got, custom)
	}
}

// TestResolveHelperBinPath_DoesNotDependOnWorkingDirectory is the review's
// repro: the daemon launched from /tmp (or anywhere that is not the repo root)
// must still find its helper. The pre-fix constant was a bare relative path, so
// this could only ever succeed by accident of where the process happened to be.
func TestResolveHelperBinPath_DoesNotDependOnWorkingDirectory(t *testing.T) {
	// A helper sitting next to the daemon binary, which is where a release
	// layout puts it. os.Executable() during `go test` is the test binary, so
	// that is the "daemon binary" for resolution purposes here.
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	sibling := filepath.Join(filepath.Dir(exe), helperBinName)
	if _, err := os.Stat(sibling); err == nil {
		t.Skip("a real helper already sits beside the test binary; nothing to prove")
	}
	fakeHelperAt(t, sibling)
	t.Cleanup(func() { os.Remove(sibling) })

	// Run from a directory that is emphatically not the repo root. (os.Chdir
	// rather than t.Chdir: this module is go1.23, and the process-wide CWD is
	// restored before any other test observes it.)
	chdirTemp(t)

	got, err := resolveHelperBinPath()
	if err != nil {
		t.Fatalf("resolveHelperBinPath from a non-repo-root CWD: %v", err)
	}
	if got != sibling {
		t.Errorf("got %q, want the binary-relative %q", got, sibling)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("got %q, want an absolute path — a relative one is what made this CWD-dependent", got)
	}
}

// TestResolveHelperBinPath_MissingHelperSaysSo covers the honest-failure half:
// when the helper genuinely is not there, the error must name the helper and
// how to get one, not leave the caller to guess.
func TestResolveHelperBinPath_MissingHelperSaysSo(t *testing.T) {
	t.Setenv(helperBinEnvVar, "")
	chdirTemp(t) // no helper/ here, and none beside the test binary

	exe, _ := os.Executable()
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), helperBinName)); err == nil {
		t.Skip("a real helper sits beside the test binary; cannot exercise the missing case")
	}

	_, err := resolveHelperBinPath()
	if err == nil {
		t.Fatal("expected an error when no helper can be found")
	}
	for _, want := range []string{helperBinName, helperBinEnvVar, "go build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// --- the second half: the client-facing reason must name the real cause ---

// TestSetupRetrieval_ReportsSpecificDisabledReason walks each way retrieval can
// be unavailable and requires a DISTINCT, accurate reason. Before Fix 8 every
// one of these produced the same "no embedder/index configured" string.
func TestSetupRetrieval_ReportsSpecificDisabledReason(t *testing.T) {
	indexedWorkspace := func(t *testing.T) string {
		t.Helper()
		ws := t.TempDir()
		if err := os.MkdirAll(filepath.Join(ws, indexDirName), 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		return ws
	}

	cases := []struct {
		name      string
		workspace func(*testing.T) string
		disabled  bool
		cfg       func() *Config
		want      string
	}{
		{
			name:      "--no-context",
			workspace: func(t *testing.T) string { return t.TempDir() },
			disabled:  true,
			want:      reasonNoContextFlag,
		},
		{
			name:      "no index built yet",
			workspace: func(t *testing.T) string { return t.TempDir() },
			want:      reasonNoIndex,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseTestConfig()
			if tc.cfg != nil {
				cfg = tc.cfg()
			}
			var stopped bool
			rs := setupRetrieval(cfg, tc.workspace(t), tc.disabled, discardLogger(), fakeEmbedderFactory(&stopped))
			if rs.DisabledReason != tc.want {
				t.Errorf("DisabledReason = %q, want %q", rs.DisabledReason, tc.want)
			}
		})
	}

	// The embedder-failure case is the one the helper-path bug actually caused,
	// and the one that used to be reported as an index problem.
	t.Run("embedder cannot start", func(t *testing.T) {
		rs := setupRetrieval(baseTestConfig(), indexedWorkspace(t), false, discardLogger(),
			erroringEmbedderFactory(errors.New("simulated: helper binary not found")))
		if rs.DisabledReason != reasonEmbedderUnavailable {
			t.Errorf("DisabledReason = %q, want %q", rs.DisabledReason, reasonEmbedderUnavailable)
		}
		if strings.Contains(rs.DisabledReason, "index") && !strings.Contains(rs.DisabledReason, "embedding helper") {
			t.Errorf("DisabledReason = %q blames the index for an embedder failure", rs.DisabledReason)
		}
	})
}

// TestSetupRetrieval_ReasonsCarryNoPaths keeps Fix 8 inside the Gate-7 line:
// these strings travel to clients, so being more specific must not mean being
// more revealing.
func TestSetupRetrieval_ReasonsCarryNoPaths(t *testing.T) {
	for _, reason := range []string{
		reasonNoContextFlag, reasonDisabledInConfig, reasonWorkspaceUnresolvable,
		reasonNoIndex, reasonIndexUnreadable, reasonEmbedderUnavailable, reasonIndexModelMismatch,
	} {
		if strings.Contains(reason, "/") && !strings.Contains(reason, "and/or") {
			t.Errorf("reason %q looks like it carries a path", reason)
		}
		if reason == "" {
			t.Error("a disabled reason is empty; every cause must say something")
		}
	}
}

// TestGatherContext_SurfacesTheConfiguredReason proves the reason actually
// reaches the client-facing outcome rather than stopping at startup.
func TestGatherContext_SurfacesTheConfiguredReason(t *testing.T) {
	s := &Server{
		logger:                  discardLogger(),
		retrievalDisabledReason: reasonEmbedderUnavailable,
	}
	out := s.gatherContext(context.Background(), "anything")
	if !out.Skipped {
		t.Fatal("expected retrieval to be skipped")
	}
	if out.Reason != reasonEmbedderUnavailable {
		t.Errorf("Reason = %q, want the specific reason %q", out.Reason, reasonEmbedderUnavailable)
	}
}

// THE HIJACK VARIANT. legacyHelperBinPath resolves against the WORKING
// DIRECTORY, and the daemon's working directory is the workspace it was asked
// to serve. So a repository shipping helper/codeterminal-embedder-helper could
// have it started as this daemon's embedder — the same defect class as the
// TUI's /mcp-server hijack, narrower only because it is the last candidate and
// needs the real helper to be absent first.
//
// The candidate is now offered only under `go run`, which is what its own
// comment always said it was for. An installed binary never sees it.
//
// Neuter check: append legacyHelperBinPath unconditionally and this fails with
// the planted path returned.
func TestResolveHelperBinPath_IgnoresTheWorkingDirectoryWhenInstalled(t *testing.T) {
	t.Setenv(helperBinEnvVar, "")

	// A hostile workspace that ships something at the legacy path.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(repo, legacyHelperBinPath)
	if err := os.WriteFile(planted, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	// Stand in for an INSTALLED daemon. A test binary is itself built into the
	// temp directory, so without this the case that actually matters could
	// never run and this test would permanently skip.
	original := goRunDetector
	goRunDetector = func() bool { return false }
	defer func() { goRunDetector = original }()

	got, err := resolveHelperBinPath()
	if err == nil {
		t.Fatalf("a helper was found where none should be: %q", got)
	}
	if _, statErr := os.Stat(got); statErr == nil {
		t.Errorf("resolveHelperBinPath returned an EXISTING path from the working directory: %q", got)
	}
}

// The explicit override still wins, and is the documented escape hatch for any
// layout the candidates do not describe.
func TestResolveHelperBinPath_OverrideStillWins(t *testing.T) {
	want := filepath.Join(t.TempDir(), "my-helper")
	t.Setenv(helperBinEnvVar, want)
	got, err := resolveHelperBinPath()
	if err != nil || got != want {
		t.Errorf("resolveHelperBinPath() = %q, %v; want %q", got, err, want)
	}
}

// Under `go run`, the candidate must still be offered — the whole point is that
// the developer workflow the comment protects is untouched.
func TestResolveHelperBinPath_OffersTheWorkingDirectoryUnderGoRun(t *testing.T) {
	t.Setenv(helperBinEnvVar, "")

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(repo, legacyHelperBinPath)
	if err := os.WriteFile(planted, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	original := goRunDetector
	goRunDetector = func() bool { return true }
	defer func() { goRunDetector = original }()

	got, err := resolveHelperBinPath()
	if err != nil {
		t.Fatalf("under go run the legacy candidate must still resolve: %v", err)
	}
	if got != legacyHelperBinPath {
		t.Errorf("resolveHelperBinPath() = %q, want %q", got, legacyHelperBinPath)
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
		{"/usr/local/bin/codeterminal-daemon", "/tmp", false},
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
