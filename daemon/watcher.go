package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mochiii/editapply"

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

// watcherDebounce coalesces the burst an editor produces around a single save
// -- a truncate, a write, sometimes a rename over the top -- into one reindex.
const watcherDebounce = 1 * time.Second

// indexKeyFor turns a filesystem event path into the index key for that file:
// workspace-relative and forward-slash, the form Chunk.FilePath is documented to
// hold on every platform. The second result is false when the path is not
// something this daemon should ever index.
//
// The ".." check is not decoration. filepath.Rel will happily answer
// "../../etc/passwd" for a path outside the workspace, and every store in this
// daemon would accept that string as a key -- the same guard ScanWorkspace's
// walk applies for the same reason. The link fix above means such a path should
// no longer be reachable; this is the layer that does not depend on that being
// true.
func (s *Server) indexKeyFor(path string) (string, bool) {
	rel, err := filepath.Rel(s.workspace, path)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	// Dotfiles are refused here as well as by the eligibility gate: this filter
	// is the only thing between a .env save and a read of it, and defence in
	// depth is the point.
	if strings.HasPrefix(filepath.Base(rel), ".") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// applyIndexUpdate brings the index back in line with one path. Called only
// from the watcher's single worker goroutine, so two updates to one file can
// never overlap.
func (s *Server) applyIndexUpdate(relSlash string, gone bool) {
	if gone {
		s.dropFromIndex(relSlash)
		return
	}
	s.logger.Printf("watcher: reindexing %s after file change", relSlash)
	// The daemon's shutdown context, not context.Background(): a reindex in
	// flight when the daemon is stopping should stop too. reindexFile applies
	// its own reindexTimeout on top -- the two-minute bound this used to pass
	// was dead code, since the inner 30 s always won first.
	if err := s.reindexFile(s.shutdownContext(), s.workspace, relSlash); err != nil {
		s.logger.Printf("watcher: background reindex failed for %s: %v", relSlash, err)
	}
}

// dropFromIndex removes every chunk of a file that no longer exists.
//
// Not reindexFile: that path deletes and then re-reads, so on a deleted file it
// does the right thing and then returns an error about the file being missing --
// a correct outcome reported as a failure, in a log line an operator has to
// learn to ignore. Deletion is its own intent and says so.
func (s *Server) dropFromIndex(relSlash string) {
	if s.store == nil {
		return // retrieval not configured for this daemon; nothing to keep current
	}
	s.logger.Printf("watcher: dropping %s from the index after delete", relSlash)
	ctx, cancel := context.WithTimeout(s.shutdownContext(), reindexTimeout)
	defer cancel()
	if err := s.store.DeleteByFilePath(ctx, relSlash); err != nil {
		s.logger.Printf("watcher: dropping %s from the vector index failed (it will keep matching "+
			"queries with code that no longer exists until the next `index` run): %v", relSlash, err)
	}
	if s.lexicalStore != nil {
		if err := s.lexicalStore.DeleteByFilePath(ctx, relSlash); err != nil {
			s.logger.Printf("watcher: dropping %s from the lexical index failed: %v", relSlash, err)
		}
	}
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

		// DEBOUNCE, then QUEUE, then ONE WORKER. Three jobs that used to be one
		// time.AfterFunc doing all of them, with a defect each.
		//
		// UNBOUNDED FAN-OUT. AfterFunc runs its function in a NEW GOROUTINE, and
		// the function embedded the file inline. One goroutine per changed path
		// meant a `git checkout` across a thousand files fired a thousand
		// concurrent embeds at a single helper subprocess holding a single model.
		// Embedding is the most expensive thing this daemon does; nothing
		// upstream of it was bounded.
		//
		// DUPLICATE CHUNKS. reindexFile is delete-then-insert, so two runs over
		// one path can interleave as delete-A, delete-B, insert-A, insert-B and
		// leave that file's chunks in the index TWICE -- the precise corruption
		// DeleteByFilePath exists to prevent, reintroduced by concurrency.
		//
		// A SET, NOT A CHANNEL of paths. A file saved fifty times while the
		// worker is busy is one unit of work, and a bounded channel would have to
		// choose between blocking this event loop (which then misses events) and
		// silently dropping a change (which leaves the index stale with no
		// signal). A set collapses repeats for free and cannot overflow, because
		// its size is bounded by the number of distinct paths in the workspace.
		//
		// The value is "this file is gone", so the LAST intent wins: written
		// then deleted resolves to a drop, deleted then re-created to a reindex.
		pending := make(map[string]*time.Timer)
		var pendingMu sync.Mutex

		updates := make(map[string]bool)
		var updatesMu sync.Mutex
		wake := make(chan struct{}, 1)

		enqueue := func(relSlash string, gone bool) {
			updatesMu.Lock()
			updates[relSlash] = gone
			updatesMu.Unlock()
			select {
			case wake <- struct{}{}:
			default: // already signalled; the drain it triggers will see this path
			}
		}

		// The single worker. Everything that touches the index goes through here,
		// in order, one at a time.
		go func() {
			for {
				select {
				case <-s.shutdownContext().Done():
					return
				case <-wake:
				}
				for {
					updatesMu.Lock()
					var relSlash string
					var gone, ok bool
					for k, v := range updates {
						relSlash, gone, ok = k, v, true
						delete(updates, k)
						break
					}
					updatesMu.Unlock()
					if !ok {
						break
					}
					s.applyIndexUpdate(relSlash, gone)
				}
			}
		}()

		for {
			select {
			case <-s.shutdownContext().Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// DELETES AND RENAMES, which this loop ignored entirely.
				//
				// Only Write and Create were ever examined, so a deleted file's
				// chunks stayed in the index for the life of the daemon -- still
				// matching queries, still being handed to the model as grounded
				// context, describing code that no longer exists. Renames are the
				// same event from the index's point of view: fsnotify reports the
				// OLD path as a Rename and the new one as a Create, so dropping the
				// old key here and letting the Create re-index the new one is the
				// whole of it.
				//
				// Deleting a DIRECTORY is covered only to the extent that its files
				// were watched: rm and git both unlink the contents first, and each
				// of those arrives as its own Remove. A tree removed under an
				// UNwatched directory (past maxWatchedDirs, or one the walk skipped)
				// still needs the next full `index` run.
				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					relSlash, ok := s.indexKeyFor(event.Name)
					if !ok {
						continue
					}
					// A debounced write for this path is now moot, and letting it
					// fire would re-read a file that is gone and log a failure about
					// it.
					pendingMu.Lock()
					if timer, exists := pending[relSlash]; exists {
						timer.Stop()
						delete(pending, relSlash)
					}
					pendingMu.Unlock()
					enqueue(relSlash, true)
					continue
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

					relSlash, ok := s.indexKeyFor(event.Name)
					if !ok {
						continue
					}

					pendingMu.Lock()
					if timer, exists := pending[relSlash]; exists {
						timer.Stop()
					}
					// The timer is created and recorded under the lock so the
					// callback's read of `t` below has a happens-before edge to this
					// write -- otherwise it is a data race the detector is right to
					// flag, however wide the one-second gap looks.
					var t *time.Timer
					t = time.AfterFunc(watcherDebounce, func() {
						// Forgotten BEFORE the work is queued, and only if it is
						// still THIS timer.
						//
						// It used to be deleted after a reindex that could take
						// minutes, by which time a later write had installed a new
						// timer under the same key -- which that delete then removed,
						// leaving a live timer nothing could cancel and a map entry
						// that no longer described reality.
						pendingMu.Lock()
						if pending[relSlash] == t {
							delete(pending, relSlash)
						}
						pendingMu.Unlock()
						enqueue(relSlash, false)
					})
					pending[relSlash] = t
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
