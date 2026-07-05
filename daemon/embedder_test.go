package main

import (
	"context"
	"math"
	"testing"
)

func TestPlaceholderEmbedder_DimMatchesConfigured(t *testing.T) {
	e := NewPlaceholderEmbedder(384)
	vecs, err := e.Embed(context.Background(), []string{"package main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vecs))
	}
	if len(vecs[0]) != 384 {
		t.Errorf("vector dim = %d, want 384", len(vecs[0]))
	}
	if e.Dim() != 384 {
		t.Errorf("Dim() = %d, want 384", e.Dim())
	}
}

func TestPlaceholderEmbedder_Deterministic(t *testing.T) {
	e := NewPlaceholderEmbedder(384)
	ctx := context.Background()

	v1, err := e.Embed(ctx, []string{"func main() {}"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v2, err := e.Embed(ctx, []string{"func main() {}"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := range v1[0] {
		if v1[0][i] != v2[0][i] {
			t.Fatalf("embedding not deterministic at index %d: %v != %v", i, v1[0][i], v2[0][i])
		}
	}
}

func TestPlaceholderEmbedder_DifferentTextsDifferentVectors(t *testing.T) {
	e := NewPlaceholderEmbedder(384)
	vecs, err := e.Embed(context.Background(), []string{
		"the quick brown fox jumps over the lazy dog",
		"completely unrelated golang source code snippet here",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	identical := true
	for i := range vecs[0] {
		if vecs[0][i] != vecs[1][i] {
			identical = false
			break
		}
	}
	if identical {
		t.Fatal("distinct texts produced identical vectors")
	}
}

func TestPlaceholderEmbedder_AlwaysNormalized(t *testing.T) {
	e := NewPlaceholderEmbedder(384)
	vecs, err := e.Embed(context.Background(), []string{"some ordinary text", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, v := range vecs {
		var sumSq float64
		for _, x := range v {
			sumSq += float64(x) * float64(x)
		}
		norm := math.Sqrt(sumSq)
		if math.Abs(norm-1) > 1e-4 {
			t.Errorf("vector %d has norm %v, want ~1", i, norm)
		}
		for j, x := range v {
			if math.IsNaN(float64(x)) {
				t.Fatalf("vector %d has NaN at index %d", i, j)
			}
		}
	}
}

// fakeEmbedder is a minimal Embedder used to prove the rest of the system
// depends only on the interface, not on PlaceholderEmbedder specifically.
type fakeEmbedder struct {
	dim int
}

func (f *fakeEmbedder) Dim() int { return f.dim }

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dim)
		v[0] = 1 // fixed, trivially distinguishable from the placeholder's output
		vecs[i] = v
	}
	return vecs, nil
}
