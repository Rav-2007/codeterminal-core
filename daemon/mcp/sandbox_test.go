package mcp

import (
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

	tmpDir := t.TempDir()
	cmd, args, err := WrapCommand("node", []string{"index.js"}, SandboxConfig{
		Mode:          SandboxDocker,
		WorkspaceRoot: tmpDir,
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
