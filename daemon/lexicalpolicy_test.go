package main

// The keyword tier's 2026-10 levers (lexicalPolicy): a searchable file path,
// filler stopwords, and one ending stripped from plain words. Each test below
// fails with its lever removed, and the zero policy is pinned to the old
// builder so "off" keeps meaning what it meant before.

import (
	"context"
	"testing"
)

func TestStemQueryWordFindsTheSharedStem(t *testing.T) {
	for in, want := range map[string]string{
		"iterating":  "iterat", // inside maxTurnIterations
		"routing":    "rout",   // inside Route(
		"created":    "creat",
		"tokens":     "token",
		"files":      "file",
		"classified": "classif",
		"deployed":   "deplo",
		"tests":      "test",
		// Short words keep their meaning instead of losing it.
		"uses":   "uses",
		"string": "string",
		// Anything typed to be found as written is left exactly alone.
		"SearchRequest":        "SearchRequest",
		"ZDR":                  "ZDR",
		"fmt.Println":          "fmt.Println",
		"zdrRefusalSubstrings": "zdrRefusalSubstrings",
		"max_tokens":           "max_tokens",
		"utf8":                 "utf8",
	} {
		if got := stemQueryWord(in); got != want {
			t.Errorf("stemQueryWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFillerWordsAreDroppedOnlyUnderThePolicy(t *testing.T) {
	q := "how can our grep tool tell which files it needs"
	if got, want := buildLexicalQuery(q, lexicalPolicy{}),
		`"can" OR "our" OR "grep" OR "tool" OR "tell" OR "files" OR "needs"`; got != want {
		t.Errorf("zero policy built %s, want %s -- the zero value must stay the old builder", got, want)
	}
	if got, want := buildLexicalQuery(q, lexicalPolicy{FillerStopwords: true}),
		`"grep" OR "tool" OR "files"`; got != want {
		t.Errorf("with filler stopwords built %s, want %s", got, want)
	}
}

// A repeated word weighs once under the new builder, and twice under the zero
// policy, exactly as it always did.
func TestARepeatedWordIsSearchedOnce(t *testing.T) {
	if got, want := buildLexicalQuery("chunk chunks Chunk", lexicalPolicy{StemWords: true}), `"chunk"`; got != want {
		t.Errorf("built %s, want %s: one word, in three spellings, is one term", got, want)
	}
	if got, want := buildLexicalQuery("chunk chunk", lexicalPolicy{}), `"chunk" OR "chunk"`; got != want {
		t.Errorf("zero policy built %s, want %s", got, want)
	}
}

func newPolicyTestStore(t *testing.T, chunks ...Chunk) *FTSChunkStore {
	t.Helper()
	store, err := NewFTSChunkStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Upsert(context.Background(), chunks); err != nil {
		t.Fatal(err)
	}
	return store
}

func lexHitIDs(hits []Chunk) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

// THE PATH IS SEARCHABLE, AND WEIGHTED. A chunk can be the answer to a question
// that names its FILE while its own forty lines never use the word -- the
// keyword tier could not see that until the path was indexed.
func TestTheFilePathIsSearchableAndWeighted(t *testing.T) {
	store := newPolicyTestStore(t,
		lexChunk("protocol/peerauth_linux.go:1-40", "protocol/peerauth_linux.go", "func checkPeer(fd int) error {\n\treturn nil\n}", 1),
		lexChunk("daemon/other.go:1-40", "daemon/other.go", "func unrelated() {}", 1),
		lexChunk("proxy/ratelimit.go:1-40", "proxy/ratelimit.go", "func allow(n int) bool { return n < limit }", 1),
		lexChunk("daemon/budget.go:1-40", "daemon/budget.go", "if used > limit {\n\treturn limit\n}", 1),
		// Bystanders, so the terms under test are rare enough for BM25 to give
		// them a positive weight: in a four-row table "limit" is in half the
		// rows, and FTS5 clamps that rarity to almost nothing.
		lexChunk("daemon/alpha.go:1-40", "daemon/alpha.go", "func alpha() {}", 1),
		lexChunk("daemon/beta.go:1-40", "daemon/beta.go", "func beta() {}", 1),
	)
	ctx := context.Background()

	hits, err := store.searchWith(ctx, "peerauth", 10, lexicalPolicy{PathWeight: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].FilePath != "protocol/peerauth_linux.go" {
		t.Errorf("a query naming the file found %v; want the chunk of protocol/peerauth_linux.go, "+
			"whose text never says peerauth -- only its path does", lexHitIDs(hits))
	}
	hits, err = store.searchWith(ctx, "peerauth", 10, lexicalPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("with the path weighted zero it must not match either, but found %v", lexHitIDs(hits))
	}

	// The weight orders, it does not just admit: by text alone budget.go wins on
	// two mentions of "limit" to one, and the path puts ratelimit.go first.
	byText, err := store.searchWith(ctx, "limit", 10, lexicalPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	byPath, err := store.searchWith(ctx, "limit", 10, lexicalPolicy{PathWeight: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(byText) != 2 || byText[0].FilePath != "daemon/budget.go" {
		t.Fatalf("precondition: by text alone %v, want daemon/budget.go first", lexHitIDs(byText))
	}
	if len(byPath) != 2 || byPath[0].FilePath != "proxy/ratelimit.go" {
		t.Errorf("with the path weighted, %v; want proxy/ratelimit.go first", lexHitIDs(byPath))
	}
}

// A STEMMED WORD MEETS ITS INFLECTIONS. The trigram index matches substrings, so
// "iterat" is inside "Iterations" where "iterating" is not.
func TestAStemmedWordMeetsItsInflections(t *testing.T) {
	store := newPolicyTestStore(t,
		lexChunk("daemon/loop.go:1-40", "daemon/loop.go", "if turn.calls >= bud.maxTurnIterations {\n\treturn\n}", 1),
	)
	ctx := context.Background()
	q := "when does it stop iterating"

	if hits, err := store.searchWith(ctx, q, 10, lexicalPolicy{}); err != nil || len(hits) != 0 {
		t.Fatalf("precondition: unstemmed, %q reached %v (err %v); it shares no substring with the chunk",
			q, lexHitIDs(hits), err)
	}
	hits, err := store.searchWith(ctx, q, 10, lexicalPolicy{StemWords: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("stemmed, %q found %v; want daemon/loop.go, whose maxTurnIterations holds \"iterat\"",
			q, lexHitIDs(hits))
	}
}
