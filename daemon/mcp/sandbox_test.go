package mcp

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWrapCommandNone(t *testing.T) {
	cmd, args, err := WrapCommand("node", []string{"server.js"}, SandboxConfig{Mode: SandboxNone})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "node" || !reflect.DeepEqual(args, []string{"server.js"}) {
		t.Errorf("WrapCommand none failed: got cmd=%q, args=%v", cmd, args)
	}
}

func TestWrapCommandAuto(t *testing.T) {
	// Stub lookPath to simulate no bwrap or docker
	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()
	lookPath = func(file string) (string, error) {
		return "", t.Context().Err()
	}

	cmd, args, err := WrapCommand("python3", []string{"script.py"}, SandboxConfig{Mode: SandboxAuto})
	if err != nil {
		t.Fatalf("unexpected error on auto fallback: %v", err)
	}
	if cmd != "python3" || !reflect.DeepEqual(args, []string{"script.py"}) {
		t.Errorf("expected auto fallback to host mode, got cmd=%q, args=%v", cmd, args)
	}

	// Test SandboxAuto with WorkspaceRoot provided (detects bwrap when stubbed)
	lookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	// Stub the capability probe alongside lookPath. These tests are about the
	// ARGUMENTS WrapCommand builds, which is a host-independent question; the
	// probe runs a real bwrap and would make them fail on any machine where
	// unprivileged user namespaces are restricted (Ubuntu 24.04+ by default).
	//
	// The pairing is deliberate and is the rule this campaign added: a test that
	// stubs a seam must be accompanied by one that does not. Argument
	// construction is covered here; actual execution is covered in
	// sandbox_exec_test.go, which runs bwrap for real and SKIPS with NOT RUN
	// when the host forbids it.
	origUsable := BwrapUsable
	defer func() { BwrapUsable = origUsable }()
	BwrapUsable = func() bool { return true }

	tmpDir := t.TempDir()
	cmd, _, err = WrapCommand("node", []string{"app.js"}, SandboxConfig{Mode: SandboxAuto, WorkspaceRoot: tmpDir})
	if err != nil {
		t.Fatalf("unexpected error on auto bwrap: %v", err)
	}
	if cmd != "bwrap" {
		t.Errorf("expected SandboxAuto to pick bwrap, got %q", cmd)
	}
}

func TestWrapCommandInvalidMode(t *testing.T) {
	_, _, err := WrapCommand("node", nil, SandboxConfig{Mode: SandboxMode("invalid")})
	if err == nil || !strings.Contains(err.Error(), "unknown sandbox mode") {
		t.Errorf("expected unknown sandbox mode error, got %v", err)
	}
}

func TestWrapCommandBubblewrapValidation(t *testing.T) {
	// Stub lookPath to simulate bwrap existing
	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()
	lookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}

	// Stub the capability probe alongside lookPath. These tests are about the
	// ARGUMENTS WrapCommand builds, which is a host-independent question; the
	// probe runs a real bwrap and would make them fail on any machine where
	// unprivileged user namespaces are restricted (Ubuntu 24.04+ by default).
	//
	// The pairing is deliberate and is the rule this campaign added: a test that
	// stubs a seam must be accompanied by one that does not. Argument
	// construction is covered here; actual execution is covered in
	// sandbox_exec_test.go, which runs bwrap for real and SKIPS with NOT RUN
	// when the host forbids it.
	origUsable := BwrapUsable
	defer func() { BwrapUsable = origUsable }()
	BwrapUsable = func() bool { return true }

	// Missing workspace root
	_, _, err := WrapCommand("node", []string{"server.js"}, SandboxConfig{Mode: SandboxBubblewrap})
	if err == nil {
		t.Fatal("expected error when bwrap has empty WorkspaceRoot")
	}

	tmpDir := t.TempDir()
	cmd, args, err := WrapCommand("python3", []string{"tool.py"}, SandboxConfig{
		Mode:              SandboxBubblewrap,
		WorkspaceRoot:     tmpDir,
		ReadOnlyWorkspace: true,
		AllowNetwork:      false,
	})
	if err != nil {
		t.Fatalf("bwrap wrap failed: %v", err)
	}
	if cmd != "bwrap" {
		t.Errorf("expected cmd='bwrap', got %q", cmd)
	}
	argsStr := strings.Join(args, " ")
	if !strings.Contains(argsStr, "--new-session") || !strings.Contains(argsStr, "--die-with-parent") || !strings.Contains(argsStr, "--cap-drop ALL") {
		t.Errorf("expected security flags (--new-session, --die-with-parent, --cap-drop ALL) in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--unshare-net") || !strings.Contains(argsStr, filepath.Clean(tmpDir)) {
		t.Errorf("expected --unshare-net and workspace root in args, got: %s", argsStr)
	}
}

