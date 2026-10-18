package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseTargetPlatform maps a release target (the vsce-style names in
// scripts/release-targets.txt) to the GOOS/GOARCH its helper is built for.
var releaseTargetPlatform = map[string][2]string{
	"linux-x64":    {"linux", "amd64"},
	"linux-arm64":  {"linux", "arm64"},
	"darwin-arm64": {"darwin", "arm64"},
	"win32-x64":    {"windows", "amd64"},
	"win32-arm64":  {"windows", "arm64"},
}

// TestEveryReleaseTargetHasAPinnedONNXRuntime keeps the release's target list
// and onnxRuntimePlatforms together. A target added to the release without a
// pinned library here would ship a helper that cannot load its runtime: code
// search off on that platform, discovered only by a user.
func TestEveryReleaseTargetHasAPinnedONNXRuntime(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "scripts", "release-targets.txt"))
	if err != nil {
		t.Fatalf("reading the release target list: %v", err)
	}
	defer f.Close()

	var targets []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "signing-required:") {
			continue
		}
		targets = append(targets, strings.Fields(line)[0])
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("scripts/release-targets.txt names no targets; this check would pass by inspecting nothing")
	}

	for _, target := range targets {
		p, ok := releaseTargetPlatform[target]
		if !ok {
			t.Errorf("release target %q has no GOOS/GOARCH mapping here; add it to releaseTargetPlatform", target)
			continue
		}
		if _, err := lookupONNXRuntimePlatform(p[0], p[1]); err != nil {
			t.Errorf("release target %q (%s/%s) has no pinned onnxruntime: %v", target, p[0], p[1], err)
		}
	}
}

// TestIntelMacSaysWhyItIsNotSupported pins the one platform that is refused on
// purpose: its error must say so plainly rather than read as a bug.
func TestIntelMacSaysWhyItIsNotSupported(t *testing.T) {
	_, err := lookupONNXRuntimePlatform("darwin", "amd64")
	if err == nil || !strings.Contains(err.Error(), "Intel Mac") {
		t.Fatalf("darwin/amd64 error = %v, want it to name Intel Mac", err)
	}
}
