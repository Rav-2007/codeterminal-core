package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// lexicalDBFileName is where a workspace's code lexical index lives,
// alongside (not inside) the chromem-go vector store directory -- a sibling
// file under the same per-workspace indexDir, same lifetime as the vector
// index, rebuilt together by buildIndex.
const lexicalDBFileName = "lexical.db"

// LexicalStore is the keyword/substring counterpart to VectorStore: it finds
// chunks by exact token/substring match (FTS5 BM25) rather than embedding
// similarity, so exact symbols, string literals, and signatures are findable
// even when they rank poorly on semantic similarity (the measured failure
// this type exists to fix -- see rerank.go's fuseRRF).
type LexicalStore interface {
	Upsert(ctx context.Context, chunks []Chunk) error
	Search(ctx context.Context, query string, k int) ([]Chunk, error)
	// DeleteByFilePath removes every chunk belonging to one workspace-relative
	// file, for the same reason VectorStore.DeleteByFilePath exists: a shrinking
	// file's orphaned tail chunks would otherwise keep serving pre-edit code.
	DeleteByFilePath(ctx context.Context, relPath string) error
	// AllIDs returns every stored chunk ID, so a full index build can tell what
	// it is replacing. See VectorStore.AllIDs; this side needs no probe vector
	// because SQLite can simply be asked.
	AllIDs(ctx context.Context) ([]string, error)
	Close() error
}

// codeChunksFTSTableDDL mirrors turns_fts's shape exactly (search.go): a
// STANDALONE fts5 table (content duplicated in, not "external content"
// mapped, sidestepping FTS5's external-content column rules) using the
// trigram tokenizer, which matches raw substrings regardless of identifier
// casing (camelCase, snake_case, dotted names like fmt.Println all just
// work -- see fts5_probe_test.go for the proof this is compiled into this
// build). chunk_id/start_line/end_line/class are UNINDEXED (carried
// alongside each row, never tokenized) so a hit can be turned directly back
// into a Chunk without a join.
//
// file_path IS INDEXED, since 2026-10, and it is the keyword tier's half of
// the change that made the meaning tier work. Prefixing the path to every
// chunk's EMBEDDED text was the biggest single gain this retrieval stack has
// measured (chunkcontext.go); the keyword tier could not see the path at all,
// so a question about the terminal client and its daemon connection could
// never reach clients/tui/daemonconn.go by the name it is filed under.
// Searching the path is weighted, not mixed into the text: see
// lexicalPolicy.PathWeight. (The eval question this is measured on is not
// quoted here, and must not be: a verbatim copy in this file would be a perfect
// keyword match for it -- TestNoIndexedFileEchoesAnEvalQuery caught exactly
// that in the first draft of this comment.)
const codeChunksFTSTableDDL = `
CREATE VIRTUAL TABLE IF NOT EXISTS code_chunks_fts USING fts5(
	content,
	chunk_id UNINDEXED,
	file_path,
	start_line UNINDEXED,
	end_line UNINDEXED,
	class UNINDEXED,
	tokenize='trigram'
)`

// legacyCodeChunksFTSTableDDL is the layout of every index written before
// 2026-10: the same table with file_path stored but not searchable. It is kept
// for two readers -- migrateLegacyFTSLayout recognises it on disk, and the
// retrieval sweep builds one to score the old ranking exactly, because the new
// layout cannot impersonate it (an indexed path changes BM25's term rarity in
// the third decimal even at weight zero).
const legacyCodeChunksFTSTableDDL = `
CREATE VIRTUAL TABLE IF NOT EXISTS code_chunks_fts USING fts5(
	content,
	chunk_id UNINDEXED,
	file_path UNINDEXED,
	start_line UNINDEXED,
	end_line UNINDEXED,
	class UNINDEXED,
	tokenize='trigram'
)`

// legacyFTSLayout recognises the legacy layout in the CREATE statement SQLite
// keeps for the table.
var legacyFTSLayout = regexp.MustCompile(`(?i)\bfile_path\s+UNINDEXED\b`)

