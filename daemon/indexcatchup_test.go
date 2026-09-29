package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/editapply"
)

// The index's copy of a hit is not what the model is shown: the file is. The
// audit's case exactly -- a constant changed on disk after indexing reached the
// model with its OLD value -- plus a hit whose file has since been deleted.
func TestAHitIsReadFromDiskNotFromTheIndex(t *testing.T) {
	root, err := editapply.ResolveRealWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	indexed := lineChunk("retry.go", 1, 20)
	now := strings.Replace(indexed.Content, "retry.go line 5", "maxStreamAttempts = 7 // changed after indexing", 1)
	if err := os.WriteFile(filepath.Join(root, "retry.go"), []byte(now), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		logger:             discardLogger(),
		embedder:           &fakeEmbedder{dim: embedDim},
		store:              fixedStore{chunks: []Chunk{indexed, lineChunk("deleted.go", 1, 20)}},
		retrievalTopK:      defaultK,
		contextBudgetChars: 1 << 20,
		rerankDisabled:     true,
		workspace:          root,
		cfg:                &Config{},
	}
	out := s.gatherContext(context.Background(), "how many attempts does the retry make")
	sent := buildAugmentedUserMessage("q", out.Chunks, false)
	if !strings.Contains(sent, "maxStreamAttempts = 7") || strings.Contains(sent, "retry.go line 5\n") {
		t.Errorf("the model was not shown the file as it is now:\n%s", sent)
	}
	if strings.Contains(sent, "deleted.go") {
		t.Errorf("a hit whose file was deleted still reached the model:\n%s", sent)
	}
}

// catchUpWorkspace is a workspace whose index was built an hour ago, holding one
// file changed since and one that was not.
func catchUpWorkspace(t *testing.T) (root string, builtAt time.Time) {
	t.Helper()
	root, err := editapply.ResolveRealWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builtAt = time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	indexDir := filepath.Join(root, indexDirName)
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp, _ := json.Marshal(embedderStamp{EmbedderID: "fake-test-embedder-v1", Dim: embedDim, BuiltAt: builtAt})
	if err := os.WriteFile(filepath.Join(indexDir, embedderStampFileName), stamp, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, age := range map[string]time.Duration{"changed.go": 0, "unchanged.go": 2 * time.Hour} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Now().Add(-age), time.Now().Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	return root, builtAt
}

// What changed while no daemon ran is re-indexed at startup -- that file and
// only that file -- the stamp moves forward, and the next start has nothing to do.
func TestTheIndexCatchesUpOnWhatChangedWhileNoDaemonRan(t *testing.T) {
	root, builtAt := catchUpWorkspace(t)
	store := &recordingStore{}
	s := &Server{logger: discardLogger(), workspace: root, embedder: &fakeEmbedder{dim: embedDim}, store: store,
		freshness: newFreshnessCache(time.Hour)}

	stale := s.freshness.get(time.Now(), func() indexFreshnessResult { return scanIndexFreshness(root, builtAt) })
	if stale.Changed != 1 {
		t.Fatalf("fixture: %d changed file(s), want 1", stale.Changed)
	}
	s.catchUpIndex()
	if got := store.deletes; len(got) != 1 || got[0] != "changed.go" {
		t.Errorf("re-indexed %v, want exactly [changed.go]", got)
	}
	after := readStampBuiltAt(filepath.Join(root, indexDirName))
	if !after.After(builtAt) {
		t.Errorf("the stamp still says %s; the index was brought up to date", after)
	}
	if res := s.freshness.get(time.Now(), func() indexFreshnessResult { return scanIndexFreshness(root, after) }); res.Changed != 0 {
		t.Errorf("/status would still report %d stale file(s) after the catch-up", res.Changed)
	}

	s.catchUpIndex()
	if got := store.deletes; len(got) != 1 {
		t.Errorf("a second start re-indexed again: %v", got)
	}
}

// Past the bound, nothing is re-embedded in the background and the log says
// what to run instead.
func TestTooManyChangesAreLeftToAFullIndex(t *testing.T) {
	root, _ := catchUpWorkspace(t)
	for i := 0; i < maxCatchUpFiles; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var logbuf bytes.Buffer
	store := &recordingStore{}
	s := &Server{logger: log.New(&logbuf, "", 0), workspace: root, embedder: &fakeEmbedder{dim: embedDim}, store: store}
	s.catchUpIndex()
	if got := store.deletes; len(got) != 0 {
		t.Errorf("re-indexed %d file(s) past the bound", len(got))
	}
	if !strings.Contains(logbuf.String(), "run `mochiii-daemon index`") {
		t.Errorf("the log does not say what to do: %q", logbuf.String())
	}
}

// The catch-up runs when the watcher starts, before any live save it queues.
func TestTheWatcherCatchesUpWhenItStarts(t *testing.T) {
	root, _ := catchUpWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var logged []string
	s := &Server{logger: capturingLogger(&mu, &logged), workspace: root, shutdownCtx: ctx,
		embedder: &fakeEmbedder{dim: embedDim}, store: &recordingStore{}}
	s.startWorkspaceWatcher()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		all := strings.Join(logged, "\n")
		mu.Unlock()
		if strings.Contains(all, "caught up 1 file(s)") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("the watcher started without catching up; log:\n%s", strings.Join(logged, "\n"))
}

// probeEmbedder reports, at the moment it is asked to embed, whether the file
// being re-indexed is still in the store -- and can be told to fail.
type probeEmbedder struct {
	fakeEmbedder
	store       *recordingStore
	file        string
	presentThen bool
	fail        bool
}

func (p *probeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for _, c := range p.store.chunks {
		if c.FilePath == p.file {
			p.presentThen = true
		}
	}
	if p.fail {
		return nil, fmt.Errorf("embedder down")
	}
	return p.fakeEmbedder.Embed(ctx, texts)
}

// A file being re-indexed stays searchable while it is embedded: its old
// chunks go only when the new ones are ready. Deleting first left it out of
// search for the whole embed, which the startup catch-up made the common case.
func TestAFileStaysSearchableWhileItIsReindexed(t *testing.T) {
	root, err := editapply.ResolveRealWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeLineFile(t, root, "a.go", 20)
	for _, fail := range []bool{false, true} {
		store := &recordingStore{chunks: []Chunk{lineChunk("a.go", 1, 20)}}
		emb := &probeEmbedder{fakeEmbedder: fakeEmbedder{dim: embedDim}, store: store, file: "a.go", fail: fail}
		s := &Server{logger: discardLogger(), workspace: root, embedder: emb, store: store}
		err := s.reindexFile(context.Background(), root, "a.go")
		if !emb.presentThen {
			t.Errorf("fail=%v: a.go was out of the index while it was being embedded", fail)
		}
		kept := 0
		for _, c := range store.chunks {
			if c.FilePath == "a.go" {
				kept++
			}
		}
		if fail && (err == nil || kept == 0) {
			t.Errorf("a failed embed: err=%v, %d chunk(s) of a.go left; want the error and the old chunks kept", err, kept)
		}
		if !fail && (err != nil || kept != 1) {
			t.Errorf("a re-index: err=%v, %d chunk(s) of a.go; want exactly the new one", err, kept)
		}
	}
}
