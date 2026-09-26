package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// memory.db stays small and correct. See memorycompact.go for the measured
// breakdown this was written against.

func openTestMemory(t *testing.T) (*MemoryStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("OpenMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func pragmaInt(t *testing.T, db *sql.DB, pragma string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`PRAGMA ` + pragma).Scan(&n); err != nil {
		t.Fatalf("PRAGMA %s: %v", pragma, err)
	}
	return n
}

// ftsIntegrity asks FTS5 to check its index against the external content
// table; it errors if the two disagree (e.g. a delete that left stale terms).
func ftsIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO turns_fts(turns_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		t.Fatalf("search index disagrees with the turns table: %v", err)
	}
}

// The text is stored ONCE: the search index reads it from turns.
func TestTheSearchIndexKeepsNoCopyOfTheText(t *testing.T) {
	store, _ := openTestMemory(t)
	var ddl string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'turns_fts'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "content='turns'") {
		t.Errorf("turns_fts is not an external-content index:\n%s", ddl)
	}
	var n int
	_ = store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'turns_fts_content'`).Scan(&n)
	if n != 0 {
		t.Error("turns_fts_content exists: the index is keeping its own copy of every turn")
	}
}

// Search, snippets, pruning and clearing all still agree with the index.
func TestSearchStaysCorrectThroughDeletes(t *testing.T) {
	store, _ := openTestMemory(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := store.AppendTurn(ctx, "/ws", "user", fmt.Sprintf("create a folder named Go_chii number %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.AppendTurn(ctx, "/other", "user", "Go_chii elsewhere")

	hits, err := store.SearchTurns(ctx, "/ws", "Go_chii", 10)
	if err != nil || len(hits) != 5 || !strings.Contains(hits[0].Snippet, "[Go_chii]") {
		t.Fatalf("search before clearing: %d hit(s), err %v, %+v", len(hits), err, hits)
	}
	ftsIntegrity(t, store.db)

	if err := store.ClearWorkspace(ctx, "/ws"); err != nil {
		t.Fatal(err)
	}
	ftsIntegrity(t, store.db)
	if hits, _ := store.SearchTurns(ctx, "/ws", "Go_chii", 10); len(hits) != 0 {
		t.Errorf("cleared turns are still found: %+v", hits)
	}
	if hits, _ := store.SearchTurns(ctx, "/other", "Go_chii", 10); len(hits) != 1 {
		t.Errorf("clearing one workspace touched another: %+v", hits)
	}
}

// Deleted history gives its space back: free pages do not pile up.
func TestClearedHistoryGivesItsSpaceBack(t *testing.T) {
	store, _ := openTestMemory(t)
	ctx := context.Background()
	if mode := pragmaInt(t, store.db, "auto_vacuum"); mode != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (incremental)", mode)
	}
	long := strings.Repeat("some fairly long conversation text about the retry loop. ", 40)
	for i := 0; i < 150; i++ {
		if err := store.AppendTurn(ctx, "/ws", "user", fmt.Sprintf("%d %s", i, long)); err != nil {
			t.Fatal(err)
		}
	}
	full := pragmaInt(t, store.db, "page_count")
	if err := store.ClearWorkspace(ctx, "/ws"); err != nil {
		t.Fatal(err)
	}
	if free := pragmaInt(t, store.db, "freelist_count"); free != 0 {
		t.Errorf("%d free page(s) left after clearing; they should go back to the filesystem", free)
	}
	if after := pragmaInt(t, store.db, "page_count"); after >= full/2 {
		t.Errorf("page_count %d -> %d after clearing everything; the file did not shrink", full, after)
	}
}

// A v4 database -- its own text copy in the index, old triggers, real turns --
// migrates in place: nothing lost, search works, the copy is gone.
func TestAVersion4MemoryMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE schema_meta (version INTEGER NOT NULL)`,
		`INSERT INTO schema_meta (version) VALUES (4)`,
		turnsTableDDL,
		turnsWorkspaceIndexDDL,
		`CREATE VIRTUAL TABLE turns_fts USING fts5(content, workspace UNINDEXED, tokenize='trigram')`,
		`CREATE TRIGGER turns_ai AFTER INSERT ON turns BEGIN
			INSERT INTO turns_fts(rowid, content, workspace) VALUES (new.id, new.content, new.workspace); END`,
		`CREATE TRIGGER turns_ad AFTER DELETE ON turns BEGIN
			DELETE FROM turns_fts WHERE rowid = old.id; END`,
		`INSERT INTO turns (workspace, role, content, created_at) VALUES ('/ws', 'user', 'make Go_chii on the desktop', '2026-09-26T10:00:00Z')`,
		`INSERT INTO turns (workspace, role, content, created_at) VALUES ('/ws', 'assistant', 'done', '2026-09-26T10:00:01Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("building v4 fixture: %v\n%s", err, stmt)
		}
	}
	db.Close()

	store, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("opening the v4 database: %v", err)
	}
	defer store.Close()
	if v := pragmaIntQuery(t, store.db, `SELECT version FROM schema_meta`); v != memorySchemaVersion {
		t.Errorf("schema version %d after migration, want %d", v, memorySchemaVersion)
	}
	if n := pragmaIntQuery(t, store.db, `SELECT count(*) FROM turns`); n != 2 {
		t.Errorf("%d turns after migration, want 2: history was lost", n)
	}
	hits, err := store.SearchTurns(context.Background(), "/ws", "Go_chii", 5)
	if err != nil || len(hits) != 1 {
		t.Errorf("search after migration: %+v %v", hits, err)
	}
	if n := pragmaIntQuery(t, store.db, `SELECT count(*) FROM sqlite_master WHERE name = 'turns_fts_content'`); n != 0 {
		t.Error("the migrated index still keeps its own copy of the text")
	}
	ftsIntegrity(t, store.db)
}

func pragmaIntQuery(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}
