//go:build warnscan

// This file is gated behind the "warnscan" build tag so `go test ./...` (the
// fast, offline unit path) never compiles or runs it — it walks whole
// repositories, which has no place in the unit path. Run it explicitly with:
//
//	go test -tags warnscan -run TestWarnScanFireRate -v ./daemon
//
// and point it at more corpora with a colon-separated list of roots:
//
//	WARNSCAN_ROOTS=/path/to/repo-a:/path/to/repo-b go test -tags warnscan ...
//
// WHY THIS EXISTS. The opaque/novel-secret half of P3-FAIL-1 waits on a
// founder decision between CHUNK_SCRUB_DESIGN's Design B (entropy redaction)
// and Design C (keyword redaction), and that decision waits on fire-rate data
// from warn-mode. Warn-mode has been wired and durable since 064a00a — and had
// recorded exactly ZERO events as of 2026-07-30, because the sink only fills
// when a daemon serves grounded turns on a real workspace and this machine has
// served none. Twelve days of waiting produced no data. This harness produces
// it directly instead: the detectors are pure functions, so a corpus can be
// measured offline in seconds rather than waited on indefinitely.
//
// WHAT IT MEASURES, AND WHY THAT ORDER MATTERS. It reproduces the production
// path exactly and in order:
//
//	ScanWorkspace   — the real walk, so the real skip gates apply
//	                  (MatchesSecretName, .gitignore, size cap, binary sniff);
//	                  secret-NAMED files never reach a detector, just as in
//	                  production.
//	scrub()         — Option A, the structural pass that is LIVE today inside
//	                  renderChunk.
//	detectors       — run on the POST-scrub text.
//
// That last step is the whole point. These detectors exist to answer "what
// opaque material would still leave the machine after Option A has already
// run", so they must see what Option A leaves behind. Running them on raw file
// text would inflate every count with secrets that are already redacted before
// they reach the wire, and would overstate the case for Designs B/C. This is
// the same ordering logChunkScrub documents at step 2 (context.go).
//
// Gate 3 holds here as it does in the sink: this harness never prints raw
// suspected-secret content. It reports fixed labels, counts, a bits/char
// distribution, and the same truncated-SHA indicators warnDetection carries.
// A sample line names file:line so a reviewer with the corpus in hand can
// classify it, without the report itself carrying the value.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// warnScanSampleCap bounds the per-detector sample list. Enough to
// hand-classify a true-vs-false-positive rate; not so much that the report
// becomes a dump.
const warnScanSampleCap = 60

