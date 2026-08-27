package main

// The chunk_index companion table is what makes the two lexical write paths
// seek instead of scan (see chunkIndexDDL for the measurement that motivated
// it). These tests pin the properties that make it safe to rely on: the
// one-to-one invariant, replacement rather than duplication, and the migration
// of a database written before the table existed.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func lexChunk(id, path, content string, start int) Chunk {
	return Chunk{
		ID: id, FilePath: path, Content: content,
		StartLine: start, EndLine: start + 39, Class: FileClassCode,
	}
}

// countRows is deliberately a raw query rather than a store method: these tests
// are about the storage layout, so they have to look at it directly.
func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

// The invariant every method depends on: one chunk_index row per FTS row.
func assertOneToOne(t *testing.T, db *sql.DB) {
	t.Helper()
	fts := countRows(t, db, "code_chunks_fts")
	idx := countRows(t, db, "chunk_index")
	if fts != idx {
		t.Errorf("one-to-one invariant broken: %d FTS row(s) vs %d chunk_index row(s)", fts, idx)
	}
	var orphans int
	if err := db.QueryRow(
		`SELECT count(*) FROM code_chunks_fts WHERE rowid NOT IN (SELECT fts_rowid FROM chunk_index)`).
		Scan(&orphans); err != nil {
		t.Fatalf("checking for orphans: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d FTS row(s) have no chunk_index entry and can never be updated or deleted", orphans)
	}
}

// Re-upserting the same chunk id must REPLACE, not accumulate -- the contract
// the old delete-by-chunk_id path provided and the rowid path has to preserve.
func TestLexicalUpsertReplacesRatherThanDuplicates(t *testing.T) {
	store, err := NewFTSChunkStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	first := []Chunk{lexChunk("a.go:0-39", "a.go", "func OriginalSymbol() {}", 0)}
	if err := store.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := []Chunk{lexChunk("a.go:0-39", "a.go", "func ReplacementSymbol() {}", 0)}
	if err := store.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}

	if n := countRows(t, store.db, "code_chunks_fts"); n != 1 {
		t.Errorf("re-upserting one chunk id left %d row(s), want 1", n)
	}
	assertOneToOne(t, store.db)

	// The replacement must be what searches find, and the original must be gone.
	hits, err := store.Search(ctx, "ReplacementSymbol", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("the replacement content is not searchable: %d hit(s)", len(hits))
	}
	stale, err := store.Search(ctx, "OriginalSymbol", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Errorf("the pre-edit content is still being served: %d hit(s)", len(stale))
	}
}