// chunkIndexDDL is the companion lookup table, and it exists because
// "UNINDEXED" in the DDL above means exactly what it says.
//
// An FTS5 column marked UNINDEXED is STORED but carries no b-tree. So
// `DELETE ... WHERE chunk_id = ?` cannot seek -- SQLite scans every row of the
// virtual table and decodes each one, and each row carries a full chunk of
// source text. That made the two write paths quadratic in the size of the
// index they were writing into:
//
//	MEASURED, 2026-08-26, before this table existed:
//	  Upsert    n=500  -> 443µs/chunk   n=4000 -> 1.906ms/chunk  (222ms -> 7.6s total)
//	  DeleteByFilePath  index=1000 -> 1.7ms      index=4000 -> 4.2ms per call
//
// Per-chunk cost RISING with n is the signature: 8x the chunks cost 34x the
// time. The vector store over the identical corpus was flat at ~92µs/chunk, so
// the lexical half was ~20x the cost of the semantic half and pulling away.
//
// It is worse than a slow index build, because DeleteByFilePath is on the
// INTERACTIVE path: reindexFile (reindex.go) calls it on every applied edit,
// synchronously, inside the per-workspace apply lock. Every edit a user
// accepted paid a full scan of the lexical index, and paid more of it the
// longer they had been working.
//
// This table gives both paths a real index to seek on: chunk_id is the primary
// key, file_path is indexed, and fts_rowid points at the FTS row so deletion
// happens by rowid -- which FTS5 does support efficiently. The FTS table keeps
// owning the search; this one owns only identity.
//
// INVARIANT, relied on by every method below: exactly one chunk_index row per
// code_chunks_fts row, and vice versa. ensureLexicalSchema establishes it on an
// existing database and every write below preserves it.
const chunkIndexDDL = `
CREATE TABLE IF NOT EXISTS chunk_index (
	chunk_id  TEXT PRIMARY KEY,
	file_path TEXT NOT NULL,
	fts_rowid INTEGER NOT NULL
)`

const chunkIndexFileDDL = `
CREATE INDEX IF NOT EXISTS idx_chunk_index_file_path ON chunk_index(file_path)`

// FTSChunkStore is a LexicalStore backed by a local, on-disk SQLite FTS5
// database -- the same modernc.org/sqlite dependency memory.go
// already uses, so this adds no new dependency.
type FTSChunkStore struct {
	db   *sql.DB
	path string

	// upgrading is non-nil while a layout upgrade runs on its own connection
	// (upgradeInBackground), and closed when it ends, however it ends. WRITES
	// WAIT FOR IT: a write racing the rebuild would only fail on the lock once
	// busy_timeout ran out, and a write that fails is a stale index entry.
	// SEARCHES DO NOT WAIT: they read the committed legacy table until the
	// upgrade commits, which is the point of running it on another connection.
	upgrading chan struct{}
}

// NewFTSChunkStore opens (creating if absent) the lexical index at
// indexDir/lexical.db. Single pooled connection, same rationale as
// MemoryStore: this store's write volume (one upsert per index
// run) never justifies a pool, and SQLite's concurrent-writer story is poor
// enough that avoiding it entirely is simplest.
//
// A legacy-layout index is upgraded before this returns (migrateLegacyFTSLayout),
// which is right for the one-shot commands. The daemon opens with
// openFTSChunkStore(dir, false) instead and upgrades in the background, because
// a large index takes long enough to rebuild that doing it on the startup path
// would hold every client in the accept backlog for the duration.
func NewFTSChunkStore(indexDir string) (*FTSChunkStore, error) {
	return openFTSChunkStore(indexDir, true)
}

