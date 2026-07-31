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
)

type jsonlSink struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
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

	s.rotateIfNeededLocked(int64(len(line)))
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
// total on-disk size to ~2x maxBytes. Caller holds mu. Any error leaves the
// current file in place -- worst case it grows slightly past the bound, which
// is still failure-safe.
func (s *jsonlSink) rotateIfNeededLocked(incoming int64) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return // no file yet (first write), or unstattable; nothing to rotate
	}
	if fi.Size()+incoming <= s.maxBytes {
		return
	}
	_ = os.Rename(s.path, s.path+".1")
}
