package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ort "github.com/yalue/onnxruntime_go"

	"mochiii/helper/helperproto"
)

// realCrossEncoder opens the cross-encoder from the local model cache, or
// skips: the model is fetched only for the outside-repository eval.
func realCrossEncoder(t *testing.T) *CrossEncoder {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".mochiii", "models", "ms-marco-minilm-l6-int8")
	if _, err := os.Stat(filepath.Join(dir, crossEncoderModelFile)); err != nil {
		t.Skipf("cross-encoder not cached at %s: %v", dir, err)
	}
	lib := filepath.Join(home, ".mochiii", "models", "onnxruntime-1.26.0", "libonnxruntime.so")
	if _, err := os.Stat(lib); err != nil {
		t.Skipf("onnxruntime not cached: %v", err)
	}
	var initErr error
	ortOnce.Do(func() {
		ort.SetSharedLibraryPath(lib)
		initErr = ort.InitializeEnvironment()
	})
	if initErr != nil {
		t.Skipf("initializing ONNX Runtime: %v", initErr)
	}
	ce, err := NewCrossEncoder(dir, 0)
	if err != nil {
		t.Fatalf("opening the cross-encoder: %v", err)
	}
	t.Cleanup(func() { _ = ce.Close() })
	return ce
}

// A pair is [CLS] query [SEP] passage [SEP] with token types 0 then 1, and a
// passage too long for the model loses its end, never the query or the final
// [SEP].
func TestCrossEncoderEncodesAQueryAndPassageAsOnePair(t *testing.T) {
	ce := realCrossEncoder(t)
	tok, err := ce.tokenizePair("parse a date", strings.Repeat("timestamp layout ", 400))
	if err != nil {
		t.Fatal(err)
	}
	const cls, sep = 101, 102
	if len(tok.ids) != maxSequenceLength || tok.ids[0] != cls || tok.ids[len(tok.ids)-1] != sep {
		t.Fatalf("a long pair is %d tokens, starting %d and ending %d; want %d, [CLS] and [SEP]",
			len(tok.ids), tok.ids[0], tok.ids[len(tok.ids)-1], maxSequenceLength)
	}
	firstSep := -1
	for i, id := range tok.ids {
		if id == sep {
			firstSep = i
			break
		}
	}
	if firstSep < 1 || tok.typeIDs[firstSep] != 0 || tok.typeIDs[firstSep+1] != 1 || tok.typeIDs[len(tok.typeIDs)-1] != 1 {
		t.Errorf("token types do not switch from 0 to 1 after the query's [SEP] (at %d): %v", firstSep, tok.typeIDs[:min(len(tok.typeIDs), firstSep+3)])
	}
}

// The scores mean something: the passage that answers the question outranks
// one that does not, for each of two questions.
func TestCrossEncoderPrefersThePassageThatAnswers(t *testing.T) {
	ce := realCrossEncoder(t)
	dateParser := "def parse_date(text):\n    \"\"\"Parse an ISO 8601 date string with its time zone.\"\"\"\n    return datetime.fromisoformat(text)\n"
	retry := "def send_with_retry(request, attempts=3):\n    \"\"\"Resend the HTTP request when the server answers 503.\"\"\"\n    for _ in range(attempts):\n        response = send(request)\n"
	for _, tc := range []struct {
		query, want string
	}{
		{"how are date strings with time zones parsed", dateParser},
		{"how does the client retry a request when the server is unavailable", retry},
	} {
		scores, err := ce.Score(tc.query, []string{dateParser, retry})
		if err != nil {
			t.Fatal(err)
		}
		if len(scores) != 2 {
			t.Fatalf("%d scores for 2 passages", len(scores))
		}
		best := 0
		if scores[1] > scores[0] {
			best = 1
		}
		if []string{dateParser, retry}[best] != tc.want {
			t.Errorf("%q: scores %v put the wrong passage first", tc.query, scores)
		}
	}
	if s, err := ce.Score("anything", nil); err != nil || s != nil {
		t.Errorf("no passages should score as nothing, got %v, %v", s, err)
	}
}

// What follows runs without the model, so CI -- which has none -- exercises
// the rerank path's plumbing and its failures, not only machines that cached
// the cross-encoder.

func TestPackPairsPadsEachRowToTheLongest(t *testing.T) {
	toks := []tokenized{
		{ids: []int{101, 7, 102}, typeIDs: []int{0, 0, 0}, mask: []int{1, 1, 1}},
		{ids: []int{101, 8, 102, 9, 102}, typeIDs: []int{0, 0, 0, 1, 1}, mask: []int{1, 1, 1, 1, 1}},
	}
	ids, mask, types := packPairs(toks, 5)
	if want := []int64{101, 7, 102, 0, 0, 101, 8, 102, 9, 102}; !slices.Equal(ids, want) {
		t.Errorf("input ids %v, want %v", ids, want)
	}
	if want := []int64{1, 1, 1, 0, 0, 1, 1, 1, 1, 1}; !slices.Equal(mask, want) {
		t.Errorf("attention mask %v, want %v", mask, want)
	}
	if want := []int64{0, 0, 0, 0, 0, 0, 0, 0, 1, 1}; !slices.Equal(types, want) {
		t.Errorf("token types %v, want %v", types, want)
	}
}

func TestScoringNoPassagesNeedsNoModel(t *testing.T) {
	if s, err := (&CrossEncoder{}).Score("anything", nil); s != nil || err != nil {
		t.Errorf("Score with no passages = %v, %v; want nothing, no error", s, err)
	}
}

// A rerank request to a helper that was never told where the cross-encoder
// lives says so, rather than failing somewhere inside ONNX Runtime.
func TestRerankWithoutAModelDirectorySaysWhy(t *testing.T) {
	t.Setenv("MOCHIII_RERANK_MODEL_DIR", "")
	resp := (&server{}).dispatch(helperproto.Request{Method: helperproto.MethodRerank, Query: "q", Texts: []string{"a"}})
	if resp.OK || !strings.Contains(resp.Error, "MOCHIII_RERANK_MODEL_DIR") {
		t.Errorf("response %+v; want a refusal that names MOCHIII_RERANK_MODEL_DIR", resp)
	}
}

func TestRerankWithAMissingModelReportsTheFile(t *testing.T) {
	t.Setenv("MOCHIII_RERANK_MODEL_DIR", t.TempDir())
	srv := &server{}
	resp := srv.dispatch(helperproto.Request{Method: helperproto.MethodRerank, Query: "q", Texts: []string{"a"}})
	if resp.OK || !strings.Contains(resp.Error, "tokenizer.json") {
		t.Errorf("response %+v; want a refusal that names the missing tokenizer.json", resp)
	}
	// Opened once: a second request gets the same answer, not a second attempt.
	if again := srv.dispatch(helperproto.Request{Method: helperproto.MethodRerank, Query: "q", Texts: []string{"a"}}); again.Error != resp.Error {
		t.Errorf("second request answered %q, first %q", again.Error, resp.Error)
	}
}
