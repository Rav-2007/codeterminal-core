package main

import "strings"

// THINKING WRITTEN INTO THE ANSWER (1.4.0).
//
// A thinking model's reasoning arrives in a field of its own from most servers
// -- "reasoning" or "reasoning_content", handled in provider.go -- but some
// local setups write it into the answer itself, as <think>...</think> before
// the reply: vLLM started without a reasoning parser, an older Ollama. That
// text was shown as the answer, kept in the chat, and sent back with every
// later question, which a model with a small window can least afford.
//
// For a server on this machine, a reply whose first text is <think> has
// everything up to </think> handed over as thinking, the way the reasoning
// fields are, and only what follows is the answer. A tag split across chunks is
// held back until it can be told apart, and nothing held back is lost when the
// stream ends.

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

const (
	thinkUndecided = iota // nothing but white space yet, or the start of "<think>"
	thinkInside           // inside the leading <think> block
	thinkAnswer           // the answer: passed through as it comes
)

// thinkSplitter splits one stream's answer text from a leading <think> block.
type thinkSplitter struct {
	state   int
	pending string // text held back while it may still be part of a tag
}

// newThinkSplitter is the splitter for a stream from apiBase: one that passes
// everything through for any server not on this machine.
func newThinkSplitter(apiBase string) *thinkSplitter {
	if !isLoopbackBase(apiBase) {
		return &thinkSplitter{state: thinkAnswer}
	}
	return &thinkSplitter{}
}

// answer returns the part of content that is the answer, and hands any
// thinking in it to onReasoning.
func (t *thinkSplitter) answer(content string, onReasoning func(string)) string {
	if t.state == thinkAnswer || content == "" {
		return content
	}
	text := t.pending + content
	t.pending = ""
	if t.state == thinkUndecided {
		trimmed := strings.TrimLeft(text, " \t\r\n")
		switch {
		case strings.HasPrefix(trimmed, thinkOpen):
			t.state = thinkInside
			text = trimmed[len(thinkOpen):]
		case strings.HasPrefix(thinkOpen, trimmed):
			t.pending = text
			return ""
		default:
			t.state = thinkAnswer
			return text
		}
	}
	if i := strings.Index(text, thinkClose); i >= 0 {
		if i > 0 && onReasoning != nil {
			onReasoning(text[:i])
		}
		t.state = thinkAnswer
		return strings.TrimLeft(text[i+len(thinkClose):], "\r\n")
	}
	keep := partialSuffix(text, thinkClose)
	if body := text[:len(text)-keep]; body != "" && onReasoning != nil {
		onReasoning(body)
	}
	t.pending = text[len(text)-keep:]
	return ""
}

// partialSuffix is the length of the longest end of s that begins tag.
func partialSuffix(s, tag string) int {
	for n := min(len(s), len(tag)-1); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}

// finishing is onFinish, after what is still held back has been handed over:
// to the answer when it never became a tag, to the thinking when it was part
// of one.
func (t *thinkSplitter) finishing(onToken func(string) error, onReasoning func(string), onFinish func(string)) func(string) {
	return func(reason string) {
		if p := t.pending; p != "" {
			t.pending = ""
			switch {
			case t.state == thinkUndecided:
				_ = onToken(p)
			case onReasoning != nil:
				onReasoning(p)
			}
		}
		if onFinish != nil {
			onFinish(reason)
		}
	}
}
