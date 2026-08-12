package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	Close() error
}

// codeChunksFTSTableDDL mirrors turns_fts's shape exactly (search.go): a
// STANDALONE fts5 table (content duplicated in, not "external content"
// mapped, sidestepping FTS5's external-content column rules) using the
// trigram tokenizer, which matches raw substrings regardless of identifier
// casing (camelCase, snake_case, dotted names like fmt.Println all just
// work -- see fts5_probe_test.go for the proof this is compiled into this
// build). file_path/start_line/end_line/class are UNINDEXED (carried
// alongside each row, never tokenized) so a hit can be turned directly back
// into a Chunk without a join.
const codeChunksFTSTableDDL = `
CREATE VIRTUAL TABLE IF NOT EXISTS code_chunks_fts USING fts5(
	content,
	chunk_id UNINDEXED,
	file_path UNINDEXED,
	start_line UNINDEXED,
	end_line UNINDEXED,
	class UNINDEXED,
	tokenize='trigram'
)`

// FTSChunkStore is a LexicalStore backed by a local, on-disk SQLite FTS5
// database -- the same modernc.org/sqlite dependency memory.go
// already uses, so this adds no new dependency.
type FTSChunkStore struct {
	db *sql.DB
}

// NewFTSChunkStore opens (creating if absent) the lexical index at
// indexDir/lexical.db. Single pooled connection, same rationale as
// MemoryStore: this store's write volume (one upsert per index
// run) never justifies a pool, and SQLite's concurrent-writer story is poor
// enough that avoiding it entirely is simplest.
func NewFTSChunkStore(indexDir string) (*FTSChunkStore, error) {
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
	if _, err := db.Exec(codeChunksFTSTableDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("creating code_chunks_fts: %w", err)
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

	return &FTSChunkStore{db: db}, nil
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
func (s *FTSChunkStore) DeleteByFilePath(ctx context.Context, relPath string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM code_chunks_fts WHERE file_path = ?`, relPath); err != nil {
		return fmt.Errorf("deleting lexical chunks for %s: %w", relPath, err)
	}
	return nil
}

func (s *FTSChunkStore) Upsert(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning lexical upsert transaction: %w", err)
	}
	defer tx.Rollback()

	del, err := tx.PrepareContext(ctx, `DELETE FROM code_chunks_fts WHERE chunk_id = ?`)
	if err != nil {
		return fmt.Errorf("preparing lexical delete: %w", err)
	}
	defer del.Close()

	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO code_chunks_fts(chunk_id, content, file_path, start_line, end_line, class) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("preparing lexical insert: %w", err)
	}
	defer ins.Close()

	for _, c := range chunks {
		if _, err := del.ExecContext(ctx, c.ID); err != nil {
			return fmt.Errorf("deleting stale lexical chunk %s: %w", c.ID, err)
		}
		if _, err := ins.ExecContext(ctx, c.ID, c.Content, c.FilePath, c.StartLine, c.EndLine, string(c.Class)); err != nil {
			return fmt.Errorf("inserting lexical chunk %s: %w", c.ID, err)
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

// lexicalQueryTokenPattern extracts word-and-punctuation runs that look like
// code tokens -- letters, digits, underscore, and '.' (so dotted names like
// fmt.Println survive as one token) -- from a natural-language query.
var lexicalQueryTokenPattern = regexp.MustCompile(`[A-Za-z0-9_.]+`)

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
// Returns "" when nothing but stopwords remain -- callers must treat that as
// "no lexical query is possible for this input" rather than passing an
// empty MATCH to FTS5.
func buildLexicalQuery(query string) string {
	tokens := lexicalQueryTokenPattern.FindAllString(query, -1)
	var terms []string
	for _, t := range tokens {
		if lexicalStopwords[strings.ToLower(t)] {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " OR ")
}

// Search runs a lexical (FTS5 MATCH) keyword/substring search over the code
// index, most-relevant match first (FTS5's built-in bm25 rank), capped at k.
// Returns (nil, nil) -- not an error -- when query reduces to nothing but
// stopwords, mirroring VectorStore.Query's "empty store, empty result"
// convention: a caller (retrieveTopK) should treat "the lexical tier found
// nothing" identically whether that's because of an empty index or an
// unmatchable query.
func (s *FTSChunkStore) Search(ctx context.Context, query string, k int) ([]Chunk, error) {
	if k <= 0 {
		return nil, nil
	}
	ftsQuery := buildLexicalQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT file_path, start_line, end_line, class, content, chunk_id
		 FROM code_chunks_fts
		 WHERE code_chunks_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`,
		ftsQuery, k,
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
