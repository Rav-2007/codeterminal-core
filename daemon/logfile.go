package main

import (
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Durable destination for the daemon's log.
//
// The daemon logged to stderr and nowhere else. A daemon started detached --
// which is the normal way to run it -- therefore had no retrievable log at
// all, so the honest degradation notices it already wrote were, in practice,
// written to nothing. --log-file gives them somewhere to survive.
//
// SCOPE, stated plainly rather than implied: this adds a FILE, with size-based
// rotation. It does NOT add log levels. Levels would mean classifying every
// existing call site across the daemon, a large mechanical change whose
// benefit is filtering; the operator question that actually motivated this
// cluster ("is this daemon healthy?") is answered directly and completely by
// the status surface, not by grepping a log at the right verbosity. Left
// undone deliberately, not overlooked.
//
// Rotation follows the discipline warnsink.go already established for the
// warn-mode sink -- a size cap with a single .1 backup -- so total on-disk use
// is bounded at ~2x the cap no matter how long the daemon runs. Adding an
// unbounded log file would have traded one operational problem for another.

// logFileMaxBytes bounds the active log before rotation, matching
// warnSinkMaxBytes. One rotation keeps a single .1 backup.
const logFileMaxBytes = 5 << 20 // 5 MiB

// rotatingFile is an io.Writer that appends to a path and rotates it once it
// would exceed maxBytes.
//
// Every write error is returned to the logger rather than swallowed -- the
// opposite of warnSink's deliberately failure-safe policy, and for the
// opposite reason: warnSink sits on the per-request retrieval path where a
// sink failure must never affect a request, whereas this is the log itself,
// and log.Logger already tolerates a failing writer without disturbing the
// caller. Note the daemon always ALSO writes to stderr (see newLogWriter), so
// a broken log file degrades to today's behavior rather than to silence.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	size     int64
	f        *os.File
}

// openRotatingFile opens (creating, 0600 -- a daemon log can contain workspace
// paths and prompt sizes) the log at path, appending to whatever is there.
func openRotatingFile(path string, maxBytes int64) (*rotatingFile, error) {
	// The parent directory is created rather than required, matching
	// jsonlsink.go -- the warn sink and the tool audit log write into the same
	// .codeterminal/logs/ and both create it on the way.
	//
	// This mattered the moment the VS Code extension started passing -log-file
	// unconditionally: on a workspace that has never been indexed, that
	// directory does not exist yet, so the daemon's own log was the one file it
	// silently failed to create. Failing soft (newLogWriter falls back to
	// stderr) meant an ADOPTING window -- which has no pipe to the daemon at all
	// -- had nowhere to read anything.
	//
	// 0700, not 0755: the log carries workspace paths and prompt sizes, and the
	// mode below says so.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// O_NOFOLLOW: refuse to append through a symlink planted at the log path,
	// the same leaf-level protection every other writer in this codebase applies.
	f, err := openNoFollow(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	// The 0600 above is a POSIX creation mode and Windows discards it, so the
	// restriction is applied explicitly rather than left to the open flags. Best
	// effort: a log that cannot be locked down is still better than no log, and
	// the daemon has nowhere to report the failure yet -- this IS the reporting
	// channel being opened.
	_ = restrictToOwner(path, 0600)

	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	return &rotatingFile{path: path, maxBytes: maxBytes, size: size, f: f}, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.size+int64(len(p)) > r.maxBytes {
		r.rotateLocked()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotateLocked renames the active file to <path>.1 (replacing any prior
// backup) and reopens. Any failure leaves the current handle in place, so the
// worst case is a file that grows past the bound rather than a lost log.
func (r *rotatingFile) rotateLocked() {
	if err := r.f.Close(); err != nil {
		return
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		// Reopen what we just closed so logging continues regardless.
		if f, err := openNoFollow(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
			r.f = f
		}
		return
	}
	f, err := openNoFollow(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	r.f = f
	r.size = 0
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// newLogWriter returns the writer the daemon's logger should use, plus a
// cleanup func.
//
// When path is empty (the default) this is exactly stderr and nothing changes.
// When set, output is TEE'd to both stderr and the file rather than redirected
// to the file alone: a foreground operator watching stderr must not lose their
// log because someone also asked for a durable copy, and the file must not be
// the single point of failure for seeing anything at all.
//
// A file that cannot be opened is reported to the caller, which warns and
// continues on stderr. Refusing to start a working daemon because its optional
// log destination is unwritable would be the wrong trade -- and would be a new
// instance of the over-strict startup failure C1 was careful to avoid.
func newLogWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stderr, func() {}, nil
	}
	rf, err := openRotatingFile(path, logFileMaxBytes)
	if err != nil {
		return os.Stderr, func() {}, err
	}
	return io.MultiWriter(os.Stderr, rf), func() { rf.Close() }, nil
}
