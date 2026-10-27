package mcp

import (
	"path/filepath"
	"strings"
	"testing"
)

// A BUILD IS OFTEN RUN FROM A SUBFOLDER (startdir.go). Given only cmd.Dir, bwrap
// went back to the workspace root with --chdir and the command ran there, so
// sandbox_exec's dir did nothing on the backend most Linux hosts get.
func TestWrapCommandStartsWhereItIsTold(t *testing.T) {
	origLookPath, origUsable := lookPath, BwrapUsable
	t.Cleanup(func() { lookPath, BwrapUsable = origLookPath, origUsable })
	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	BwrapUsable = func() bool { return true }

	root := t.TempDir()
	sub := filepath.Join(root, "gson")
	for _, tc := range []struct {
		name string
		cfg  SandboxConfig
		flag string
	}{
		{"bwrap", SandboxConfig{Mode: SandboxBubblewrap, WorkspaceRoot: root}, "--chdir"},
		{"docker", SandboxConfig{Mode: SandboxDocker, WorkspaceRoot: root, Image: "img"}, "-w"},
	} {
		for _, start := range []string{"", sub} {
			cfg := tc.cfg
			cfg.StartDir = start
			_, args, err := WrapCommand("mvn", []string{"test"}, cfg)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			want := filepath.Clean(root)
			if start != "" {
				want = filepath.Clean(sub)
			}
			if got := flagValue(args, tc.flag); got != want {
				t.Errorf("%s with StartDir %q starts in %q, want %q: %s", tc.name, start, got, want, strings.Join(args, " "))
			}
		}
	}
}

// flagValue is the argument after the first flag in args, or "".
func flagValue(args []string, flag string) string {
	for i, a := range args[:len(args)-1] {
		if a == flag {
			return args[i+1]
		}
	}
	return ""
}
