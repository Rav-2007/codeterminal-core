package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// The workspace-too-large marker is workspace content, so it is untrusted, and
// degradations() reads it on every prompt. These tests pin the three properties
// one capped, link-refusing, non-blocking read buys -- and each asserts the
// DEGRADATION IS STILL REPORTED, because dropping it would let anyone who can
// write a byte into the workspace hide the fact that retrieval is disabled.

func markerServer(t *testing.T, contents func(path string) error) (*Server, *bytes.Buffer) {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(ws, ".codeterminal", "index")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := contents(filepath.Join(dir, "TOO_LARGE")); err != nil {
		t.Fatalf("planting the marker: %v", err)
	}
	var logbuf bytes.Buffer
	return &Server{workspace: ws, logger: log.New(&logbuf, "", 0)}, &logbuf
}

func findTooLarge(t *testing.T, degs []protocol.Degradation) protocol.Degradation {
	t.Helper()
	for _, d := range degs {
		if d.Component == protocol.DegradedWorkspaceTooLarge {
			return d
		}
	}
	t.Fatalf("the workspace-too-large degradation was not reported at all; suppressing it lets a "+
		"workspace hide that retrieval is off (degradations: %+v)", degs)
	return protocol.Degradation{}
}

// A legitimate marker -- the exact shape index_cmd.go writes -- must keep
// working, metadata and all. Widening a refusal must not break the one caller
// it exists for.
func TestTooLargeMarker_LegitimatePayloadStillCarriesItsMetadata(t *testing.T) {
	srv, _ := markerServer(t, func(p string) error {
		return os.WriteFile(p, []byte(`{"file_count": 12345, "limit_exceeded": true, "time_ms": 900, "mem_mb": 42}`), 0o644)
	})
	d := findTooLarge(t, srv.degradations())
	if d.Metadata == nil {
		t.Fatal("a legitimate marker lost its metadata")
	}
	if got := d.Metadata["file_count"]; got != float64(12345) {
		t.Errorf("file_count = %v, want 12345", got)
	}
	if got := d.Metadata["limit_exceeded"]; got != true {
		t.Errorf("limit_exceeded = %v, want true", got)
	}
}

// The cap must be comfortably above what the only writer can emit, so it never
// becomes a real limit. index_cmd.go's four-field Sprintf is at most 122 bytes
// even with every integer at int64 maxima.
func TestTooLargeMarker_CapIsWellAboveTheWriterItGuards(t *testing.T) {
	const worstCaseWriterPayload = 122
	if maxTooLargeMarkerBytes < worstCaseWriterPayload*8 {
		t.Fatalf("maxTooLargeMarkerBytes = %d, which is less than 8x the %d bytes index_cmd.go can "+
			"produce; the cap is close enough to legitimate output to become a real limit",
			maxTooLargeMarkerBytes, worstCaseWriterPayload)
	}
}

// An oversized marker: the degradation survives, the metadata does not, the
// daemon says why, and neither the heap nor the wire message follows the file's
// size.
func TestTooLargeMarker_OversizedIsCappedAndSaysSo(t *testing.T) {
	const markerMB = 256
	srv, logbuf := markerServer(t, func(p string) error {
		body := append([]byte(`{"pad":"`), bytes.Repeat([]byte("A"), markerMB*1024*1024)...)
		return os.WriteFile(p, append(body, '"', '}'), 0o644)
	})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	degs := srv.degradations()
	runtime.ReadMemStats(&after)
	allocMB := float64(after.TotalAlloc-before.TotalAlloc) / (1024 * 1024)

	d := findTooLarge(t, degs)
	if d.Metadata != nil {
		t.Errorf("an oversized marker's metadata was kept: %d key(s)", len(d.Metadata))
	}

	// DETERMINISTIC. The wire message cannot follow the marker's size, because
	// bytes that were never read cannot be forwarded. 4 KiB is generous headroom
	// over the fixed Component+Detail strings.
	enc, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > 4096 {
		t.Errorf("the degradation for a %d MiB marker marshals to %d bytes; it is still carrying the file",
			markerMB, len(enc))
	}

	// DETERMINISTIC ENOUGH TO GATE. Uncapped this allocated 512 MiB for a 256 MiB
	// marker (2x: the read, then the decode). The bound below is 64x the capped
	// path's needs and 64x below the uncapped path's, so GC noise cannot decide it.
	if allocMB > 8 {
		t.Errorf("reading a %d MiB marker allocated %.1f MiB; the cap is not being applied", markerMB, allocMB)
	}

	if !strings.Contains(logbuf.String(), "exceeds") {
		t.Errorf("an oversized marker was dropped silently; the log says %q", logbuf.String())
	}
}

