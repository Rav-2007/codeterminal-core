//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// stateHome returns the per-user state root StateDir puts "mochiii" under:
// $XDG_STATE_HOME if set, otherwise ~/.local/state.
//
// Unchanged behaviour: the same variable, the same fallback, the same resulting
// path -- only the "mochiii" leaf moved up into StateDir, so that both branches
// can return a state ROOT and neither repeats the service name. The XDG default
// was never the bug; it is correct on this platform. Keeping it in its own file
// is what lets the Windows branch exist without touching it (see memory.go's
// StateDir for the Windows failure that split this out).
//
// Not os.TempDir() when the variable is unset, which is what the lockfile's
// runtimeDir falls back to: a lockfile is disposable and this is the user's
// conversation history, so the fallback has to be somewhere durable.
func stateHome() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state"), nil
}
