package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// H8 -- THE AUDIT LOG WITH MORE THAN ONE DAEMON WRITING IT.
//
// jsonlSink holds a mutex, which orders writes within ONE process. Two daemons
// on one workspace -- a CLI invocation while the TUI is running, which is
// exactly the pair the Gate 6 apply/undo work already had to serialise across
// processes -- are two processes and two mutexes, and rotation is an os.Rename
// that either of them may perform at any moment.
//
// Registered in the 2026-08-01 gate as scoped-and-not-reached. What the audit
// log is FOR is answering "did you approve this?", so a torn or unparseable
// line is the failure that matters: an audit nobody can read is not an audit.
//
// Real processes, not goroutines. Goroutines would share the mutex and prove
// nothing about the thing under test.

const (
	sinkHelperEnv   = "CODETERMINAL_JSONL_SINK_HELPER"
	sinkHelperLines = 400
)

// TestJSONLSinkHelperProcess is not a test. It is the child process, selected
// by name via os.Args[0], and it exits before asserting anything when its
// environment variable is absent -- the standard Go re-exec pattern.
func TestJSONLSinkHelperProcess(t *testing.T) {
	spec := os.Getenv(sinkHelperEnv)
	if spec == "" {
		t.Skip("not the helper process")
	}
	path, maxBytes, id := parseSinkHelperSpec(t, spec)

	sink := newJSONLSink(path, maxBytes)
	for i := 0; i < sinkHelperLines; i++ {
		sink.append(map[string]any{
			"writer": id,
			"seq":    i,
			// Padding, so the file reaches the rotation threshold quickly and
			// so a torn line is long enough to be obvious rather than a
			// coincidence of short strings.
			"pad": strings.Repeat("x", 120),
		})
	}
}

func parseSinkHelperSpec(t *testing.T, spec string) (path string, maxBytes int64, id int) {
	t.Helper()
	parts := strings.Split(spec, "|")
	if len(parts) != 3 {
		t.Fatalf("malformed helper spec %q", spec)
	}
	maxBytes, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		t.Fatalf("helper spec max bytes: %v", err)
	}
	id, err = strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("helper spec id: %v", err)
	}
	return parts[0], maxBytes, id
}

// Four processes appending to one sink, through several rotations.
func TestTheAuditLogStaysReadableWithFourDaemonsWriting(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns four processes")
	}
	const writers = 4
	// Small enough that sinkHelperLines x writers rotates the file repeatedly:
	// rotation is the interesting moment, so the test needs many of them.
	const maxBytes = 16 * 1024

	dir := t.TempDir()
	path := filepath.Join(dir, "toolcalls.jsonl")

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestJSONLSinkHelperProcess")
			cmd.Env = append(os.Environ(),
				fmt.Sprintf("%s=%s|%d|%d", sinkHelperEnv, path, maxBytes, id))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("writer %d: %v\n%s", id, err, out)
			}
		}(i)
	}
	wg.Wait()

	// THE PROPERTY: every line that survived is a complete, parseable record.
	// Both files, because rotation means a line may legitimately be in either.
	total, seen := 0, map[string]bool{}
	for _, p := range []string{path, path + ".1"} {
		f, err := os.Open(p)
		if err != nil {
			continue // .1 may not exist if rotation never fired
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for line := 1; scanner.Scan(); line++ {
			text := scanner.Text()
			if text == "" {
				continue
			}
			var rec struct {
				Writer int    `json:"writer"`
				Seq    int    `json:"seq"`
				Pad    string `json:"pad"`
			}
			if err := json.Unmarshal([]byte(text), &rec); err != nil {
				t.Fatalf("%s line %d is not a complete record, so the audit cannot be read: %v\n%q",
					filepath.Base(p), line, err, truncateForMessage(text))
			}
			if len(rec.Pad) != 120 {
				t.Fatalf("%s line %d parsed but is not intact (pad is %d bytes, want 120): two "+
					"processes' writes interleaved within one line",
					filepath.Base(p), line, len(rec.Pad))
			}
			seen[fmt.Sprintf("%d/%d", rec.Writer, rec.Seq)] = true
			total++
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		_ = f.Close()
	}

	written := writers * sinkHelperLines
	// Records CAN be lost, and legitimately: rotation bounds the file at ~2x
	// maxBytes and this test deliberately writes far past that, so anything
	// rotated twice is gone by design. What must not happen is a line that
	// cannot be read.
	t.Logf("H8: %d records written by %d processes, %d readable and intact across both files "+
		"(%d distinct); the rest were rotated out, which is what the size bound is for",
		written, writers, total, len(seen))

	if total == 0 {
		t.Fatal("nothing survived at all, so this proves nothing about interleaving")
	}
}

func truncateForMessage(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "..."
}
