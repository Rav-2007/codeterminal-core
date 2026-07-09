package main

import (
	"database/sql"
	"strings"
	"testing"
)

// TestFTS5TrigramAvailableInBuild is a standalone probe, deliberately
// isolated from MemoryStore/turns -- it proves FTS5 plus the trigram
// tokenizer are actually compiled into THIS build's modernc.org/sqlite
// dependency before anything in the real memory store is built on that
// assumption. If this test ever starts failing (e.g. a modernc.org/sqlite
// upgrade drops FTS5), every downstream search feature built on top of it
// would silently break, so this is the canary: run it in isolation first.
func TestFTS5TrigramAvailableInBuild(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE VIRTUAL TABLE probe USING fts5(content, tokenize='trigram')`); err != nil {
		t.Fatalf("FTS5 (trigram tokenizer) NOT available in this build: %v", err)
	}

	rows := []string{
		"what is a goroutine?",
		"fmt.Println(\"hello world\")",
		"how do channels work in go",
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO probe(content) VALUES (?)`, r); err != nil {
			t.Fatalf("inserting probe row %q: %v", r, err)
		}
	}

	// Keyword query: a whole word present in exactly one row.
	t.Run("keyword match", func(t *testing.T) {
		rowsGot, err := db.Query(`SELECT content FROM probe WHERE probe MATCH 'goroutine'`)
		if err != nil {
			t.Fatalf("MATCH query: %v", err)
		}
		defer rowsGot.Close()
		var got []string
		for rowsGot.Next() {
			var content string
			if err := rowsGot.Scan(&content); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, content)
		}
		if len(got) != 1 || got[0] != rows[0] {
			t.Fatalf("keyword match got %v, want exactly [%q]", got, rows[0])
		}
	})

	// Substring query: a code-like token, mid-string, not a whole "word" by
	// unicode61 standards -- this is specifically what the trigram
	// tokenizer buys over the default tokenizer.
	t.Run("substring match on code token", func(t *testing.T) {
		rowsGot, err := db.Query(`SELECT content FROM probe WHERE probe MATCH '"fmt.Println"'`)
		if err != nil {
			t.Fatalf("MATCH query: %v", err)
		}
		defer rowsGot.Close()
		var got []string
		for rowsGot.Next() {
			var content string
			if err := rowsGot.Scan(&content); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, content)
		}
		if len(got) != 1 || got[0] != rows[1] {
			t.Fatalf("substring match got %v, want exactly [%q]", got, rows[1])
		}
	})

	// snippet()/highlight(): both must be callable and must actually wrap
	// the matched text, not just return it verbatim.
	t.Run("snippet and highlight", func(t *testing.T) {
		var snip string
		err := db.QueryRow(
			`SELECT snippet(probe, 0, '[', ']', '...', 8) FROM probe WHERE probe MATCH 'goroutine'`,
		).Scan(&snip)
		if err != nil {
			t.Fatalf("snippet(): %v", err)
		}
		if !strings.Contains(snip, "[goroutine]") {
			t.Errorf("snippet() = %q, want it to wrap the match in [...]", snip)
		}

		var hl string
		err = db.QueryRow(
			`SELECT highlight(probe, 0, '[', ']') FROM probe WHERE probe MATCH 'goroutine'`,
		).Scan(&hl)
		if err != nil {
			t.Fatalf("highlight(): %v", err)
		}
		if !strings.Contains(hl, "[goroutine]") {
			t.Errorf("highlight() = %q, want it to wrap the match in [...]", hl)
		}
	})
}