func TestWrapCommandDockerValidation(t *testing.T) {
	// Stub lookPath and UID/GID to simulate docker existing
	origLookPath := lookPath
	origGetUID := getUID
	origGetGID := getGID
	defer func() {
		lookPath = origLookPath
		getUID = origGetUID
		getGID = origGetGID
	}()
	lookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	getUID = func() int { return 1000 }
	getGID = func() int { return 1000 }

	// Missing workspace root
	_, _, err := WrapCommand("node", []string{"server.js"}, SandboxConfig{Mode: SandboxDocker})
	if err == nil {
		t.Fatal("expected error when docker has empty WorkspaceRoot")
	}

	// An image is now required, and its absence is its own refusal: without one
	// the command name landed where docker expects an image, so
	// `docker run ... make leak` asked Docker for an image called "make". That
	// is not hypothetical -- it is what CI hit the first time a host without a
	// usable bwrap routed real traffic to this backend.
	if _, _, err := WrapCommand("node", nil, SandboxConfig{
		Mode:          SandboxDocker,
		WorkspaceRoot: t.TempDir(),
	}); err == nil {
		t.Fatal("expected an error when the docker sandbox has no Image")
	}

	tmpDir := t.TempDir()
	cmd, args, err := WrapCommand("node", []string{"index.js"}, SandboxConfig{
		Mode:          SandboxDocker,
		WorkspaceRoot: tmpDir,
		Image:         "node:20-alpine",
		AllowNetwork:  false,
		MemoryLimitMB: 512,
		CPULimit:      1.5,
	})
	if err != nil {
		t.Fatalf("docker wrap failed: %v", err)
	}
	if cmd != "docker" {
		t.Errorf("expected cmd='docker', got %q", cmd)
	}
	argsStr := strings.Join(args, " ")
	if !strings.Contains(argsStr, "--security-opt=no-new-privileges:true") || !strings.Contains(argsStr, "--pids-limit=200") || !strings.Contains(argsStr, "--memory=512m") {
		t.Errorf("expected security flags (--security-opt, --pids-limit, --memory=512m) in docker args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "--network none") || !strings.Contains(argsStr, filepath.Clean(tmpDir)) {
		t.Errorf("expected --network none and workspace root in docker args, got: %s", argsStr)
	}
	// ORDER IS THE BUG. docker run takes [flags] IMAGE COMMAND [args], so the
	// image must sit immediately before the command; anything else makes docker
	// read the command as the image name.
	if !strings.Contains(argsStr, "node:20-alpine node index.js") {
		t.Errorf("expected the image immediately before the command, got: %s", argsStr)
	}
}

func TestConnectWithSandboxError(t *testing.T) {
	ctx := t.Context()
	cfg := LaunchConfig{
		Name:    "test-sandbox-err",
		Command: "node",
		Sandbox: SandboxConfig{
			Mode: SandboxMode("invalid-mode"),
		},
	}
	_, err := Connect(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "sandbox preparation error") {
		t.Fatalf("expected sandbox preparation error, got %v", err)
	}
}

// DockerUsable is the gate that decides whether SandboxAuto may route to the
// docker backend, and it exists because routing there without an image
// generated `docker run ... make leak` -- which asks Docker for an image
// literally called "make". CI hit exactly that the first time a host with an
// unusable bwrap sent real traffic down this path.
func TestDockerUsable(t *testing.T) {
	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()

	present := func(string) (string, error) { return "/usr/bin/docker", nil }
	absent := func(string) (string, error) { return "", exec.ErrNotFound }

	for _, tc := range []struct {
		name  string
		image string
		look  func(string) (string, error)
		want  bool
	}{
		{"binary and image", "alpine:3", present, true},
		// The case that matters: a docker binary is not a usable backend. This
		// is the shape of GitHub's ubuntu-latest runners, where docker is
		// installed and no image is configured.
		{"binary but no image", "", present, false},
		{"image but no binary", "alpine:3", absent, false},
		{"neither", "", absent, false},
		{"whitespace image is no image", "   ", present, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookPath = tc.look
			if got := DockerUsable(SandboxConfig{Image: tc.image}); got != tc.want {
				t.Errorf("DockerUsable(image=%q) = %v, want %v", tc.image, got, tc.want)
			}
		})
	}
}

// SandboxAuto must not select a backend it cannot drive. With bwrap unusable
// and no image configured, the only honest answer is host mode -- not a docker
// invocation that fails at run time with a confusing error.
func TestWrapCommandAuto_SkipsDockerWithoutAnImage(t *testing.T) {
	origLookPath := lookPath
	origUsable := BwrapUsable
	defer func() { lookPath = origLookPath; BwrapUsable = origUsable }()

	// Every binary "exists"; bwrap cannot run. This is the CI host exactly.
	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	BwrapUsable = func() bool { return false }

	cmd, args, err := WrapCommand("make", []string{"leak"}, SandboxConfig{
		Mode:          SandboxAuto,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("auto should degrade to host mode, not error: %v", err)
	}
	if cmd != "make" || len(args) != 1 || args[0] != "leak" {
		t.Errorf("expected the bare command, got cmd=%q args=%v -- a docker wrapper here is the bug that asked Docker for an image named \"make\"", cmd, args)
	}

	// With an image, the same host DOES get docker.
	cmd, args, err = WrapCommand("make", []string{"leak"}, SandboxConfig{
		Mode:          SandboxAuto,
		WorkspaceRoot: t.TempDir(),
		Image:         "alpine:3",
	})
	if err != nil {
		t.Fatalf("auto with an image should select docker: %v", err)
	}
	if cmd != "docker" {
		t.Errorf("expected docker, got %q", cmd)
	}
	if joined := strings.Join(args, " "); !strings.Contains(joined, "alpine:3 make leak") {
		t.Errorf("image must sit immediately before the command, got: %s", joined)
	}
}
