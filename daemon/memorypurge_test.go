package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// THE MIGRATION DECISION: PURGE. Taken by the daemon/ owner on 2026-09-13.
//
// Item 1 (1a + 1b) closes the provider path and the handshake payload for every
// turn written from now on. It does NOT reach rows already on disk: those hold
// the user's prompts verbatim in `turns`, and in the `turns_fts` index beside
// it, from every session that predates the fix.
//
// That residual is reachable, and by the worst possible query. SearchTurns
// (search.go) returns snippet(turns_fts, ...) of the raw content column and
// passes through neither prepareHistory nor scrub -- so /search over a
// pre-fix conversation returns the secret to whoever types the string that
// matches it, indefinitely.
//
// Three options were put to the owner: LEAVE (record the residual), MIGRATE
// (rewrite rows through scrub in place), PURGE (delete them). PURGE was chosen.
// The cost is real and is stated rather than softened: cross-session history
// predating the fix is destroyed, once, on first open by a daemon carrying
// schema version 4. There is no undo, and no backup is taken -- taking one
// would recreate on disk exactly the plaintext this removes.
//
// KEYED ON THE SCHEMA VERSION, not on a timestamp, because "before the fix" is
// a property of which BINARY wrote the row and the rows carry no such marker.
// The version bump is the marker.
func TestMemorySchema_PurgesPreFixTurnsOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")

	store, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("opening memory store: %v", err)
	}
	ctx := t.Context()
	const ws = "/workspace/pre-fix"
	const secret = "AKIAIOSFODNN7EXAMPLE"
	// Written the way a pre-fix daemon wrote them: straight to the store, no
	// scrub anywhere in the path.
	if err := store.AppendTurn(ctx, ws, "user", "deploy with "+secret); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := store.AppendTurn(ctx, ws, "assistant", "done"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	// Mark the database as the version that predates the purge, which is what
	// an upgrading user's file looks like.
	if _, err := store.db.Exec(`UPDATE schema_meta SET version = 3`); err != nil {
		t.Fatalf("setting schema version: %v", err)
	}

	// Premise, checked rather than assumed: the raw secret really is on disk
	// and really is in the FTS index before the upgrade runs. Without this the
	// assertions below could pass against a database that never held it.
	var ftsHits int
	if err := store.db.QueryRow(
		`SELECT count(*) FROM turns_fts WHERE turns_fts MATCH ?`, `"`+secret+`"`).Scan(&ftsHits); err != nil {
		t.Fatalf("querying turns_fts: %v", err)
	}
	if ftsHits == 0 {
		t.Fatal("premise broken: the sentinel is not in turns_fts, so a clean result after " +
			"the upgrade would prove nothing")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// The upgrade: a daemon carrying the current schema version opens the file.
	reopened, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("reopening memory store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	turns, err := reopened.LoadRecentTurns(ctx, ws, 10, true)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	for _, turn := range turns {
		if strings.Contains(turn.Content, secret) {
			t.Errorf("a pre-fix turn survived the upgrade with its secret intact: %q", turn.Content)
		}
	}
	if len(turns) != 0 {
		t.Errorf("got %d turn(s) after the purge, want 0 -- PURGE deletes pre-fix history, "+
			"it does not redact it in place (that would be MIGRATE, which was not chosen)", len(turns))
	}

	// THE FTS INDEX IS THE HALF THAT IS EASY TO MISS. turns_fts is a separate
	// virtual table; deleting from `turns` only clears it because search.go
	// installs an AFTER DELETE trigger. If that trigger were absent or dropped,
	// the rows would vanish from the table the user can see and remain in the
	// index that /search actually queries -- which is the one place the secret
	// was reachable from.
	if err := reopened.db.QueryRow(
		`SELECT count(*) FROM turns_fts WHERE turns_fts MATCH ?`, `"`+secret+`"`).Scan(&ftsHits); err != nil {
		t.Fatalf("querying turns_fts after upgrade: %v", err)
	}
	if ftsHits != 0 {
		t.Errorf("the secret is still in turns_fts (%d hit(s)) after the purge. /search reads "+
			"that index, so the row is still reachable by the string that matches it.", ftsHits)
	}
}

// A NEW database must not be "purged" into a broken state, and an
// already-current one must not lose history on every restart.
func TestMemorySchema_PurgeRunsOnceAndNotOnFreshDatabases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	ctx := t.Context()
	const ws = "/workspace/post-fix"
	if err := store.AppendTurn(ctx, ws, "user", "hello"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	reopened, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	turns, err := reopened.LoadRecentTurns(ctx, ws, 10, false)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("got %d turn(s) after reopening a CURRENT database, want 1. The purge must be "+
			"a one-time migration keyed on the schema version, not something that runs on "+
			"every open -- that would delete history on every daemon restart.", len(turns))
	}
}
