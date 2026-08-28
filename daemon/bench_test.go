package main

import (
	"fmt"
	"strings"
	"testing"
)

// Benchmarks for the retrieval hot path — the per-turn CPU between a user
// pressing Enter and the augmented prompt being handed to the provider.
//
// Why these exist: the only end-to-end latency figure this project owns was
// measured 2026-07-17 and put retrieval at ~14 ms (embed 5.4 + search/rerank
// 8.8). Everything benchmarked below was added to that path AFTER that
// measurement -- structural scrubbing (scrub), and the warn-mode entropy and
// keyword detectors -- so the 14 ms figure does not describe today's code and
// nothing in the tree could price the difference. These are the instrument.
//
// They are sized to a REAL turn, not to a round number: chunkLines is 40 and
// benchTurnChunks is built from defaultK / defaultContextBudgetChars, so it
// models what actually gets folded into one prompt. A benchmark over a 1 MB
// synthetic blob would produce a bigger, more impressive, and entirely
// meaningless number.
//
// Those two constants are read, not written out, deliberately: they moved on
// 2026-08-28 (5 -> 10 chunks, 8000 -> 16000 chars), which roughly doubles the
// per-turn scrubbing work these benchmarks price. A comment quoting the old
// values would have gone quietly wrong.

// benchChunkBody is one realistic 40-line source chunk. It deliberately
// contains the token shapes the fire-rate report found dominating real
// retrieval traffic -- a git SHA, a UUID, a base64-ish blob, and a
// credential-named assignment -- because a corpus of plain prose would
// under-report the detectors' cost by skipping their inner loops entirely.
// (Report: entropy fires on ~33% of real retrieved chunks.)
var benchChunkBody = strings.Join([]string{
	"package retrieval",
	"",
	"import (",
	"\t\"context\"",
	"\t\"database/sql\"",
	"\t\"fmt\"",
	"\t\"strings\"",
	")",
	"",
	"// commitStamp pins the corpus this index was built from.",
	"const commitStamp = \"9f2c1ab74e5d3809bb61a04f7c2e8d135a6b90fe\"",
	"const sessionID = \"3f7a1c92-8e4b-4d21-9a05-6b1e7c8d2f43\"",
	"",
	"var encodedManifest = \"eyJ2ZXJzaW9uIjoxLCJjaHVua3MiOjQ2NiwiZmlsZXMiOjgxfQ==\"",
	"",
	"type Store struct {",
	"\tdb   *sql.DB",
	"\troot string",
	"}",
	"",
	"// Nearest returns the k chunks closest to vec, ordered by score.",
	"func (s *Store) Nearest(ctx context.Context, vec []float32, k int) ([]Chunk, error) {",
	"\trows, err := s.db.QueryContext(ctx, nearestQuery, k)",
	"\tif err != nil {",
	"\t\treturn nil, fmt.Errorf(\"nearest: %w\", err)",
	"\t}",
	"\tdefer rows.Close()",
	"",
	"\tvar out []Chunk",
	"\tfor rows.Next() {",
	"\t\tvar c Chunk",
	"\t\tif err := rows.Scan(&c.ID, &c.FilePath, &c.Content); err != nil {",
	"\t\t\treturn nil, err",
	"\t\t}",
	"\t\tout = append(out, c)",
	"\t}",
	"\treturn out, rows.Err()",
	"}",
	"",
	"func connect(dsn string) (*sql.DB, error) {",
	"\treturn sql.Open(\"sqlite\", strings.TrimSpace(dsn))",
	"}",
}, "\n")

// benchTurnChunks builds the chunk set for one grounded turn: defaultK chunks,
// distinct file paths so nothing dedupes them away.
func benchTurnChunks() []Chunk {
	chunks := make([]Chunk, defaultK)
	for i := range chunks {
		chunks[i] = Chunk{
			ID:        fmt.Sprintf("internal/retrieval/store_%d.go:1-40", i),
			FilePath:  fmt.Sprintf("internal/retrieval/store_%d.go", i),
			StartLine: 1,
			EndLine:   40,
			Content:   benchChunkBody,
			Class:     FileClassCode,
		}
	}
	return chunks
}

