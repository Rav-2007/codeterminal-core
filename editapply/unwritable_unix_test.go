//go:build unix

package editapply

import (
	"os"
	"testing"
)

// makeDirUnwritable removes write permission from dir for the duration of the
// test, so a caller can exercise the write-failure and rollback paths.
//
// The mechanism is the seam, not the tests. Three tests here injected a write
// failure with a bare os.Chmod, which on Windows toggles a read-only ATTRIBUTE
// that does not govern directory writability at all -- so Apply succeeded and
// CI's first Windows run reported three failures whose only cause was the
// injection technique:
//
//	expected Apply to fail on an unwritable target, got nil
//
// Constraining the whole FILE to unix was the wrong fix: apply_atomic_test.go
// and createddirs_test.go are mostly platform-neutral, and tagging them would
// have deleted real Windows coverage of the rollback and manifest logic to
// silence one helper. Only the injection is POSIX-shaped, so only the injection
// is per-platform.
func makeDirUnwritable(t *testing.T, dir string) {
	t.Helper()
	// Mode bits do not constrain root, so the injection would silently fail to
	// inject and the test would report a product defect that is not there.
	// Presence is not capability; neither is a chmod that returned nil.
	if os.Geteuid() == 0 {
		t.Skip("NOT RUN: running as root, which ignores the mode bits this test uses to force a write failure")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