// warnScanSample is one recorded fire, carrying only what a human needs to go
// look it up in the corpus themselves — never the value.
type warnScanSample struct {
	Root      string `json:"root"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	Class     string `json:"class"`
	Shape     string `json:"shape"`
	Note      string `json:"note"`
	Indicator string `json:"indicator"`
}

// warnScanDetectorStats is one detector's tally over a corpus.
type warnScanDetectorStats struct {
	Fires       int              `json:"fires"`
	ByShape     map[string]int   `json:"by_shape"`
	ByClass     map[string]int   `json:"by_class"`
	ByKeyword   map[string]int   `json:"by_keyword,omitempty"` // keyword detector only
	ByFile      map[string]int   `json:"-"`                    // for the top-offenders table
	Samples     []warnScanSample `json:"samples"`
	ChunksFired int              `json:"chunks_fired"` // chunks with >=1 fire (not total fires)
}

func newWarnScanDetectorStats() *warnScanDetectorStats {
	return &warnScanDetectorStats{
		ByShape:   map[string]int{},
		ByClass:   map[string]int{},
		ByKeyword: map[string]int{},
		ByFile:    map[string]int{},
	}
}

func (s *warnScanDetectorStats) record(c Chunk, root string, d warnDetection) {
	s.Fires++
	s.ByShape[d.Shape]++
	s.ByClass[string(c.Class)]++
	s.ByFile[c.FilePath]++
	if kw := warnScanKeywordFromNote(d.Note); kw != "" {
		s.ByKeyword[kw]++
	}
	if len(s.Samples) < warnScanSampleCap {
		s.Samples = append(s.Samples, warnScanSample{
			Root:      root,
			File:      c.FilePath,
			StartLine: c.StartLine,
			Class:     string(c.Class),
			Shape:     d.Shape,
			Note:      d.Note,
			Indicator: d.Indicator,
		})
	}
}

// warnScanKeywordFromNote pulls the keyword label out of the keyword
// detector's secret-free note ("keyword=password value_len=18"). Returns ""
// for the entropy detector's note, which has no keyword. The note format is
// fixed by detectKeywordSecrets; this only reads labels it wrote.
func warnScanKeywordFromNote(note string) string {
	rest, ok := strings.CutPrefix(note, "keyword=")
	if !ok {
		return ""
	}
	kw, _, _ := strings.Cut(rest, " ")
	return kw
}

// warnScanCorpus is the full measurement over one root.
type warnScanCorpus struct {
	Root         string         `json:"root"`
	FilesScanned int            `json:"files_scanned"`
	Chunks       int            `json:"chunks"`
	Skipped      map[string]int `json:"skipped"`

	// OptionAChunks / OptionAKinds measure what the LIVE structural redactor
	// already removes, so the deferred detectors' numbers can be read as "on
	// top of what already works" rather than in isolation.
	OptionAChunks int            `json:"option_a_chunks"`
	OptionAKinds  map[string]int `json:"option_a_kinds"`

	Entropy *warnScanDetectorStats `json:"entropy"`
	Keyword *warnScanDetectorStats `json:"keyword"`

	// EntropyHistogram buckets bits/char over EVERY token the entropy detector
	// considers — including the ones below threshold, which the sink never sees
	// because it only records fires. Without the below-threshold population a
	// threshold cannot be chosen, only guessed: the question "what does moving
	// 4.0 to 4.5 cost and buy" is unanswerable from fires alone.
	EntropyHistogram map[string]int `json:"entropy_histogram"`
	EntropyTokens    int            `json:"entropy_tokens_considered"`
}

func TestWarnScanFireRate(t *testing.T) {
	roots := warnScanRoots(t)
	var corpora []*warnScanCorpus

	for _, root := range roots {
		corpus, err := warnScanOne(root)
		if err != nil {
			t.Fatalf("scanning %s: %v", root, err)
		}
		corpora = append(corpora, corpus)
	}

	for _, c := range corpora {
		warnScanReport(t, c)
	}

	if out := os.Getenv("WARNSCAN_OUT"); out != "" {
		blob, err := json.MarshalIndent(corpora, "", "  ")
		if err != nil {
			t.Fatalf("marshalling report: %v", err)
		}
		if err := os.WriteFile(out, blob, 0600); err != nil {
			t.Fatalf("writing %s: %v", out, err)
		}
		t.Logf("wrote machine-readable report to %s", out)
	}
}

// warnScanRoots returns the corpora to scan: the repo this test lives in by
// default (".." from the daemon package dir), plus anything in WARNSCAN_ROOTS.
func warnScanRoots(t *testing.T) []string {
	t.Helper()
	var roots []string
	if env := os.Getenv("WARNSCAN_ROOTS"); env != "" {
		for _, r := range strings.Split(env, ":") {
			if r = strings.TrimSpace(r); r != "" {
				roots = append(roots, r)
			}
		}
	}
	if len(roots) == 0 {
		roots = []string{".."}
	}
	for i, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			t.Fatalf("resolving root %s: %v", r, err)
		}
		roots[i] = abs
	}
	return roots
}

// warnScanOne runs the production pipeline over one root and tallies it.
func warnScanOne(root string) (*warnScanCorpus, error) {
	res, err := ScanWorkspace(root)
	if err != nil {
		return nil, err
	}

	c := &warnScanCorpus{
		Root:             root,
		FilesScanned:     res.FilesScanned,
		Chunks:           len(res.Chunks),
		Skipped:          map[string]int{},
		OptionAKinds:     map[string]int{},
		Entropy:          newWarnScanDetectorStats(),
		Keyword:          newWarnScanDetectorStats(),
		EntropyHistogram: map[string]int{},
	}
	for reason, n := range res.Skipped {
		c.Skipped[string(reason)] = n
	}

	for _, chunk := range res.Chunks {
		// Option A first — exactly as renderChunk does it. Everything below
		// measures the residual this leaves, not the raw file.
		cleaned, redactions := scrub(chunk.Content, false)
		if len(redactions) > 0 {
			c.OptionAChunks++
			for _, r := range redactions {
				c.OptionAKinds[r.Kind]++
			}
		}

		// The full token population, including below-threshold tokens the sink
		// never records. Same regexp and same entropy function the detector
		// uses, so the histogram and the fire count describe one population.
		var entropyFires int
		for _, tok := range entropyTokenPattern.FindAllString(cleaned, -1) {
			bits := shannonEntropy(tok)
			c.EntropyTokens++
			c.EntropyHistogram[warnScanBucket(bits)]++
			if bits >= entropyWarnThresholdBitsPerChar {
				entropyFires++
			}
		}

		fired := map[string]bool{}
		for _, d := range detectHighEntropy(cleaned) {
			c.Entropy.record(chunk, root, d)
			fired["entropy"] = true
		}
		for _, d := range detectKeywordSecrets(cleaned) {
			c.Keyword.record(chunk, root, d)
			fired["keyword"] = true
		}
		if fired["entropy"] {
			c.Entropy.ChunksFired++
		}
		if fired["keyword"] {
			c.Keyword.ChunksFired++
		}

		// Consistency check: the histogram's above-threshold count and the
		// detector's fire count are two independent readings of one population.
		// If they ever disagree, the harness is measuring something other than
		// what production measures and every number below is void.
		if entropyFires != len(detectHighEntropy(cleaned)) {
			return nil, fmt.Errorf(
				"harness/detector disagreement on %s: histogram counted %d above-threshold tokens, detector reported %d",
				chunk.ID, entropyFires, len(detectHighEntropy(cleaned)))
		}
	}

	return c, nil
}

// warnScanBucket labels a bits/char value with its 0.25-wide bucket. Fixed
// labels only — a bucket name can never carry token material.
func warnScanBucket(bits float64) string {
	lo := float64(int(bits*4)) / 4
	return strconv.FormatFloat(lo, 'f', 2, 64)
}

func warnScanReport(t *testing.T, c *warnScanCorpus) {
	t.Helper()
	per1k := func(n int) string {
		if c.Chunks == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f", float64(n)*1000/float64(c.Chunks))
	}

	fmt.Printf("\n════ warn-mode fire-rate scan: %s ════\n", c.Root)
	fmt.Printf("files scanned=%d  chunks=%d\n", c.FilesScanned, c.Chunks)
	fmt.Printf("skipped: %s\n", warnScanSortedCounts(c.Skipped))
	fmt.Printf("\nOption A (LIVE structural redactor): %d/%d chunks redacted (%s per 1k)  kinds: %s\n",
		c.OptionAChunks, c.Chunks, per1k(c.OptionAChunks), warnScanSortedCounts(c.OptionAKinds))

	for name, s := range map[string]*warnScanDetectorStats{"entropy": c.Entropy, "keyword": c.Keyword} {
		fmt.Printf("\n── detector: %s ──\n", name)
		fmt.Printf("fires=%d (%s per 1k chunks)  chunks with >=1 fire=%d (%s per 1k)\n",
			s.Fires, per1k(s.Fires), s.ChunksFired, per1k(s.ChunksFired))
		fmt.Printf("by shape: %s\n", warnScanSortedCounts(s.ByShape))
		fmt.Printf("by class: %s\n", warnScanSortedCounts(s.ByClass))
		if len(s.ByKeyword) > 0 {
			fmt.Printf("by keyword: %s\n", warnScanSortedCounts(s.ByKeyword))
		}
		fmt.Printf("top files: %s\n", warnScanTopN(s.ByFile, 12))
		fmt.Printf("samples (%d of %d):\n", len(s.Samples), s.Fires)
		for _, sm := range s.Samples {
			fmt.Printf("  %s:%d [%s/%s] %s %s\n", sm.File, sm.StartLine, sm.Class, sm.Shape, sm.Note, sm.Indicator)
		}
	}

	fmt.Printf("\n── entropy bits/char distribution (%d tokens considered, threshold %.2f) ──\n",
		c.EntropyTokens, entropyWarnThresholdBitsPerChar)
	keys := make([]string, 0, len(c.EntropyHistogram))
	for k := range c.EntropyHistogram {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.ParseFloat(keys[i], 64)
		b, _ := strconv.ParseFloat(keys[j], 64)
		return a < b
	})
	for _, k := range keys {
		n := c.EntropyHistogram[k]
		bar := strings.Repeat("█", warnScanBarWidth(n, c.EntropyTokens))
		marker := "  "
		if f, _ := strconv.ParseFloat(k, 64); f >= entropyWarnThresholdBitsPerChar {
			marker = "→ " // at or above the firing threshold
		}
		fmt.Printf("%s%s  %6d  %s\n", marker, k, n, bar)
	}
	fmt.Println()
}

func warnScanBarWidth(n, total int) int {
	if total == 0 {
		return 0
	}
	w := n * 50 / total
	if w == 0 && n > 0 {
		w = 1
	}
	return w
}

func warnScanSortedCounts(m map[string]int) string {
	if len(m) == 0 {
		return "(none)"
	}
	type kv struct {
		k string
		v int
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s=%d", p.k, p.v))
	}
	return strings.Join(parts, " ")
}

func warnScanTopN(m map[string]int, n int) string {
	if len(m) == 0 {
		return "(none)"
	}
	type kv struct {
		k string
		v int
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	if len(pairs) > n {
		pairs = pairs[:n]
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s=%d", p.k, p.v))
	}
	return strings.Join(parts, " ")
}
