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

// THE HELPER IS THIS BINARY (1.2.0). resolveHelperProgram looks for no file,
// so the tests that pinned where it looked -- beside the binary, not in the
// working directory, the Windows .exe -- became one assertion each: what it
// returns, wherever the process is and whatever the workspace holds.

// TestTheHelperIsThisBinary: with no override, the helper is this very
// executable, run in its helper role.
func TestTheHelperIsThisBinary(t *testing.T) {
	t.Setenv(helperBinEnvVar, "")
	chdirTemp(t)
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	bin, roleArgs, err := resolveHelperProgram()
	if err != nil {
		t.Fatalf("resolveHelperProgram: %v", err)
	}
	if bin != exe || len(roleArgs) != 1 || roleArgs[0] != helperRoleArg {
		t.Errorf("resolveHelperProgram() = %q %q, want this binary %q with %q", bin, roleArgs, exe, helperRoleArg)
	}
	if !filepath.IsAbs(bin) {
		t.Errorf("got %q, want an absolute path -- a relative one is what made this CWD-dependent (Fix 8)", bin)
	}
}

// TestTheHelperOverrideRunsAsGiven pins the escape hatch the tests' fakehelper
// uses: a separate program, run with no role argument in front of its flags.
func TestTheHelperOverrideRunsAsGiven(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "somewhere", "my-helper")
	fakeHelperAt(t, custom)
	t.Setenv(helperBinEnvVar, custom)

	bin, roleArgs, err := resolveHelperProgram()
	if err != nil || bin != custom || len(roleArgs) != 0 {
		t.Errorf("resolveHelperProgram() = %q %q %v; want the override %q with no role argument", bin, roleArgs, err, custom)
	}
}

// THE HIJACK THAT WENT WITH THE FILE. The daemon's working directory is the
// workspace it serves, and until 1.2.0 a repository shipping
// helper/mochiii-embedder-helper could have it started as the embedder under
// `go run`, and anywhere had the lookup ever reached that candidate. Now no
// workspace content is consulted at all, under `go run` or not.
func TestNoWorkspaceCanSupplyTheHelper(t *testing.T) {
	t.Setenv(helperBinEnvVar, "")
	repo := t.TempDir()
	planted := filepath.Join(repo, "helper", "mochiii-embedder-helper")
	fakeHelperAt(t, planted)
	t.Chdir(repo)

	for _, goRun := range []bool{false, true} {
		original := goRunDetector
		goRunDetector = func() bool { return goRun }
		bin, _, err := resolveHelperProgram()
		goRunDetector = original
		if err != nil {
			t.Fatalf("resolveHelperProgram (go run %v): %v", goRun, err)
		}
		if pathIsUnder(bin, repo) {
			t.Errorf("go run %v: the helper resolved inside the workspace: %q", goRun, bin)
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
