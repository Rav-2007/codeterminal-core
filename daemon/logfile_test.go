package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A -log-file whose directory does not exist yet must still produce a log.
//
// Not hypothetical: the VS Code extension now passes -log-file unconditionally,
// pointing at <workspace>/.codeterminal/logs/daemon.log, and on a workspace that
// has never been indexed that directory does not exist. openRotatingFile
// returned ENOENT, newLogWriter fell back to stderr, and a detached daemon's
// stderr goes nowhere -- so the one window that most needs a log (an ADOPTING
// one, which has no pipe to the daemon at all) got nothing.
//
// Neuter: drop the MkdirAll in openRotatingFile and this fails with ENOENT.
func TestOpenRotatingFile_CreatesItsParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".codeterminal", "logs", "daemon.log")

	rf, err := openRotatingFile(path, logFileMaxBytes)
	if err != nil {
		t.Fatalf("openRotatingFile with a missing parent: %v", err)
	}
	defer rf.Close()

	if _, err := rf.Write([]byte("hello\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("log contains %q, want %q", got, "hello\n")
	}

	// The directory holds a log carrying workspace paths and prompt sizes, and
	// the file mode already says 0600; the directory must not be looser.
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("log directory is %04o, want owner-only; it sits beside the warn sink "+
			"and the tool audit log, which are both 0700", perm)
	}
}
