// jsonlSink is the shared substrate under the daemon's two append-only local
// record files: the warn-mode fire log (warnsink.go) and the tool-call audit
// log (toolaudit.go).
//
// It was extracted rather than copied because the properties that make it safe
// are the whole point of it, and two copies of a safety property is one copy
// that will eventually be fixed alone: local filesystem only with deliberately
// no io.Writer or network seam, O_NOFOLLOW so a symlink planted at the log name
// cannot redirect the append, size-rotated so an unbounded log never becomes
// its own problem, every error swallowed so a sink failure can never fail a
// request, and a nil receiver is a valid no-op so callers need no special case.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

type jsonlSink struct {
	mu       sync.Mutex
	path     string
	maxBytes int64

	// droppedForBound counts records refused because the file was at its size
	// bound and rotation would not succeed. It exists so that state is
	// OBSERVABLE rather than merely swallowed: every other error on this path
	// is discarded by design, which is precisely why a standing rotation
	// failure could remove the size bound with nothing to show for it.
	droppedForBound atomic.Uint64
}

// newJSONLSink returns a sink writing to path, or nil (a valid no-op) if path
// is empty. Construction never fails the caller: the parent dir is best-effort
// created here (0700 -- these files sit next to sensitive material), and any
// residual problem surfaces later as a harmlessly swallowed write.
func newJSONLSink(path string, maxBytes int64) *jsonlSink {
	if path == "" {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	return &jsonlSink{path: path, maxBytes: maxBytes}
}

// append marshals v and writes it as one line. All errors are swallowed by
// design (see the package comment above): a full disk must never break a
// retrieval request or an agent turn. Safe on a nil receiver.
func (s *jsonlSink) append(v any) {
	if s == nil {
		return
	}
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	// REFUSE, RATHER THAN APPEND PAST THE BOUND. If the file is over its limit
	// and rotation will not succeed, this record is dropped.
	//
	// The argument, because it trades one loss for another: this sink ALREADY
	// drops records silently when it cannot open the file (below), so losing a
	// record is inside its contract. Growing without limit is not. The package
	// comment says the sink exists so that "a full disk must never break a
	// retrieval request" -- and an unbounded log is a way to CAUSE a full disk,
	// so appending past the bound defeats the property it was protecting.
	// Between losing telemetry and removing the only bound on a log's growth,
	// losing telemetry is the smaller harm and the one already accepted here.
	if !s.rotateIfNeededLocked(int64(len(line))) {
		s.droppedForBound.Add(1)
		return
	}
	// O_NOFOLLOW: the path is a fixed workspace log path, not client input, but
	// these files are sensitive enough that appending through a symlink planted
	// at the log name must be refused (ELOOP joins the other swallowed errors,
	// keeping the sink failure-safe on the request path).
	f, err := openNoFollow(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return // swallow: disk full, read-only dir, path is a directory, symlink, ...
	}
	// Both swallowed on purpose (see the package comment): a close or short
	// write failure here must not surface anywhere near the request path.
	defer func() { _ = f.Close() }()
	_, _ = f.Write(line)
}

// rotateIfNeededLocked renames the active file to <path>.1 (replacing any prior
// backup) once appending incoming bytes would push it past maxBytes, bounding
// total on-disk size to ~2x maxBytes. Caller holds mu.
//
// Reports whether it is SAFE TO APPEND: true when no rotation was needed or the
// rotation succeeded, false when the file is over its bound and still there.
//
// THIS USED TO RETURN NOTHING, and its comment read: "Any error leaves the
// current file in place -- worst case it grows slightly past the bound, which
// is still failure-safe." That is true for ONE transient failure and wrong for
// a standing one. The rename was best-effort and append wrote regardless, so
// every later write re-stat'ed, re-failed and appended anyway: growth became
// unbounded and permanent, with every error swallowed so nothing could notice.
//
// Measured on the shape that produces it -- a writable log inside a
// non-writable directory, since rename(2) needs the DIRECTORY and O_APPEND on
// an existing file needs only the FILE -- a 4 KiB bound reached 620,013 bytes
// and was still climbing: 151x, not "slightly past".
func (s *jsonlSink) rotateIfNeededLocked(incoming int64) bool {
	fi, err := os.Stat(s.path)
	if err != nil {
		return true // no file yet (first write), or unstattable; nothing to rotate
	}
	if fi.Size()+incoming <= s.maxBytes {
		return true
	}
	if err := os.Rename(s.path, s.path+".1"); err != nil {
		// The file is over its bound and could not be rotated away. Appending
		// here is what removed the bound; the caller drops the record instead.
		return false
	}
	return true
}

// droppedForBoundCount reports how many records this sink refused because it
// was at its size bound and could not rotate. Zero is the normal answer; a
// rising number means the log directory is not writable and the operator is
// losing telemetry rather than disk.
func (s *jsonlSink) droppedForBoundCount() uint64 {
	if s == nil {
		return 0
	}
	return s.droppedForBound.Load()
}
