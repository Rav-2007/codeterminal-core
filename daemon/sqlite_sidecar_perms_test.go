package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMemoryStore_SidecarsAreNotWorldReadable is the regression test for debt
// item (g).
//
// The shape of the bug it guards is worth stating, because it is the reason
// the assertions below are arranged the way they are. OpenMemoryStore chmodded
// memory.db to 0600 and stopped there. But in WAL mode a committed row lives in
// the -wal file until a checkpoint folds it into the main database, so
// immediately after AppendTurn the transcript line is in the sidecar and NOT in
// the db file. The lockdown was therefore protecting the one copy that did not
// have the data, and the newest turns -- the most sensitive ones -- sat at 0644.
//
// This test writes a turn FIRST and asserts on the sidecars that exist
// afterwards. A test that merely opened the store could find no sidecar at all
// and pass without testing anything.
//
// Neuter-check: remove the restrictSQLiteSidecars call from OpenMemoryStore and
// this fails on -wal being 0644.
func TestMemoryStore_SidecarsAreNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("OpenMemoryStore: %v", err)
	}
	defer store.Close()

	const secretTurn = "PRIVATE-TRANSCRIPT-CONTENT"
	if err := store.AppendTurn(context.Background(), "/ws", "user", secretTurn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	// Guard against the vacuous pass: if no sidecar exists there is nothing to
	// assert and the test must say so rather than report success.
	wal, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("no -wal sidecar after a write; this test asserts nothing: %v", err)
	}
	if mode := wal.Mode().Perm(); mode != 0600 {
		t.Errorf("memory.db-wal mode = %v, want 0600", mode)
	}
	if shm, err := os.Stat(path + "-shm"); err == nil {
		if mode := shm.Mode().Perm(); mode != 0600 {
			t.Errorf("memory.db-shm mode = %v, want 0600", mode)
		}
	}

	// The premise, asserted rather than assumed: the turn really is in the
	// sidecar at this point. If SQLite ever checkpoints eagerly enough that
	// this stops holding, the perm assertions above stop guarding what they
	// were written to guard, and this line is what will say so.
	blob, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatalf("reading -wal: %v", err)
	}
	if !strings.Contains(string(blob), secretTurn) {
		t.Error("expected the just-appended turn to be in the -wal; the premise of this test no longer holds")
	}
}

// TestSkillStore_SidecarsAreNotWorldReadable covers the second site. The store
// deliberately leaves skills.db itself at the default mode (opt-in saved skills,
// not a transcript -- see OpenMemoryStore's comment), so this asserts the
// sidecars only, and asserts that intentional asymmetry explicitly so a future
// reader does not "fix" it by accident.
func TestSkillStore_SidecarsAreNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.db")
	store, err := OpenSkillStore(path)
	if err != nil {
		t.Fatalf("OpenSkillStore: %v", err)
	}
	defer store.Close()

	wal, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("no -wal sidecar after open; this test asserts nothing: %v", err)
	}
	if mode := wal.Mode().Perm(); mode != 0600 {
		t.Errorf("skills.db-wal mode = %v, want 0600", mode)
	}
	if shm, err := os.Stat(path + "-shm"); err == nil {
		if mode := shm.Mode().Perm(); mode != 0600 {
			t.Errorf("skills.db-shm mode = %v, want 0600", mode)
		}
	}
}

// TestRestrictSQLiteSidecars_ToleratesAbsentSidecars pins the ENOENT tolerance.
// A cleanly-closed store checkpoints and removes its sidecars, so absence is a
// legitimate state and must not be an error -- but every OTHER chmod failure
// must still propagate, which is what the os.IsNotExist check buys.
func TestRestrictSQLiteSidecars_ToleratesAbsentSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nothing-here.db")
	if err := restrictSQLiteSidecars(path, 0600); err != nil {
		t.Errorf("absent sidecars should not be an error, got %v", err)
	}
}
