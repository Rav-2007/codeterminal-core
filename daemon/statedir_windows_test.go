//go:build windows

package main

import (
	"path/filepath"
	"testing"
)

// The environment variable, and the home-relative default, that stateHome
// consults on this platform. memory_test.go asserts the behaviour; these two
// supply the only things that differ between platforms.
//
// LOCALAPPDATA, not APPDATA: statedir_windows.go deliberately uses the LOCAL
// one, because Roaming AppData syncs across machines in a domain and memory.db
// names machine-local state -- a conversation keyed by a workspace path, which
// resolves to nothing on the machine it would sync to. Naming the constant here
// means memory_test.go pins that choice on every Windows run, where before it
// asserted XDG semantics and passed by agreeing with the bug.
const stateDirEnvVar = "LOCALAPPDATA"

// ~\AppData\Local, which is what %LOCALAPPDATA% expands to.
//
// NOT {".local", "state"}, and that is the point of this line: a literal
// ".local\state" folder in the user's profile root is the 2026-10-11 failure
// itself (quoted in memory.go above StateDir), and this is what fails if anyone
// restores it.
var stateDirHomeFallback = []string{"AppData", "Local"}

// Windows-only, because the claim is Windows-only: XDG_STATE_HOME must not
// decide where state lives here. Nothing on Windows sets it by default -- which
// is exactly why the old code's FALLBACK was the whole behaviour there -- but a
// developer with a POSIX shell or a shared dotfile repo can have it set, and
// honouring it would put the transcript back on a POSIX path on a Windows disk:
// the shipped bug's exact shape, reached by a different route.
//
// The shared tests in memory_test.go cannot ask this. To them the variable's
// name is a seam, and on this platform that name is LOCALAPPDATA; this is the
// half of the behaviour that genuinely does not exist on Unix, split out the way
// protocol/paths_unix_test.go's mode assertion is.
func TestStateDir_IgnoresXDGStateHomeOnWindows(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("XDG_STATE_HOME", `C:\xdg-must-not-win`)

	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if want := filepath.Join(local, "mochiii"); got != want {
		t.Errorf("StateDir() = %q, want %q -- XDG_STATE_HOME must not divert Windows state", got, want)
	}
}
