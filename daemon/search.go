package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// turnsFTSTableDDL creates the lexical search index over turns.content -- an
// FTS5 virtual table using the trigram tokenizer (substring matching on
// code-like tokens, e.g. "fmt.Println", not just whole-word matches; see
// fts5_probe_test.go for the standalone proof this tokenizer is available in
// this build). This is a STANDALONE fts5 table, not an "external content"
// table pointed at turns -- content is duplicated into it rather than
// referenced, trading a little extra disk for avoiding FTS5's
// external-content column-mapping rules entirely. At this store's write
// volume (one row per conversation turn) that tradeoff is a non-issue.
//
// workspace is stored as an UNINDEXED column (carried alongside each row,
// never tokenized) so SearchTurns can filter to one workspace as part of the
// FTS scan itself rather than a separate pass.
//
// turns_fts.rowid is always kept EQUAL to the source turns.id (see the
// insert trigger below), which is what lets SearchTurns join back to turns
// for role/created_at without a separate id column of its own.
const turnsFTSTableDDL = `
CREATE VIRTUAL TABLE IF NOT EXISTS turns_fts USING fts5(
	content,
	workspace UNINDEXED,
	tokenize='trigram'
)`

// turnsFTSInsertTriggerDDL keeps turns_fts in sync with turns at the
// database level. AppendTurn's own SQL (memory.go) never mentions turns_fts
// at all -- the base write path is completely unchanged by this feature's
// existence, and would keep working identically if turns_fts and these
// triggers didn't exist (see
// TestAppendTurn_WorksEvenWithoutSearchIndexOrTriggers in search_test.go).
const turnsFTSInsertTriggerDDL = `
CREATE TRIGGER IF NOT EXISTS turns_ai AFTER INSERT ON turns BEGIN
	INSERT INTO turns_fts(rowid, content, workspace) VALUES (new.id, new.content, new.workspace);
END`

// turnsFTSDeleteTriggerDDL mirrors turns' only other mutation --
// ClearWorkspace's bulk DELETE (memory.go) -- firing once per row deleted so
// turns_fts never accumulates entries for turns that no longer exist. There
// is deliberately no UPDATE trigger: nothing in this codebase ever updates
// an existing turns row (AppendTurn only inserts, ClearWorkspace only bulk
// deletes) -- add one if that ever changes.
const turnsFTSDeleteTriggerDDL = `
CREATE TRIGGER IF NOT EXISTS turns_ad AFTER DELETE ON turns BEGIN
	DELETE FROM turns_fts WHERE rowid = old.id;
END`

// createSearchIndex creates turns_fts and its sync triggers if they don't
// already exist. Safe to call on every startup (see ensureMemorySchema in
// memory.go) -- IF NOT EXISTS on all three statements makes it idempotent.
func createSearchIndex(db *sql.DB) error {
	if _, err := db.Exec(turnsFTSTableDDL); err != nil {
		return fmt.Errorf("creating turns_fts: %w", err)
	}
	if _, err := db.Exec(turnsFTSInsertTriggerDDL); err != nil {
		return fmt.Errorf("creating turns_fts insert trigger: %w", err)
	}
	if _, err := db.Exec(turnsFTSDeleteTriggerDDL); err != nil {
		return fmt.Errorf("creating turns_fts delete trigger: %w", err)
	}
	return nil
}

// backfillSearchIndex populates turns_fts from every row already in turns.
// Needed because a newly-created FTS5 table starts empty even on a
// pre-existing, already-populated memory.db -- the insert trigger only
// covers turns written AFTER turns_fts existed. Called exactly once, from
// ensureMemorySchema's version<2 migration branch, which itself only ever
// runs once per database (guarded by schema_meta.version, the same gate
// skills.go's ensureSchema pattern already relies on) -- so this needs no
// separate "already ran" check of its own.
func backfillSearchIndex(db *sql.DB) error {
	if _, err := db.Exec(`INSERT INTO turns_fts(rowid, content, workspace) SELECT id, content, workspace FROM turns`); err != nil {
		return fmt.Errorf("backfilling turns_fts: %w", err)
	}
	return nil
}

// searchSnippetTokenBudget is snippet()'s "max tokens" argument. With the
// trigram tokenizer each "token" is only 3 characters (heavily
// overlapping), NOT a word like it would be with the default unicode61
// tokenizer -- passing a small count here (e.g. the 10-20 that would be
// reasonable for word tokens) truncates snippets after only a
// dozen-ish characters, mid-word, and can even cut off part of the
// highlighted match itself. Verified empirically: a budget of 12 truncated
// "goroutine" down to "goro" inside the highlight brackets; 64 renders the
// full match and surrounding context cleanly for normal turn-length text.
const searchSnippetTokenBudget = 64

// SearchHit is one lexical match returned by SearchTurns.
type SearchHit struct {
	Role      string
	Snippet   string
	CreatedAt string
}

// SearchTurns performs a lexical (FTS5 MATCH) keyword/substring search over
// workspace's stored turns, most-relevant match first (FTS5's built-in bm25
// rank), capped at limit. This complements the existing RAG/embedding
// retrieval rather than replacing it -- it never touches the vector store,
// and does something entirely different: exact lexical matching over what
// the user and model actually said in past turns, not semantic similarity
// over workspace code.
//
// query is always wrapped as one literal double-quoted phrase (any embedded
// '"' doubled, FTS5's own escaping convention) rather than passed through as
// raw FTS5 query syntax. Two reasons: (1) with the trigram tokenizer, an
// UNQUOTED query matches rows containing the same trigrams scattered
// anywhere, not necessarily contiguously -- quoting is what makes
// "fmt.Println" match the actual substring "fmt.Println" rather than any
// row that happens to contain all of its 3-character pieces in any order;
// (2) a raw user-typed string could otherwise be interpreted as FTS5
// operators (OR/NOT/-/parentheses), making search behavior depend on
// whatever punctuation a user happened to type -- the same "don't let
// external input dictate query semantics" instinct as history.go's role
// validation on conversation content.
func (s *MemoryStore) SearchTurns(ctx context.Context, workspace, query string, limit int) ([]SearchHit, error) {
	phrase := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.role, snippet(turns_fts, 0, '[', ']', '...', ?), t.created_at
		 FROM turns_fts
		 JOIN turns t ON t.id = turns_fts.rowid
		 WHERE turns_fts MATCH ? AND turns_fts.workspace = ?
		 ORDER BY rank
		 LIMIT ?`,
		searchSnippetTokenBudget, phrase, workspace, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("searching turns: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.Role, &h.Snippet, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning search hit: %w", err)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("searching turns: %w", err)
	}
	return hits, nil
}
