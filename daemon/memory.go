package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"codeterminal/protocol"

	_ "modernc.org/sqlite"
)

// memorySchemaVersion is the schema version this binary knows how to read
// and write. Mirrors skills.go's ensureSchema pattern (schema_meta table,
// single migration point for future version bumps).
//
// v1 -> v2: added turns_fts (search.go) -- a lexical FTS5 search index over
// turns.content, kept in sync by triggers, backfilled once from any turns
// that already existed. The turns table itself is untouched by this bump.
const memorySchemaVersion = 2

// MemoryStore is a per-user, cross-session store of conversation turns, one
// conversation per workspace (see AppendTurn/LoadRecentTurns/ClearWorkspace).
// Open once and reuse; it holds a single pooled connection for its lifetime,
// same as SkillStore.
type MemoryStore struct {
	db *sql.DB
}

// StateDir returns the per-user directory conversation memory is persisted
// under: $XDG_STATE_HOME/mochiii if set, else ~/.local/state/mochiii. This
// is deliberately separate from any workspace directory -- conversation
// history must never be something a workspace's own repo could accidentally
// commit.
func StateDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "mochiii"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "mochiii"), nil
}

// DefaultMemoryDBPath returns the conventional location for the
// conversation-memory database under StateDir().
func DefaultMemoryDBPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "memory.db"), nil
}

// OpenMemoryStore opens (creating the file and its parent directory if
// absent) the SQLite database at path and ensures its schema is current.
// Permissions are locked down explicitly: the containing directory to
// 0700, and -- unlike skills.go's OpenSkillStore, which only restricts its
// directory -- the database file itself to 0600, since this store holds a
// full conversation transcript rather than opt-in saved skills. The -wal/-shm
// sidecars are restricted too (both stores do this): in WAL mode they hold
// committed rows the main file does not yet have, so locking down only the db
// file left the newest turns readable.
func OpenMemoryStore(path string) (*MemoryStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating memory db directory: %w", err)
	}

	// Refuse a symlinked db FILE (same rationale as OpenSkillStore): the driver
	// would otherwise follow a symlink planted at memory.db and write the
	// conversation transcript — and the os.Chmod(path, 0600) below would chmod —
	// through it to an outside file. Leaf-only, so a relocated parent dir is
	// unaffected; the driver's -wal/-shm sidecar OPENS remain uncovered by this
	// guard (their modes are restricted after the schema step, but a symlink
	// planted at a sidecar path is still followed by the driver).
	if sym, err := leafIsSymlink(path); err != nil {
		return nil, fmt.Errorf("checking memory db path: %w", err)
	} else if sym {
		return nil, fmt.Errorf("memory db %s is a symlink; refusing to open it", path)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening memory db %s: %w", path, err)
	}

	// A single connection sidesteps SQLite's poor concurrent-writer story
	// entirely, same rationale as SkillStore -- this store's write volume
	// (one append per completed exchange) never justifies a pool.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting journal_mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting busy_timeout: %w", err)
	}

	if err := ensureMemorySchema(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, fmt.Errorf("restricting memory db permissions: %w", err)
	}
	// The -wal/-shm sidecars need the same lockdown, and for this store they
	// need it MORE than the db file does: in WAL mode a just-appended turn is
	// in the sidecar and not yet in memory.db, so chmodding only the db file
	// left the newest transcript lines world-readable. See
	// restrictSQLiteSidecars for why this call sits after ensureMemorySchema
	// rather than beside the journal_mode pragma.
	if err := restrictSQLiteSidecars(path, 0600); err != nil {
		db.Close()
		return nil, err
	}

	return &MemoryStore{db: db}, nil
}

// Close releases the underlying database handle.
func (s *MemoryStore) Close() error {
	return s.db.Close()
}

// ensureMemorySchema creates schema_meta and turns on a fresh database, or
// reads the recorded version on an existing one. Refuses to operate on a DB
// whose recorded version is newer than this binary understands. A file that
// isn't a valid SQLite database at all (corruption, or a stray non-DB file
// at this path) surfaces its error here, in the PRAGMA/CREATE TABLE calls
// above and below -- there's no separate corruption-detection step, it's
// just the natural failure of these statements.
//
// A fresh database (sql.ErrNoRows) creates turns AND the search index
// together at the current version in one shot -- there are no pre-existing
// turns to backfill, so no migration step is needed. An existing database
// still at v1 gets the search index added plus a one-time backfill from
// every turns row already on disk (see search.go); the turns table itself
// is never touched by that migration. Either way, this always leaves
// turns_fts in place before returning, so every other method in this
// package can assume it exists.
func ensureMemorySchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creating schema_meta: %w", err)
	}

	var version int
	err := db.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		if _, err := db.Exec(turnsTableDDL); err != nil {
			return fmt.Errorf("creating turns table: %w", err)
		}
		if err := createSearchIndex(db); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO schema_meta (version) VALUES (?)`, memorySchemaVersion); err != nil {
			return fmt.Errorf("recording schema version: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("reading schema version: %w", err)
	case version > memorySchemaVersion:
		return fmt.Errorf("memory db schema version %d is newer than this binary supports (%d); upgrade codeterminal-daemon", version, memorySchemaVersion)
	case version < 2:
		if err := createSearchIndex(db); err != nil {
			return err
		}
		if err := backfillSearchIndex(db); err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE schema_meta SET version = ?`, memorySchemaVersion); err != nil {
			return fmt.Errorf("recording schema version: %w", err)
		}
		return nil
	default:
		return nil
	}
}

