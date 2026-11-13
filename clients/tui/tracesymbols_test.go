//go:build unix

package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A DEFAULT BUILD OF THE CLIENT HAS NO TRACE CODE. The first switch is the build
// tag: without `-tags trace` the recorder is not compiled and every call site is
// an empty function the compiler removes. This builds the real client both ways
// and reads each binary's symbol table and strings. The traced build is the
// control: if the check could not see the recorder when it is there, a pass on the
// default build would mean nothing.
func TestADefaultBuildHasNoTraceCode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the client twice")
	}
	defSyms, defBin := clientSymbolsAndBytes(t, "")
	tracedSyms, tracedBin := clientSymbolsAndBytes(t, "trace")

	// The control: the traced build has the recorder, its helpers and its switch.
	for _, want := range []string{"mochiii/clients/tui/tuitrace.(*Turn).Emit", "mochiii/clients/tui/tuitrace.Begin", "mochiii/clients/tui.traceCommandParsed", "mochiii/clients/tui.(*streamTrace).dialDone"} {
		if !strings.Contains(tracedSyms, want) {
			t.Fatalf("the traced build has no symbol %q: this test cannot see the recorder, so its result on the default build means nothing", want)
		}
	}
	if !bytes.Contains(tracedBin, []byte("MOCHIII_TRACE")) {
		t.Fatal("the traced build does not contain the MOCHIII_TRACE switch")
	}

	// The default build has none of it.
	for _, line := range strings.Split(defSyms, "\n") {
		for _, bad := range []string{"tuitrace", "tui.trace", "streamTrace", "dialFailure", "dialClass"} {
			if strings.Contains(line, bad) {
				t.Errorf("the default build has a trace symbol: %s", line)
			}
		}
	}
	for _, bad := range []string{"MOCHIII_TRACE", "tui-%d", "command_local_done", "handshake_rejected", "no_lockfile"} {
		if bytes.Contains(defBin, []byte(bad)) {
			t.Errorf("the default build contains the string %q", bad)
		}
	}
}

func clientSymbolsAndBytes(t *testing.T, tags string) (string, []byte) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mochiii")
	args := []string{"build", "-o", bin}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	if out, err := exec.Command("go", append(args, "./cmd/mochiii-tui")...).CombinedOutput(); err != nil {
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