// DeleteByFilePath must take every chunk of one file and nothing belonging to
// any other -- reindexFile calls it on every applied edit.
func TestLexicalDeleteByFilePathIsScopedToOneFile(t *testing.T) {
	store, err := NewFTSChunkStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	if err := store.Upsert(ctx, []Chunk{
		lexChunk("a.go:0-39", "a.go", "package a\nfunc AlphaOne() {}", 0),
		lexChunk("a.go:40-79", "a.go", "func AlphaTwo() {}", 40),
		lexChunk("b.go:0-39", "b.go", "package b\nfunc BetaOne() {}", 0),
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteByFilePath(ctx, "a.go"); err != nil {
		t.Fatal(err)
	}

	if n := countRows(t, store.db, "code_chunks_fts"); n != 1 {
		t.Errorf("after deleting a.go's two chunks, %d row(s) remain, want 1", n)
	}
	assertOneToOne(t, store.db)

	if hits, _ := store.Search(ctx, "AlphaOne", 10); len(hits) != 0 {
		t.Errorf("a deleted file is still searchable: %d hit(s)", len(hits))
	}
	if hits, _ := store.Search(ctx, "BetaOne", 10); len(hits) != 1 {
		t.Errorf("deleting a.go also removed b.go: %d hit(s) for BetaOne, want 1", len(hits))
	}
}

// openLegacyLexicalDB writes a database in the pre-chunk_index shape: the FTS
// table alone, populated, with no companion table. This is what is already on
// disk in every existing install.
func openLegacyLexicalDB(t *testing.T, dir string, rows []Chunk, duplicate bool) string {
	t.Helper()
	path := filepath.Join(dir, lexicalDBFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(codeChunksFTSTableDDL); err != nil {
		t.Fatal(err)
	}
	insert := func(c Chunk) {
		if _, err := db.Exec(
			`INSERT INTO code_chunks_fts(chunk_id, content, file_path, start_line, end_line, class) VALUES (?,?,?,?,?,?)`,
			c.ID, c.Content, c.FilePath, c.StartLine, c.EndLine, string(c.Class)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range rows {
		insert(c)
		if duplicate {
			// The old delete-by-chunk_id path could leave a second row under the
			// same id behind. Reproduce that so the sweep is tested against the
			// damage it exists to repair, not a hypothetical.
			insert(c)
		}
	}
	return path
}

// A database written before chunk_index existed must come up working: backfilled,
// searchable, and deletable. Without the backfill every one of its rows would be
// unreachable by the new rowid-based delete.
func TestLexicalLegacyDatabaseIsMigratedOnOpen(t *testing.T) {
	dir := t.TempDir()
	legacy := []Chunk{
		lexChunk("a.go:0-39", "a.go", "package a\nfunc LegacyAlpha() {}", 0),
		lexChunk("a.go:40-79", "a.go", "func LegacyBeta() {}", 40),
		lexChunk("b.go:0-39", "b.go", "package b\nfunc LegacyGamma() {}", 0),
	}
	openLegacyLexicalDB(t, dir, legacy, false)

	store, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatalf("opening a pre-chunk_index database failed: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	if n := countRows(t, store.db, "chunk_index"); n != len(legacy) {
		t.Errorf("backfill produced %d chunk_index row(s) for %d FTS row(s)", n, len(legacy))
	}
	assertOneToOne(t, store.db)

	// Content indexed by the old binary must still be findable.
	if hits, _ := store.Search(ctx, "LegacyGamma", 10); len(hits) != 1 {
		t.Errorf("content indexed before the migration is no longer searchable: %d hit(s)", len(hits))
	}

	// And the new delete path must reach rows it did not itself insert -- the
	// property that would silently not work without the backfill.
	if err := store.DeleteByFilePath(ctx, "a.go"); err != nil {
		t.Fatal(err)
	}
	if hits, _ := store.Search(ctx, "LegacyAlpha", 10); len(hits) != 0 {
		t.Errorf("a legacy row survived DeleteByFilePath: %d hit(s)", len(hits))
	}
	if n := countRows(t, store.db, "code_chunks_fts"); n != 1 {
		t.Errorf("%d FTS row(s) remain after deleting a.go's two, want 1", n)
	}
	assertOneToOne(t, store.db)
}

// Duplicate rows the old path could leave behind are unreachable forever once
// deletes go by rowid, so the migration sweeps them rather than inheriting them.
func TestLexicalMigrationSweepsUnreachableDuplicates(t *testing.T) {
	dir := t.TempDir()
	legacy := []Chunk{lexChunk("a.go:0-39", "a.go", "func DuplicatedSymbol() {}", 0)}
	openLegacyLexicalDB(t, dir, legacy, true) // two FTS rows, one chunk id

	store, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if n := countRows(t, store.db, "code_chunks_fts"); n != 1 {
		t.Errorf("the duplicate row was not swept: %d FTS row(s) remain, want 1", n)
	}
	assertOneToOne(t, store.db)

	// And the surviving row is still the real content, not an empty husk.
	hits, err := store.Search(context.Background(), "DuplicatedSymbol", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("the swept file is no longer searchable: %d hit(s)", len(hits))
	}
}

// An already-migrated database must not be re-swept on every open: the backfill
// is gated on chunk_index being empty, and a second open has to be a no-op.
func TestLexicalMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]Chunk, 0, 5)
	for i := 0; i < 5; i++ {
		chunks = append(chunks, lexChunk(
			fmt.Sprintf("f%d.go:0-39", i), fmt.Sprintf("f%d.go", i),
			fmt.Sprintf("func Symbol%d() {}", i), 0))
	}
	if err := store.Upsert(ctx, chunks); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	reopened, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	if n := countRows(t, reopened.db, "code_chunks_fts"); n != len(chunks) {
		t.Errorf("reopening changed the row count: %d, want %d", n, len(chunks))
	}
	assertOneToOne(t, reopened.db)
	if hits, _ := reopened.Search(ctx, "Symbol3", 10); len(hits) != 1 {
		t.Errorf("content did not survive a reopen: %d hit(s)", len(hits))
	}
}
