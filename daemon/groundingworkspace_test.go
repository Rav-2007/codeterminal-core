package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TWO SPELLINGS OF ONE DIRECTORY MUST NOT READ AS A MISMATCH.
//
// buildGroundingInfo compared with filepath.Clean, which is LEXICAL: it removes
// ".", ".." and duplicate separators and nothing else. A workspace reached
// through a symlink therefore compared unequal to itself, and the daemon
// reported WORKSPACE MISMATCH on every turn about the very directory it was
// serving.
//
// This became ordinary the moment a second window could ADOPT the first
// window's daemon: window A opens /tmp/proj and starts it, window B opens
// /private/tmp/proj, computes the same workspace tag (both resolve identically,
// which is why adoption works at all) and adopts it -- then sends its own
// spelling with every prompt.
//
// macOS makes it the default rather than an edge case, because /tmp and /var
// are symlinks into /private. Found by twoworkspaces_test.go on the first macOS
// CI run this repository has ever had:
//
//	window A (repo /private/var/folders/.../002) was answered by the daemon
//	serving /var/folders/.../002
//
// Those are the same directory.
//
// Neuter check: put filepath.Clean(a) != filepath.Clean(b) back in
// sameWorkspaceDir and this fails.
func TestGroundingInfo_ASymlinkedWorkspaceIsNotAMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows; NOT RUN on this platform")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "proj")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}

	info := buildGroundingInfo(retrievalOutcome{}, real, link)
	if info.WorkspaceMismatch {
		t.Errorf("the daemon grounding against %s reported a MISMATCH for a client that named "+
			"the same directory as %s. Every turn from a window that reached this workspace by "+
			"another spelling is flagged, about a daemon serving exactly the right code.",
			real, link)
	}
}

// The flag must still fire for a genuinely different workspace, or removing the
// false positive would have removed the signal with it.
func TestGroundingInfo_ADifferentWorkspaceIsStillAMismatch(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if !buildGroundingInfo(retrievalOutcome{}, a, b).WorkspaceMismatch {
		t.Errorf("two genuinely different workspaces (%s, %s) did not report a mismatch; the "+
			"comparison is now too permissive to be worth having", a, b)
	}
}

// A client path that does not exist on this machine cannot be resolved, and
// must fall back to the lexical answer rather than to a wrong one. This is the
// normal case for a remote or stale client path.
func TestGroundingInfo_AnUnresolvablePathFallsBackToLexical(t *testing.T) {
	real := t.TempDir()
	if buildGroundingInfo(retrievalOutcome{}, real, real).WorkspaceMismatch {
		t.Error("identical strings must compare equal without needing the filesystem at all")
	}
	if !buildGroundingInfo(retrievalOutcome{}, real, filepath.Join(real, "nope", "gone")).WorkspaceMismatch {
		t.Error("an unresolvable client path that is lexically different must still be a mismatch")
	}
}
