package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codeterminal/editapply"

	"github.com/fsnotify/fsnotify"
)

// maxWatchedDirs bounds how many directories the watcher will register.
//
// Every watched directory costs one inotify watch, drawn from a per-user kernel
// budget (fs.inotify.max_user_watches) shared with every other program on the
// machine — an editor, a language server, a bundler. Exhausting it does not
// just break this daemon, it breaks whatever asks next, so the ceiling is here
// to be a good neighbour rather than to protect ourselves.
const maxWatchedDirs = 5000

// watchableDirName reports whether a directory of this name is worth an inotify
// watch.
//
// ONE PREDICATE, TWO CALLERS, and that is the point of extracting it. The
// initial walk had this rule inline and the Create handler had no rule at all,
// so a directory that appeared after startup was watched whatever it was called
// -- `git init` in a subdirectory, an npm install, a build that materialises its
// own output tree. The walk skips those precisely because they churn, and a
// watcher that agrees with the walk only until the workspace changes shape is
// not a watcher that agrees with the walk.
func watchableDirName(name string) bool {
	return !strings.HasPrefix(name, ".") && name != "node_modules" && name != "vendor"
}

// startWorkspaceWatcher starts a recursive filesystem watcher on the workspace.
// When a file is written (e.g. by the user in VS Code), it triggers a background
// re-embedding via reindexFile to keep the vector store perfectly synced.
func (s *Server) startWorkspaceWatcher() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.logger.Printf("watcher: failed to create fsnotify watcher: %v", err)
		return
	}

	// Walk the workspace and add all directories to the watcher.
	// Skip hidden directories (like .git) and node_modules.
	dirCount := 0
	failed := 0
	err = filepath.WalkDir(s.workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if dirCount >= maxWatchedDirs {
				s.logger.Printf("watcher: reached the %d directory limit, stopping recursive watch to prevent inotify exhaustion", maxWatchedDirs)
				return filepath.SkipAll
			}
			if !watchableDirName(d.Name()) {
				return filepath.SkipDir
			}
			// Only count a directory we are ACTUALLY watching. Incrementing
			// regardless made the budget a count of attempts, so a workspace
			// that exhausted the kernel's inotify quota would stop walking
			// early with most of itself unwatched.
			if addErr := watcher.Add(path); addErr != nil {
				failed++
				return nil
			}
			dirCount++
		}
		return nil
	})
	if err != nil {
		s.logger.Printf("watcher: failed to walk workspace for watcher: %v", err)
		_ = watcher.Close()
		return
	}
	// Say so rather than degrading in silence. A watcher covering half the
	// workspace looks identical to one covering all of it right up until the
	// assistant answers from a stale index -- the failure mode the whole
	// freshness effort exists to prevent.
	if failed > 0 {
		s.logger.Printf("watcher: %d directories could not be watched (often fs.inotify.max_user_watches); "+
			"saves under them will not refresh the index until the next full `index` run", failed)
	}

	go func() {
		defer func() { _ = watcher.Close() }()

		// debounce map to avoid slamming the embedder on every keystroke save
		pending := make(map[string]*time.Timer)
		var pendingMu sync.Mutex

		for {
			select {
			case <-s.shutdownContext().Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// We care about writes (saves in VS Code) and creations.
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					// os.LSTAT, NEVER os.Stat, and editapply/linkmode.go says so in
					// as many words: "Pass fs.DirEntry.Type() or the result of an
					// LSTAT -- never a Stat, which has already followed the link and
					// will answer about the target."
					//
					// This asked os.Stat. A symlink to a directory therefore came
					// back IsDir, was handed to watcher.Add, and registered an
					// inotify watch on a directory OUTSIDE the workspace. Every write
					// behind it then arrived as <workspace>/<link>/<file>, survived
					// filepath.Rel, and reached reindexFile -- which reads the file
					// and puts its content in the retrieval index. Index content
					// becomes prompt context, and prompt context leaves the machine.
					//
					// ScanWorkspace has always refused link-like entries, and the
					// walk above inherits that for free (WalkDir reports a link by
					// its own type, so d.IsDir() is false and it never descends).
					// This handler was the one incremental door the indexer's
					// confinement did not cover.
					//
					// IsLinkLike rather than ModeSymlink for the reason
					// editapply/linkmode.go exists: a Windows junction is
					// ModeIrregular and needs no privilege to create. And none of
					// this needs an attacker -- `ln -s ../shared lib` is ordinary,
					// and so is checking out a branch that contains one.
					//
					// Placed before the IsDir branch so it covers a linked FILE too,
					// which readEligibleFile would refuse anyway; refusing here as
					// well costs nothing and keeps one rule in one place.
					info, err := os.Lstat(event.Name)
					if err == nil && editapply.IsLinkLike(info.Mode()) {
						continue
					}
					if err == nil && info.IsDir() {
						// A directory that appeared after the walk. Same rules as the
						// walk, via the same predicate: only watch what the walk
						// would have watched, and only count what is really watched.
						if !watchableDirName(info.Name()) {
							continue
						}
						pendingMu.Lock()
						if dirCount < maxWatchedDirs {
							if err := watcher.Add(event.Name); err == nil {
								dirCount++
							}
						}
						pendingMu.Unlock()
						continue
					}

					// Debounce file writes.
					rel, err := filepath.Rel(s.workspace, event.Name)
					if err != nil {
						continue
					}

					// Ignore hidden files or obvious build artifacts
					if strings.HasPrefix(filepath.Base(rel), ".") {
						continue
					}

					// Convert to forward slashes for the index key
					relSlash := filepath.ToSlash(rel)

					pendingMu.Lock()
					if timer, exists := pending[relSlash]; exists {
						timer.Stop()
					}
					pending[relSlash] = time.AfterFunc(1*time.Second, func() {
						// Re-index in the background
						s.logger.Printf("watcher: reindexing %s after file change", relSlash)
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
						if err := s.reindexFile(ctx, s.workspace, relSlash); err != nil {
							s.logger.Printf("watcher: background reindex failed for %s: %v", relSlash, err)
						}
						cancel()

						// Prevent memory leak by removing the timer once it fires
						pendingMu.Lock()
						delete(pending, relSlash)
						pendingMu.Unlock()
					})
					pendingMu.Unlock()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				s.logger.Printf("watcher: error: %v", err)
			}
		}
	}()
}
