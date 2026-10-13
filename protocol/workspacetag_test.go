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
// The same inputs and the same outputs are asserted in
// clients/vscode/src/test/suite/workspacetag.test.ts. Change either derivation
// and one of the two suites goes red immediately, instead of the extension
// going quiet in a month.
//
// The values are sha256(path) hex-truncated to 16, computed independently of
// both implementations (`printf %s /tmp/x | sha256sum`) so they cannot be a
// transcription of whatever this code happens to do. Re-deriving a Windows row,
// the path goes in the %s ARGUMENT and never in the format string: `printf
// 'C:\temp'` prints a TAB. `C:\Users\dev\project` survives that mistake, which
// is what makes it worth a comment -- the next vector added may not.
var goldenTags = []struct{ path, tag string }{
	{"/home/user/projects/alpha", "ba9a0dc4a29671d1"},
	{"/", "8a5edab282632443"},
	{"/tmp/x", "2e56aa36f538b33b"},
	// ONE Windows folder, spelled the two ways the two languages spell it. Both
	// rows are sha256 of the UPPER-case form, which is the entire fix: the tag
	// stops depending on which caller asked. The lower-case row read
	// a1901dd0ca27e0d8 until 2026-10-11 -- see TestWorkspaceTag_IgnoresDriveLetterCase.
	{`C:\Users\dev\project`, "a1270d97448ed1e4"},
	{`c:\Users\dev\project`, "a1270d97448ed1e4"},
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

// THE REGRESSION TEST FOR A REAL OUTAGE, confirmed on a Windows machine
// 2026-10-11: the extension could never find its daemon on ANY Windows machine.
// It waited on a lockfile that was never going to appear, restarted the daemon
// forever, and said nothing in either log -- the whole product, dead on that
// platform, for every Windows user of 0.1.1.
//
// Neither side's path was wrong. VS Code's uri.fsPath through fs.realpathSync
// gave the extension a lower-case drive letter; filepath.Abs through
// filepath.EvalSymlinks gave the daemon an upper-case one. One byte of case:
//
//	c:\Users\shaik\OneDrive\Desktop\catalyst -> 03ea217e0c967379 (extension waited here)
//	C:\Users\shaik\OneDrive\Desktop\catalyst -> 3b707727f6517a4d (daemon listened here)
//
// Asserted as an identity, not against a constant, because the identity is the
// property that matters: whatever the tag turns out to be, the two spellings
// have to share it.
func TestWorkspaceTag_IgnoresDriveLetterCase(t *testing.T) {
	for _, p := range []struct{ lower, upper string }{
		{`c:\Users\dev\project`, `C:\Users\dev\project`},
		// The exact folder from the captured failure.
		{`c:\Users\shaik\OneDrive\Desktop\catalyst`, `C:\Users\shaik\OneDrive\Desktop\catalyst`},
		// Forward slashes: VS Code hands these out too, and the fix must not
		// care which separator follows the colon.
		{`d:/work/repo`, `D:/work/repo`},
	} {
		lo, up := WorkspaceTag(p.lower), WorkspaceTag(p.upper)
		if lo != up {
			t.Errorf("WorkspaceTag(%q) = %q but WorkspaceTag(%q) = %q -- one folder, two "+
				"daemons, which is the 2026-10-11 Windows outage exactly", p.lower, lo, p.upper, up)
		}
	}

	// DIRECTION, which the identity above cannot catch on its own: lower-casing
	// both spellings would pass it, and would be the wrong fix. The daemon is
	// already writing the upper-case tag, so normalising DOWN moves the daemon
	// too, onto a third tag nothing is listening on. Pinned against the tag the
	// daemon was observed listening on, so the direction cannot silently flip.
	const daemonsObservedTag = "3b707727f6517a4d" // sha256(`C:\Users\shaik\OneDrive\Desktop\catalyst`)[:16]
	if got := WorkspaceTag(`c:\Users\shaik\OneDrive\Desktop\catalyst`); got != daemonsObservedTag {
		t.Errorf("WorkspaceTag of the captured folder = %q, want the daemon's existing %q; "+
			"normalising the drive letter DOWN agrees on a tag no running daemon holds and "+
			"strands every session open across the upgrade", got, daemonsObservedTag)
	}
}

// POSIX has no drive letter, so normalizeRoot must be a no-op there -- a colon
// is a legal filename character on Linux and macOS, and eating one would
// re-tag every Unix workspace for a Windows bug. The POSIX goldenTags rows
// above are the real guard; these are the shapes that nearly look like drives.
func TestNormalizeRoot_LeavesNonDrivePathsAlone(t *testing.T) {
	for _, path := range []string{
		"/home/user/projects/alpha",
		"/",
		"",
		"/tmp/c:/odd-but-legal-on-unix",
		"c",       // too short to carry a colon
		"ab:/x",   // two letters before the colon is not a drive
		"1:/x",    // not a letter
		"C:/keep", // already upper-case: unchanged, not re-cased
	} {
		if got := normalizeRoot(path); got != path {
			t.Errorf("normalizeRoot(%q) = %q, want it untouched", path, got)
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
