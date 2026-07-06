package main

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Target model (next step): BAAI/bge-small-en-v1.5
//   - Dim: 384 (matches the placeholder, so this interface does not change)
//   - Pooling: CLS (NOT mean pooling) + L2 normalize
//   - Retrieval queries must be prefixed with the BGE query instruction; stored
//     documents are embedded WITHOUT the prefix (asymmetric query/document handling)

// Embedder turns text into vectors. Every other package here depends only on
// this interface, never on a concrete implementation, so the real local
// model described above can be swapped in with a one-line change.
//
// Embed and EmbedQuery exist separately because BGE is asymmetric: queries
// are embedded with an instruction prefix, documents are not (see
// BgeEmbedder in bge_embedder.go, the only implementation where these two
// methods actually differ). ID returns a stable identity string that must
// change whenever an implementation's embedding semantics change (model
// version, pooling method, prefix convention) — it gets stamped into every
// index built with it, so a later mismatch can be detected instead of
// silently comparing incompatible vectors (see embedderstamp.go).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)      // documents — no prefix
	EmbedQuery(ctx context.Context, texts []string) ([][]float32, error) // queries — prefix applied where relevant
	Dim() int
	ID() string
}

// embedDim is the vector width shared by every Embedder implementation in
// this codebase (the placeholder was deliberately chosen to match the real
// model's output dimension, so swapping between them never ripples into
// stored data shapes).
const embedDim = 384

// PlaceholderEmbedder is a local, deterministic, hash-based bag-of-tokens
// embedder. NOT semantically meaningful; replaced by a real local model in
// the next step. It needs no network and no model file, and it must never
// make a network call.
type PlaceholderEmbedder struct {
	dim int
}

// NewPlaceholderEmbedder returns a PlaceholderEmbedder producing vectors of
// the given dimension.
func NewPlaceholderEmbedder(dim int) *PlaceholderEmbedder {
	return &PlaceholderEmbedder{dim: dim}
}

// Dim returns the configured vector width.
func (e *PlaceholderEmbedder) Dim() int { return e.dim }

// ID identifies this embedder for the stale-index guard (embedderstamp.go).
// It deliberately has nothing in common with BgeEmbedder's ID even though
// both may report the same Dim — dim alone can't distinguish them, which is
// exactly why the guard checks ID too.
func (e *PlaceholderEmbedder) ID() string { return "placeholder-hash-v1" }

// EmbedQuery delegates to Embed: the placeholder has no real query/document
// asymmetry to apply since it isn't semantically meaningful either way.
func (e *PlaceholderEmbedder) EmbedQuery(ctx context.Context, texts []string) ([][]float32, error) {
	return e.Embed(ctx, texts)
}

// Embed hashes each text's tokens into a fixed-width vector (the "hashing
// trick") and L2-normalizes it. Purely arithmetic: no I/O, no network.
func (e *PlaceholderEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vecs[i] = e.embedOne(text)
	}
	return vecs, nil
}

func (e *PlaceholderEmbedder) embedOne(text string) []float32 {
	vec := make([]float32, e.dim)
	for _, token := range tokenize(text) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(token))
		sum := h.Sum64()

		idx := int(sum % uint64(e.dim))
		sign := float32(1)
		if (sum>>1)%2 == 1 {
			sign = -1
		}
		vec[idx] += sign
	}

	var normSq float64
	for _, v := range vec {
		normSq += float64(v) * float64(v)
	}
	if normSq == 0 {
		// An all-zero vector (e.g. empty or token-less content) would divide
		// by a zero norm below, and chromem-go's own normalization step would
		// do the same on the way in — either poisons every element with NaN.
		// Pin one fixed dimension instead so the vector is always unit-length.
		vec[0] = 1
		normSq = 1
	}
	norm := float32(math.Sqrt(normSq))
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}

// tokenize lowercases text and splits it on runs of non-alphanumeric
// characters.
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