// openFTSChunkStore is NewFTSChunkStore with the upgrade of a legacy layout
// either done now or left to upgradeInBackground; legacyLayout on the result
// says which one the caller has to deal with.
func openFTSChunkStore(indexDir string, upgradeNow bool) (*FTSChunkStore, error) {
	// 0700, and this is a correction rather than a preference.
	//
	// This database holds the CHUNK TEXT of the user's workspace -- the same
	// source the completion request carries, sitting on disk in full. Every
	// other store this daemon owns treats that class of content as private:
	// memory.db is 0600 inside a 0700 directory, and chromem creates its own
	// collection directory 0700, which is why the vector half of this very
	// index is unreadable to other users. The lexical half was created 0644
	// inside a 0755 directory and was world-readable on any shared machine.
	//
	// MkdirAll does not touch the mode of a directory that already exists, so
	// existing installs need the explicit Chmod: they are precisely the ones
	// with an index already written.
	if err := os.MkdirAll(indexDir, 0700); err != nil {
		return nil, fmt.Errorf("creating index directory %s: %w", indexDir, err)
	}
	if err := restrictToOwner(indexDir, 0700); err != nil {
		return nil, fmt.Errorf("restricting index directory %s: %w", indexDir, err)
	}

	path := filepath.Join(indexDir, lexicalDBFileName)
	// Refuse a symlinked db FILE (same rationale as OpenSkillStore/OpenMemoryStore):
	// path is derived from indexDir (config, not client input), but the driver
	// would otherwise follow a symlink planted at lexical.db and write through it.
	// Leaf-only; a relocated parent dir is unaffected, sidecars remain uncovered.
	if sym, err := leafIsSymlink(path); err != nil {
		return nil, fmt.Errorf("checking lexical db path: %w", err)
	} else if sym {
		return nil, fmt.Errorf("lexical db %s is a symlink; refusing to open it", path)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening lexical index %s: %w", path, err)
	}

	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("setting journal_mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("setting busy_timeout: %w", err)
	}
	// synchronous=NORMAL, and ONLY here -- memory.db deliberately does not get
	// this. Under WAL, NORMAL stops fsync-ing on every commit and syncs at
	// checkpoint instead; the exposure is that a machine losing power mid-write
	// can lose the most recent commits. For conversation transcripts that would
	// be losing the user's data, which is why MemoryStore keeps the default. This
	// database is a DERIVED CACHE of files that are still on disk: the worst case
	// is a stale lexical index, which `index` rebuilds and which checkEmbedderStamp
	// already treats as a recoverable state. Paying a per-commit fsync to protect
	// a rebuildable artifact is the wrong trade, and buildIndex commits once per
	// 40-chunk batch.
	if _, err := db.Exec(`PRAGMA synchronous=NORMAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("setting synchronous: %w", err)
	}
	if err := ensureLexicalSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if upgradeNow {
		if err := migrateLegacyFTSLayout(db); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	// The db file and its -wal/-shm sidecars, after the schema step so the
	// sidecars WAL mode created actually exist to be restricted. In WAL mode a
	// sidecar holds committed rows the main file does not have yet, so locking
	// down only lexical.db would leave the newest chunks readable -- the same
	// reasoning OpenMemoryStore records, applied to the store that was missed.
	if err := restrictToOwner(path, 0600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("restricting lexical index permissions: %w", err)
	}
	if err := restrictSQLiteSidecars(path, 0600); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &FTSChunkStore{db: db, path: path}, nil
}

// legacyLayout reports whether the index is still in the pre-2026-10 layout.
func (s *FTSChunkStore) legacyLayout() bool {
	legacy, err := ftsLayoutIsLegacy(context.Background(), s.db)
	return err == nil && legacy
}

// upgradeInBackground rebuilds a legacy layout on a connection of its own and
// returns at once; done is called when the rebuild ends, with its duration and
// any error. Searches keep reading the legacy table meanwhile -- WAL lets them --
// and writes wait for the end (see upgrading). Called at most once, before the
// store is shared.
//
// MEASURED 2026-10-03 on this repository's own index (7,976 chunks, a 106 MB
// lexical.db), which is why this exists: about ten seconds inline with the
// machine busy. The file cap allows indexes several times that, and every
// second of it was a second every client sat in the accept backlog.
func (s *FTSChunkStore) upgradeInBackground(done func(took time.Duration, err error)) {
	s.upgrading = make(chan struct{})
	go func() {
		began := time.Now()
		err := func() error {
			db, err := sql.Open("sqlite", s.path)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
				return err
			}
			return migrateLegacyFTSLayout(db)
		}()
		close(s.upgrading)
		done(time.Since(began), err)
	}()
}

// waitForUpgrade blocks a write until a background upgrade has ended.
func (s *FTSChunkStore) waitForUpgrade(ctx context.Context) error {
	if s.upgrading == nil {
		return nil
	}
	select {
	case <-s.upgrading:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for the lexical index upgrade: %w", ctx.Err())
	}
}

// ensureLexicalSchema creates both tables and, on a database written before
// chunk_index existed, backfills it from the FTS rows already there.
//
// The backfill is gated on chunk_index being EMPTY rather than on a version
// number, because this store has never had a schema_meta table and adding one
// now would not help the databases that are already on disk. "Empty companion,
// non-empty FTS" is the only shape an un-migrated database can have, and it is
// answerable with an indexed EXISTS rather than a count over the FTS table.
//
// The orphan sweep afterwards is not defensive padding. The OLD delete-by-
// chunk_id path could leave duplicate FTS rows behind (two rows for one
// chunk_id, only one of which any later delete would find), and an FTS row with
// no chunk_index entry is unreachable forever: it can never be updated or
// deleted, so it would keep serving pre-edit code into every future search.
// Establishing the one-to-one invariant here is what lets every method below
// assume it.
func ensureLexicalSchema(db *sql.DB) error {
	for _, stmt := range []struct{ what, ddl string }{
		{"code_chunks_fts", codeChunksFTSTableDDL},
		{"chunk_index", chunkIndexDDL},
		{"idx_chunk_index_file_path", chunkIndexFileDDL},
	} {
		if _, err := db.Exec(stmt.ddl); err != nil {
			return fmt.Errorf("creating %s: %w", stmt.what, err)
		}
	}

	var populated bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM chunk_index)`).Scan(&populated); err != nil {
		return fmt.Errorf("checking chunk_index: %w", err)
	}
	if populated {
		return nil
	}

	// Either a fresh database (both tables empty, so this is two no-op scans of
	// nothing) or one that predates chunk_index (one scan, once, ever).
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO chunk_index(chunk_id, file_path, fts_rowid)
		 SELECT chunk_id, file_path, rowid FROM code_chunks_fts`); err != nil {
		return fmt.Errorf("backfilling chunk_index: %w", err)
	}
	if _, err := db.Exec(
		`DELETE FROM code_chunks_fts
		 WHERE rowid NOT IN (SELECT fts_rowid FROM chunk_index)`); err != nil {
		return fmt.Errorf("sweeping orphaned lexical rows: %w", err)
	}
	return nil
}

// migrateLegacyFTSLayout rebuilds a legacy code_chunks_fts (file_path stored,
// not searchable) in the current layout, in place.
//
// NOTHING IS RE-EMBEDDED. The keyword index is rebuilt from its own rows -- the
// text is already stored in it -- so an existing install upgrades the first time
// it opens the index rather than being told to run `index` and wait for every
// chunk to be embedded again. It still re-tokenizes every chunk, which is why
// the daemon runs it in the background (upgradeInBackground): about ten seconds
// for this repository's own index with the machine busy, and more for larger.
//
// ROWIDS ARE CARRIED ACROSS, and that is what keeps chunk_index valid: its
// fts_rowid column points at FTS rows by rowid, so a copy that renumbered them
// would orphan every entry and break the one-to-one invariant every method in
// this file relies on.
//
// ONE WRITE TRANSACTION, RE-CHECKED UNDER THE LOCK. The daemon and the CLI open
// the same index, and both can find it legacy at the same moment. BEGIN
// IMMEDIATE takes the write lock up front; the loser waits on busy_timeout,
// then finds the layout already current and does nothing. A failure rolls back
// to the legacy table intact, which keyword search reads exactly as before --
// the upgrade is lost, never the tier.
func migrateLegacyFTSLayout(db *sql.DB) error {
	ctx := context.Background()
	if legacy, err := ftsLayoutIsLegacy(ctx, db); err != nil || !legacy {
		return err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrating the lexical index: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("migrating the lexical index: %w", err)
	}
	done := false
	defer func() {
		if !done {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()

	legacy, err := ftsLayoutIsLegacy(ctx, conn)
	if err != nil {
		return err
	}
	if !legacy {
		return nil // another process migrated it while this one waited
	}
	for _, stmt := range []string{
		`ALTER TABLE code_chunks_fts RENAME TO code_chunks_fts_legacy`,
		codeChunksFTSTableDDL,
		`INSERT INTO code_chunks_fts(rowid, content, chunk_id, file_path, start_line, end_line, class)
		 SELECT rowid, content, chunk_id, file_path, start_line, end_line, class FROM code_chunks_fts_legacy`,
		`DROP TABLE code_chunks_fts_legacy`,
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrating the lexical index to a searchable file path: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("migrating the lexical index: %w", err)
	}
	done = true
	return nil
}

// ftsLayoutIsLegacy reports whether code_chunks_fts on disk is the legacy
// layout, read from the CREATE statement SQLite keeps for it.
func ftsLayoutIsLegacy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var ddl string
	if err := q.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'code_chunks_fts'`).Scan(&ddl); err != nil {
		return false, fmt.Errorf("reading the lexical index layout: %w", err)
	}
	return legacyFTSLayout.MatchString(ddl), nil
}

