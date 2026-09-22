//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// These tests are Linux-only for two reasons: workspaceExposesRealHome resolves
// the real home with os.UserHomeDir, which follows $HOME on unix but reads
// %USERPROFILE% on Windows (so t.Setenv("HOME", ...) would not move it there);
// and the feature -- the Landlock backend granting the home when the workspace
// is the home -- exists only on Linux. On Windows sandbox_exec resolves to
// SandboxNone, whose prompt never calls workspaceExposesRealHome.

// workspaceExposesRealHome is true exactly when confining a command to the
// workspace would still leave the real home inside it: the workspace IS the
// home, contains it, or is the filesystem root. An ordinary ~/projects/foo is
// not that -- only its own subtree is granted.
func TestWorkspaceExposesRealHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		ws   string
		want bool
	}{
		{home, true},               // the workspace IS the home
		{filepath.Dir(home), true}, // the workspace CONTAINS the home
		{"/", true},                // the filesystem root
		{filepath.Join(home, "projects", "foo"), false}, // an ordinary project under home
		{"/opt/build", false},                           // unrelated
	}
	for _, c := range cases {
		if got := workspaceExposesRealHome(c.ws); got != c.want {
			t.Errorf("workspaceExposesRealHome(%q) = %v, want %v", c.ws, got, c.want)
		}
	}

	// A workspace that is a SYMLINK to the home is exposed too: Landlock grants
	// the resolved inode, so the check must resolve the link rather than trust
	// its path. Without EvalSymlinks this returned false and the prompt would
	// have denied home access it actually grants. (F3)
	link := filepath.Join(t.TempDir(), "proj-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if !workspaceExposesRealHome(link) {
		t.Error("a workspace symlinked to the home was not detected as exposing it")
	}
}

// THE PROMPT DOES NOT DENY WHAT IT CANNOT DENY. When the workspace is the user's
// home folder, the landlock policy grants that home read+write, so the sentence
// must not claim "not your home folder" -- it must say the home is exposed. A
// workspace inside the home keeps the plain, true wording.
//
// Neuter check: drop the workspaceExposesRealHome branch in
// sandboxConfinementSentence, and the home-as-workspace case keeps claiming
// "not your home folder".
func TestThePromptIsHonestWhenTheWorkspaceIsYourHome(t *testing.T) {
	forceBwrap(t, false)
	forceLandlock(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := mcp.SandboxConfig{
		Mode: mcp.SandboxAuto, WorkspaceRoot: home, AllowNetwork: true,
		LandlockFallback: true, MemoryLimitMB: 2048, PidsLimit: 512,
	}
	if mcp.ResolveMode(cfg) != mcp.SandboxLandlock {
		t.Fatalf("test setup: cfg resolved to %v, not landlock", mcp.ResolveMode(cfg))
	}
	sentence := sandboxConfinementSentence(cfg)
	if strings.Contains(sentence, "not your home folder") {
		t.Errorf("the workspace IS the home, but the prompt still denies home access:\n%s", sentence)
	}
	if !strings.Contains(sentence, "CAN read and write your home folder") {
		t.Errorf("the prompt does not disclose that the home folder is exposed:\n%s", sentence)
	}

	projects := filepath.Join(home, "projects", "foo")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.WorkspaceRoot = projects
	if normal := sandboxConfinementSentence(cfg); !strings.Contains(normal, "not your home folder") {
		t.Errorf("an ordinary ~/projects workspace lost the accurate wording:\n%s", normal)
	}
}
