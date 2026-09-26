package main

import "testing"

func TestSmallTalkIsRecognised(t *testing.T) {
	for _, p := range []string{"hi", "hiii", "Hey!", "heyyy there", "hello mochiii", "thanks", "thank you so much",
		"ok", "okay cool", "good morning", "bye", "how are you?", "yo", "thx!!"} {
		if !isSmallTalk(p) {
			t.Errorf("isSmallTalk(%q) = false, want true", p)
		}
	}
}

// Anything that names something is a real question and keeps its code search.
func TestRealQuestionsAreNotSmallTalk(t *testing.T) {
	for _, p := range []string{"", "fix the tests", "hi, explain retry.go", "what does gatherContext do",
		"ok now refactor the parser", "why", "hello world program in go", "good first issue ideas",
		"thanks, now add a test for isSmallTalk", "explain", "no errors?", "how are you handling retries"} {
		if isSmallTalk(p) {
			t.Errorf("isSmallTalk(%q) = true, want false", p)
		}
	}
}

// The reported turn: "hi" reached the model wrapped in ~32 KB of code and was
// answered as if the code were the message. The store here ALWAYS has a hit,
// and the control question proves it: only the greeting goes without.
func TestAGreetingGetsNoRetrievedCode(t *testing.T) {
	s := &Server{
		logger:             discardLogger(),
		embedder:           &fakeEmbedder{dim: embedDim},
		store:              fixedStore{chunks: []Chunk{lineChunk("config.go", 1, 20)}},
		retrievalTopK:      defaultK,
		contextBudgetChars: 1 << 20,
	}
	if got := s.gatherContext(t.Context(), "what does the config loader do"); got.Skipped || len(got.Chunks) == 0 {
		t.Fatalf("control: a real question got no code (%+v); the harness proves nothing", got)
	}
	for _, greeting := range []string{"hi", "hiii", "thanks!", "ok cool"} {
		if got := s.gatherContext(t.Context(), greeting); !got.Skipped || len(got.Chunks) != 0 {
			t.Errorf("gatherContext(%q) attached %d chunk(s); a greeting must get none", greeting, len(got.Chunks))
		}
	}
}