// Has reports whether a chunk id is already stored.
//
// It answers one question for buildIndex: may this chunk's write be skipped
// entirely? The vector store can say the text is unchanged, but the two stores
// are separate databases and can legitimately disagree -- a build that ran while
// lexical.db failed to open populated one and not the other. Skipping on the
// vector store's word alone would leave those chunks permanently missing from
// lexical search, findable semantically and not by symbol, which is exactly the
// asymmetry fuseRRF exists to fix.
//
// Cheap enough to ask per chunk BECAUSE of chunk_index: this is a primary-key
// seek, not the scan the same question would have cost against the FTS table.
func (s *FTSChunkStore) Has(ctx context.Context, id string) bool {
	var present bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM chunk_index WHERE chunk_id = ?)`, id).Scan(&present); err != nil {
		// An unanswerable question is answered "no": the caller then writes the
		// chunk, which is the safe direction and the pre-existing behaviour.
		return false
	}
	return present
}

// Close releases the underlying database handle.
func (s *FTSChunkStore) Close() error {
	return s.db.Close()
}

// Upsert writes chunks to the lexical index, keyed by Chunk.ID. Unlike
// ChromemStore (whose underlying chromem-go documents are keyed by ID map
// and overwrite automatically), a standalone FTS5 table has no such
// upsert-by-key behavior built in -- so each chunk is explicitly deleted by
// chunk_id before being re-inserted, inside one transaction per batch, to
// give the same "re-indexing replaces rather than duplicates" contract
// VectorStore.Upsert already documents.
// DeleteByFilePath removes every row for one workspace-relative file. file_path
// is an UNINDEXED FTS5 column — not full-text searchable, but still stored and
// perfectly usable in an ordinary WHERE clause, which is what this needs.
// Both statements seek: the subquery rides idx_chunk_index_file_path, and FTS5
// deletes by rowid without scanning. They run in one transaction because the
// one-to-one invariant must not be observable as broken -- a search landing
// between them would otherwise see rows whose identity had already been
// deleted.
// AllIDs returns every stored chunk ID. chunk_index is the authoritative row
// per chunk (code_chunks_fts is the trigram side, keyed by fts_rowid), so one
// column scan answers it exactly -- no probe vector and no similarity maths,
// unlike the vector store's implementation.
func (s *FTSChunkStore) AllIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chunk_id FROM chunk_index`)
	if err != nil {
		return nil, fmt.Errorf("listing lexical chunk ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning lexical chunk id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing lexical chunk ids: %w", err)
	}
	return ids, nil
}

