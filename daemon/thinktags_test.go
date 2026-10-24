package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// split feeds chunks through a splitter for a server on this machine, the way
// a stream does, and returns the answer and the thinking it produced.
func split(base string, chunks ...string) (answer, thinking string) {
	var a, th strings.Builder
	t := newThinkSplitter(base)
	onReasoning := func(s string) { th.WriteString(s) }
	for _, c := range chunks {
		a.WriteString(t.answer(c, onReasoning))
	}
	t.finishing(func(s string) error { a.WriteString(s); return nil }, onReasoning, nil)("stop")
	return a.String(), th.String()
}

func TestThinkingWrittenIntoTheAnswerIsSplitOff(t *testing.T) {
	const local = "http://127.0.0.1:11434/v1"
	for _, tc := range []struct {
		name             string
		base             string
		chunks           []string
		answer, thinking string
	}{
		{"one chunk", local, []string{"<think>weigh it</think>\n\nThe answer."}, "The answer.", "weigh it"},
		{"tags split across chunks", local, []string{"<thi", "nk>weigh", " it</th", "ink>\nThe answer."}, "The answer.", "weigh it"},
		{"white space before the tag", local, []string{"\n  ", "<think>x</think>A"}, "A", "x"},
		{"an answer that starts like the tag", local, []string{"<", "div>hello</div>"}, "<div>hello</div>", ""},
		{"a tag later in the answer is the answer's", local, []string{"Use ", "<think> tags like this."}, "Use <think> tags like this.", ""},
		{"never closed: all of it was thinking", local, []string{"<think>still going</th"}, "", "still going</th"},
		{"cut before the tag could be told", local, []string{"<thi"}, "<thi", ""},
		{"a provider not on this machine is passed through", "https://api.example.com/v1",
			[]string{"<think>x</think>A"}, "<think>x</think>A", ""},
	} {
		answer, thinking := split(tc.base, tc.chunks...)
		if answer != tc.answer || thinking != tc.thinking {
			t.Errorf("%s: answer %q, thinking %q; want %q, %q", tc.name, answer, thinking, tc.answer, tc.thinking)
		}
	}
}

// THROUGH THE REAL STREAM: a local server that writes its thinking into the
// answer has it delivered as thinking, never as answer text.
//
// Neuter check: make thinkSplitter.answer return content unchanged, and the
// thinking is in the answer.
func TestALocalServersInlineThinkingNeverReachesTheAnswer(t *testing.T) {
	var events []string
	for _, c := range []string{"<thi", "nk>Let me reason about greet.go.</th", "ink>\n\ngreetingFor welcomes a visitor."} {
		b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": c}}}})
		events = append(events, "data: "+string(b))
	}
	events = append(events, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, "data: [DONE]")
	local := newScriptedLocal(t, []string{"completion"}, 4096, func(int, map[string]any) (int, []string) { return 0, events })
	var answer, thinking strings.Builder
	_, err := streamCompletion(context.Background(), local.base(), "", "m", []chatMessage{{Role: "user", Content: "hi"}}, nil,
		providerRouting{}, func(s string) error { answer.WriteString(s); return nil }, nil,
		func(s string) { thinking.WriteString(s) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if answer.String() != "greetingFor welcomes a visitor." || thinking.String() != "Let me reason about greet.go." {
		t.Errorf("answer %q, thinking %q", answer.String(), thinking.String())
	}
}