// The one the size framing did not predict. On POSIX, open(2) on a FIFO with no
// writer blocks forever, and degradations() is on the prompt path -- so an
// unguarded read here hangs every turn, permanently, holding a connection slot.
func TestTooLargeMarker_FIFODoesNotHangTheTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no filesystem FIFO; its named pipes are not reachable by a workspace path")
	}
	srv, logbuf := markerServer(t, mkfifoForTest)

	done := make(chan []protocol.Degradation, 1)
	go func() { done <- srv.degradations() }()

	select {
	case degs := <-done:
		d := findTooLarge(t, degs)
		if d.Metadata != nil {
			t.Errorf("a FIFO marker produced metadata: %+v", d.Metadata)
		}
		if !strings.Contains(logbuf.String(), "not a regular file") {
			t.Errorf("a FIFO marker was handled silently; the log says %q", logbuf.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("degradations() did not return within 5s on a FIFO marker: the prompt path is blocked, " +
			"and nothing will ever unblock it")
	}
}

// A symlink at the leaf is refused rather than followed, so the marker cannot be
// aimed at /dev/zero or at a file outside the workspace. Present-and-unreadable
// is a different state from absent and is logged as one.
func TestTooLargeMarker_SymlinkAtTheLeafIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows; the O_NOFOLLOW equivalent is covered by platform_windows.go")
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(outside, []byte(`{"leaked": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, logbuf := markerServer(t, func(p string) error { return os.Symlink(outside, p) })

	for _, d := range srv.degradations() {
		if d.Component == protocol.DegradedWorkspaceTooLarge && d.Metadata != nil {
			t.Errorf("a symlinked marker was followed: metadata %+v", d.Metadata)
		}
	}
	if !strings.Contains(logbuf.String(), "could not be opened") {
		t.Errorf("a symlinked marker was skipped silently; the log says %q", logbuf.String())
	}
}

// Malformed but small: still a degradation, still no metadata, still said out
// loud. This is the path that was already silent before the cap existed.
func TestTooLargeMarker_MalformedJSONIsReportedNotSwallowed(t *testing.T) {
	srv, logbuf := markerServer(t, func(p string) error {
		return os.WriteFile(p, []byte(`{"file_count": `), 0o644)
	})
	d := findTooLarge(t, srv.degradations())
	if d.Metadata != nil {
		t.Errorf("malformed JSON produced metadata: %+v", d.Metadata)
	}
	if !strings.Contains(logbuf.String(), "not valid JSON") {
		t.Errorf("a malformed marker was swallowed; the log says %q", logbuf.String())
	}
}

// An empty marker is what the indexer would leave if a write were interrupted.
// It is not an error and must not be logged as one, but it is still a marker.
func TestTooLargeMarker_EmptyIsStillAMarker(t *testing.T) {
	srv, logbuf := markerServer(t, func(p string) error { return os.WriteFile(p, nil, 0o644) })
	d := findTooLarge(t, srv.degradations())
	if d.Metadata != nil {
		t.Errorf("an empty marker produced metadata: %+v", d.Metadata)
	}
	if logbuf.Len() != 0 {
		t.Errorf("an empty marker logged something; it is a normal state: %q", logbuf.String())
	}
}
