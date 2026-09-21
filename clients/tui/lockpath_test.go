package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// THE TEST THAT WOULD HAVE CAUGHT IT.
//
// The daemon's lockfile moved from per-user to per-workspace so two projects
// could both be open (protocol.WorkspaceTag, daemon/twoworkspaces_test.go).
// That change updated the daemon, the protocol and the VS Code client, and left
// this one reading protocol.LockPath(). The daemon always writes the tagged
// name, so the two could never meet: every terminal run failed with "daemon not
// found", naming a path nothing writes.
//
// Every other test in this package substitutes lockPathFunc for a fake, so the
// real derivation was the one thing never exercised -- the classic shape of a
// test suite that passes while the product cannot start. These call the REAL
// lockPathFunc and compare it against the daemon's own helper, so the two are
// pinned to each other rather than each asserted separately.
func TestTheLockPathMatchesTheDaemons(t *testing.T) {
	defer restoreRoot(setRoot(t, "/home/someone/project"))

	got := lockPathFunc()
	want := protocol.LockPathFor("/home/someone/project")
	if got != want {
		t.Fatalf("client looks for %q, daemon writes %q", got, want)
	}
	if got == protocol.LockPath() {
		t.Fatalf("client resolved the per-USER path %q, which no daemon has written since "+
			"the lockfile became per-workspace", got)
	}
}

// Two workspaces must not resolve to one lockfile -- that is the whole reason
// the tag exists, and a client that ignored it would silently be answered by
// the other project's daemon about the other project's code.
func TestTwoWorkspacesResolveToDifferentLockfiles(t *testing.T) {
	defer restoreRoot(setRoot(t, "/home/someone/alpha"))
	alpha := lockPathFunc()

	setDaemonWorkspaceRoot("/home/someone/beta")
	beta := lockPathFunc()

	if alpha == beta {
		t.Fatalf("both workspaces resolved to %q", alpha)
	}
}

// An unresolvable or absent workspace falls back to the per-user name rather
// than to a path built from an empty tag. protocol.LockPathFor owns that rule;
// this pins the client to it instead of to a copy of it.
func TestNoWorkspaceFallsBackToThePerUserName(t *testing.T) {
	defer restoreRoot(setRoot(t, ""))
	if got := lockPathFunc(); got != protocol.LockPath() {
		t.Fatalf("empty root resolved to %q, want the per-user %q", got, protocol.LockPath())
	}
}

// End to end through the real derivation: a lockfile written where the DAEMON
// would write it must be the one connectToDaemon reads. XDG_RUNTIME_DIR moves
// the whole thing into a temp directory, so nothing here touches the real one.
func TestTheClientReadsTheLockfileTheDaemonWouldWrite(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)

	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("creating workspace: %v", err)
	}
	defer restoreRoot(setRoot(t, root))

	// Written at protocol.LockPathFor -- the daemon's own helper, not the
	// client's idea of where it goes.
	daemonSide := protocol.LockPathFor(root)
	if err := os.MkdirAll(filepath.Dir(daemonSide), 0o700); err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}
	lock := protocol.LockFile{
		PID:        os.Getpid(),
		SocketPath: filepath.Join(runtime, "nothing-listens-here.sock"),
		Address: protocol.Address{
			Transport: protocol.TransportUnix,
			Address:   filepath.Join(runtime, "nothing-listens-here.sock"),
		},
	}
	blob, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshalling lockfile: %v", err)
	}
	if err := os.WriteFile(daemonSide, blob, 0o600); err != nil {
		t.Fatalf("writing lockfile: %v", err)
	}

	// The connection itself must fail -- nothing is listening on that socket --
	// but it has to fail at DIALING, which is what proves the lockfile the
	// daemon would have written is the one the client found and parsed.
	if _, err := connectToDaemon("test"); err == nil {
		t.Fatal("connected to a socket nothing is listening on")
	} else if strings.Contains(err.Error(), "daemon not found") {
		t.Fatalf("the client never found the lockfile the daemon wrote at %s: %v", daemonSide, err)
	}
}

func setRoot(t *testing.T, root string) string {
	t.Helper()
	prev := daemonWorkspaceRoot
	setDaemonWorkspaceRoot(root)
	return prev
}

func restoreRoot(prev string) { setDaemonWorkspaceRoot(prev) }
