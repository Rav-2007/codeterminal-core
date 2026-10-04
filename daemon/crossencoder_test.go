package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// fakeScorer scores a text by the number its content ends with: "score=7"
// scores 7.
type fakeScorer struct {
	calls int
	got   []string
	err   error
}

func (f *fakeScorer) Rerank(_ context.Context, _ string, texts []string) ([]float32, error) {
	f.calls++
	f.got = texts
	if f.err != nil {
		return nil, f.err
	}
	out := make([]float32, len(texts))
	for i, t := range texts {
		var v float32
		_, _ = fmt.Sscanf(t[strings.LastIndex(t, "score=")+len("score="):], "%g", &v)
		out[i] = v
	}
	return out, nil
}

func scoredChunks(scores ...float32) []Chunk {
	out := make([]Chunk, len(scores))
	for i, s := range scores {
		out[i] = Chunk{ID: fmt.Sprintf("f.go:%d-%d", i*30+1, i*30+40), FilePath: "f.go", Content: fmt.Sprintf("x\nscore=%g", s)}
	}
	return out
}

func ids(cs []Chunk) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestCrossEncoderReorderReplacesTheOrderOfTheTopN(t *testing.T) {
	ranked := scoredChunks(1, 9, 5, 3)
	s := &fakeScorer{}
	got, err := crossEncoderReorder(context.Background(), s, "q", ranked, crossEncoderPolicy{TopN: 3}, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The top three are reordered by score; the fourth keeps its place after them.
	want := []string{ranked[1].ID, ranked[2].ID, ranked[0].ID, ranked[3].ID}
	if strings.Join(ids(got), ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", ids(got), want)
	}
	if len(s.got) != 3 || s.got[0] != embedPrefixFor("f.go")+ranked[0].Content {
		t.Errorf("the scorer saw %d texts, the first %q; want the top three's embed texts, path included", len(s.got), s.got[0])
	}
}

// Blending lets a candidate both stages like beat one only the cross-encoder
// likes: rank 1 by retrieval and 2 by the cross-encoder beats rank 3 and 1.
func TestCrossEncoderBlendWeighsBothOrders(t *testing.T) {
	ranked := scoredChunks(5, 1, 9)
	got, err := crossEncoderReorder(context.Background(), &fakeScorer{}, "q", ranked, crossEncoderPolicy{TopN: 3, Blend: true}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != ranked[0].ID {
		t.Errorf("blended order = %v; the candidate first by retrieval and second by the cross-encoder should lead", ids(got))
	}
	pure, _ := crossEncoderReorder(context.Background(), &fakeScorer{}, "q", ranked, crossEncoderPolicy{TopN: 3}, 10)
	if pure[0].ID != ranked[2].ID {
		t.Errorf("unblended order = %v; the cross-encoder's favourite should lead", ids(pure))
	}
}

func TestCrossEncoderReorderTruncatesAndFailsSoft(t *testing.T) {
	ranked := scoredChunks(1, 2, 3, 4, 5)
	got, _ := crossEncoderReorder(context.Background(), &fakeScorer{}, "q", ranked, crossEncoderPolicy{TopN: 5}, 2)
	if len(got) != 2 || got[0].ID != ranked[4].ID {
		t.Errorf("k=2 returned %v", ids(got))
	}
	boom := errors.New("helper gone")
	got, err := crossEncoderReorder(context.Background(), &fakeScorer{err: boom}, "q", ranked, crossEncoderPolicy{TopN: 5}, 3)
	if !errors.Is(err, boom) || strings.Join(ids(got), ",") != strings.Join(ids(ranked[:3]), ",") {
		t.Errorf("on a scorer error got %v, %v; want the ranked order's top three and the error", ids(got), err)
	}
	s := &fakeScorer{}
	if _, err := crossEncoderReorder(context.Background(), s, "q", ranked[:1], crossEncoderPolicy{TopN: 5}, 3); err != nil || s.calls != 0 {
		t.Errorf("one candidate needs no scoring: calls=%d err=%v", s.calls, err)
	}
}

func TestCrossEncoderAssetsArePinned(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if len(crossEncoderModelAssets) != 2 {
		t.Fatalf("%d assets; the helper opens the model and its tokenizer", len(crossEncoderModelAssets))
	}
	for _, a := range crossEncoderModelAssets {
		if a.size <= 0 || !hex64.MatchString(a.sha256Hex) || !strings.HasPrefix(a.url, crossEncoderModelBaseURL+"/") {
			t.Errorf("asset %+v is not pinned by size, checksum and revision", a)
		}
	}
	if !strings.Contains(crossEncoderModelBaseURL, "/resolve/a09144355adeed5f58c8ed011d209bf8ee5a1fec") {
		t.Error("the cross-encoder is not pinned to a revision")
	}
	if dir, err := defaultCrossEncoderCacheDir(); err != nil || !strings.HasSuffix(dir, "ms-marco-minilm-l6-int8") {
		t.Errorf("cache dir %q, %v", dir, err)
	}
}
