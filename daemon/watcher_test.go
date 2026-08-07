package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

// THE INCREMENTAL DOOR INTO THE INDEX THAT THE INDEXER'S CONFINEMENT DID NOT
// COVER.
//
// ScanWorkspace refuses a link-like entry and never descends through one, and
// the watcher's INITIAL walk inherits that for free: filepath.WalkDir reports a
// symlink by its own type, so d.IsDir() is false and the directory is neither
// entered nor watched.
//
// The Create handler was the hole. It asked os.STAT, which follows the link and
// answers about the target, so a symlinked directory came back IsDir and was
// handed to watcher.Add -- registering an inotify watch on a directory OUTSIDE
// the workspace. Every write behind it then arrived as <workspace>/<link>/<file>,
// survived filepath.Rel, and reached reindexFile, which reads the file and puts
// its content in the retrieval index. Index content becomes prompt context, and
// prompt context leaves the machine.
//
// `ln -s ../shared lib` is ordinary, and so is checking out a branch that
// contains one. This needs no attacker.
//
// Neuter check: change os.Lstat back to os.Stat (or drop the IsLinkLike branch)
// and this test fails with the outside file named in the message.
func TestWorkspaceWatcher_RefusesToWatchThroughASymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows; NOT RUN on this platform")
	}
	s, ws, cancel := newWatcherServer(t)
	defer cancel()

	outside := t.TempDir()

	var mu sync.Mutex
	var logged []string
	s.logger = capturingLogger(&mu, &logged)

	s.startWorkspaceWatcher()
	time.Sleep(200 * time.Millisecond)

	// Created AFTER the walk, deliberately: the Create event is the only way in.
	if err := os.Symlink(outside, filepath.Join(ws, "shared")); err != nil {
		t.Skip(err)
	}
	time.Sleep(300 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("CANARY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // past the 1s debounce

	mu.Lock()
	defer mu.Unlock()
	for _, line := range logged {
		if strings.Contains(line, "reindexing") {
			t.Errorf("a write OUTSIDE the workspace reached the reindex path through a symlinked "+
				"directory: %q -- the watcher followed a link the indexer refuses, and whatever it "+
				"indexes becomes prompt context", line)
		}
	}
}

// THE SAME DIVERGENCE, ITS SECOND SYMPTOM.
//
// TestWorkspaceWatcher_SkipsHiddenAndVendorDirectories creates its directories
// BEFORE the watcher starts, so it only ever exercised the initial walk. The
// Create handler had no skip rule at all, so a directory that appeared after
// startup was watched whatever it was called.
//
// .git is the one that hurts: it churns on every checkout, fetch, commit and
// gc, and the dotfile filter does not save you -- the filter tests the BASE
// name, and .git/objects/ab/cdef0123 has a base of cdef0123.
//
// `git init` in a subdirectory, an npm install, and a build that materialises
// its own output tree all reach this path. Neuter check: drop the
// watchableDirName call from the Create handler and this test fails.
func TestWorkspaceWatcher_AppliesTheWalksSkipRulesToDirectoriesCreatedLater(t *testing.T) {
	s, ws, cancel := newWatcherServer(t)
	defer cancel()

	var mu sync.Mutex
	var logged []string
	s.logger = capturingLogger(&mu, &logged)

	s.startWorkspaceWatcher()
	time.Sleep(200 * time.Millisecond)

	// ONE LEVEL, AND A PAUSE, both load-bearing. A first draft of this test used
	// MkdirAll(".git/objects") and then wrote inside objects/ -- and it passed
	// against the UNFIXED watcher, for a reason that had nothing to do with the
	// property: MkdirAll creates both levels in microseconds, so objects/ already
	// existed by the time the handler got around to watching .git/, no Create
	// event for it was ever seen, and nothing inside it could fire. A test that
	// passes because the event never arrives proves nothing about what the code
	// would do with the event.
	//
	// So: create the skipped directory alone, let the handler actually process
	// its Create, and only then write a file directly inside it.
	for _, d := range []string{".git", "node_modules"} {
		if err := os.Mkdir(filepath.Join(ws, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond)

	// Bases that do NOT start with a dot, so the dotfile filter cannot be what
	// saves us -- the directory rule has to.
	for _, f := range []string{".git/HEAD", "node_modules/index.js"} {
		if err := os.WriteFile(filepath.Join(ws, f), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, line := range logged {
		if strings.Contains(line, "reindexing") {
			t.Errorf("a directory created after startup was watched despite the walk skipping "+
				"its name, and a write inside it triggered a reindex: %q", line)
		}
	}
}

// lockedStore is recordingStore made safe to read from the test goroutine while
// the watcher's worker writes it. recordingStore itself is used only by
// single-goroutine tests and deliberately stays that way.
type lockedStore struct {
	mu    sync.Mutex
	inner recordingStore
}

func (l *lockedStore) Upsert(ctx context.Context, chunks []Chunk) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.Upsert(ctx, chunks)
}

func (l *lockedStore) Query(ctx context.Context, v []float32, k int) ([]Chunk, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.Query(ctx, v, k)
}

func (l *lockedStore) DeleteByFilePath(ctx context.Context, relPath string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.DeleteByFilePath(ctx, relPath)
}

func (l *lockedStore) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.Count()
}

func (l *lockedStore) indexedContent(relPath string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.indexedContent(relPath)
}

// chunksFor counts what the index currently holds for one file. Two reindexes
// of one path that overlap leave this at twice what it should be.
func (l *lockedStore) chunksFor(relPath string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.inner.chunks {
		if c.FilePath == relPath {
			n++
		}
	}
	return n
}

// watcherServerWithIndex is newWatcherServer plus a real in-memory index, so a
// test can ask what the daemon would actually be able to retrieve rather than
// inferring it from a log line.
func watcherServerWithIndex(t *testing.T) (*Server, string, *lockedStore) {
	t.Helper()
	s, ws, _ := newWatcherServer(t)
	store := &lockedStore{}
	s.store = store
	s.embedder = NewPlaceholderEmbedder(embedDim)
	return s, ws, store
}

// seedIndex puts a file on disk and its chunks in the index, exactly as a full
// `index` run would leave them.
func seedIndex(t *testing.T, s *Server, ws, relPath, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, relPath), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	chunks := chunkContent([]byte(content), relPath)
	for i := range chunks {
		chunks[i].Vector = make([]float32, embedDim)
	}
	if err := s.store.Upsert(context.Background(), chunks); err != nil {
		t.Fatal(err)
	}
}

// A DELETED FILE STAYED IN THE INDEX FOR THE LIFE OF THE DAEMON.
//
// The event loop only ever examined Write and Create, so Remove and Rename fell
// through to nothing. The chunks of a deleted file kept matching queries and
// kept being handed to the model as grounded context -- describing code that no
// longer exists, with the same `grounded ✓` a correct answer gets.
//
// Neuter check: drop the Remove|Rename branch and this test fails with the
// deleted file's contents still retrievable.
func TestWorkspaceWatcher_DropsADeletedFileFromTheIndex(t *testing.T) {
	s, ws, store := watcherServerWithIndex(t)

	seedIndex(t, s, ws, "doomed.go", "package main\n\nconst Canary = \"STILL-INDEXED\"\n")
	if !strings.Contains(store.indexedContent("doomed.go"), "STILL-INDEXED") {
		t.Fatal("test bug: the index should start holding the file")
	}

	s.startWorkspaceWatcher()
	time.Sleep(200 * time.Millisecond)

	if err := os.Remove(filepath.Join(ws, "doomed.go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	if got := store.indexedContent("doomed.go"); got != "" {
		t.Errorf("a deleted file is still in the index and still retrievable: %q -- retrieval "+
			"will keep serving it as grounded context for code that no longer exists", got)
	}
}

// A rename is the same problem wearing a different event: fsnotify reports the
// OLD path as Rename and the new one as Create, so the old key must be dropped
// or the file is indexed under both names, one of which is a lie.
func TestWorkspaceWatcher_DropsTheOldKeyWhenAFileIsRenamed(t *testing.T) {
	s, ws, store := watcherServerWithIndex(t)

	seedIndex(t, s, ws, "old.go", "package main\n\nconst Canary = \"UNDER-THE-OLD-NAME\"\n")

	s.startWorkspaceWatcher()
	time.Sleep(200 * time.Millisecond)

	if err := os.Rename(filepath.Join(ws, "old.go"), filepath.Join(ws, "new.go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	if got := store.indexedContent("old.go"); got != "" {
		t.Errorf("the index still holds %q under the pre-rename path: %q", "old.go", got)
	}
}

// concurrencyProbeEmbedder records the greatest number of Embed calls that were
// ever in flight at the same time. Sleeping inside Embed is what makes overlap
// observable at all: without it every call finishes before the next begins and
// a maximum of 1 proves nothing.
type concurrencyProbeEmbedder struct {
	mu       sync.Mutex
	inFlight int
	max      int
}

func (e *concurrencyProbeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.inFlight++
	if e.inFlight > e.max {
		e.max = e.inFlight
	}
	e.mu.Unlock()

	time.Sleep(150 * time.Millisecond)

	e.mu.Lock()
	e.inFlight--
	e.mu.Unlock()

	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, embedDim)
	}
	return out, nil
}

func (e *concurrencyProbeEmbedder) EmbedQuery(ctx context.Context, texts []string) ([][]float32, error) {
	return e.Embed(ctx, texts)
}
func (e *concurrencyProbeEmbedder) Dim() int   { return embedDim }
func (e *concurrencyProbeEmbedder) ID() string { return "concurrency-probe" }

func (e *concurrencyProbeEmbedder) peak() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.max
}

// EMBEDDING WAS FANNED OUT WITHOUT A BOUND.
//
// time.AfterFunc runs its function in a NEW GOROUTINE, and the function
// embedded the file inline. One goroutine per changed path meant a burst --
// `git checkout`, a branch switch, a formatter over the tree -- fired one
// concurrent embed per file at a single helper subprocess holding a single
// model. Embedding is the most expensive thing this daemon does and nothing
// upstream of it was bounded.
//
// The same fan-out is a CORRECTNESS bug on one path: reindexFile is
// delete-then-insert, so two overlapping runs interleave as delete-A, delete-B,
// insert-A, insert-B and leave that file's chunks in the index twice -- the
// exact duplication DeleteByFilePath exists to prevent.
//
// Neuter check: restore the inline time.AfterFunc body in place of the single
// worker and this reports a peak in the high single digits.
func TestWorkspaceWatcher_ReindexesOneFileAtATime(t *testing.T) {
	s, ws, _ := newWatcherServer(t)
	store := &lockedStore{}
	probe := &concurrencyProbeEmbedder{}
	s.store = store
	s.embedder = probe

	s.startWorkspaceWatcher()
	time.Sleep(200 * time.Millisecond)

	// Written together so their debounce timers expire together, which is what a
	// checkout looks like from in here.
	const files = 8
	for i := 0; i < files; i++ {
		name := filepath.Join(ws, "f"+strconv.Itoa(i)+".go")
		if err := os.WriteFile(name, []byte("package main\n\nfunc F"+strconv.Itoa(i)+"() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 1s debounce, then 8 serialized embeds at 150ms each, plus slack.
	time.Sleep(1*time.Second + files*150*time.Millisecond + 1500*time.Millisecond)

	if peak := probe.peak(); peak > 1 {
		t.Errorf("%d embeds were in flight at once: a burst of file changes fans out one "+
			"goroutine per path straight at the single embedder helper, and two runs over ONE "+
			"path would double that file's chunks", peak)
	}
	// A serialized pipeline that never gets round to the work is not a fix.
	for i := 0; i < files; i++ {
		rel := "f" + strconv.Itoa(i) + ".go"
		if store.chunksFor(rel) == 0 {
			t.Errorf("%s was never indexed; serializing the work must not drop it", rel)
		}
	}
}
