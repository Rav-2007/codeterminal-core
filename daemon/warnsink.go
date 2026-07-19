// This file gives the WARN-MODE (log-only) chunk-secret measurement a DURABLE
// home. Its stderr twin (logChunkScrub in context.go) vanishes with the daemon
// process, so on its own nothing ever accumulated — the whole point of warn
// mode is to gather fire-rate data over time for a future founder decision on
// Designs B/C (CHUNK_SCRUB_DESIGN.md §4). This sink is that durable substrate.
//
// It changes NOTHING about what is sent to the model: warn mode remains
// log-only. It only makes the existing, already-correct detections survive a
// restart, as append-only JSON lines on the LOCAL filesystem.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// warnEvent is one durable warn-mode fire record, written as a single JSON
// line. Like the stderr notice it mirrors, it carries NO raw suspected-secret
// material — only fixed labels, source offsets, a secret-free note, and the
// one-way indicator hash (see chunkscrub.go). Adding a field here that could
// reconstruct the value would reintroduce the exact leak Gate 3 verified absent.
type warnEvent struct {
	Ts        string    `json:"ts"`         // RFC3339 UTC, so a rate over time is computable
	Detector  string    `json:"detector"`   // "entropy" | "keyword"
	File      string    `json:"file"`       // source path of the chunk the fire came from
	StartLine int       `json:"start_line"` // chunk span, to relocate the fire in-source
	EndLine   int       `json:"end_line"`
	Class     FileClass `json:"class"`     // chunk's file class (code/test/doc/config/other), threaded from retrieval
	Shape     string    `json:"shape"`     // fixed-label token shape (hex/base64/uuid-like/mixed/unknown)
	Note      string    `json:"note"`      // secret-free stats, e.g. "len=41 bits_per_char=5.11"
	Indicator string    `json:"indicator"` // "sha256:xxxxxxxx" over the value; never the value
}

// warnSinkMaxBytes bounds the active file before rotation. One rotation keeps a
// single .1 backup, so on-disk usage stays bounded at ~2x this no matter how
// long the daemon runs on a busy repo — an unbounded measurement log is its own
// future problem.
const warnSinkMaxBytes = 5 << 20 // 5 MiB

// warnSink is a durable, append-only, size-rotated LOCAL sink for warn-mode
// fire events. Constraints, matching the audited discipline of the stderr path
// it complements (it does not replace stderr — both are written):
//
//   - LOCAL FILESYSTEM ONLY. There is deliberately no io.Writer/network seam
//     here: the only output is an openNoFollow write on a local path. Warn
//     events sit next to suspected secrets and must never gain a network egress.
//   - FAILURE-SAFE ON THE REQUEST PATH. Every error (full disk, read-only dir,
//     path-is-a-directory, marshal failure) is swallowed. A sink failure must
//     never fail or delay a retrieval request. A nil *warnSink is a valid
//     no-op sink, so tests and retrieval-disabled daemons need no special case.
type warnSink struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
}

// newWarnSink returns a sink writing to path, or nil (a valid no-op) if path is
// empty. Construction never fails the caller: the parent dir is best-effort
// created here (0700 — this is secret-adjacent measurement data), and any
// residual problem surfaces later as a harmlessly swallowed write.
func newWarnSink(path string) *warnSink {
	if path == "" {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	return &warnSink{path: path, maxBytes: warnSinkMaxBytes}
}

// write appends ev as one JSON line. All errors are swallowed by design (see
// the type doc): measurement must never break retrieval. Safe on a nil receiver.
func (w *warnSink) write(ev warnEvent) {
	if w == nil {
		return
	}
	if ev.Ts == "" {
		ev.Ts = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	w.rotateIfNeededLocked(int64(len(line)))
	// O_NOFOLLOW: w.path is a fixed workspace log path, not client input, but
	// this sink sits next to suspected secrets — refuse to append through a
	// symlink planted at the log name (ELOOP joins the other swallowed errors,
	// keeping the sink failure-safe on the request path).
	f, err := openNoFollow(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return // swallow: disk full, read-only dir, path is a directory, symlink, ...
	}
	defer f.Close()
	_, _ = f.Write(line) // swallow a short/failed write too
}

// rotateIfNeededLocked renames the active file to <path>.1 (replacing any prior
// backup) once appending incoming bytes would push it past maxBytes, bounding
// total on-disk size to ~2x maxBytes. Caller holds mu. Any error leaves the
// current file in place — worst case it grows slightly past the bound, which is
// still failure-safe.
func (w *warnSink) rotateIfNeededLocked(incoming int64) {
	fi, err := os.Stat(w.path)
	if err != nil {
		return // no file yet (first write), or unstattable; nothing to rotate
	}
	if fi.Size()+incoming <= w.maxBytes {
		return
	}
	_ = os.Rename(w.path, w.path+".1")
}
