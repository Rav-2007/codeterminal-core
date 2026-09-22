//go:build linux

// Linux-only for the same reason as sandbox_landlock_test.go (which defines the
// stubBackends helper these use): the backend selection and argument assertions
// are built from unix paths that filepath rewrites on Windows.
package mcp

import (
	"os/exec"
	"strings"
	"testing"
)

// PassedOver distinguishes a backend that is simply absent from one that is
// present but unusable. The ladder-order test runs with bwrap present, so this
// stubs bwrap absent and docker present to reach the other two reasons.
func TestPassedOverNamesAbsentAndPresentBackendsDistinctly(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(file string) (string, error) {
		if file == "docker" {
			return "/usr/bin/docker", nil // present, but no image is configured
		}
		return "", exec.ErrNotFound // bwrap absent
	}
	stubBackends(t, false, false)
	cfg := SandboxConfig{Mode: SandboxAuto, WorkspaceRoot: "/work", AllowNetwork: true, LandlockFallback: true}
	joined := strings.Join(PassedOver(cfg), " | ")
	for _, want := range []string{"bwrap is not installed", "Docker has no image configured for this"} {
		if !strings.Contains(joined, want) {
			t.Errorf("PassedOver did not name %q: %s", want, joined)
		}
	}
}

// When docker is the backend chosen, PassedOver names only bwrap above it and
// stops -- it does not go on to explain docker or landlock, which were not
// passed over.
func TestPassedOverStopsAtTheChosenBackend(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "/usr/bin/x", nil } // docker present
	stubBackends(t, false, false)
	cfg := SandboxConfig{Mode: SandboxAuto, WorkspaceRoot: "/work", AllowNetwork: true, LandlockFallback: true, Image: "img"}
	if ResolveMode(cfg) != SandboxDocker {
		t.Fatalf("setup: cfg resolved to %v, not docker", ResolveMode(cfg))
	}
	if got := PassedOver(cfg); len(got) != 1 || !strings.Contains(got[0], "bwrap") {
		t.Errorf("docker chosen: PassedOver = %q, want only the bwrap reason", got)
	}
}

// WrapCommand refuses an explicitly requested backend that cannot run here,
// naming the reason rather than silently degrading to a weaker one.
func TestWrapCommandRefusesExplicitBackendsThatCannotRun(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })

	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if _, _, err := WrapCommand("go", nil, SandboxConfig{Mode: SandboxBubblewrap, WorkspaceRoot: "/work"}); err == nil {
		t.Error("bwrap requested without bwrap on PATH was not refused")
	}

	stubBackends(t, false, false)
	if _, _, err := WrapCommand("go", nil, SandboxConfig{Mode: SandboxLandlock, WorkspaceRoot: "/work"}); err == nil {
		t.Error("landlock requested on a host that cannot enforce it was not refused")
	}
	stubBackends(t, false, true)
	if _, _, err := WrapCommand("go", nil, SandboxConfig{Mode: SandboxLandlock, WorkspaceRoot: ""}); err == nil {
		t.Error("landlock with no workspace was not refused")
	}
}

// The docker backend, given a home and a pids limit, binds the home read-write
// and caps the pids -- the two branches the other docker tests skip.
func TestWrapCommandDockerBindsHomeAndCapsPids(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	cfg := SandboxConfig{Mode: SandboxDocker, WorkspaceRoot: "/work", Image: "img", HomeDir: "/home/u", PidsLimit: 128}
	bin, args, err := WrapCommand("go", []string{"build"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := bin + " " + strings.Join(args, " ")
	for _, want := range []string{"-v /home/u:/home/u", "HOME=/home/u", "--pids-limit=128"} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker args missing %q: %s", want, joined)
		}
	}
}
