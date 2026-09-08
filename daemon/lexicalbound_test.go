package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// The retrieval bound: what it removes, what it deliberately does not change,
// and -- the part that had to be measured rather than assumed -- that it
// removes the cost rather than relocating it.
//
// Durations here are WALL-CLOCK. Every bound compares against a figure an order
// of magnitude away (12.5 s against 1 s, 3.6 s against 1 s), so machine speed
// cannot decide an outcome. The DETERMINISTIC facts -- which error is returned,
// whether the tier ran, what the client is told -- are asserted exactly.

func boundTestChunks(n int) []Chunk {
	chunks := make([]Chunk, n)
	for i := range chunks {
		chunks[i] = Chunk{
			ID: fmt.Sprintf("c%d", i), FilePath: fmt.Sprintf("pkg/file%d.go", i),
			StartLine: 1, EndLine: 20,
			Content: fmt.Sprintf("func Handler%d(ctx context.Context) error { return nil }", i),
		}
	}
	return chunks
}

// DETERMINISTIC. The derivation in maxLexicalQueryChars is only honest if the
// number stays where the budget put it.
func TestLexicalBound_IsTheDerivedNumber(t *testing.T) {
	if maxLexicalQueryChars != 32768 {
		t.Fatalf("maxLexicalQueryChars = %d. The doc comment derives 32,768 from a 100 ms budget and a "+
			"measured k of ~6.1e-8 ms/char^2. If the number moved, re-derive it there and record the "+
			"new measurement -- do not just update this test.", maxLexicalQueryChars)
	}
	if !lexicalQueryTooLong(strings.Repeat("x", maxLexicalQueryChars+1)) {
		t.Error("a query one char over the bound was not refused")
	}
	if lexicalQueryTooLong(strings.Repeat("x", maxLexicalQueryChars)) {
		t.Error("a query exactly at the bound was refused; the bound is off by one")
	}
}

