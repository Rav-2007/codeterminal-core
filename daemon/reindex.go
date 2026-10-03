package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// reindexTimeout bounds one file's re-embed so a wedged embedder helper can
// never hold an apply response (or the workspace lock) open indefinitely. A
// single edited file is a handful of chunks, orders of magnitude below the
// whole-workspace index this shares its embedder with.
const reindexTimeout = 30 * time.Second

// reindexFile brings the index back in line with one file's current on-disk
// content (Fix 5).
//
// Without this the index kept whatever the file looked like when `index` last
// ran, so the assistant reasoned about PRE-EDIT code on the very next turn —
// verified in the review by an edit that changed pathPrefix from "FILE:" to
// "path:" and a follow-up turn that was still served the old value. It poisons
// every subsequent turn in a session, and it gets worse the more the assistant
// is actually used.
//
// Scoped deliberately to the one file the apply path already knows changed: no
// workspace walk, no mtime sweep, no full re-embed. The delete comes first and
// covers the whole file, because chunk IDs encode line ranges — re-upserting
// alone would leave a shrinking file's former tail chunks behind, still
// matching queries with code that no longer exists.
//
// A file that has become ineligible since it was indexed (now gitignored, now
// too large, now binary, now secret-named) takes the same path: its chunks are
// deleted and nothing replaces them, which is the correct end state. Callers
// treat any error here as non-fatal — see reindexAfterApply.
func (s *Server) reindexFile(ctx context.Context, realRoot, relPath string) error {
	if s.embedder == nil || s.store == nil {
		return nil // retrieval not configured for this daemon; nothing to keep current
	}

	ctx, cancel := context.WithTimeout(ctx, reindexTimeout)
	defer cancel()

	// THE OLD CHUNKS GO AS LATE AS POSSIBLE -- after the new ones are embedded,
	// immediately before they are written. Deleting first left the file out of
	// search for as long as embedding took, and the startup catch-up made that
	// window the common case rather than a race: MEASURED 2026-09-29, a question
	// about retry.go arriving while it was being caught up found no retry.go at
	// all. The delete still covers the whole file, so a shrunk file's old tail
	// goes too; and if embedding fails the old chunks stay searchable, their text
	// kept current at query time by refreshHitsFromDisk.
	drop := func() error {
		if err := s.store.DeleteByFilePath(ctx, relPath); err != nil {
			return err
		}
		if s.lexicalStore != nil {
			return s.lexicalStore.DeleteByFilePath(ctx, relPath)
		}
		return nil
	}

	// relPath is an index key, so it is forward-slash on every platform (see the
	// Chunk doc comment). Everything below this line touches the filesystem or
	// the gitignore matcher, both of which want the native separator — so the
	// conversion happens here, once, and the key itself is never rewritten.
	nativeRel := filepath.FromSlash(relPath)

	// Re-read through the indexer's own eligibility gate, so a file the walk
	// would skip is never admitted by this shorter path.
	content, _, skip, err := readEligibleFile(filepath.Join(realRoot, nativeRel), nativeRel, newGitignoreMatcher(realRoot))
	if err != nil {
		// Gone or unreadable: nothing of it should stay searchable.
		if dropErr := drop(); dropErr != nil {
			return dropErr
		}
		return fmt.Errorf("re-reading %s: %w", relPath, err)
	}
	if skip {
		return drop()
	}

	chunks := chunkContent(content, relPath)
	if len(chunks) == 0 {
		return drop()
	}

	vecs, err := s.embedder.Embed(ctx, embedTextsFor(chunks))
	if err != nil {
		return fmt.Errorf("re-embedding %s: %w", relPath, err)
	}
	for i := range chunks {
		chunks[i].Vector = vecs[i]
	}

	if err := drop(); err != nil {
		return err
	}
	if err := s.store.Upsert(ctx, chunks); err != nil {
		return err
	}
	if s.lexicalStore != nil {
		if err := s.lexicalStore.Upsert(ctx, chunks); err != nil {
			return err
		}
	}
	return nil
}

// reindexAfterApply is the apply path's hook into reindexFile. It is called
// ONLY after a write that actually landed: a refused or failed apply left the
// file exactly as the index already describes it (Fix 1 guarantees that much —
// a failed apply does not mutate), so re-indexing there would be pure cost, and
// worse, would make a failure look like a change.
//
// Failure here is logged and swallowed. The edit is on disk and the response
// says so; degrading to a stale index for that one file is strictly better than
// telling a client its applied edit failed. The next full `index` run repairs
// it either way.
//
// Runs synchronously, inside the per-workspace lock the apply handler already
// holds. Doing it in the background would race the very thing this fixes: the
// next prompt can arrive immediately, and would then be served the stale chunks
// this call exists to replace.
func (s *Server) reindexAfterApply(realRoot, relPath string) {
	if s.embedder == nil || s.store == nil {
		return
	}
	started := time.Now()
	if err := s.reindexFile(context.Background(), realRoot, relPath); err != nil {
		s.logger.Printf("apply-edit: re-index of %s failed (edit is applied; index is stale for this file until the next `index` run): %v", relPath, err)
		return
	}
	s.logger.Printf("apply-edit: re-indexed %s in %s", relPath, time.Since(started).Round(time.Millisecond))
}

