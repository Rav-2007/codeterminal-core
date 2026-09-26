package main

import (
	"strings"
	"unicode"
)

// smallTalkWords are the words a greeting, thanks or acknowledgement is made
// of. A prompt made ONLY of these (see isSmallTalk) asks nothing about code.
var smallTalkWords = map[string]bool{
	"hi": true, "hello": true, "hey": true, "heya": true, "hiya": true, "yo": true, "howdy": true,
	"sup": true, "whats": true, "what's": true, "up": true, "how": true, "are": true, "you": true,
	"u": true, "doing": true, "good": true, "morning": true, "afternoon": true, "evening": true,
	"night": true, "gm": true, "gn": true, "thanks": true, "thank": true, "thx": true, "ty": true,
	"ok": true, "okay": true, "k": true, "kk": true, "cool": true, "nice": true, "great": true,
	"awesome": true, "bye": true, "goodbye": true, "cya": true, "later": true, "yes": true,
	"no": true, "yep": true, "nope": true, "sure": true, "there": true, "mochiii": true,
	"so": true, "much": true, "a": true, "lot": true, "again": true,
}

// isSmallTalk reports whether prompt is only a greeting, thanks or
// acknowledgement -- short, and every word from smallTalkWords, with
// stretched letters folded ("hiii" is "hi", "heyyy" is "hey"). Anything
// naming something else ("hi, explain retry.go") is a real question.
func isSmallTalk(prompt string) bool {
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(unicode.IsLetter(r) || r == '\'')
	})
	if len(words) == 0 || len(words) > 6 {
		return false
	}
	for _, w := range words {
		if !smallTalkWords[w] && !smallTalkWords[foldStretched(w)] {
			return false
		}
	}
	return true
}

// foldStretched collapses a run of three or more of the same letter to one:
// "hiii" -> "hi", "heyyy" -> "hey", "thanksss" -> "thanks". Runs of two are
// kept, because English has them ("good", "cool").
func foldStretched(w string) string {
	r := []rune(w)
	var out []rune
	for i := 0; i < len(r); {
		j := i
		for j < len(r) && r[j] == r[i] {
			j++
		}
		n := j - i
		if n >= 3 {
			n = 1
		}
		for k := 0; k < n; k++ {
			out = append(out, r[i])
		}
		i = j
	}
	return string(out)
}