// At the bound, the cost must still be inside the budget it was derived from.
func TestLexicalBound_AtTheBoundTheCostIsInsideBudget(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Upsert(t.Context(), boundTestChunks(200)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := store.Search(t.Context(), strings.Repeat("x", maxLexicalQueryChars), 10); err != nil {
		t.Fatalf("a query exactly at the bound was refused: %v", err)
	}
	elapsed := time.Since(start)
	// Budget is 100 ms; measured 64 ms. 1 s is 10x the measurement, so this
	// fails only if the curve has genuinely changed, not because a runner is slow.
	if elapsed > time.Second {
		t.Errorf("a query at the bound took %s; the 100 ms budget the bound was derived from no "+
			"longer holds, so the bound needs re-deriving", elapsed)
	}
}

// Over the bound, the tier declines instead of working. Uncancellable work is
// only stopped by not starting it.
func TestLexicalBound_OverTheBoundTheTierDeclinesImmediately(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Upsert(t.Context(), boundTestChunks(200)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	hits, err := store.Search(t.Context(), strings.Repeat("x", 400000), 10)
	elapsed := time.Since(start)

	if !errors.Is(err, errLexicalQueryTooLong) {
		t.Fatalf("a 400k query returned err=%v, want errLexicalQueryTooLong", err)
	}
	if hits != nil {
		t.Errorf("a refused query returned %d hits", len(hits))
	}
	// This measured 12.47 s before the bound.
	if elapsed > time.Second {
		t.Errorf("refusing a 400k query took %s; the phrase is still being built and matched", elapsed)
	}
}

// THE ONE I PREDICTED WOULD FAIL FIRST TIME. A bound applied at handleSearch
// while gatherContext has already paid the cost is not a fix, it is a
// relocation. So the PROMPT path is measured, not just the search handler.
func TestLexicalBound_ThePromptPathDoesNotPayTheCostEither(t *testing.T) {
	dir := t.TempDir()
	lexical, err := NewFTSChunkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lexical.Close()
	chunks := boundTestChunks(200)
	if err := lexical.Upsert(t.Context(), chunks); err != nil {
		t.Fatal(err)
	}

	var logbuf bytes.Buffer
	srv := &Server{
		logger:        log.New(&logbuf, "", 0),
		workspace:     t.TempDir(),
		embedder:      &fakeEmbedder{dim: 8},
		store:         fixedStore{chunks: chunks[:5]},
		lexicalStore:  lexical,
		retrievalTopK: 5,
	}

	start := time.Now()
	outcome := srv.gatherContext(t.Context(), strings.Repeat("x", 400000))
	elapsed := time.Since(start)

	// Measured 12.47 s in the lexical tier alone before the bound.
	if elapsed > 2*time.Second {
		t.Errorf("gatherContext on a 400k prompt took %s; the bound is not on this path and the cost "+
			"has only moved", elapsed)
	}
	// NOT A REFUSAL. The turn still gets grounded, by the semantic tier.
	if outcome.Skipped {
		t.Errorf("a long prompt was refused retrieval entirely (reason %q); it was supposed to drop "+
			"to semantic-only, not lose its turn", outcome.Reason)
	}
	if len(outcome.Chunks) == 0 {
		t.Error("a long prompt got no chunks at all; the semantic tier should still have answered")
	}
	// VISIBLE. A reduced tier that says nothing is the failure degraded.go exists to stop.
	if !strings.Contains(logbuf.String(), "keyword tier is skipped") {
		t.Errorf("the skipped lexical tier was not reported; log was %q", logbuf.String())
	}
}

// The explicit search is a different state from the implicit one and must not
// share its behaviour: the user asked, so they get told, with the limit, the
// actual length, and what to do instead.
func TestSearchRequest_OverTheBoundIsRefusedActionably(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	if err := mem.AppendTurn(t.Context(), "/ws", "user", "hello goroutines"); err != nil {
		t.Fatal(err)
	}
	srv := &Server{workspace: "/ws", memory: mem, logger: discardLogger()}

	var buf strings.Builder
	start := time.Now()
	srv.handleSearch(t.Context(), json.NewEncoder(&buf), protocol.SearchRequest{
		Search: true, Query: strings.Repeat("x", 400000), Limit: 5,
	})
	elapsed := time.Since(start)

	var resp protocol.SearchResponse
	if err := json.Unmarshal([]byte(buf.String()), &resp); err != nil {
		t.Fatalf("decoding: %v (raw %q)", err, buf.String())
	}
	if elapsed > time.Second {
		t.Errorf("refusing a 400k search took %s; the query ran before being refused", elapsed)
	}
	for _, want := range []string{"400000", fmt.Sprint(maxLexicalQueryChars), "distinctive phrase"} {
		if !strings.Contains(resp.Error, want) {
			t.Errorf("the refusal does not mention %q, so it does not say what happened or what to do: %q",
				want, resp.Error)
		}
	}
}

// F6. The doc comment claimed the query "is always wrapped as one literal
// double-quoted phrase"; a NUL defeated that wrapper and leaked the engine's
// own syntax error. Refused now, so the claim is true again.
func TestSearchRequest_NULIsRefusedAndTheEngineErrorNeverReachesTheClient(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	if err := mem.AppendTurn(t.Context(), "/ws", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	srv := &Server{workspace: "/ws", memory: mem, logger: discardLogger()}

	var buf strings.Builder
	srv.handleSearch(t.Context(), json.NewEncoder(&buf), protocol.SearchRequest{
		Search: true, Query: string(rune(0)) + "anything", Limit: 5,
	})
	var resp protocol.SearchResponse
	if err := json.Unmarshal([]byte(buf.String()), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if strings.Contains(resp.Error, "SQL logic error") || strings.Contains(resp.Error, "fts5") {
		t.Errorf("the engine's own error reached the client: %q", resp.Error)
	}
	if !strings.Contains(resp.Error, "NUL") {
		t.Errorf("the refusal does not say what was wrong: %q", resp.Error)
	}
}

// F5. Every other search failure goes through socketSafeError, like the two
// handlers that always did. Asserted on the routing, not on today's error set:
// "no path in the errors we happen to produce" is not a control.
func TestSearchClientError_RoutesUnrecognisedErrorsThroughSocketSafeError(t *testing.T) {
	srv := &Server{workspace: "/private/work", logger: discardLogger()}
	got := srv.searchClientError(fmt.Errorf("opening /private/work/state/memory.db: permission denied"), 10)
	if strings.Contains(got, "/private/work") {
		t.Errorf("an unrecognised search error carried an absolute path to the client: %q", got)
	}
}