func (s *FTSChunkStore) DeleteByFilePath(ctx context.Context, relPath string) error {
	if err := s.waitForUpgrade(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning lexical delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM code_chunks_fts
		 WHERE rowid IN (SELECT fts_rowid FROM chunk_index WHERE file_path = ?)`, relPath); err != nil {
		return fmt.Errorf("deleting lexical chunks for %s: %w", relPath, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_index WHERE file_path = ?`, relPath); err != nil {
		return fmt.Errorf("deleting lexical index rows for %s: %w", relPath, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing lexical delete: %w", err)
	}
	return nil
}

func (s *FTSChunkStore) Upsert(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if err := s.waitForUpgrade(ctx); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning lexical upsert transaction: %w", err)
	}
	// Explicit, like every other discard in this function: a rollback after the
	// commit below has succeeded is a no-op by definition, and leaving one of the
	// five bare while the rest are marked is how a reader learns to skip them all.
	defer func() { _ = tx.Rollback() }()

	// Four prepared statements rather than two, and the pair that replaced the
	// old `DELETE ... WHERE chunk_id = ?` is the whole point: that statement had
	// no index to use and scanned the entire FTS table once PER CHUNK. Looking
	// the rowid up first turns each replacement into two index seeks.
	lookup, err := tx.PrepareContext(ctx, `SELECT fts_rowid FROM chunk_index WHERE chunk_id = ?`)
	if err != nil {
		return fmt.Errorf("preparing lexical lookup: %w", err)
	}
	defer func() { _ = lookup.Close() }()

	delFTS, err := tx.PrepareContext(ctx, `DELETE FROM code_chunks_fts WHERE rowid = ?`)
	if err != nil {
		return fmt.Errorf("preparing lexical delete: %w", err)
	}
	defer func() { _ = delFTS.Close() }()

	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO code_chunks_fts(chunk_id, content, file_path, start_line, end_line, class) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("preparing lexical insert: %w", err)
	}
	defer func() { _ = ins.Close() }()

	// INSERT OR REPLACE, not INSERT: the chunk_index row for a re-indexed chunk
	// already exists and must now point at the new FTS rowid.
	insIdx, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO chunk_index(chunk_id, file_path, fts_rowid) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("preparing lexical index insert: %w", err)
	}
	defer func() { _ = insIdx.Close() }()

	for _, c := range chunks {
		var oldRowID int64
		switch err := lookup.QueryRowContext(ctx, c.ID).Scan(&oldRowID); {
		case err == nil:
			if _, err := delFTS.ExecContext(ctx, oldRowID); err != nil {
				return fmt.Errorf("deleting stale lexical chunk %s: %w", c.ID, err)
			}
		case errors.Is(err, sql.ErrNoRows):
			// First time this chunk id has been seen; nothing to replace.
		default:
			return fmt.Errorf("looking up lexical chunk %s: %w", c.ID, err)
		}

		res, err := ins.ExecContext(ctx, c.ID, c.Content, c.FilePath, c.StartLine, c.EndLine, string(c.Class))
		if err != nil {
			return fmt.Errorf("inserting lexical chunk %s: %w", c.ID, err)
		}
		rowID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("reading rowid for lexical chunk %s: %w", c.ID, err)
		}
		if _, err := insIdx.ExecContext(ctx, c.ID, c.FilePath, rowID); err != nil {
			return fmt.Errorf("indexing lexical chunk %s: %w", c.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing lexical upsert: %w", err)
	}
	return nil
}

