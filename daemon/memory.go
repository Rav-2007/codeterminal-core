package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"mochiii/protocol"

	_ "modernc.org/sqlite"
)

// memorySchemaVersion is the schema version this binary knows how to read
// and write (schema_meta table, single migration point for future version bumps).
const memorySchemaVersion = 6

// maxTurnsPerWorkspace caps retained history PER CHAT: per workspace and, since
// schema v6, per surface (chatscope.go) -- so a busy terminal cannot evict the
// editor's history any more than a busy workspace can evict another's. (Debt
// item (h): the turns table had no cap or prune and grew forever.) The name is
// older than the surface.
//
// Per-workspace rather than global, deliberately: a global cap lets one busy
// workspace evict another's history, which is a surprising way to lose data.
// Count-based rather than age-based for the same reason -- an age cap silently
// empties a workspace that simply was not used for a while, and "I came back
// after a month and my conversation was gone" is worse than an old turn being
// dropped from a workspace still in active use.
//
// 1000 is ~500 exchanges, far above the 12 turns hydrateRecentTurns actually
// replays, so this bounds disk growth without touching what a user can see.
const maxTurnsPerWorkspace = 1000

// THERE IS NO AGE LIMIT, and there was one: turns older than thirty days were
// dropped on every append (maxTurnAge, 2026-07 to 2026-10-11). It contradicted
// the paragraph above, and it did the damage that paragraph predicts, one turn
// at a time and out of sight. A chat still in use lost its oldest turns from
// here while the screen went on showing them, and the next /save then REPLACED
// the saved copy with what was left -- MEASURED 2026-10-11: a 20-turn saved
// chat, resumed forty days later, was a 2-turn chat after one question and one
// save, and the 20-turn file was gone. A chat ends when its user ends it
// (ctrl+n, /clear); until then the count cap above is the only bound.

// MemoryStore is a per-user, cross-session store of conversation turns, one
// conversation per workspace (see AppendTurn/LoadRecentTurns/ClearWorkspace).
// Open once and reuse; it holds a single pooled connection for its lifetime.
type MemoryStore struct {
	db *sql.DB
}

