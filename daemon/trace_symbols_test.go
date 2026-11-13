//go:build unix

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A DEFAULT BUILD HAS NO TRACE CODE. The first switch is the build tag: without
// `-tags trace` the recorder is not compiled and the call sites are dead code
// the compiler removes. This builds the real daemon both ways and reads each
// binary's symbol table and strings. The traced build is the control: if the
// check could not see the recorder when it is there, a pass on the default build
// would mean nothing.
func TestADefaultBuildHasNoTraceEmitter(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon twice")
	}
	defSyms, defBin := daemonSymbolsAndBytes(t, "")
	tracedSyms, tracedBin := daemonSymbolsAndBytes(t, "trace")

	// The control: the traced build has the recorder, its switch and its helpers.
	for _, want := range []string{"mochiii/daemon/trace.(*Turn).Emit", "mochiii/daemon/trace.NewTurn", "mochiii/daemon/trace.Enable", "mochiii/daemon/trace.NewTurnWithID", "main.traceKindOf", "main.traceTurnOf",
		// The terminal client is linked into this binary too (programrole.go), so its
		// recorder is part of the same question.
		"mochiii/clients/tui/tuitrace.(*Turn).Emit", "mochiii/clients/tui/tuitrace.Begin"} {
		if !strings.Contains(tracedSyms, want) {
			t.Fatalf("the traced build has no symbol %q: this test cannot see the recorder, so its result on the default build means nothing", want)
		}
	}
	if !bytes.Contains(tracedBin, []byte("MOCHIII_TRACE")) {
		t.Fatal("the traced build does not contain the MOCHIII_TRACE switch")
	}

	// The default build has none of it.
	for _, line := range strings.Split(defSyms, "\n") {
		for _, bad := range []string{"mochiii/daemon/trace", "main.trace", "tracecheck", "tuitrace", "tui.trace", "streamTrace", "dialFailure"} {
			if strings.Contains(line, bad) {
				t.Errorf("the default build has a trace symbol: %s", line)
			}
		}
	}
	for _, bad := range []string{"MOCHIII_TRACE", "trace: writing to", "trace: off", "daemon-%d.jsonl", "tui-%d", "handshake_rejected"} {
		if bytes.Contains(defBin, []byte(bad)) {
			t.Errorf("the default build contains the string %q", bad)
		}
	}
}

// daemonSymbolsAndBytes builds the daemon with tags and returns `go tool nm`'s
// listing and the binary.
func daemonSymbolsAndBytes(t *testing.T, tags string) (string, []byte) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mochiii-daemon")
	args := []string{"build", "-o", bin}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	if out, err := exec.Command("go", append(args, ".")...).CombinedOutput(); err != nil {
		t.Fatalf("go build %v: %v\n%s", args, err, out)
	}
	nm, err := exec.Command("go", "tool", "nm", bin).Output()
	if err != nil {
		t.Fatalf("go tool nm: %v", err)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	return string(nm), b
}
