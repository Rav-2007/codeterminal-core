package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
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
					stat, err := os.Stat(event.Name)
					if err == nil && stat.IsDir() {
						// It's a new directory, add it to the watcher if under limit.
						pendingMu.Lock()
						if dirCount < maxWatchedDirs {
							// Same rule as the initial walk: only count what is
							// really being watched.
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
