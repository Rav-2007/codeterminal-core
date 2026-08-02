//go:build unix

package protocol

import "os"

// $XDG_RUNTIME_DIR if set, otherwise the OS temp dir.
//
// The fallback is the NORMAL case on macOS, which is why ensureOwnerOnlyDir is
// not paranoia: it makes the parent /tmp, which is world-writable and shared.
func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}