func BenchmarkScrub(b *testing.B) {
	b.SetBytes(int64(len(benchChunkBody)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cleaned, reds := scrub(benchChunkBody, false)
		_, _ = cleaned, reds
	}
}

func BenchmarkDetectHighEntropy(b *testing.B) {
	b.SetBytes(int64(len(benchChunkBody)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = detectHighEntropy(benchChunkBody)
	}
}

func BenchmarkDetectKeywordSecrets(b *testing.B) {
	b.SetBytes(int64(len(benchChunkBody)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = detectKeywordSecrets(benchChunkBody)
	}
}

func BenchmarkDetectWarnModeSecrets(b *testing.B) {
	b.SetBytes(int64(len(benchChunkBody)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = detectWarnModeSecrets(benchChunkBody)
	}
}

func BenchmarkRenderChunk(b *testing.B) {
	c := benchTurnChunks()[0]
	b.SetBytes(int64(len(c.Content)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = renderChunk(1, c, false)
	}
}

// BenchmarkBuildAugmentedUserMessage prices the whole prompt-assembly step for
// one turn: defaultK chunks scrubbed, delimiter-neutralized and concatenated.
func BenchmarkBuildAugmentedUserMessage(b *testing.B) {
	chunks := benchTurnChunks()
	prompt := "why does the nearest-neighbour query drop the score column?"
	var total int
	for _, c := range chunks {
		total += len(c.Content)
	}
	b.SetBytes(int64(total))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = buildAugmentedUserMessage(prompt, chunks, false)
	}
}

// BenchmarkTurnScrubWork prices the SCRUB HALF of a turn as the code actually
// performs it today, which is twice: renderChunk scrubs every chunk to build
// the outbound prompt (context.go), and logChunkScrub independently scrubs the
// same budget-kept set again to measure it, then runs the warn-mode detectors
// on its own copy of the result.
//
// The two sub-benchmarks isolate exactly that duplication, so the cost of
// removing it is a measured number rather than an assumption. Note what is NOT
// claimed here: that the duplication is worth removing. It is real, but "real"
// and "worth a change on the money path's neighbour" are different findings,
// and only the numbers decide.
func BenchmarkTurnScrubWork(b *testing.B) {
	chunks := benchTurnChunks()
	var total int
	for _, c := range chunks {
		total += len(c.Content)
	}

	// as-shipped: scrub in renderChunk, then scrub AGAIN in logChunkScrub,
	// then detect on the second result.
	b.Run("as_shipped_double_scrub", func(b *testing.B) {
		b.SetBytes(int64(total))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, c := range chunks {
				rendered, _ := scrub(c.Content, false) // renderChunk's pass
				_ = rendered
				cleaned, reds := scrub(c.Content, false) // logChunkScrub's pass
				_ = redactionKinds(reds)
				_ = detectWarnModeSecrets(cleaned)
			}
		}
	})

	// hypothetical: scrub once per chunk, share the result with both consumers.
	b.Run("single_scrub_shared", func(b *testing.B) {
		b.SetBytes(int64(total))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, c := range chunks {
				cleaned, reds := scrub(c.Content, false)
				_ = cleaned // renderChunk would use this
				_ = redactionKinds(reds)
				_ = detectWarnModeSecrets(cleaned)
			}
		}
	})
}

func BenchmarkChunkContent(b *testing.B) {
	// A file large enough to exercise the striding loop rather than the
	// single-window early exit: 40-line chunks, 30-line stride.
	content := []byte(strings.Repeat(benchChunkBody+"\n", 8))
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = chunkContent(content, "internal/retrieval/store.go")
	}
}

// benchCandidates builds the overfetch pool rerank actually sees. rerankPoolSize
// governs it, so the pool is derived rather than hardcoded to a guess.
func benchCandidates(n int) []Chunk {
	out := make([]Chunk, n)
	for i := range out {
		class := FileClassCode
		if i%3 == 0 {
			class = FileClassTest
		}
		out[i] = Chunk{
			ID:        fmt.Sprintf("pkg/file_%d.go:1-40", i),
			FilePath:  fmt.Sprintf("pkg/file_%d.go", i),
			StartLine: 1,
			EndLine:   40,
			Content:   benchChunkBody,
			Class:     class,
			Score:     float32(n-i) / float32(n),
			RawScore:  float32(n-i) / float32(n),
		}
	}
	return out
}

func BenchmarkRerankChunks(b *testing.B) {
	candidates := benchCandidates(rerankPoolSize(defaultK))
	const query = "fix the nearest-neighbour query dropping the score column"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = rerankChunks(candidates, defaultK, query)
	}
}

func BenchmarkFuseRRF(b *testing.B) {
	semantic := benchCandidates(rerankPoolSize(defaultK))
	lexical := benchCandidates(lexicalPoolSize(defaultK))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = fuseRRF(semantic, lexical, 60)
	}
}

// BenchmarkLooksTestSeeking guards the query-classification path that debt (b)
// twice regressed on captured tool output -- once via testSeekingWords matching
// `.test` binary names, once via testFuncPattern matching a `--- FAIL:` line.
// Both fixes added regexp work to a function called once per turn; this prices
// it so a third fix cannot quietly make it expensive.
func BenchmarkLooksTestSeeking(b *testing.B) {
	queries := []string{
		"how does the chunker split files",
		"FAIL codeterminal/editapply [build failed]\ncodeterminal/daemon [codeterminal/daemon.test]",
		"--- FAIL: TestIsZDRRoutingRefusal_RejectsNonZDR (0.00s)",
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = looksTestSeeking(queries[i%len(queries)])
	}
}