// id is an explicit INTEGER PRIMARY KEY (a SQLite rowid alias) so
// LoadRecentTurns can order by true insertion order. created_at is for
// display/debugging only -- it's only second-resolution, so two turns
// appended back-to-back (a user prompt and its assistant answer) can land
// in the same second and would sort ambiguously if used for ordering.
const turnsTableDDL = `
CREATE TABLE IF NOT EXISTS turns (
	id         INTEGER PRIMARY KEY,
	workspace  TEXT NOT NULL,
	role       TEXT NOT NULL,
	content    TEXT NOT NULL,
	created_at TEXT NOT NULL
)`

// AppendTurn records one turn of conversation for workspace. Called
// write-through, once per completed exchange (see handleConn in
// server.go), so a non-clean daemon shutdown loses at most the in-flight
// request, never anything already answered.
func (s *MemoryStore) AppendTurn(ctx context.Context, workspace, role, content string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO turns (workspace, role, content, created_at) VALUES (?, ?, ?, ?)`,
		workspace, role, content, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("appending turn: %w", err)
	}
	return nil
}

// LoadRecentTurns returns the most recent turns for workspace, oldest
// first, capped at limit. Ordering is by id (SQLite's rowid), never
// created_at -- see the turnsTableDDL comment above.
//
// Disk is untrusted input (a hand-edited or corrupted database could
// contain anything), so the fetched rows are re-validated through
// prepareHistory (history.go) -- the exact same role check applied to
// client-supplied History on the wire. A row whose role isn't exactly
// "user" or "assistant" is dropped rather than passed through: the same
// injection defense, extended to cover on-disk data.
func (s *MemoryStore) LoadRecentTurns(ctx context.Context, workspace string, limit int) ([]protocol.Turn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content FROM turns WHERE workspace = ? ORDER BY id DESC LIMIT ?`,
		workspace, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("loading turns: %w", err)
	}
	defer rows.Close()

	var reversed []protocol.Turn
	for rows.Next() {
		var t protocol.Turn
		if err := rows.Scan(&t.Role, &t.Content); err != nil {
			return nil, fmt.Errorf("scanning turn: %w", err)
		}
		reversed = append(reversed, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loading turns: %w", err)
	}

	chronological := make([]protocol.Turn, len(reversed))
	for i, t := range reversed {
		chronological[len(reversed)-1-i] = t
	}

	outcome := prepareHistory(chronological)
	turns := make([]protocol.Turn, len(outcome.Messages))
	for i, m := range outcome.Messages {
		turns[i] = protocol.Turn{Role: m.Role, Content: m.Content}
	}
	return turns, nil
}

// ClearWorkspace deletes all persisted turns for workspace -- the
// server-side half of ctrl+n's reset (see PromptRequest.Reset in
// protocol.go and handleConn in server.go).
func (s *MemoryStore) ClearWorkspace(ctx context.Context, workspace string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM turns WHERE workspace = ?`, workspace)
	if err != nil {
		return fmt.Errorf("clearing workspace history: %w", err)
	}
	return nil
}

// setupMemoryStore opens the cross-session conversation-memory database at
// its conventional location, gracefully degrading -- never failing the
// caller -- exactly like setupRetrieval (retrieval_setup.go): a missing,
// corrupt, or unwritable store just means the daemon starts with no
// cross-session memory, logged once, not a startup failure.
func setupMemoryStore(logger *log.Logger) *MemoryStore {
	path, err := DefaultMemoryDBPath()
	if err != nil {
		logger.Printf("conversation memory disabled: resolving state dir: %v", err)
		return nil
	}
	store, err := OpenMemoryStore(path)
	if err != nil {
		logger.Printf("conversation memory disabled: opening %s: %v", path, err)
		return nil
	}
	logger.Printf("conversation memory: %s", path)
	return store
}
