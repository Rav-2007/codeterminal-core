package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// WHAT IS LOOKED UP, AND WHAT NEVER IS. The misses matter as much as the hits:
// every word in the second half is one a small project could easily not
// contain, and a note that "please" is missing from the index would be true
// and worthless.
func TestOnlyNamesWrittenToBeFoundAreLookedUp(t *testing.T) {
	for query, want := range map[string]string{
		"where is parseConfigV2 defined":              "parseConfigV2",
		"what reads config/settings.yaml at startup?": "settings.yaml",
		"the MAX_RETRIES limit and fmt.Println.":      "MAX_RETRIES fmt.Println",
		"HTTPServer and httpServer and HTTPSERVER":    "HTTPServer",
		"how is sha256 used":                          "sha256",

		"Explain how the retry limit works, please.": "",
		"PLEASE DO NOT guess":                        "",
		"e.g. the 1st or 2nd item, i.e. v2 of 3.14":  "",
		"read/write and built-in client/server code": "",
		// Names the gates hide are never looked up: the index leaves them out,
		// and answering about them is answering what a listing refuses to.
		"where are credentials.json, deploy/.env and clientSecret read": "",
	} {
		if got := strings.Join(writtenNames(query), " "); got != want {
			t.Errorf("writtenNames(%q) = %q, want %q", query, got, want)
		}
	}

	many := "a_1 b_2 c_3 d_4 e_5 f_6 g_7 h_8 i_9 j_10 k_11"
	if got := writtenNames(many); len(got) != maxAbsentNamesAsked {
		t.Errorf("one search may add %d seeks, and %d names came back", maxAbsentNamesAsked, len(got))
	}
}

// THE INDEX'S OWN ANSWER, on a real one: a name in a chunk's text or in a
// file's path is held whatever its case, anything else is not -- and an index
// that cannot tell "missing" from "never indexed" says nothing at all.
func TestTheKeywordIndexSaysWhichNamesItDoesNotHold(t *testing.T) {
	ctx := context.Background()
	store, err := NewFTSChunkStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if got := store.namesNotHeld(ctx, []string{"parseConfig"}); got != nil {
		t.Fatalf("an index with nothing in it called %q missing; it has not looked at anything", got)
	}

	if err := store.Upsert(ctx, []Chunk{
		lexChunk("conf/settings.yml:1-40", "conf/settings.yml", "retries: 3\n", 1),
		lexChunk("main.go:1-40", "main.go", "package main\n\nfunc parseConfig() {}\n", 1),
	}); err != nil {
		t.Fatal(err)
	}
	asked := []string{"parseConfig", "PARSECONFIG", "settings.yml", "settings.yaml", "loadDefaults", `say"hi`, "go"}
	// "go" is shorter than a trigram index can match, so it is not judged.
	want := []string{"settings.yaml", "loadDefaults", `say"hi`}
	if got := store.namesNotHeld(ctx, asked); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("namesNotHeld = %q, want %q", got, want)
	}

	// The old layout stores a file's path without making it searchable, so
	// settings.yml -- which is there -- would be reported missing.
	dir := t.TempDir()
	writeLegacyLayoutIndex(t, dir, []Chunk{lexChunk("conf/settings.yml:1-40", "conf/settings.yml", "retries: 3\n", 1)})
	legacy, err := openFTSChunkStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacy.Close() }()
	if got := legacy.namesNotHeld(ctx, []string{"settings.yml", "loadDefaults"}); got != nil {
		t.Errorf("an index in the old layout called %q missing; it cannot see file names", got)
	}
}

