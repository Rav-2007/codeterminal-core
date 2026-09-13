package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestSearchTurns_FindsByKeyword covers the whole-word lexical case: a plain
// English word from a stored turn must be found via SearchTurns.
func TestSearchTurns_FindsByKeyword(t *testing.T) {
	store, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/search-keyword"

	if err := store.AppendTurn(ctx, ws, "user", "what is a goroutine?"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := store.AppendTurn(ctx, ws, "assistant", "a goroutine is a lightweight thread managed by the go runtime"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	hits, err := store.SearchTurns(ctx, ws, "goroutine", 10)
	if err != nil {
		t.Fatalf("SearchTurns: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (both turns mention 'goroutine'): %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.Snippet == "" {
			t.Errorf("hit %+v has an empty snippet", h)
		}
	}
}

// TestSearchTurns_FindsBySubstringCodeToken proves the trigram tokenizer is
// actually wired up end-to-end: a code-like token containing punctuation
// (not a "word" by normal tokenizer standards) must still be found as a
// substring match.
func TestSearchTurns_FindsBySubstringCodeToken(t *testing.T) {
	store, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/search-substring"

	if err := store.AppendTurn(ctx, ws, "assistant", `use fmt.Println("hello") to print in Go`); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := store.AppendTurn(ctx, ws, "assistant", "unrelated turn about something else entirely"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	hits, err := store.SearchTurns(ctx, ws, "fmt.Println", 10)
	if err != nil {
		t.Fatalf("SearchTurns: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want exactly 1 (only the turn containing fmt.Println): %+v", len(hits), hits)
	}
	if hits[0].Role != "assistant" {
		t.Errorf("hit role = %q, want %q", hits[0].Role, "assistant")
	}
}

// TestSearchTurns_ScopedToWorkspace proves search never leaks across
// workspaces: a query that matches turns in two different workspaces must
// only return the one being searched.
func TestSearchTurns_ScopedToWorkspace(t *testing.T) {
	store, _ := openTestMemoryStore(t)
	ctx := context.Background()

	if err := store.AppendTurn(ctx, "/workspace/a", "user", "how do channels work in go"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := store.AppendTurn(ctx, "/workspace/b", "user", "channels question from workspace b"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	hitsA, err := store.SearchTurns(ctx, "/workspace/a", "channels", 10)
	if err != nil {
		t.Fatalf("SearchTurns a: %v", err)
	}
	if len(hitsA) != 1 {
		t.Fatalf("workspace a: got %d hits, want 1: %+v", len(hitsA), hitsA)
	}

	hitsB, err := store.SearchTurns(ctx, "/workspace/b", "channels", 10)
	if err != nil {
		t.Fatalf("SearchTurns b: %v", err)
	}
	if len(hitsB) != 1 {
		t.Fatalf("workspace b: got %d hits, want 1: %+v", len(hitsB), hitsB)
	}

	hitsC, err := store.SearchTurns(ctx, "/workspace/c-never-wrote-anything", "channels", 10)
	if err != nil {
		t.Fatalf("SearchTurns c: %v", err)
	}
	if len(hitsC) != 0 {
		t.Fatalf("workspace c: got %d hits, want 0 (never wrote anything): %+v", len(hitsC), hitsC)
	}
}

// TestSearchTurns_NoMatchReturnsEmptyNotError confirms a query with no
// matches is a normal empty result, not an error.
func TestSearchTurns_NoMatchReturnsEmptyNotError(t *testing.T) {
	store, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/no-match"

	if err := store.AppendTurn(ctx, ws, "user", "something completely unrelated"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	hits, err := store.SearchTurns(ctx, ws, "nonexistentxyzzy", 10)
	if err != nil {
		t.Fatalf("SearchTurns: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0: %+v", len(hits), hits)
	}
}

// TestBackfillSearchIndex_IndexesPreExistingRows simulates the real-world
// upgrade case this migration exists for: a memory.db that already has
// turns written by an older (pre-search) binary. Opening it with the
// current binary must retroactively index everything already on disk, not
// just turns written from this point forward.
func TestBackfillSearchIndex_IndexesPreExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "memory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Simulate a pre-search (schema v1) database: turns table + schema_meta
	// at version 1, no turns_fts, written directly rather than through
	// OpenMemoryStore (which would already bring the search index along).
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("creating schema_meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (version) VALUES (1)`); err != nil {
		t.Fatalf("seeding schema_meta v1: %v", err)
	}
	if _, err := db.Exec(turnsTableDDL); err != nil {
		t.Fatalf("creating turns table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO turns (workspace, role, content, created_at) VALUES (?, ?, ?, ?)`,
		"/workspace/pre-existing", "user", "a turn written before search existed", "2020-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("seeding pre-existing turn: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	// Now open it the normal way -- this must trigger the v1->v2 migration
	// and backfill.
	store, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("OpenMemoryStore (migration): %v", err)
	}
	defer store.Close()

	hits, err := store.SearchTurns(context.Background(), "/workspace/pre-existing", "written before search", 10)
	if err != nil {
		t.Fatalf("SearchTurns: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 (the backfilled pre-existing turn): %+v", len(hits), hits)
	}
}

// TestBackfillSearchIndex_IdempotentAcrossReopens proves opening the same
// (already-migrated) database twice never double-indexes: each turn must
// appear in search results exactly once, not duplicated.
func TestBackfillSearchIndex_IdempotentAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "memory.db")

	store1, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("OpenMemoryStore (first open): %v", err)
	}
	const ws = "/workspace/reopen"
	if err := store1.AppendTurn(context.Background(), ws, "user", "a turn about reopening databases"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("closing first store: %v", err)
	}

	// Reopen the same file -- ensureMemorySchema runs again, hitting the
	// version==2 default (no-op) branch since the first open already
	// migrated it. Reopen a THIRD time too, for good measure.
	store2, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("OpenMemoryStore (second open): %v", err)
	}
	if err := store2.Close(); err != nil {
		t.Fatalf("closing second store: %v", err)
	}
	store3, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("OpenMemoryStore (third open): %v", err)
	}
	defer store3.Close()

	hits, err := store3.SearchTurns(context.Background(), ws, "reopening", 10)
	if err != nil {
		t.Fatalf("SearchTurns: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits after 3 opens, want exactly 1 (no double-indexing): %+v", len(hits), hits)
	}
}

// TestAppendTurn_WorksEvenWithoutSearchIndexOrTriggers proves the "strictly
// additive" design: AppendTurn's own SQL never references turns_fts, so if
// the search index and its sync triggers were removed entirely -- this
// constructs a store with ONLY the original turns table, bypassing
// createSearchIndex/backfillSearchIndex the way ensureMemorySchema normally
// calls them -- normal turn writes and reads are completely unaffected.
// FTS5 search is a pure addition layered on top of an unmodified base path,
// never a dependency of it.
func TestAppendTurn_WorksEvenWithoutSearchIndexOrTriggers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "memory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(turnsTableDDL); err != nil {
		t.Fatalf("creating turns table (search index deliberately omitted): %v", err)
	}

	store := &MemoryStore{db: db}
	ctx := context.Background()
	if err := store.AppendTurn(ctx, "/workspace/no-search", "user", "does this still work without any search index?"); err != nil {
		t.Fatalf("AppendTurn without a search index present: %v", err)
	}

	got, err := store.LoadRecentTurns(ctx, "/workspace/no-search", 12, false)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	if len(got) != 1 || got[0].Content != "does this still work without any search index?" {
		t.Fatalf("got %+v, want the turn written with no search index present at all", got)
	}
}
