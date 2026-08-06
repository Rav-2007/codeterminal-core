package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// watcher.go shipped with zero test coverage while being wired into every
// daemon at main.go:300. It turns an ORDINARY FILE SAVE — not a tool call, not
// an approved action — into a read of that file's content and an insertion into
// the retrieval index. That is the least-supervised path into the index in the
// product, so it needs the most evidence.

// capturingLogger returns a logger that records its lines under mu, so a test
// can assert on what the watcher goroutine did without racing it.
func capturingLogger(mu *sync.Mutex, out *[]string) *log.Logger {
	return log.New(funcWriter(func(p []byte) (int, error) {
		mu.Lock()
		*out = append(*out, string(p))
		mu.Unlock()
		return len(p), nil
	}), "", 0)
}

type funcWriter func([]byte) (int, error)

func (f funcWriter) Write(p []byte) (int, error) { return f(p) }

// newWatcherServer builds a Server with a real workspace and a cancellable
// shutdown, wired the way main.go wires one.
func newWatcherServer(t *testing.T) (*Server, string, context.CancelFunc) {
	t.Helper()
	ws := t.TempDir()
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{logger: discardLogger(), workspace: real, shutdownCtx: ctx}
	t.Cleanup(cancel)
	return s, real, cancel
}

// THE ESCAPE, and the reason this file exists.
//
// ScanWorkspace refuses symlinked entries during its walk, and its comment
// claimed that "check alone stops any symlink escape". reindexFile does not
// walk — it calls readEligibleFile directly — so both incremental paths (an
// applied edit, and every save this watcher sees) reached the read with no
// symlink check.
//
// Neuter check: remove the ModeSymlink branch from shouldSkipFile and this test
// fails with the private key's contents in the failure message.
func TestIndexGate_RefusesASymlinkOutOfTheWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows; NOT RUN on this platform")
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(secret, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nCANARY\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	// The link is deliberately named something the secret-name gate likes: that
	// gate sees the LINK's name, never the target's.
	link := filepath.Join(ws, "notes.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip(err)
	}

	content, reason, skip, err := readEligibleFile(link, "notes.md", newGitignoreMatcher(ws))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !skip {
		t.Fatalf("ESCAPE: a symlink out of the workspace was admitted for indexing; content=%q", content)
	}
	if reason != SkipSymlink {
		t.Errorf("skip reason = %q, want %q", reason, SkipSymlink)
	}
	if strings.Contains(string(content), "CANARY") {
		t.Error("content from outside the workspace was returned")
	}
}

// A symlink to a file INSIDE the workspace is refused too. Same rule, and worth
// pinning: the check is on the entry's own type, not on where it points, so
// there is no target inspection to get wrong and no TOCTOU window.
func TestIndexGate_RefusesAnInternalSymlinkToo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows; NOT RUN on this platform")
	}
	ws := t.TempDir()
	realFile := filepath.Join(ws, "real.go")
	if err := os.WriteFile(realFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, filepath.Join(ws, "alias.go")); err != nil {
		t.Skip(err)
	}

	_, reason, skip, err := readEligibleFile(filepath.Join(ws, "alias.go"), "alias.go", newGitignoreMatcher(ws))
	if err != nil {
		t.Fatal(err)
	}
	if !skip || reason != SkipSymlink {
		t.Errorf("an internal symlink was admitted (skip=%v reason=%q); it would index the same content twice under two keys", skip, reason)
	}

	// The real file must still be indexable — a gate that refuses everything is
	// not a gate.
	_, _, skip, err = readEligibleFile(realFile, "real.go", newGitignoreMatcher(ws))
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Error("the real file was refused; the symlink check is over-broad")
	}
}

// The watcher must start, walk, and stop cleanly when the daemon shuts down. A
// watcher goroutine that outlives its Server holds inotify descriptors and a
// reference to the whole Server for the life of the process.
func TestWorkspaceWatcher_StartsAndStopsWithTheDaemon(t *testing.T) {
	s, ws, cancel := newWatcherServer(t)
	if err := os.MkdirAll(filepath.Join(ws, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	before := runtime.NumGoroutine()
	s.startWorkspaceWatcher()

	// Give the watcher goroutine time to reach its select.
	time.Sleep(200 * time.Millisecond)
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("the watcher goroutine outlived the shutdown signal (%d goroutines, started from %d)", runtime.NumGoroutine(), before)
}

// Hidden directories and dependency trees must not be watched. Beyond the
// inotify cost, .git churns constantly and would drive a reindex storm on every
// checkout, fetch and commit.
func TestWorkspaceWatcher_SkipsHiddenAndVendorDirectories(t *testing.T) {
	s, ws, cancel := newWatcherServer(t)
	defer cancel()

	for _, d := range []string{".git/objects", "node_modules/pkg", "vendor/dep", "src/real"} {
		if err := os.MkdirAll(filepath.Join(ws, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The watcher does not export its watch list, so assert on the property that
	// matters and is observable: it survives a write into each skipped tree
	// without reindexing anything. A reindex would log; nothing may.
	//
	// The logger is installed BEFORE the watcher starts. Assigning it afterwards
	// races with the watcher goroutine's own reads of it — caught by -race.
	var mu sync.Mutex
	var logged []string
	s.logger = capturingLogger(&mu, &logged)

	s.startWorkspaceWatcher()
	time.Sleep(200 * time.Millisecond)

	for _, f := range []string{".git/objects/x", "node_modules/pkg/index.js", "vendor/dep/d.go"} {
		if err := os.WriteFile(filepath.Join(ws, f), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1500 * time.Millisecond) // past the 1s debounce

	mu.Lock()
	defer mu.Unlock()
	for _, line := range logged {
		if strings.Contains(line, "reindexing") {
			t.Errorf("a write inside a skipped directory triggered a reindex: %q", line)
		}
	}
}

// A dotfile write must not be reindexed even inside a watched directory: the
// watcher's own filter is the only thing standing between a .env save and the
// eligibility gate, and defence in depth is the point.
func TestWorkspaceWatcher_IgnoresDotfiles(t *testing.T) {
	s, ws, cancel := newWatcherServer(t)
	defer cancel()

	var mu sync.Mutex
	var logged []string
	s.logger = capturingLogger(&mu, &logged)

	s.startWorkspaceWatcher()
	time.Sleep(200 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(ws, ".env"), []byte("OPENROUTER_API_KEY=sk-or-v1-CANARY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, line := range logged {
		if strings.Contains(line, ".env") {
			t.Errorf("a dotfile write reached the reindex path: %q", line)
		}
	}
}

// The watcher must survive a workspace that does not exist rather than taking
// the daemon down at startup.
func TestWorkspaceWatcher_MissingWorkspaceIsNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{
		logger:      discardLogger(),
		workspace:   filepath.Join(t.TempDir(), "does-not-exist"),
		shutdownCtx: ctx,
	}
	s.startWorkspaceWatcher() // must not panic
	time.Sleep(100 * time.Millisecond)
}