// search_code OPENS WITH THE LINE, and only when there is something to say:
// the results follow it untouched, and a search that names what the project
// has, or names nothing, is byte for byte what it was before this existed.
func TestSearchCodeOpensWithTheNamesTheIndexDoesNotHold(t *testing.T) {
	ctx := context.Background()
	index, err := NewFTSChunkStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	if err := index.Upsert(ctx, []Chunk{
		lexChunk("main.go:1-12", "main.go", "package main\n\nfunc parseConfig() {}\n", 1),
	}); err != nil {
		t.Fatal(err)
	}
	hits := []Chunk{lineChunk("main.go", 1, 12)}
	s := &Server{
		logger: discardLogger(), cfg: &Config{},
		embedder: &fakeEmbedder{dim: embedDim}, store: fixedStore{chunks: hits}, lexicalStore: index,
		retrievalTopK: len(hits), contextBudgetChars: 1 << 20, rerankDisabled: true,
	}
	search := func(ctx context.Context, query string) string {
		t.Helper()
		raw, err := json.Marshal(map[string]string{"query": query})
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.builtinSearchCode(ctx, raw)
		if err != nil || res.IsError || res.Empty {
			t.Fatalf("search_code(%q): empty=%t error=%t %v\n%s", query, res.Empty, res.IsError, err, res.Content)
		}
		return res.Content
	}

	lead, body := splitAbsentNamesLead(search(ctx, "what reads config/settings.yaml and calls loadDefaults"))
	if !strings.Contains(lead, `"settings.yaml", "loadDefaults"`) {
		t.Fatalf("the search did not say which names the index does not hold: %q", lead)
	}
	if !strings.HasPrefix(body, "[0] main.go:1-12") {
		t.Errorf("the results do not follow the line as they were: %q", body)
	}
	for _, query := range []string{
		"where is parseConfig defined",        // a name the project has
		"explain the startup sequence please", // no name at all
	} {
		if got := search(ctx, query); got != body {
			t.Errorf("search_code(%q) changed with nothing to say:\n%s", query, got)
		}
	}

	if lead, _ := splitAbsentNamesLead(search(ctx, "a_1 b_2 c_3 d_4 e_5")); !strings.Contains(lead, `"a_1", "b_2", "c_3" and 2 more.`) {
		t.Errorf("five missing names are not cut to %d and a count: %q", maxAbsentNamesSaid, lead)
	}

	// A turn that has changed files may be searching for what it just wrote,
	// and the index describes the project as it was.
	edited := context.WithValue(ctx, stageCtxKey{}, &stagedWorkspace{touched: map[string]bool{"new.go": true}})
	if got := search(edited, "where is loadDefaults"); got != body {
		t.Errorf("a turn with edits was told a name is missing from an index that predates them:\n%s", got)
	}
	// A keyword index that cannot answer the question is not asked it.
	s.lexicalStore = nil
	if got := search(ctx, "where is loadDefaults"); got != body {
		t.Errorf("a search with no keyword index said something about names:\n%s", got)
	}
}

// holdsNothing is a keyword index that reports every name missing.
type holdsNothing struct{ LexicalStore }

func (holdsNothing) namesNotHeld(_ context.Context, names []string) []string { return names }

// THE LINE MUST NOT HIDE A SEARCH THAT IS GOING IN CIRCLES. A model looking for
// a file that is not there asks for one spelling after another and gets the
// same nearest code back each time; the stall count catches that by comparing
// results (stall.go). The line names the spelling, so with it counted every
// one of those results would be different bytes, and new.
func TestAMissingNameDoesNotMakeARepeatedSearchLookNew(t *testing.T) {
	ctx := context.Background()
	s := &Server{lexicalStore: holdsNothing{}}
	const body = "[0] main.go:1-12\npackage main\n"
	first := s.absentNamesLead(ctx, "settings.yaml")
	second := s.absentNamesLead(ctx, "settings.yml")
	if first == "" || first == second {
		t.Fatalf("premise broken: the two searches need different lines, got %q and %q", first, second)
	}

	turn := &agentTurn{}
	if got := weigh(turn, "search_code", `search_code({"query":"settings.yaml"})`, mcp.Result{Content: first + body}); got != first+body {
		t.Fatalf("the first search was altered: %q", got)
	}
	got := weigh(turn, "search_code", `search_code({"query":"settings.yml"})`, mcp.Result{Content: second + body})
	if strings.Contains(got, "package main") {
		t.Errorf("the same code was sent a second time: %q", got)
	}
	if !strings.HasPrefix(got, second) {
		t.Errorf("the line about this search's own name was lost with the repeat: %q", got)
	}
	if turn.stall != stallWeightFailed {
		t.Errorf("stall = %d after a search returned what an earlier one had, want %d", turn.stall, stallWeightFailed)
	}

	// ONLY A SEARCH'S OWN LINE IS SET ASIDE. A file that opens with the same
	// words is a file, and two files that differ there are different files.
	turn = &agentTurn{}
	weigh(turn, "read_file", `read_file({"path":"a.txt"})`, mcp.Result{Content: first + body})
	if got := weigh(turn, "read_file", `read_file({"path":"b.txt"})`, mcp.Result{Content: second + body}); got != second+body || turn.stall != 0 {
		t.Errorf("two different files were taken for one because they open like the line: stall=%d %q", turn.stall, got)
	}

	// A line the result cap cut short is not a line: the result stays whole.
	cut := absentNamesOpen + `"settings.yaml". The code below`
	if lead, rest := splitAbsentNamesLead(cut); lead != "" || rest != cut {
		t.Errorf("a cut line was split: lead=%q body=%q", lead, rest)
	}
}
