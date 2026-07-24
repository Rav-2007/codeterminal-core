package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockTestWorkspace makes a temp workspace and returns it symlink-resolved, the
// form every writer keys the lock on (see ResolveRealWorkspaceRoot). t.TempDir
// can hand back a path with a symlinked prefix, which would otherwise key two
// callers onto two different lock files.
func lockTestWorkspace(t *testing.T) string {
	t.Helper()
	root, err := ResolveRealWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp workspace: %v", err)
	}
	return root
}

// TestApply_ConcurrentSameFileNeverLosesAnAppliedEdit is the M4 regression: the
// read-modify-write lost update, reproduced through the real Apply door.
//
// Two edits to DIFFERENT regions of the SAME file are prepared against the same
// original, then applied concurrently. Each one's NewContent is the whole file
// with only its own span replaced, so if both writes land, the second wholesale
// overwrite silently destroys the first's edit — while BOTH Apply calls report
// success. That is the 100%-reproducible race Gate 6 closed for two socket
// requests and which reopened for the CLI and TUI, each of which applies from
// its own process where the daemon's in-memory mutex cannot reach.
//
// The invariant asserted is the honest one: every Apply that returned nil must
// have its edit present in the final file. Serialized, exactly one succeeds and
// the loser is refused as stale (VerifyUnchanged sees the winner's bytes) — no
// silent loss. Unserialized, both return nil and only one edit survives.
func TestApply_ConcurrentSameFileNeverLosesAnAppliedEdit(t *testing.T) {
	// The unlocked window (VerifyUnchanged -> write) is narrow, so this runs
	// many rounds: one is enough to prove correctness when locked, but the
	// neutered build needs repetition to hit the race reliably.
	const rounds = 30

	for round := 0; round < rounds; round++ {
		root := lockTestWorkspace(t)
		const original = "AAA\nBBB\n"
		target := filepath.Join(root, "f.txt")
		if err := os.WriteFile(target, []byte(original), 0644); err != nil {
			t.Fatalf("seeding target: %v", err)
		}

		first, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "AAA", Replace: "XXX"})
		if err != nil {
			t.Fatalf("preparing first edit: %v", err)
		}
		second, err := PrepareEdit(root, EditBlock{FilePath: "f.txt", Search: "BBB", Replace: "YYY"})
		if err != nil {
			t.Fatalf("preparing second edit: %v", err)
		}

		backupDir, err := NewBackupSessionDir(root)
		if err != nil {
			t.Fatalf("creating backup session: %v", err)
		}

		prepared := []*PreparedEdit{first, second}
		results := make([]error, len(prepared))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, p := range prepared {
			wg.Add(1)
			go func(i int, p *PreparedEdit) {
				defer wg.Done()
				<-start // release both at once, to actually contend
				results[i] = Apply(root, p, backupDir)
			}(i, p)
		}
		close(start)
		wg.Wait()

		final, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("reading target after applies: %v", err)
		}
		want := []string{"XXX", "YYY"}
		for i, res := range results {
			if res == nil && !strings.Contains(string(final), want[i]) {
				t.Fatalf("round %d: Apply #%d reported SUCCESS but its edit %q is not in the file (content=%q) — a concurrent write silently destroyed it (lost update)",
					round, i, want[i], string(final))
			}
		}
	}
}

// TestNewBackupSessionDir_ConcurrentCallsGetDistinctSessions covers the
// backup-session collapse race: NewBackupSessionDir stats for an unused
// timestamp then MkdirAlls it, so two runs starting in the same second both
// find it absent and both create it — sharing ONE session dir and interleaving
// their before/after snapshots, which corrupts what undo later restores from.
func TestNewBackupSessionDir_ConcurrentCallsGetDistinctSessions(t *testing.T) {
	root := lockTestWorkspace(t)
	// Deliberately not more than the retention cap. Past it, prune legitimately
	// deletes the oldest session and a later call may reuse that now-free name —
	// which is name reuse after a completed prune, NOT two LIVE sessions sharing
	// one directory. Only the latter is the collapse bug under test here.
	const n = backupSessionsToKeep

	dirs := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			dirs[i], errs[i] = NewBackupSessionDir(root)
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[string]int, n)
	for i, dir := range dirs {
		if errs[i] != nil {
			t.Fatalf("NewBackupSessionDir #%d: %v", i, errs[i])
		}
		if prev, dup := seen[dir]; dup {
			t.Fatalf("concurrent runs #%d and #%d were handed the SAME backup session dir %q — their before/after snapshots would collapse into one session", prev, i, dir)
		}
		seen[dir] = i
	}
}

// TestLockWorkspaceApply_ExcludesASecondHolder proves the primitive itself
// actually excludes, rather than silently succeeding twice — the property every
// caller above depends on. A second acquisition must not complete while the
// first is held, and must complete once it is released.
func TestLockWorkspaceApply_ExcludesASecondHolder(t *testing.T) {
	root := lockTestWorkspace(t)

	release, err := LockWorkspaceApply(root)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		secondRelease, err := LockWorkspaceApply(root)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		secondRelease()
	}()

	select {
	case <-acquired:
		release()
		t.Fatal("a second holder acquired the workspace apply lock while it was already held — it does not exclude, so nothing it guards is serialized")
	case <-time.After(150 * time.Millisecond):
		// Correctly blocked.
	}

	release()

	select {
	case <-acquired:
		// Released and handed over, as it must be.
	case <-time.After(2 * time.Second):
		t.Fatal("second holder never acquired the lock after it was released — the lock is not being handed over")
	}
}

// TestLockWorkspaceApply_DifferentWorkspacesDoNotBlockEachOther pins the keying
// decision: the lock is per resolved workspace root, so unrelated workspaces
// must never serialize against each other.
func TestLockWorkspaceApply_DifferentWorkspacesDoNotBlockEachOther(t *testing.T) {
	first := lockTestWorkspace(t)
	second := lockTestWorkspace(t)

	releaseFirst, err := LockWorkspaceApply(first)
	if err != nil {
		t.Fatalf("acquiring first workspace: %v", err)
	}
	defer releaseFirst()

	done := make(chan struct{})
	go func() {
		releaseSecond, err := LockWorkspaceApply(second)
		if err != nil {
			t.Errorf("acquiring second workspace: %v", err)
		} else {
			releaseSecond()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("locking one workspace blocked locking a DIFFERENT one — the lock is too coarse and would serialize unrelated workspaces")
	}
}
