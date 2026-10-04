package main

import (
	"context"
	"sort"
)

// The cross-encoder stage, MEASURE ONLY as of 2026-10-04.
//
// A bi-encoder (BGE) embeds the question and each chunk apart and compares the
// vectors; a cross-encoder reads the question and one chunk together and
// scores how well that chunk answers it. That is slower -- one model call per
// candidate -- and better at exactly the misses the outside-repository eval
// left: the answer's chunk not in the top ten at all, which no class weight or
// widening reaches. Whether it earns its download and its latency is what
// TestExternalLeverSweep measures; nothing in production calls this.

// pairScorer scores each of texts against query. HelperProcess.Rerank is the
// real one.
type pairScorer interface {
	Rerank(ctx context.Context, query string, texts []string) ([]float32, error)
}

// crossEncoderPolicy is how the cross-encoder reorders ranked candidates.
type crossEncoderPolicy struct {
	// TopN is how many of the ranked candidates it scores; the rest keep
	// their place after them.
	TopN int
	// Blend fuses the cross-encoder's order with the current one by
	// reciprocal rank (1/(rrfK+rank) each) instead of replacing it, so a
	// candidate both stages like beats one only the cross-encoder likes.
	Blend bool
}

// crossEncoderReorder returns the top k of ranked after the cross-encoder has
// scored its first p.TopN. Each chunk is scored as the indexer embeds it --
// embedPrefixFor its path, then its content -- rebuilt here because the stores
// keep Content and not EmbedText, so a retrieved hit arrives without its path.
// On a scorer error the ranked order is returned unchanged, with the error.
func crossEncoderReorder(ctx context.Context, s pairScorer, query string, ranked []Chunk, p crossEncoderPolicy, k int) ([]Chunk, error) {
	n := min(p.TopN, len(ranked))
	if n <= 1 {
		return ranked[:min(k, len(ranked))], nil
	}
	head := ranked[:n]
	passages := make([]string, n)
	for i, c := range head {
		passages[i] = embedPrefixFor(c.FilePath) + c.Content
	}
	scores, err := s.Rerank(ctx, query, passages)
	if err != nil {
		return ranked[:min(k, len(ranked))], err
	}

	order := make([]int, n) // indexes into head, best first by the cross-encoder
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })

	if p.Blend {
		ceRank := make([]int, n) // head index -> its 1-based cross-encoder rank
		for r, i := range order {
			ceRank[i] = r + 1
		}
		fused := func(i int) float64 { return 1/(rrfK+float64(i+1)) + 1/(rrfK+float64(ceRank[i])) }
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return fused(order[a]) > fused(order[b]) })
	}

	out := make([]Chunk, 0, len(ranked))
	for _, i := range order {
		out = append(out, head[i])
	}
	out = append(out, ranked[n:]...)
	return out[:min(k, len(out))], nil
}