// lexicalStopwords are common English function words stripped from a
// natural-language query before it's turned into an FTS5 MATCH expression --
// they carry no code-identifying signal (nothing named "the" or "does") and
// including them would only dilute the OR against real symbol/keyword terms.
// Deliberately small and code-review-scoped, not a general stopword list.
var lexicalStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "been": true, "by": true, "do": true, "does": true,
	"for": true, "how": true, "in": true, "is": true, "it": true, "its": true,
	"of": true, "on": true, "or": true, "that": true, "the": true,
	"this": true, "to": true, "was": true, "were": true, "what": true,
	"when": true, "where": true, "which": true, "who": true, "why": true,
	"with": true,
}

// lexicalFillerWords are general-English filler that questions carry and code
// does not: pronouns, modals, and the scaffolding of "what does the user get
// told". Dropped only under lexicalPolicy.FillerStopwords.
//
// CONSERVATIVE ON PURPOSE. Words that can NAME code stay searchable however
// often questions use them -- get, make, next, before, after, all, not, out,
// one -- because each is an identifier somewhere (deadlineCap.Before, the make
// that sandbox_exec runs). A dropped word is not searched for at all, so a
// wrong entry here costs recall outright, and nothing repository-specific
// belongs on it: "daemon" is everywhere in THIS corpus and nowhere in a user's.
var lexicalFillerWords = map[string]bool{
	"has": true, "have": true, "had": true, "can": true, "could": true, "would": true,
	"should": true, "will": true, "shall": true, "may": true, "might": true, "must": true,
	"into": true, "from": true, "our": true, "your": true, "you": true, "we": true,
	"they": true, "them": true, "their": true, "there": true, "then": true, "than": true,
	"so": true, "if": true, "too": true, "very": true, "just": true, "only": true,
	"also": true, "did": true, "done": true, "i": true, "me": true, "my": true,
	"much": true, "many": true, "some": true, "need": true, "needs": true,
	"told": true, "tell": true, "about": true, "between": true,
}

// lexicalSuffixes are the endings stemQueryWord strips, longest first.
var lexicalSuffixes = []string{"ations", "ation", "ings", "ing", "ied", "ies", "edly", "ed", "es", "s"}

var plainLowercaseWord = regexp.MustCompile(`^[a-z]+$`)

// stemQueryWord strips one common English ending from a plain lowercase word,
// so the trigram index -- which matches SUBSTRINGS -- finds every inflection
// of it. "iterating" becomes "iterat", which is inside maxTurnIterations;
// "routing" becomes "rout", inside Route(. Unstemmed, neither question could
// meet its answer by keyword at all, whatever the ranking.
//
// ONLY PLAIN LOWERCASE WORDS. A word with a capital, digit, underscore or dot
// is something the user typed to be found as written -- SearchRequest, ZDR,
// fmt.Println -- and is left exactly alone.
//
// The stem keeps at least four letters, so a short word keeps its meaning
// ("uses" stays "uses", not "us"), and a long stem ending in i, y or e drops it
// so the stem sits inside every form ("deployed" -> "deplo", inside deploy and
// deployment alike; "classified" -> "classif", inside classifyFile).
func stemQueryWord(t string) string {
	if !plainLowercaseWord.MatchString(t) {
		return t
	}
	for _, suf := range lexicalSuffixes {
		if strings.HasSuffix(t, suf) && len(t)-len(suf) >= 4 {
			b := t[:len(t)-len(suf)]
			if len(b) > 4 && strings.ContainsRune("iye", rune(b[len(b)-1])) {
				b = b[:len(b)-1]
			}
			return b
		}
	}
	return t
}

