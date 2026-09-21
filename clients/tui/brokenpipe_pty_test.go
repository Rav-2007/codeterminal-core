//go:build linux

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// THE REAL BINARY, A REAL PIPE, A READER THAT STOPS EARLY.
//
// MEASURED before ignoreSIGPIPE existed: `mochiii-tui --prompt ... | head`
// exited 141 -- 128+13, killed by SIGPIPE. Someone reading the first few lines
// of an answer is doing an ordinary thing, and dying by signal for it is not a
// clean exit. The in-process test beside this one drives the same path with a
// writer that returns EPIPE; this one proves the SIGNAL is actually handled,
// which only the real process can show.
func TestRealBinaryPipedIntoAnEarlyReaderExitsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary and starts a subprocess")
	}
	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	bin := buildTUI(t)
	// A long answer, so the writer is still streaming when the reader leaves.
	hostileDaemonForPTY(t, real, strings.Repeat("a long streamed answer. ", 2000))

	cmd := exec.Command(bin, "--workspace", real, "--prompt", "say something")
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.NewFile(0, os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Read a little, then go away -- exactly what `head` does.
	buf := make([]byte, 64)
	if _, err := io.ReadFull(stdout, buf); err != nil {
		t.Fatalf("reading the first bytes: %v", err)
	}
	_ = stdout.Close()

	err = cmd.Wait()
	var ws syscall.WaitStatus
	if ee, ok := err.(*exec.ExitError); ok {
		ws = ee.Sys().(syscall.WaitStatus)
	} else if err != nil {
		t.Fatalf("waiting: %v", err)
	} else {
		ws = cmd.ProcessState.Sys().(syscall.WaitStatus)
	}

	if ws.Signaled() {
		t.Fatalf("the client was KILLED by %v when its reader went away. A "+
			"broken pipe is normal pipeline behaviour, not a reason to die by "+
			"signal -- see ignoreSIGPIPE.", ws.Signal())
	}
	if ws.ExitStatus() != 0 {
		t.Errorf("exit %d, want 0: `| head` is a request for part of the answer, not an error", ws.ExitStatus())
	}
}
