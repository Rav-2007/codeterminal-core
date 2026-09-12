package main

import (
	"os"
	"path/filepath"
	"testing"
)

// F-6 -- THE SIZE BOUND IS REMOVABLE BY A PERSISTENT RENAME FAILURE.
//
// rotateIfNeededLocked's comment says: "Any error leaves the current file in
// place -- worst case it grows slightly past the bound, which is still
// failure-safe." That is true of ONE transient failure and false of a standing
// one. Rotation is best-effort (`_ = os.Rename`) and append proceeds regardless,
// so every subsequent write re-stats, re-fails, and appends anyway. Growth is
// then unbounded and permanent, which is the exact condition a size bound
// exists to prevent.
//
// THE FIXTURE IS THE WHOLE ARGUMENT, so it is worth stating why it is not
// contrived. rename(2) needs write permission on the DIRECTORY; open(2) with
// O_APPEND on an existing file needs write permission on the FILE. A writable
// log inside a non-writable directory therefore fails rotation and succeeds at
// appending -- indefinitely. That is a real deployment shape (a log directory
// owned by an installer or made immutable by policy), and on Windows an open
// handle on the .1 backup produces the same asymmetry for a different reason.
//
// PORTABILITY, STATED RATHER THAN ASSUMED: this fixture relies on POSIX
// directory permissions and does not hold for root, which bypasses them. Both
// cases skip with a reason instead of passing quietly -- a skip is not a pass.
func TestJSONLSink_BoundsGrowthWhenRotationKeepsFailing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: DAC permission checks are bypassed, so a non-writable " +
			"directory cannot be constructed. This is a skip, not a pass.")
	}

	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "audit.jsonl")

	const maxBytes = 4 << 10
	sink := newJSONLSink(path, maxBytes)

	// One write so the file exists and is owned by us before the directory is
	// sealed -- O_CREATE inside a non-writable directory would fail, and that
	// would bound growth for the wrong reason.
	sink.append(map[string]string{"seed": "x"})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("seed write did not create the log: %v", err)
	}

	// Seal the directory: rename now fails, append still succeeds.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	// Confirm the fixture actually produces the asymmetry, rather than trusting
	// that it does. If rename succeeds here, the premise is wrong and the
	// result below would be meaningless.
	if err := os.Rename(path, path+".probe"); err == nil {
		_ = os.Rename(path+".probe", path)
		t.Skip("this filesystem permits rename inside a non-writable directory, so the " +
			"persistent-rotation-failure condition cannot be built here. Skip, not pass.")
	}

	payload := map[string]string{"record": string(make([]byte, 256))}
	for range 400 {
		sink.append(payload)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// The documented bound is ~2x maxBytes (active file plus one backup). A
	// generous 4x still fails decisively when nothing bounds it at all.
	if fi.Size() > 4*maxBytes {
		t.Errorf("the log grew to %d bytes against a %d-byte bound (%.1fx).\n"+
			"rotateIfNeededLocked discards its rename error and append() writes anyway, so a "+
			"STANDING rotation failure removes the bound entirely -- it does not merely push "+
			"the file 'slightly past' it. Nothing logs this, because every error on this path "+
			"is swallowed by design.", fi.Size(), maxBytes, float64(fi.Size())/float64(maxBytes))
	}

	// THE BOUND HOLDING IS NOT ENOUGH: the operator must be able to tell that
	// records are being lost. Every other error here is swallowed by design, so
	// without this the fix would trade silent unbounded growth for silent
	// unbounded data loss -- two different failures wearing one silence (M5).
	if dropped := sink.droppedForBoundCount(); dropped == 0 {
		t.Error("the log stayed bounded but droppedForBoundCount() is 0, so nothing records " +
			"that telemetry was discarded. A bound that holds invisibly is not observable.")
	}
}