// lexicalPolicy is how the keyword tier builds its query and ranks what
// matches. A struct for the reason expandPolicy is one: the retrieval sweep
// scores candidate policies through searchWith itself, so the instrument and
// production read the same fields and cannot drift apart. The ZERO VALUE is the
// pre-2026-10 behaviour exactly.
type lexicalPolicy struct {
	// PathWeight is the BM25 weight of the file_path column against content's
	// 1.0. Zero means the path neither scores nor matches.
	PathWeight float64
	// FillerStopwords drops lexicalFillerWords as well as lexicalStopwords.
	FillerStopwords bool
	// StemWords strips one common ending from plain lowercase words.
	StemWords bool
}

// defaultLexicalPolicy is what production searches with.
var defaultLexicalPolicy = lexicalPolicy{}

// lexicalQueryTokenPattern extracts word-and-punctuation runs that look like
// code tokens -- letters, digits, underscore, and '.' (so dotted names like
// fmt.Println survive as one token) -- from a natural-language query.
var lexicalQueryTokenPattern = regexp.MustCompile(`[A-Za-z0-9_.]+`)

// maxLexicalQueryChars bounds the text that may be turned into an FTS5 MATCH.
//
// DERIVED FROM A BUDGET AND A MEASURED CURVE, not chosen for roundness. All
// three inputs are recorded so the number can be re-derived on another machine
// rather than inherited on faith.
//
//	BUDGET  100 ms for the lexical tier. It sits on the critical path before the
//	        first token of an answer, alongside embedding and the vector query,
//	        and 100 ms is the threshold at which a wait stops reading as instant.
//
//	CURVE   cost = k*n^2 in the LENGTH OF THE QUERY. Measured x4.05 per doubling
//	        (5.29, 3.95, 4.12 over 100k->200k->400k->800k). Near the bound,
//	        where fixed overhead no longer dominates, k settles at
//	        ~6.1e-8 ms/char^2: 32,000 chars -> 64 ms and 64,000 -> 249 ms.
//
//	CORPUS  IRRELEVANT, which is what makes one number portable across
//	        workspaces. At a fixed 100k query: 50 turns -> 726 ms, 500 -> 672 ms,
//	        5,000 -> 731 ms. The cost is in parsing and expanding the phrase, not
//	        in scanning the index, so a large workspace does not need a smaller
//	        bound.
//
// Solving k*n^2 = 100 ms gives n ~= 40,500. 32,768 is the largest power of two
// under it and measures at 64 ms, 1.6x inside budget; the next one up, 65,536,
// measures 249 ms, 2.5x over. The budget and the curve pick this number
// between them -- it is not a preference.
//
// WHAT IT REMOVES. The only other bound on this input is the 16 MiB whole-request
// cap, and at that length the same curve gives 6.1e-8 * (16,777,216)^2 ~= 4.8
// HOURS of CPU in one handler. (An earlier estimate of ~7 hours in this pass
// extrapolated from an 800k point on a smaller corpus; 4.8 hours is the figure
// from the constant measured near the bound, and supersedes it.) Cancelling
// cannot rescue that: sqlite3_interrupt is not polled inside an FTS5 phrase
// match -- see serveConn and TestSQLiteCancellation_DoesNotReachAnFTS5PhraseMatch.
// Not starting the work is the only lever there is.
const maxLexicalQueryChars = 32768

// errLexicalQueryTooLong is returned instead of running a MATCH whose cost is
// unbounded. Callers treat it as "no lexical answer is possible for this input",
// which the two of them do differently on purpose -- see lexicalQueryTooLong.
var errLexicalQueryTooLong = errors.New("query exceeds the maximum searchable length")

// lexicalQueryTooLong is the ONE predicate behind both the enforcement and the
// reporting of this bound.
//
// It has two callers and they are not duplicates. FTSChunkStore.Search and
// MemoryStore.SearchTurns ENFORCE it, so the cost can never be paid however the
// query arrives; similarChunks (context.go) consults it only to SAY that the
// keyword tier is being skipped this turn. Enforcement without reporting is a
// silent degradation, and reporting without enforcement is a suggestion. Both
// read the same function so the two cannot drift apart.
func lexicalQueryTooLong(query string) bool { return len(query) > maxLexicalQueryChars }

