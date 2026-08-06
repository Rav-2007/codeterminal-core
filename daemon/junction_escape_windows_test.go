//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE VECTOR THE POSIX TABLE CANNOT SPELL.
//
// TestScanWorkspace_SymlinkedDirEscapeNotIndexed already proves the indexer
// will not walk through a symlinked directory, and it runs on Windows too. It
// passes there for a reason that does not generalise: os.Symlink makes a
// SYMLINK, tagged IO_REPARSE_TAG_SYMLINK, which Go reports as ModeSymlink on
// every platform.
//
// A junction is the other kind. Same effect on the filesystem -- a directory
// that is really somewhere else -- but tagged IO_REPARSE_TAG_MOUNT_POINT, which
// Go 1.23+ reports as ModeIrregular, not ModeSymlink. The walk's check looked
// only for ModeSymlink, so a junction was walked straight through.
//
// AND IT IS THE CHEAPER VECTOR OF THE TWO. Creating a symbolic link on Windows
// needs SeCreateSymbolicLinkPrivilege -- an administrator, or Developer Mode.
// Creating a junction needs nothing at all. The escape the confinement check
// missed is the one an unprivileged process can actually build.
//
// What it costs: this walk is a READ PRIMITIVE. Whatever it indexes becomes
// retrievable text, which is put into prompts and leaves the machine on the
// completion request. Walking out of the workspace is not a tidiness problem.
//
// Neuter check: change editapply.IsLinkLike back to `mode&fs.ModeSymlink != 0`
// and this fails on windows-latest while every POSIX symlink test stays green
// -- which is exactly how the gap survived.
func TestScanWorkspace_JunctionEscapeNotIndexed(t *testing.T) {
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "nested", "secret.txt"), []byte("OUTSIDE_JUNCTION_SECRET\n"), 0600); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}

	mustMakeJunction(t, filepath.Join(workspace, "escape_junction"), filepath.Join(outside, "nested"))

	res, err := ScanWorkspace(workspace)
	if err != nil {
		t.Fatalf("ScanWorkspace: %v", err)
	}

	for _, c := range res.Chunks {
		if strings.Contains(c.Content, "OUTSIDE_JUNCTION_SECRET") {
			t.Fatalf("ESCAPE: content from OUTSIDE the workspace was indexed through a junction "+
				"(chunk %s, path %s). Indexed text is retrieved into prompts and sent to the "+
				"model, so this is an exfiltration path, not a tidiness problem", c.ID, c.FilePath)
		}
	}
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1 (only main.go); the junction was walked", res.FilesScanned)
	}
	if res.Skipped[SkipSymlink] != 1 {
		t.Errorf("SkipSymlink count = %d, want 1 -- the junction must be SKIPPED and counted, "+
			"not merely absent from the results for some other reason", res.Skipped[SkipSymlink])
	}
}

// The single-file eligibility gate is the same primitive by a different door:
// reindexFile calls it directly after an apply, without going through the walk.
func TestReadEligibleFile_RefusesAJunction(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("OUTSIDE_JUNCTION_SECRET\n"), 0600); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	link := filepath.Join(workspace, "escape_junction")
	mustMakeJunction(t, link, outside)

	// The link ITSELF is what the gate is handed when a walk or a reindex names
	// it, and a junction is a directory reparse point, so this is the exact
	// entry the ModeSymlink check let through.
	content, reason, skipped, err := readEligibleFile(link, "escape_junction", newGitignoreMatcher(workspace))
	if err == nil && !skipped {
		t.Fatalf("ESCAPE: readEligibleFile accepted a junction (reason %q, %d bytes); "+
			"its content would be embedded and retrieved into a prompt", reason, len(content))
	}
	if skipped && reason != SkipSymlink {
		t.Errorf("the junction was skipped as %q, want %q -- it must be refused AS a link, "+
			"not incidentally by the size or binary sniffers", reason, SkipSymlink)
	}
}

// mustMakeJunction plants a directory junction, or skips with a stated reason.
//
// mklink is a cmd.exe BUILTIN, not an executable, so it cannot be exec'd
// directly. There is no os.Symlink equivalent for junctions and no exported Go
// API for one -- the alternative is DeviceIoControl(FSCTL_SET_REPARSE_POINT)
// with a hand-built REPARSE_DATA_BUFFER, which is a lot of unsafe code to put
// in a test whose subject is somebody else's reparse tag.
func mustMakeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("NOT RUN: could not create a junction (%v: %s); the junction vector is UNTESTED here", err, out)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat on the junction just created: %v", err)
	}
	// The premise, asserted rather than assumed: if a future Go reports
	// junctions as ModeSymlink again, this test would still pass while proving
	// something else entirely.
	if info.Mode()&os.ModeSymlink != 0 {
		t.Skipf("NOT RUN: this Go reports a junction as ModeSymlink, so it is already covered "+
			"by the POSIX symlink tests and this vector no longer exists (mode = %v)", info.Mode())
	}
}
