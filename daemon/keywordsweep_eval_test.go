//go:build eval

// The 2026-10 keyword-tier and packing sweep.
//
// WHAT IT DECIDES. Three levers, pre-registered in docs/RETRIEVAL_EVAL_TREND.md
// before this ran: the file path as a searchable, weighted column of the
// keyword index; filler stopwords and one ending stripped from plain query
// words; and delivery that places every retrieved hit before widening any of
// them. Each is scored alone and in combination, on the 49 queries the levers
// are chosen on AND the 28 held-out ones nothing was tuned on, inside ONE index
// build -- the condition the rule requires, because this eval's corpus is the
// repository and two builds a commit apart are not the same haystack.
//
// IT SCORES THROUGH PRODUCTION'S OWN FUNCTIONS: searchWith for the keyword
// tier, fuseRRF and rerankChunks for the ranking, deliverWithinBudget for the
// delivery. A candidate is a policy value, never a re-implementation.
//
// TWO BASELINE ROWS, and the difference between them is real. LEGACY runs a
// keyword index built in the pre-2026-10 layout, so it IS the old product, to
// the last decimal. SHIPPED runs production's index and defaults on this tree,
// and it is the row TestRerankEvalRetrievalRanking must reproduce. An indexed
// path column shifts BM25's term rarity in the third decimal even at weight
// zero, so the new layout cannot stand in for the old one -- which is why the
// legacy index is built here rather than assumed.
//
//	go test -tags eval -count=1 -timeout 50m -v -run TestKeywordAndPackingSweep ./
package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// keywordArm is one keyword-tier configuration.
type keywordArm struct {
	name   string
	legacy bool // search the legacy-layout index instead of production's
	policy lexicalPolicy
}

// packingArm is one delivery configuration.
type packingArm struct {
	name   string
	policy expandPolicy
	budget int
}

// newLegacyLayoutStore builds a keyword index in the pre-2026-10 layout. It
// cannot go through NewFTSChunkStore, which would migrate it on open.
func newLegacyLayoutStore(t *testing.T, chunks []Chunk) *FTSChunkStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), lexicalDBFileName))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{legacyCodeChunksFTSTableDDL, chunkIndexDDL, chunkIndexFileDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	store := &FTSChunkStore{db: db}
	if err := store.Upsert(context.Background(), chunks); err != nil {
		t.Fatal(err)
	}
	if legacy, err := ftsLayoutIsLegacy(context.Background(), db); err != nil || !legacy {
		t.Fatalf("the baseline index is not in the legacy layout (err %v); the LEGACY row would be a lie", err)
	}
	return store
}

func TestKeywordAndPackingSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}
	corpus := sharedEvalCorpus(t)
	embedder, store := corpus.Embedder, corpus.Store
	repoRoot, scan := corpus.RepoRoot, corpus.Scan
	ctx := context.Background()

	prodLex, ok := corpus.LexicalStore.(*FTSChunkStore)
	if !ok {
		t.Fatalf("the shared corpus's keyword index is a %T, not the production *FTSChunkStore", corpus.LexicalStore)
	}
	legacyLex := newLegacyLayoutStore(t, scan.Chunks)

	type querySet struct {
		name    string
		queries []rerankEvalQuery
		exact   [][]string
	}
	sets := []querySet{
		{"tuning", rerankEvalQueries, resolveExactChunks(t, rerankEvalQueries, scan.Chunks)},
		{"held-out", heldOutEvalQueries, resolveExactChunks(t, heldOutEvalQueries, scan.Chunks)},
	}
	if t.Failed() {
		t.Fatal("ground truth did not resolve; every number below would be measured against nothing")
	}

	// EMBED AND POOL THE MEANING TIER ONCE per query; it does not vary by arm.
	semPools := make([][][]Chunk, len(sets))
	for si, set := range sets {
		semPools[si] = make([][]Chunk, len(set.queries))
		for i, q := range set.queries {
			vecs, err := embedder.EmbedQuery(ctx, []string{q.query})
			if err != nil {
				t.Fatalf("EmbedQuery(%q): %v", q.query, err)
			}
			sem, err := store.Query(ctx, vecs[0], rerankPoolSize(defaultK))
			if err != nil {
				t.Fatalf("Query(%q): %v", q.query, err)
			}
			semPools[si][i] = sem
		}
	}

	keywordArms := []keywordArm{
		{"LEGACY (old index layout)", true, lexicalPolicy{}},
		{"SHIPPED", false, defaultLexicalPolicy},
		{"stem", false, lexicalPolicy{StemWords: true}},
		{"filler+stem", false, lexicalPolicy{FillerStopwords: true, StemWords: true}},
		{"path2", false, lexicalPolicy{PathWeight: 2}},
		{"path4", false, lexicalPolicy{PathWeight: 4}},
		{"path8", false, lexicalPolicy{PathWeight: 8}},
		{"path4+stem", false, lexicalPolicy{PathWeight: 4, StemWords: true}},
		{"path2+filler+stem", false, lexicalPolicy{PathWeight: 2, FillerStopwords: true, StemWords: true}},
		{"path4+filler+stem", false, lexicalPolicy{PathWeight: 4, FillerStopwords: true, StemWords: true}},
		{"path8+filler+stem", false, lexicalPolicy{PathWeight: 8, FillerStopwords: true, StemWords: true}},
	}
	two := defaultExpandPolicy
	two.TwoPass = true
	twoWide3 := two
	twoWide3.WidenFirst = 3
	packingArms := []packingArm{
		{"one-pass", defaultExpandPolicy, defaultContextBudgetChars},
		{"two-pass", two, defaultContextBudgetChars},
		{"two-pass widen-top3", twoWide3, defaultContextBudgetChars},
		{"two-pass b=28000", two, 28000},
	}

	type outcome struct {
		retrieved, delivered []bool
		chars                int
	}
	type row struct {
		name string
		sets []outcome
	}
	var rows []row
	for _, ka := range keywordArms {
		lex := prodLex
		if ka.legacy {
			lex = legacyLex
		}
		// The keyword tier's hits for this arm, per set and query: ranked
		// production's way, then handed to every packing arm.
		hits := make([][][]Chunk, len(sets))
		for si, set := range sets {
			hits[si] = make([][]Chunk, len(set.queries))
			for i, q := range set.queries {
				kw, err := lex.searchWith(ctx, q.query, lexicalPoolSize(defaultK), ka.policy)
				if err != nil {
					t.Fatalf("searchWith(%q, %+v): %v", q.query, ka.policy, err)
				}
				hits[si][i] = rerankChunks(fuseRRF(semPools[si][i], kw, rrfK), defaultK, q.query)
			}
		}
		for _, pa := range packingArms {
			r := row{name: ka.name + " | " + pa.name}
			for si, set := range sets {
				o := outcome{retrieved: make([]bool, len(set.queries)), delivered: make([]bool, len(set.queries))}
				for i := range set.queries {
					h := hits[si][i]
					o.retrieved[i] = rankOfChunk(h, set.exact[i]) != 0
					kept := deliverWithinBudget(nil, h, repoRoot, pa.policy, defaultK, pa.budget, false).Chunks
					o.delivered[i] = rankOfChunk(kept, set.exact[i]) != 0
					for _, c := range kept {
						o.chars += len(renderChunk(1, c, false))
					}
				}
				o.chars /= max(len(set.queries), 1)
				r.sets = append(r.sets, o)
			}
			rows = append(rows, r)
		}
	}

	count := func(b []bool) int {
		n := 0
		for _, v := range b {
			if v {
				n++
			}
		}
		return n
	}
	diff := func(base, arm []bool) string {
		var gained, lost []string
		for i := range base {
			switch {
			case arm[i] && !base[i]:
				gained = append(gained, fmt.Sprint(i+1))
			case base[i] && !arm[i]:
				lost = append(lost, fmt.Sprint(i+1))
			}
		}
		return fmt.Sprintf("+[%s] -[%s]", strings.Join(gained, " "), strings.Join(lost, " "))
	}
	shapeCounts := func(set querySet, delivered []bool) map[string]int {
		m := map[string]int{}
		for i, q := range set.queries {
			if delivered[i] {
				m[q.shape]++
			}
		}
		return m
	}

	// The SHIPPED row with today's packing is the reference every verdict is
	// taken against: it is what production does on this tree.
	ref := slices.IndexFunc(rows, func(r row) bool { return r.name == "SHIPPED | one-pass" })
	if ref < 0 {
		t.Fatal("no SHIPPED | one-pass row")
	}
	refTune, refHeld := rows[ref].sets[0], rows[ref].sets[1]
	refShapes := shapeCounts(sets[0], refTune.delivered)

	fmt.Println("\n=== Keyword-tier and packing sweep (one index build, both query sets) ===")
	fmt.Printf("%-42s %9s %9s %9s %9s %7s  %-8s %s\n",
		"arm", "ret/49", "DEL/49", "ret/28", "DEL/28", "chars", "rule", "delivered vs SHIPPED|one-pass: tuning ; held-out")
	for _, r := range rows {
		tune, held := r.sets[0], r.sets[1]
		shapes := shapeCounts(sets[0], tune.delivered)
		shapeOK := true
		for s, n := range refShapes {
			if shapes[s] < n-1 {
				shapeOK = false
			}
		}
		verdict := "-"
		if count(tune.delivered) >= count(refTune.delivered)+2 && count(held.delivered) >= count(refHeld.delivered) && shapeOK {
			verdict = "PASSES"
		}
		fmt.Printf("%-42s %6d/49 %6d/49 %6d/28 %6d/28 %7d  %-8s %s ; %s\n",
			r.name, count(tune.retrieved), count(tune.delivered), count(held.retrieved), count(held.delivered),
			tune.chars, verdict, diff(refTune.delivered, tune.delivered), diff(refHeld.delivered, held.delivered))
	}
	fmt.Println("\nrule: DEL/49 >= SHIPPED+2, DEL/28 >= SHIPPED, no tuning shape down by more than 1 " +
		"(and each lever no worse alone -- read the single-lever rows)")
	fmt.Printf("SHIPPED | one-pass: retrieved %d/49, DELIVERED %d/49, held-out DELIVERED %d/28 -- "+
		"must match TestRerankEvalRetrievalRanking on the same tree.\n",
		count(refTune.retrieved), count(refTune.delivered), count(refHeld.delivered))
	if count(refTune.delivered) < 20 {
		t.Errorf("the SHIPPED row delivered only %d/49: this harness is not measuring the product", count(refTune.delivered))
	}
}
