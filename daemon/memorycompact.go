package main

import (
	"context"
	"database/sql"
	"fmt"
)

// KEEPING memory.db SMALL. MEASURED on a real one before this existed: 60
// turns, 43 KB of text, in a 1.2 MB file --
//
//	602 KB  free pages, never returned after history was cleared or pruned
//	442 KB  trigram search index, fragmented across many small segments
//	 61 KB  the search index's own second copy of the text (fixed in v5)
//	 65 KB  the turns themselves
//
// With the three fixes here and in search.go it is 246 KB, search and
// snippets unchanged. Compressing the text itself was measured too and left
// out: it would save ~40 KB more and break snippet(), which reads the text.

// compactMemory runs once per open. The first time on a database it switches
// on incremental auto-vacuum (which only takes effect through a VACUUM), and
// every time it merges the search index into one segment and returns free
// pages to the filesystem.
func compactMemory(db *sql.DB) error {
	var mode int
	if err := db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return fmt.Errorf("reading auto_vacuum: %w", err)
	}
	if mode != 2 { // 2 = INCREMENTAL
		if _, err := db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
			return fmt.Errorf("enabling incremental auto-vacuum: %w", err)
		}
		if _, err := db.Exec(`VACUUM`); err != nil {
			return fmt.Errorf("vacuuming memory db: %w", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO turns_fts(turns_fts) VALUES ('optimize')`); err != nil {
		return fmt.Errorf("optimizing the search index: %w", err)
	}
	return reclaimFreePages(context.Background(), db)
}

// reclaimFreePages hands the file's free pages back to the filesystem. Cheap
// when there are none, so it runs after every delete.
//
// READ AS A QUERY, TO THE END. incremental_vacuum frees one page per STEP of
// the statement, and a plain Exec stepped it once -- measured: 253 pages
// became 252 with 144 still free. Draining the rows is what runs it through.
func reclaimFreePages(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA incremental_vacuum`)
	if err != nil {
		return fmt.Errorf("reclaiming free pages: %w", err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("reclaiming free pages: %w", err)
	}
	return rows.Close()
}
