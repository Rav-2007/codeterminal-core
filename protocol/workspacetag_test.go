package protocol

import (
	"path/filepath"
	"strings"
	"testing"
)

// THE GOLDEN VECTORS, AND WHY THEY ARE WORTH A FILE.
//
// WorkspaceTag is computed independently in two languages: here, and in
// clients/vscode/src/daemonClient.ts. Neither calls the other, and they must
// agree BYTE FOR BYTE or the extension looks for a lockfile the daemon never
// wrote — a client that can never find its daemon, with nothing in either log
// to say why. That is a worse failure than the bug this whole mechanism fixes,
// because at least the old one connected to something.
//
// A shared constant cannot span the language boundary, so a shared VALUE does.
// The same three inputs and the same three outputs are asserted in
// clients/vscode/src/test/suite/workspacetag.test.ts. Change either derivation
// and one of the two suites goes red immediately, instead of the extension
// going quiet in a month.
//
// The values are sha256(path) hex-truncated to 16, computed independently of
// both implementations (`printf %s /tmp/x | sha256sum`) so they cannot be a
// transcription of whatever this code happens to do.
var goldenTags = []struct{ path, tag string }{
	{"/home/user/projects/alpha", "ba9a0dc4a29671d1"},
	{"/", "8a5edab282632443"},
	{"/tmp/x", "2e56aa36f538b33b"},
}

func TestWorkspaceTag_MatchesTheGoldenVectors(t *testing.T) {
	for _, g := range goldenTags {
		if got := WorkspaceTag(g.path); got != g.tag {
			t.Errorf("WorkspaceTag(%q) = %q, want %q.\n"+
				"These vectors are shared with clients/vscode/src/test/suite/workspacetag.test.ts; "+
				"if this derivation changed on purpose, change BOTH or the extension will look for "+
				"a lockfile the daemon never wrote", g.path, got, g.tag)
		}
	}
}

func TestWorkspaceTag_IsBoundedAndFilesystemSafe(t *testing.T) {
	// A real project path, far longer than any socket path budget.
	long := "/home/user/" + strings.Repeat("deeply/nested/", 20) + "project"

	tag := WorkspaceTag(long)
	if len(tag) != workspaceTagLen {
		t.Errorf("WorkspaceTag length = %d, want %d — the socket beside it shares a "+
			"103-byte sockaddr_un budget", len(tag), workspaceTagLen)
	}
	for _, r := range tag {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("WorkspaceTag(%q) contains %q, which is not hex; the tag becomes a "+
				"filename on three platforms and a pipe name on one", long, r)
		}
	}
}

// Two different workspaces must not share a daemon, which is the whole point.
func TestWorkspaceTag_DistinguishesSiblingWorkspaces(t *testing.T) {
	a, b := WorkspaceTag("/home/user/projects/alpha"), WorkspaceTag("/home/user/projects/beta")
	if a == b {
		t.Fatalf("sibling workspaces share the tag %q; they would collide exactly as the "+
			"per-user lockfile did", a)
	}
}

// An empty root reproduces the old per-user paths exactly. A client with no
// workspace to offer must still resolve, and so must a daemon that could not
// canonicalise its own root.
func TestPathsFor_EmptyRootIsTheLegacyPath(t *testing.T) {
	if got, want := LockPathFor(""), LockPath(); got != want {
		t.Errorf("LockPathFor(\"\") = %q, want the legacy %q", got, want)
	}
	if got, want := SocketPathFor(""), SocketPath(); got != want {
		t.Errorf("SocketPathFor(\"\") = %q, want the legacy %q", got, want)
	}
}

// The daemon and the client derive this independently; they must land in the
// same directory, and it must be the one SocketDir creates and locks down.
func TestPathsFor_StayInsideTheRuntimeDirectory(t *testing.T) {
	const root = "/home/user/projects/alpha"
	dir := filepath.Join(RuntimeDir(), serviceDirName)

	for _, p := range []string{LockPathFor(root), SocketPathFor(root)} {
		if filepath.Dir(p) != dir {
			t.Errorf("%q is not in %q; SocketDir is what makes that directory 0700, so a "+
				"path outside it is outside the access control too", p, dir)
		}
	}
}

// DefaultAddressFor is what the daemon binds and what a client dials, so the
// two must agree on it for the same workspace and disagree for different ones.
// Asserted here rather than only through the daemon, because a mismatch here is
// invisible until a client cannot find a daemon that is plainly running.
func TestDefaultAddressFor_MatchesTheWorkspacesOwnPaths(t *testing.T) {
	const root = "/home/user/projects/alpha"

	a := DefaultAddressFor(root)
	if a.IsZero() {
		t.Fatal("DefaultAddressFor returned the zero Address")
	}
	if a.Transport != DefaultAddress().Transport {
		t.Errorf("transport = %q, want the platform default %q; a per-workspace address must "+
			"not change WHICH transport is used", a.Transport, DefaultAddress().Transport)
	}
	if b := DefaultAddressFor("/home/user/projects/beta"); a.Address == b.Address {
		t.Errorf("two workspaces produced the same address %q; the second daemon would fail "+
			"to bind exactly as it did before", a.Address)
	}
	if empty := DefaultAddressFor(""); empty != DefaultAddress() {
		t.Errorf("DefaultAddressFor(\"\") = %v, want the legacy %v", empty, DefaultAddress())
	}
	// Same workspace, same answer -- a client derives this independently of the
	// daemon and gets no second chance.
	if again := DefaultAddressFor(root); again != a {
		t.Errorf("DefaultAddressFor is not deterministic: %v then %v", a, again)
	}
}
