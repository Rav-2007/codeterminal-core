package protocol

import (
	"os"
	"path/filepath"
	"testing"
)

// The discovery paths every client derives independently. If these drift, the
// client simply never finds the daemon.
//
// WRITTEN ONCE, RUN ON BOTH PLATFORMS, which it previously was not. This test
// hardcoded "/run/user/1000" and XDG_RUNTIME_DIR, so on Windows it asserted
// POSIX answers against a correct Windows implementation and reported four
// failures that were entirely its own:
//
//	RuntimeDir() = "C:\\Users\\runneradmin\\AppData\\Local",
//	want the XDG_RUNTIME_DIR value
//
// The fix is not a //go:build unix constraint. runtimeDir has the SAME SHAPE on
// both platforms -- an environment variable if set, the OS temp dir otherwise --
// and only the variable's name differs. So the name is the seam
// (runtimeDirEnvVar, in paths_unix_test.go and paths_windows_test.go) and every
// assertion below is shared. Constraining instead would have left the Windows
// discovery path asserting nothing at all, which is the outcome that let the
// admin-ownership outage reach a runner in the first place.
//
// t.TempDir() rather than a literal path, and filepath.Join rather than string
// concatenation, so the expectations are native on whichever platform runs them.
func TestRuntimePaths(t *testing.T) {
	base := t.TempDir()
	t.Setenv(runtimeDirEnvVar, base)

	if got := RuntimeDir(); got != base {
		t.Errorf("RuntimeDir() = %q, want the %s value %q", got, runtimeDirEnvVar, base)
	}
	if got, want := SocketPath(), filepath.Join(base, "codeterminal", "daemon.sock"); got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
	if got, want := LockPath(), filepath.Join(base, "codeterminal", "daemon.lock"); got != want {
		t.Errorf("LockPath() = %q, want %q", got, want)
	}

	// Unset falls back to the OS temp dir rather than failing.
	t.Setenv(runtimeDirEnvVar, "")
	if got := RuntimeDir(); got != os.TempDir() {
		t.Errorf("RuntimeDir() with %s unset = %q, want os.TempDir() %q", runtimeDirEnvVar, got, os.TempDir())
	}
}

// SocketDir must return the right path and be usable twice. The PERMISSIONS
// half of the old test is POSIX-shaped and lives in paths_unix_test.go; the
// Windows equivalent is ownership, asserted in socketdir_windows_test.go.
//
// Split rather than dropped: "the directory is created where clients look for
// it, and creating it again is not an error" is a claim both platforms make, and
// idempotence in particular is what a second daemon launch depends on.
func TestSocketDir_IsCreatedWhereClientsLookAndIsIdempotent(t *testing.T) {
	base := t.TempDir()
	t.Setenv(runtimeDirEnvVar, base)

	dir, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	if want := filepath.Join(base, "codeterminal"); dir != want {
		t.Errorf("SocketDir() = %q, want %q", dir, want)
	}
	if _, err := SocketDir(); err != nil {
		t.Errorf("SocketDir() on an existing directory: %v", err)
	}
}