// StateDir returns the per-user directory conversation memory is persisted
// under: $XDG_STATE_HOME/mochiii on Unix, %LOCALAPPDATA%\mochiii on Windows.
// Each platform's state root, and its fallback when the variable is unset,
// lives in statedir_unix.go and statedir_windows.go. The directory is
// deliberately separate from any workspace directory -- conversation history
// must never be something a workspace's own repo could accidentally commit.
//
// PLATFORM-SPLIT, AND IT WAS NOT UNTIL 2026-10-11, when a real Windows run
// logged this from setupMemoryStore below:
//
//	conversation memory: C:\Users\shaik\.local\state\mochiii\memory.db
//
// An XDG path on Windows. XDG_STATE_HOME is never set there, so the fallback
// WAS the behaviour on that platform -- every Windows user's transcript went to
// a literal ".local\state" folder in their profile root. Windows keeps no state
// there, so nothing that manages state knew about it: not the user looking for
// their history, not a reset, not a backup policy, not an uninstall.
//
// Split by build tag rather than by a runtime.GOOS branch, following
// protocol/runtimedir_unix.go and protocol/runtimedir_windows.go. A runtime
// branch is dead code on whichever platform runs the tests, and the untested
// half is exactly the half that shipped wrong here.
func StateDir() (string, error) {
	base, err := stateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mochiii"), nil
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
// 0700, and the database file itself to 0600, since this store holds a
// full conversation transcript. The -wal/-shm sidecars are restricted too:
// in WAL mode they hold committed rows the main file does not yet have, so
// locking down only the db file left the newest turns readable.
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
	if err := compactMemory(db); err != nil {
		_ = db.Close() // the compaction failure is the error worth reporting
		return nil, err
	}

	if err := restrictToOwner(path, 0600); err != nil {
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

// sqlExecer is what the schema helpers need: both *sql.DB and *sql.Tx have it,
// so the same DDL runs on a fresh database and inside the v6 transaction.
type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// ensureMemorySchema creates schema_meta and turns on a fresh database, or
// reads the recorded version on an existing one. Refuses to operate on a DB
// whose recorded version is newer than this binary understands. A file that
// isn't a valid SQLite database at all (corruption, or a stray non-DB file
// at this path) surfaces its error here, in the PRAGMA/CREATE TABLE calls
// above and below -- there's no separate corruption-detection step, it's
// just the natural failure of these statements.
//
// A fresh database creates turns AND the search index together at the current
// version in one shot -- there are no pre-existing turns to backfill. An
// existing one is brought to the current version by two steps, in order, each
// guarded by the recorded version: the v4 purge, then the v6 rebuild.
//
// THE v6 REBUILD SUBSUMES EVERY EARLIER INDEX STEP. v2 created the search
// index and backfilled it, v3 added the workspace index, v5 rebuilt the search
// index without its own copy of the text. v6 changes the shape of turns and of
// turns_fts, so it drops and recreates both from the CURRENT definitions; a v1,
// v2, v3 or v4 database therefore has nothing to run before it except the
// purge, and those steps are gone rather than kept to build an index that is
// dropped one statement later (and whose triggers would name a column the old
// table does not have).
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
		if _, err := db.Exec(turnsScopeIndexDDL); err != nil {
			return fmt.Errorf("creating turns scope index: %w", err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta (version) VALUES (?)`, memorySchemaVersion); err != nil {
			return fmt.Errorf("recording schema version: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("reading schema version: %w", err)
	case version > memorySchemaVersion:
		return fmt.Errorf("memory db schema version %d is newer than this binary supports (%d); upgrade mochiii-daemon", version, memorySchemaVersion)
	}

	// v4 -- THE PURGE. Decided by the daemon/ owner on 2026-09-13, memo item 1,
	// migration question: PURGE.
	//
	// Every row written by a binary before this version holds the user's prompt
	// UNSCRUBBED, because persistTurn stored promptReq.Prompt verbatim. 1a and
	// 1b close that for new turns and for anything on the way to the model, but
	// neither reaches bytes already on disk -- and SearchTurns (search.go)
	// reads the raw content column through turns_fts, passing through neither
	// scrub nor prepareHistory. A pre-fix secret is therefore retrievable by
	// /search, by the exact string that matches it, for as long as the row
	// lives.
	//
	// DELETE, NOT REWRITE. Rewriting rows through scrub in place (MIGRATE) was
	// offered and not chosen. The cost of what was chosen is real: cross-session
	// history predating the fix is destroyed, once, with no undo and no backup.
	// A backup is deliberately NOT taken -- it would write to disk exactly the
	// plaintext this exists to remove.
	//
	// The DELETE fires search.go's AFTER DELETE trigger per row, which is what
	// clears turns_fts. That coupling is asserted by a test, because the table
	// the user can see and the index /search actually queries are different
	// objects and only one of them is named here.
	if version < 4 {
		if _, err := db.Exec(`DELETE FROM turns`); err != nil {
			return fmt.Errorf("purging pre-scrub turns: %w", err)
		}
	}
	if version < 6 {
		return migrateToScopedTurns(db)
	}
	return nil
}

// migrateToScopedTurns is v6: turns gains the surface column and turns_fts is
// rebuilt around it. Everything already stored becomes the editor's chat
// (SurfaceVSCode) -- the owner's decision, 2026-11-14: the terminal starts a
// fresh chat on every launch anyway, so the editor is the one that has a chat
// worth keeping.
//
// ONE TRANSACTION, AND ITS FIRST STATEMENT IS A WRITE. memory.db is shared by
// the daemons of every workspace on the machine, and which binary started a
// given one depends on who opened the folder first, so two daemons can both
// read "version 5" and both migrate. The write comes first so the second one
// waits for the first on busy_timeout instead of failing later when it tries to
// upgrade a read it already holds (SQLite cannot upgrade a stale read in WAL
// mode), and the version is read again INSIDE the transaction: the second
// daemon then finds v6 and does nothing.
//
// The surface column has NO DEFAULT, on purpose. A daemon older than this
// version that is still running keeps inserting without it, and a default would
// let those rows land silently in the editor's chat; without one the insert
// fails, loudly, until that daemon restarts.
func migrateToScopedTurns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("starting the v6 migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once Commit has succeeded

	if _, err := tx.Exec(`UPDATE schema_meta SET version = version`); err != nil {
		return fmt.Errorf("locking memory db for the v6 migration: %w", err)
	}
	var version int
	if err := tx.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&version); err != nil {
		return fmt.Errorf("re-reading schema version: %w", err)
	}
	if version >= memorySchemaVersion {
		return tx.Commit() // another daemon migrated while this one waited
	}

	for _, stmt := range []string{
		// The search index first: its triggers name turns, and an external-content
		// table cannot be altered, only rebuilt.
		`DROP TRIGGER IF EXISTS turns_ai`,
		`DROP TRIGGER IF EXISTS turns_ad`,
		`DROP TABLE IF EXISTS turns_fts`,
		`ALTER TABLE turns RENAME TO turns_v5`,
		turnsTableDDL,
		// Ids are kept, so a client's revision of a chat that survives (the
		// editor's) still names the turns it was shown.
		`INSERT INTO turns (id, workspace, surface, window_id, role, content, created_at)
		   SELECT id, workspace, '` + string(SurfaceVSCode) + `', '', role, content, created_at FROM turns_v5`,
		`DROP TABLE turns_v5`,
		turnsScopeIndexDDL,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrating turns to schema v6: %w", err)
		}
	}
	if err := createSearchIndex(tx); err != nil {
		return err
	}
	if err := backfillSearchIndex(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE schema_meta SET version = ?`, memorySchemaVersion); err != nil {
		return fmt.Errorf("recording schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing the v6 migration: %w", err)
	}
	return nil
}

// id is an explicit INTEGER PRIMARY KEY (a SQLite rowid alias) so
// LoadRecentTurns can order by true insertion order. created_at is for
// display/debugging only -- it's only second-resolution, so two turns
// appended back-to-back (a user prompt and its assistant answer) can land
// in the same second and would sort ambiguously if used for ordering.
//
// window_id says which terminal window's chat it is, "" for the surface's shared
// chat (everything but a terminal that sent a window id); like surface it is NOT
// NULL with no default, so a writer that forgets it fails instead of landing
// somewhere.
//
// surface says which kind of client's chat the turn is in (chatscope.go). It is
// NOT NULL with no default, and not CHECKed: the closed set is enforced in Go
// (chatScope.check), because a CHECK would turn adding a client into a table
// rebuild, and a default would turn a writer that forgot the column into a
// silent write to somebody's chat.
const turnsTableDDL = `
CREATE TABLE IF NOT EXISTS turns (
	id         INTEGER PRIMARY KEY,
	workspace  TEXT NOT NULL,
	surface    TEXT NOT NULL,
	window_id  TEXT NOT NULL,
	role       TEXT NOT NULL,
	content    TEXT NOT NULL,
	created_at TEXT NOT NULL
)`

// turnsScopeIndexDDL indexes exactly the access pattern every query in this
// package uses: filter by chat (workspace, surface and window), order by id. It serves
// LoadRecentTurns, ClearWorkspace and pruneWorkspace alike.
const turnsScopeIndexDDL = `
CREATE INDEX IF NOT EXISTS idx_turns_scope_id ON turns(workspace, surface, window_id, id)`

// AppendTurn records one turn of conversation for workspace. Called
// write-through, once per completed exchange (see handleConn in
// server.go), so a non-clean daemon shutdown loses at most the in-flight
// request, never anything already answered.
//
// It also enforces retention, in ONE place, so every caller gets a bounded
// store for free with no per-call-site change -- the same discipline debt item
// (i) used for backup-session pruning.
func (s *MemoryStore) AppendTurn(ctx context.Context, sc chatScope, role, content string) error {
	if err := sc.check(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO turns (workspace, surface, window_id, role, content, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sc.Workspace, string(sc.Surface), sc.Window, role, content, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("appending turn: %w", err)
	}
	// Retention is best-effort, and the discard is explicit rather than an
	// `if err != nil` whose branches both return nil. The turn IS written by
	// this point; failing the caller would report a lost exchange that was not
	// lost. Unbounded growth is a slow problem, telling a user their message
	// failed when it did not is an immediate one.
	//
	// Known gap, stated rather than hidden: MemoryStore holds no logger and the
	// daemon's counters hang off *Server, so a persistently failing prune is
	// currently silent. Giving this a counter needs the store to reach the
	// server's counters, which is a wider change than retention warrants here.
	_ = s.pruneWorkspace(ctx, sc)
	return nil
}

// pruneWorkspace drops everything beyond the newest maxTurnsPerWorkspace turns
// of one chat (one workspace, one surface). By count only, never by age -- see
// the note under maxTurnsPerWorkspace.
//
// The subquery finds the id of the (max+1)th newest turn and deletes at or
// below it. When the chat holds fewer than max+1 turns, the subquery yields
// NULL and nothing is deleted.
//
// turns_fts needs no attention here: search.go's AFTER DELETE trigger
// (turns_ad) removes the matching row, so the search index cannot drift out of
// sync with what was pruned.
func (s *MemoryStore) pruneWorkspace(ctx context.Context, sc chatScope) error {
	if err := sc.check(); err != nil {
		return err
	}
	surface := string(sc.Surface)
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM turns
		   WHERE workspace = ? AND surface = ? AND window_id = ?
		     AND id <= (SELECT id FROM turns WHERE workspace = ? AND surface = ? AND window_id = ?
		                   ORDER BY id DESC LIMIT 1 OFFSET ?)`,
		sc.Workspace, surface, sc.Window, sc.Workspace, surface, sc.Window, maxTurnsPerWorkspace,
	)
	if err != nil {
		return fmt.Errorf("pruning workspace history: %w", err)
	}
	return reclaimFreePages(ctx, s.db)
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
func (s *MemoryStore) LoadRecentTurns(ctx context.Context, sc chatScope, limit int, scrubDisabled bool) ([]protocol.Turn, error) {
	turns, _, err := s.loadRecentTurnsAt(ctx, sc, limit, scrubDisabled)
	return turns, err
}

// loadRecentTurnsAt is LoadRecentTurns plus the id of the newest row it read
// (0 for none), taken from THE SAME QUERY as the turns. That is what lets a
// chat revision (chatsync.go) say exactly what a client was shown: an id read
// by a second query could already count a turn the first one did not return,
// and the client would then be told it has a turn it never saw.
func (s *MemoryStore) loadRecentTurnsAt(ctx context.Context, sc chatScope, limit int, scrubDisabled bool) ([]protocol.Turn, int64, error) {
	if err := sc.check(); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, role, content FROM turns WHERE workspace = ? AND surface = ? AND window_id = ? ORDER BY id DESC LIMIT ?`,
		sc.Workspace, string(sc.Surface), sc.Window, limit,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("loading turns: %w", err)
	}
	defer rows.Close()

	var reversed []protocol.Turn
	var last int64
	for rows.Next() {
		var t protocol.Turn
		var id int64
		if err := rows.Scan(&id, &t.Role, &t.Content); err != nil {
			return nil, 0, fmt.Errorf("scanning turn: %w", err)
		}
		if id > last {
			last = id
		}
		reversed = append(reversed, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("loading turns: %w", err)
	}

	chronological := make([]protocol.Turn, len(reversed))
	for i, t := range reversed {
		chronological[len(reversed)-1-i] = t
	}

	outcome := prepareHistory(chronological, scrubDisabled)
	turns := make([]protocol.Turn, len(outcome.Messages))
	for i, m := range outcome.Messages {
		turns[i] = protocol.Turn{Role: m.Role, Content: m.Content}
	}
	return turns, last, nil
}

// lastTurnID is the id of the chat's newest stored turn, 0 when it has none.
// Callers that need it to agree with turns they hold read both under
// historyMu (chatsync.go); everyone else uses the id loadRecentTurnsAt or
// turnsAfter returned with the turns themselves.
func (s *MemoryStore) lastTurnID(ctx context.Context, sc chatScope) (int64, error) {
	if err := sc.check(); err != nil {
		return 0, err
	}
	var id sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(id) FROM turns WHERE workspace = ? AND surface = ? AND window_id = ?`,
		sc.Workspace, string(sc.Surface), sc.Window).Scan(&id); err != nil {
		return 0, fmt.Errorf("reading the newest turn: %w", err)
	}
	return id.Int64, nil
}

// turnsAfter returns the chat's turns with an id above afterID, oldest first,
// and the id of the last one returned (afterID when there are none). afterID 0
// is the whole chat. Unvalidated, like LoadAllTurns: the caller filters what it
// shows.
func (s *MemoryStore) turnsAfter(ctx context.Context, sc chatScope, afterID int64) ([]storedTurn, int64, error) {
	if err := sc.check(); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, role, content, created_at FROM turns WHERE workspace = ? AND surface = ? AND window_id = ? AND id > ? ORDER BY id`,
		sc.Workspace, string(sc.Surface), sc.Window, afterID)
	if err != nil {
		return nil, 0, fmt.Errorf("loading turns: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only; rows.Err below reports failures
	last := afterID
	var out []storedTurn
	for rows.Next() {
		var t storedTurn
		var id int64
		if err := rows.Scan(&id, &t.Role, &t.Content, &t.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scanning turn: %w", err)
		}
		last = id
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("loading turns: %w", err)
	}
	return out, last, nil
}

// storedTurn is one row as it is on disk, with the time it was written.
type storedTurn struct {
	Role, Content, CreatedAt string
}

// LoadAllTurns returns every persisted turn of the chat, oldest first --
// the whole current chat, which /history save writes to history
// (chatarchive.go). Unlike LoadRecentTurns it neither caps nor re-validates:
// what it returns is archived as it is and re-validated when it is read back.
func (s *MemoryStore) LoadAllTurns(ctx context.Context, sc chatScope) ([]storedTurn, error) {
	if err := sc.check(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content, created_at FROM turns WHERE workspace = ? AND surface = ? AND window_id = ? ORDER BY id`,
		sc.Workspace, string(sc.Surface), sc.Window)
	if err != nil {
		return nil, fmt.Errorf("loading turns: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only; rows.Err below reports failures
	var out []storedTurn
	for rows.Next() {
		var t storedTurn
		if err := rows.Scan(&t.Role, &t.Content, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning turn: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loading turns: %w", err)
	}
	return out, nil
}

// ReplaceWorkspace makes turns the chat's whole conversation (one workspace, one
// surface; the other surface's chat is not touched), in one transaction -- how /history resume brings a saved chat back.
//
// EACH TURN KEEPS THE TIME IT WAS FIRST SAID, so a resumed chat still says
// when it happened (FOUND 2026-10-01: stamping them all with the moment of
// resuming made the next save claim the chat had started then). That is safe
// only because nothing prunes by age -- see the note under
// maxTurnsPerWorkspace for what happened while something did. created_at is
// display-only (see turnsTableDDL); order is by id, which insertion order
// preserves.
func (s *MemoryStore) ReplaceWorkspace(ctx context.Context, sc chatScope, turns []storedTurn) error {
	if err := sc.check(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replacing workspace history: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once Commit has succeeded
	if _, err := tx.ExecContext(ctx, `DELETE FROM turns WHERE workspace = ? AND surface = ? AND window_id = ?`,
		sc.Workspace, string(sc.Surface), sc.Window); err != nil {
		return fmt.Errorf("replacing workspace history: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, t := range turns {
		// Each turn keeps its own time; one without (an older saved chat) is
		// stamped now. Order is the insertion order (ORDER BY id), never time.
		at := t.CreatedAt
		if at == "" {
			at = now
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO turns (workspace, surface, window_id, role, content, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			sc.Workspace, string(sc.Surface), sc.Window, t.Role, t.Content, at); err != nil {
			return fmt.Errorf("replacing workspace history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replacing workspace history: %w", err)
	}
	return reclaimFreePages(ctx, s.db)
}

// ClearWorkspace deletes all persisted turns of the chat (one workspace, one
// surface) -- the server-side half of ctrl+n's reset (see PromptRequest.Reset
// in protocol.go and handleConn in server.go). The other surface's chat stays.
func (s *MemoryStore) ClearWorkspace(ctx context.Context, sc chatScope) error {
	if err := sc.check(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM turns WHERE workspace = ? AND surface = ? AND window_id = ?`,
		sc.Workspace, string(sc.Surface), sc.Window)
	if err != nil {
		return fmt.Errorf("clearing workspace history: %w", err)
	}
	return reclaimFreePages(ctx, s.db)
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