// maxCatchUpFiles bounds the startup catch-up. Above it, re-embedding in the
// background would hold the embedder for many minutes while the user is asking
// questions of it; a full `index` is the right tool, and the log says so.
const maxCatchUpFiles = 500

// catchUpIndex re-indexes the files that changed while no daemon was running.
//
// The watcher keeps the index current from the moment it starts, and nothing
// covered the time before: an edit between sessions, a checkout, a pull. Those
// files kept their old chunks indefinitely -- the user's own index was eight
// days and 338 files behind when this was written -- so retrieval ranked on,
// and until refreshHitsFromDisk quoted, code that no longer existed.
//
// It runs ONCE, FIRST, inside the watcher's single worker goroutine, so it is
// serialised with every live save and cannot race one. The list is the
// freshness scan's own (scanChangedFiles): the files /status counts as changed
// are exactly the files re-indexed here. When every one succeeded and the scan
// saw the whole workspace, the stamp's BuiltAt moves to when this began -- files
// saved since are the watcher's -- and /status stops reporting a staleness that
// is no longer true. Deleted files leave chunks behind (the vector store cannot
// list by file); refreshHitsFromDisk drops those at query time.
func (s *Server) catchUpIndex() {
	if s.embedder == nil || s.store == nil || s.workspace == "" {
		return // retrieval not configured for this daemon; nothing to keep current
	}
	s.purgePrunedFromIndex()

	indexDir := filepath.Join(s.workspace, indexDirName)
	builtAt := readStampBuiltAt(indexDir)
	if builtAt.IsZero() {
		return // an index from before stamps carried a time: when it was built is unknown
	}
	began := time.Now()
	_, changed, complete := scanChangedFiles(s.workspace, builtAt, true)
	if len(changed) == 0 {
		return
	}
	if len(changed) > maxCatchUpFiles {
		s.logger.Printf("index: %d file(s) changed since the index was built at %s -- too many to catch up in the background; "+
			"run `mochiii-daemon index` to rebuild it", len(changed), builtAt.UTC().Format(time.RFC3339))
		return
	}
	s.logger.Printf("index: re-indexing %d file(s) that changed while the daemon was not running", len(changed))
	failed := 0
	for _, rel := range changed {
		ctx := s.shutdownContext()
		if ctx.Err() != nil {
			return
		}
		if err := s.reindexFile(ctx, s.workspace, rel); err != nil {
			if ctx.Err() != nil {
				return // the daemon is stopping; the next start picks this up
			}
			failed++
			s.logger.Printf("index: catch-up re-index of %s failed: %v", rel, err)
		}
	}
	if failed == 0 && complete {
		if err := advanceStampBuiltAt(indexDir, began); err != nil {
			s.logger.Printf("index: caught up, but recording when failed (the next start will check these files again): %v", err)
		}
		if s.freshness != nil {
			s.freshness.forget()
		}
	}
	s.logger.Printf("index: caught up %d file(s) in %s (%d failed)", len(changed)-failed,
		time.Since(began).Round(time.Millisecond), failed)
}

// purgePrunedFromIndex drops from the index every file the walk would never
// have indexed: anything under a build-output or dependency directory
// (underPrunedDir).
//
// The gate refuses those files now, but until 2026-10 the watcher and the
// re-index after an edit admitted them -- this repository's own index held 62
// files from clients/vscode/out -- and a refusal only stops new ones. Left
// alone, the ones already there would keep taking result slots until the next
// full `index`, which nothing prompts anyone to run. So once per start, before
// catching up, the index is asked what it holds and those files are dropped.
// Steady-state cost is one read of the chunk IDs and nothing else.
func (s *Server) purgePrunedFromIndex() {
	ctx, cancel := context.WithTimeout(s.shutdownContext(), reindexTimeout)
	defer cancel()

	var ids []string
	var err error
	if s.lexicalStore != nil {
		ids, err = s.lexicalStore.AllIDs(ctx) // a column read; the vector store needs a probe query
	} else {
		ids, err = s.store.AllIDs(ctx, s.embedder.Dim())
	}
	if err != nil {
		s.logger.Printf("index: could not list the index to drop build output from it: %v", err)
		return
	}
	paths := map[string]bool{}
	for _, id := range ids {
		if p := filePathOfChunkID(id); p != "" && underPrunedDir(p) {
			paths[p] = true
		}
	}
	if len(paths) == 0 {
		return
	}
	failed := 0
	for p := range paths {
		if err := s.store.DeleteByFilePath(ctx, p); err != nil {
			failed++
			continue
		}
		if s.lexicalStore != nil {
			if err := s.lexicalStore.DeleteByFilePath(ctx, p); err != nil {
				failed++
			}
		}
	}
	s.logger.Printf("index: dropped %d file(s) under build-output or dependency directories, which the index "+
		"should never have held (%d failed)", len(paths)-failed, failed)
}
