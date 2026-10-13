//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// stateHome returns the per-user state root StateDir puts "mochiii" under:
// %LOCALAPPDATA% if set, otherwise ~\AppData\Local.
//
// LOCAL, not Roaming. os.UserConfigDir() would give Roaming AppData, which
// syncs across machines in a domain environment -- and memory.db names
// MACHINE-LOCAL state: one conversation per workspace path, resolved on this
// machine, keyed by a tag derived from that path. Followed to another machine
// it would be a transcript keyed to folders that do not exist there, silently
// unreachable, while the two machines' copies overwrote each other in the
// profile sync. protocol/runtimedir_windows.go insists on LOCAL for the
// lockfile for the same reason, stated there as a pipe name on a DIFFERENT
// machine being worse than no lockfile at all.
//
// Not os.TempDir() when the variable is unset, which is what that lockfile's
// runtimeDir falls back to: a lockfile is disposable and this is the user's
// conversation history, so the fallback has to be somewhere durable. It spells
// out the default %LOCALAPPDATA% expands to, and fails the same way the Unix
// branch does when even the home directory cannot be resolved.
//
// Derived from the environment variable rather than os.UserCacheDir() so that a
// test can set it (statedir_windows_test.go), matching runtimedir_windows.go.
func stateHome() (string, error) {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, "AppData", "Local"), nil
}
