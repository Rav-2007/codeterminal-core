// Per-workspace serialization lock — FAIL-3 Gate 6 (data integrity). lockWorkspace
// get-or-creates the *sync.Mutex guarding one resolved workspace root (kept in
// Server.applyLocks, whose field lives with the Server struct in server.go), held
// across the filesystem-mutating Apply/Undo/prune spans so two operations on the
// same workspace can't interleave. See BACKLOG.md, "Gate 6 deep audit" (commit d96794e).

package main

import (
	"sync"
)

// lockWorkspace acquires the per-workspace serialization lock for root and
// returns the release function, meant to be invoked with defer at the start of
// a critical section (defer s.lockWorkspace(root)()). It is the daemon's
// data-integrity guard for the filesystem-mutating request paths (FAIL-3,
// Gate 6): Apply, Undo, and the backup prune that runs inside Apply all take
// this lock for the whole span from their first read to their last write, so
// two such operations on the SAME workspace can never interleave — closing the
// read-modify-write lost update, the shared-backup-session collapse, the
// undo-guard defeat, the double-restore, and the prune-vs-undo race the Gate 6
// audit reproduced.
//
// It is keyed by resolved workspace ROOT — not per-file (too fine: misses the
// cross-file backup-session bookkeeping) and not global (too coarse: would
// serialize unrelated workspaces), so two operations on genuinely different
// workspaces get different mutexes and never block each other. All three
// operations mutate the filesystem — none is a pure reader — so an exclusive
// sync.Mutex is exactly right; an RWMutex would buy nothing. It is in-process
// only: there is exactly one daemon process behind the socket, so an in-memory
// lock fully covers the concurrency without a cross-process file lock. Each
// caller takes exactly this one lock and never nests another, so no acquisition
// ordering exists to deadlock on.
func (s *Server) lockWorkspace(root string) func() {
	m, _ := s.applyLocks.LoadOrStore(root, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