// buildLexicalQuery converts a natural-language question into an FTS5 MATCH
// expression over the trigram-tokenized code index: split into tokens, drop
// stopwords, quote each remaining token individually and OR them together.
//
// Each token is quoted (not left bare) so the trigram tokenizer requires it
// to match as a CONTIGUOUS substring -- e.g. "refusal" must appear as the
// literal substring "refusal", not merely have all its trigrams scattered
// somewhere in the row (see turns_fts's doc comment in search.go for that
// distinction). Tokens are ORed rather than ANDed: the query terms are
// English words the user chose, not necessarily every one of which appears
// verbatim near the answer, so requiring only at least one to hit (ranked by
// bm25, which naturally favors chunks matching more/rarer terms) is more
// forgiving than an AND that could return zero rows over word-choice
// mismatches.
//
// p adds to that: filler words dropped too (FillerStopwords) and one common
// ending stripped from plain words (StemWords). The zero policy builds exactly
// what this function built before either existed.
//
// Returns "" when nothing but stopwords remain -- callers must treat that as
// "no lexical query is possible for this input" rather than passing an
// empty MATCH to FTS5.
func buildLexicalQuery(query string, p lexicalPolicy) string {
	tokens := lexicalQueryTokenPattern.FindAllString(query, -1)
	// A repeated word counts once: BM25 sums over the query's phrases, so a
	// duplicate would weigh its term twice for no reason the user gave. Part of
	// the new builder only, so the zero policy stays the old one exactly.
	dedupe := p.FillerStopwords || p.StemWords
	seen := make(map[string]bool, len(tokens))
	var terms []string
	for _, t := range tokens {
		lower := strings.ToLower(t)
		if lexicalStopwords[lower] || (p.FillerStopwords && lexicalFillerWords[lower]) {
			continue
		}
		if p.StemWords {
			t = stemQueryWord(t)
		}
		if dedupe {
			key := strings.ToLower(t)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		terms = append(terms, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " OR ")
}

// Search runs a lexical (FTS5 MATCH) keyword/substring search over the code
// index under defaultLexicalPolicy, most-relevant match first (BM25), capped
// at k. Returns (nil, nil) -- not an error -- when query reduces to nothing but
// stopwords, mirroring VectorStore.Query's "empty store, empty result"
// convention: a caller (retrieveTopK) should treat "the lexical tier found
// nothing" identically whether that's because of an empty index or an
// unmatchable query.
func (s *FTSChunkStore) Search(ctx context.Context, query string, k int) ([]Chunk, error) {
	return s.searchWith(ctx, query, k, defaultLexicalPolicy)
}

// searchWith is Search under an explicit policy. It is production's own path,
// and the retrieval sweep calls it with candidate policies so that what it
// scores is what would ship.
func (s *FTSChunkStore) searchWith(ctx context.Context, query string, k int, p lexicalPolicy) ([]Chunk, error) {
	if k <= 0 {
		return nil, nil
	}
	// Bounded BEFORE the phrase is built, so the cost is never started rather
	// than started and abandoned -- abandoning it is not available (see
	// maxLexicalQueryChars).
	if lexicalQueryTooLong(query) {
		return nil, errLexicalQueryTooLong
	}

	ftsQuery := buildLexicalQuery(query, p)
	if ftsQuery == "" {
		return nil, nil
	}
	// A path that does not score must not match either. Without the column
	// filter, a chunk whose only tie to the query is its file name would join
	// the list with a score of zero -- a row the old layout could never return.
	if p.PathWeight <= 0 {
		ftsQuery = "{content} : (" + ftsQuery + ")"
	}

	// The weights follow the column order of codeChunksFTSTableDDL; the
	// UNINDEXED columns hold no terms, so theirs never matter.
	rows, err := s.db.QueryContext(ctx,
		`SELECT file_path, start_line, end_line, class, content, chunk_id
		 FROM code_chunks_fts
		 WHERE code_chunks_fts MATCH ?
		 ORDER BY bm25(code_chunks_fts, 1.0, 0.0, ?, 0.0, 0.0, 0.0)
		 LIMIT ?`,
		ftsQuery, p.PathWeight, k,
	)
	if err != nil {
		return nil, fmt.Errorf("searching lexical index: %w", err)
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		var class string
		if err := rows.Scan(&c.FilePath, &c.StartLine, &c.EndLine, &class, &c.Content, &c.ID); err != nil {
			return nil, fmt.Errorf("scanning lexical hit: %w", err)
		}
		c.Class = FileClass(class)
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("searching lexical index: %w", err)
	}
	return chunks, nil
}
