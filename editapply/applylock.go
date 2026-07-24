package editapply

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// applyLockName is the per-workspace lock file backing LockWorkspaceApply. It
// lives beside backups/ under .codeterminal — a directory the indexer already
// prunes and the edit writer already refuses to target (ProtectedDirNames), so
// the lock file is never indexed, never sent to a model, and never writable by
// an edit block.
//
// The file is created once and then left in place forever. It is deliberately
// never deleted on release: flock() locks an inode, so a release-then-unlink
// cycle lets a second process acquire a lock on an inode the first has already
// unlinked, and the two would then hold "the same" lock simultaneously. An
// empty, permanently-present lock file is the standard, correct shape.
const applyLockName = "apply.lock"

// LockWorkspaceApply acquires the exclusive, CROSS-PROCESS lock serializing
// workspace-mutating apply/undo operations on realWorkspaceRoot, and returns
// the release function (meant to be deferred immediately).
//
// WHY THIS EXISTS (the gap it closes). FAIL-3 Gate 6 closed five data-integrity
// races on the Apply/Undo path — read-modify-write lost update, shared
// backup-session collapse, undo-guard defeat, double restore, and prune-vs-undo
// — with the daemon's per-workspace sync.Mutex (Server.applyLocks). That lock is
// IN-PROCESS ONLY, and its own doc-comment justified that with "there is exactly
// one daemon process behind the socket". That premise is false: there are THREE
// independent writers of the same workspace files, and only one of them is the
// daemon —
//
//  1. the daemon's socket handlers (handleApplyEdit/handleUndo)  — took the mutex
//  2. the CLI `edits apply` / `edits undo` (daemon/apply_cmd.go)  — a SEPARATE PROCESS
//  3. the Mochiii TUI's review flow (clients/tui/chat.go)         — a SEPARATE PROCESS
//
// so every one of those five races reproduced again the moment a CLI or TUI run
// overlapped a daemon one — which is normal, legitimate use (edit in the IDE
// while a terminal apply is open), not an exotic attack. An in-memory mutex
// cannot serialize separate processes; only the filesystem can.
//
// WHY flock. The lock must survive the holder being killed: a crashed CLI must
// not wedge the workspace forever. flock(2) is released by the kernel when the
// file descriptor closes, INCLUDING on process death, so there is no stale-lock
// problem and no lock-breaking heuristic to get wrong (an O_EXCL "lock file"
// would need exactly that, and would strand on SIGKILL). Each acquisition opens
// its OWN descriptor, so the lock also serializes goroutines within one process
// (flock is per open file description, not per process) — which is what lets a
// single mechanism cover both the cross-process and the in-process case.
//
// SCOPE — what callers must hold this across. It is taken INSIDE the three
// mutation primitives rather than at their call sites, precisely because the bug
// being fixed is a caller that forgot to take a lock. Every writer therefore
// gets it whether or not it remembers to ask:
//
//	Apply                 — VerifyUnchanged -> backups -> write (the lost-update TOCTOU)
//	NewBackupSessionDir   — the stat/mkdir mint plus its prune (session collapse)
//	runUndoSession        — the guard check through the commit (undo-guard defeat)
//
// Each of those spans is atomic with respect to the others, which is all five
// races need; the primitives do NOT need to be locked together as one unit,
// because Apply independently re-checks staleness byte-for-byte (VerifyUnchanged)
// and so refuses rather than clobbers if another process landed in between.
//
// It is deliberately NOT held across human confirmation. Every span above is
// non-interactive, so a CLI or TUI user sitting on a [y/N] prompt never blocks
// the daemon. (The one exception is the CLI undo's rare "overwrite changed
// files?" prompt, which sits inside runUndoSession's guarded branch; the daemon's
// own undo never prompts.)
//
// It BLOCKS until acquired. The spans are short and non-interactive, so waiting
// is correct: the alternative — failing fast — would surface a spurious error on
// a legitimate concurrent apply, which is exactly the outcome serialization
// exists to avoid.
//
// Keyed by the resolved workspace ROOT, matching Server.applyLocks: not per-file
// (too fine — misses cross-file backup-session bookkeeping) and not global (too
// coarse — would serialize unrelated workspaces). Callers must pass an already
// symlink-resolved root (see ResolveRealWorkspaceRoot), so two clients naming the
// same workspace by different paths land on the same lock file.
func LockWorkspaceApply(realWorkspaceRoot string) (release func(), err error) {
	dir := filepath.Join(realWorkspaceRoot, ".codeterminal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}

	// O_NOFOLLOW on the leaf, matching every other writer in this tree: a symlink
	// pre-planted at the lock path is refused rather than followed. The path is
	// constant and workspace-local (never client-supplied), so this is
	// defense-in-depth, not confinement.
	path := filepath.Join(dir, applyLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening apply lock %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	return func() {
		// Closing the descriptor releases the flock on its own; the explicit
		// LOCK_UN just makes the release a stated act rather than a side effect.
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
