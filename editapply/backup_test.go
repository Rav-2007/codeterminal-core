package editapply

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func mkBackupSessionDir(t *testing.T, backupsRoot, name string) string {
	t.Helper()
	dir := filepath.Join(backupsRoot, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return dir
}

func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func containsString(names []string, s string) bool {
	for _, n := range names {
		if n == s {
			return true
		}
	}
	return false
}

// TestNewBackupSessionDir_PrunesOldestWhenOverLimit covers the end-to-end
// path: 6 pre-existing session dirs (using fixed, unambiguously-past dates
// so ordering doesn't depend on wall-clock timing), then a real
// NewBackupSessionDir call -- which mints a 7th dir with today's real
// timestamp -- must leave exactly the newest 5 behind (the new dir plus
// the 4 newest of the 6 pre-existing ones), pruning the oldest 2.
func TestNewBackupSessionDir_PrunesOldestWhenOverLimit(t *testing.T) {
	root := realTempDir(t)
	backupsRoot := filepath.Join(root, ".mochiii", "backups")

	old := []string{
		"20200101-000001",
		"20200101-000002",
		"20200101-000003",
		"20200101-000004",
		"20200101-000005",
		"20200101-000006",
	}
	for _, name := range old {
		mkBackupSessionDir(t, backupsRoot, name)
	}

	newDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}

	remaining := readDirNames(t, backupsRoot)
	if len(remaining) != 5 {
		t.Fatalf("expected exactly 5 session dirs to remain, got %d: %v", len(remaining), remaining)
	}

	for _, prunedName := range old[:2] {
		if containsString(remaining, prunedName) {
			t.Errorf("expected oldest dir %q to be pruned, but it still exists: %v", prunedName, remaining)
		}
	}
	for _, keptName := range old[2:] {
		if !containsString(remaining, keptName) {
			t.Errorf("expected newer pre-existing dir %q to survive pruning, but it's gone: %v", keptName, remaining)
		}
	}
	if !containsString(remaining, filepath.Base(newDir)) {
		t.Errorf("expected the just-created dir %q to survive pruning, but it's gone: %v", filepath.Base(newDir), remaining)
	}
}

// TestNewBackupSessionDir_JustCreatedDirNeverPruned isolates the
// never-self-prune guarantee with a boundary case: exactly 5 pre-existing
// dirs (already at the keep limit) plus a 6th just-created one must still
// keep the new one and drop exactly the single oldest pre-existing dir.
func TestNewBackupSessionDir_JustCreatedDirNeverPruned(t *testing.T) {
	root := realTempDir(t)
	backupsRoot := filepath.Join(root, ".mochiii", "backups")

	old := []string{
		"20200101-000001",
		"20200101-000002",
		"20200101-000003",
		"20200101-000004",
		"20200101-000005",
	}
	for _, name := range old {
		mkBackupSessionDir(t, backupsRoot, name)
	}

	newDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}

	remaining := readDirNames(t, backupsRoot)
	if len(remaining) != 5 {
		t.Fatalf("expected exactly 5 session dirs to remain, got %d: %v", len(remaining), remaining)
	}
	if !containsString(remaining, filepath.Base(newDir)) {
		t.Fatalf("the just-created dir must never be pruned, but it's gone: %v", remaining)
	}
	if containsString(remaining, old[0]) {
		t.Errorf("expected the single oldest pre-existing dir %q to be pruned: %v", old[0], remaining)
	}
}

// TestPruneBackupSessions_StraySkippedNotDeleted confirms a non-directory
// entry sitting directly in backupsRoot (e.g. a stray file left by some
// other process) is silently skipped -- never deleted, and never crashes
// pruning.
func TestPruneBackupSessions_StraySkippedNotDeleted(t *testing.T) {
	root := realTempDir(t)
	backupsRoot := filepath.Join(root, ".mochiii", "backups")

	for i := 1; i <= 6; i++ {
		mkBackupSessionDir(t, backupsRoot, fmt.Sprintf("session-%02d", i))
	}
	strayFile := filepath.Join(backupsRoot, "notes.txt")
	if err := os.WriteFile(strayFile, []byte("not a session dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pruneBackupSessions(backupsRoot, 5)

	if _, err := os.Stat(strayFile); err != nil {
		t.Errorf("stray file must never be touched by pruning, but Stat failed: %v", err)
	}

	remaining := readDirNames(t, backupsRoot)
	dirCount := 0
	for _, name := range remaining {
		if name == "notes.txt" {
			continue
		}
		dirCount++
	}
	if dirCount != 5 {
		t.Errorf("expected exactly 5 session dirs to remain (plus the untouched stray file), got %d dirs: %v", dirCount, remaining)
	}
}

// TestPruneBackupSessions_FewerThanKeepDoesNothing confirms a backups root
// with fewer than `keep` session dirs is left completely alone.
func TestPruneBackupSessions_FewerThanKeepDoesNothing(t *testing.T) {
	root := realTempDir(t)
	backupsRoot := filepath.Join(root, ".mochiii", "backups")

	names := []string{"20200101-000001", "20200101-000002", "20200101-000003"}
	for _, name := range names {
		mkBackupSessionDir(t, backupsRoot, name)
	}

	pruneBackupSessions(backupsRoot, 5)

	remaining := readDirNames(t, backupsRoot)
	if len(remaining) != 3 {
		t.Fatalf("expected all 3 dirs to survive (below the keep threshold), got %d: %v", len(remaining), remaining)
	}
}

// TestPruneBackupSessions_ConfinedToBackupsRoot proves pruning never
// touches anything outside backupsRoot: a sibling directory (a stand-in for
// the rest of the user's workspace, at the same level as .mochiii)
// must survive untouched even while backupsRoot itself is pruned down from
// 6 sessions to the newest 5.
func TestPruneBackupSessions_ConfinedToBackupsRoot(t *testing.T) {
	root := realTempDir(t)
	backupsRoot := filepath.Join(root, ".mochiii", "backups")

	sibling := filepath.Join(root, "src")
	siblingFile := filepath.Join(sibling, "main.go")
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(siblingFile, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	old := []string{
		"20200101-000001",
		"20200101-000002",
		"20200101-000003",
		"20200101-000004",
		"20200101-000005",
		"20200101-000006",
	}
	for _, name := range old {
		mkBackupSessionDir(t, backupsRoot, name)
	}

	pruneBackupSessions(backupsRoot, 5)

	if _, err := os.Stat(siblingFile); err != nil {
		t.Fatalf("sibling file outside backupsRoot must survive pruning untouched, but Stat failed: %v", err)
	}
	remaining := readDirNames(t, backupsRoot)
	if len(remaining) != 5 {
		t.Errorf("expected exactly 5 session dirs to remain inside backupsRoot, got %d: %v", len(remaining), remaining)
	}
}
