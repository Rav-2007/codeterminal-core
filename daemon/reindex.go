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

	if err := s.store.DeleteByFilePath(ctx, relPath); err != nil {
		return err
	}
	if s.lexicalStore != nil {
		if err := s.lexicalStore.DeleteByFilePath(ctx, relPath); err != nil {
			return err
		}
	}

	// Re-read through the indexer's own eligibility gate, so a file the walk
	// would skip is never admitted by this shorter path.
	content, _, skip, err := readEligibleFile(filepath.Join(realRoot, relPath), relPath, newGitignoreMatcher(realRoot))
	if err != nil {
		return fmt.Errorf("re-reading %s: %w", relPath, err)
	}
	if skip {
		return nil
	}

	chunks := chunkContent(content, relPath)
	if len(chunks) == 0 {
		return nil
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}
	vecs, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("re-embedding %s: %w", relPath, err)
	}
	for i := range chunks {
		chunks[i].Vector = vecs[i]
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
