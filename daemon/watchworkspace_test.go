package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// holdWatcherWalk makes the next watcher walk signal when it begins and then
// wait for the returned release. The release also runs at cleanup, so a test
// that fails early does not leave the watcher goroutine blocked.
func holdWatcherWalk(t *testing.T) (started <-chan struct{}, release func()) {
	t.Helper()
	begun := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	saved := watcherWalkStarting
	watcherWalkStarting = func() {
		close(begun)
		<-gate
	}
	t.Cleanup(func() {
		release()
		watcherWalkStarting = saved
	})
	return begun, release
}

// THE POINT OF IT: a daemon answers without waiting for its watcher's walk.
// The walk over a big tree took longer than the client's 30 s on Windows, and
// the person saw "did not answer within 30s" (watcher.go, watchWorkspace).
//
// Neuter check: make watchWorkspace call startWorkspaceWatcher directly instead
// of in a goroutine, and this test fails at its 5 s deadline.
func TestWatchingTheWorkspaceDoesNotWaitForTheWalk(t *testing.T) {
	started, release := holdWatcherWalk(t)
	s, _, _ := newWatcherServer(t)
	s.embedder, s.store = &fakeEmbedder{dim: embedDim}, &recordingStore{}

	returned := make(chan struct{})
	go func() {
		s.watchWorkspace()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("watchWorkspace waited for the watcher's walk: the daemon would not answer until it finished")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("watchWorkspace returned but no watcher started: an indexed workspace would never be kept current")
	}
	release()
}

// No index, no walk: there is nothing for the watcher to keep current, and the
// home folder -- never offered for indexing -- is where the walk cost most.
//
// Neuter check: drop the store check in watchWorkspace, and the walk starts.
func TestAWorkspaceWithNoIndexIsNotWalked(t *testing.T) {
	started, _ := holdWatcherWalk(t)
	var mu sync.Mutex
	var logged []string
	s, _, _ := newWatcherServer(t)
	s.logger = capturingLogger(&mu, &logged)

	s.watchWorkspace()
	select {
	case <-started:
		t.Fatal("a daemon with no index walked its workspace to watch it")
	case <-time.After(300 * time.Millisecond):
	}
	mu.Lock()
	all := strings.Join(logged, "\n")
	mu.Unlock()
	if !strings.Contains(all, "watcher: off") {
		t.Errorf("the log does not say the watcher is off, or why: %q", all)
	}
}
