//go:build unix

package protocol

import (
	"os"
	"testing"
)

// The environment variable runtimeDir consults on this platform. paths_test.go
// asserts the behaviour; this supplies the only thing that differs.
const runtimeDirEnvVar = "XDG_RUNTIME_DIR"

// SocketDir creates the directory OWNER-ONLY. The socket lives there and the
// socket is the daemon's entire attack surface, so 0700 is load-bearing: it is
// half of what makes "any same-uid process, and only a same-uid process" true.
//
// POSIX-ONLY, and split out rather than made cross-platform, because the
// property genuinely does not exist on Windows. There are no mode bits there:
// os.Chmod toggles a read-only ATTRIBUTE and directory access is decided by the
// ACL, so a 0700 assertion on Windows would test Go's emulation layer rather
// than the security property. The Windows half of this claim is OWNERSHIP, and
// it is asserted in socketdir_windows_test.go against the real security
// descriptor.
func TestSocketDirIsOwnerOnly(t *testing.T) {
	base := t.TempDir()
	t.Setenv(runtimeDirEnvVar, base)

	dir, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("SocketDir mode = %#o, want 0700 -- the daemon socket lives here and a "+
			"group- or world-accessible directory widens the only attack surface it has", perm)
	}
}
