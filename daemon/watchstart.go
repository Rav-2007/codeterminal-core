package main

import "time"

// watchWorkspace is how the daemon starts its watcher: in the background, and
// only when there is an index for it to keep current.
//
// THE WALK USED TO STAND BETWEEN A NEW DAEMON AND ITS FIRST ANSWER. main ran
// startWorkspaceWatcher before Serve, and its walk visits the workspace's
// directories until maxWatchedDirs of them are watched. On Windows each watch
// is a directory handle, and `mochiii` started in a user's home folder
// (2026-10-23, after `npm install -g mochiii`) walked into AppData and did not
// answer within the client's 30 s: "did not answer within 30s", and nothing else.
//
// NO INDEX, NO WATCHER. The watcher exists only to keep an index current
// (applyIndexUpdate). A daemon that started without one never picks one up --
// the client stops and restarts it after indexing (clients/tui/firstrun.go) --
// and a home folder or a drive root is never offered for indexing, so the walk
// there bought nothing at all.
//
// In a file of its own so that watcher.go keeps its exact lines: the retrieval
// eval's corpus is this repository, and moving watcher.go's text moved where
// its chunks fall (question 19 of rerank_eval_test.go lost its answer).
func (s *Server) watchWorkspace() {
	if s.store == nil || s.embedder == nil {
		s.logger.Print("watcher: off -- there is no index here to keep current")
		return
	}
	go func() {
		watcherWalkStarting()
		start := time.Now()
		s.startWorkspaceWatcher() // returns once its walk is done; the watching goes on in its own goroutine
		s.logger.Printf("watcher: started; its walk took %s", time.Since(start).Round(time.Millisecond))
	}()
}

// watcherWalkStarting runs just before the watcher's walk. Tests hold it to
// prove that nothing waits for the walk.
var watcherWalkStarting = func() {}
