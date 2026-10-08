package main

import "testing"

// Which messages are a greeting and nothing else. The second list is the one
// that matters: a message wrongly called a greeting is answered without tools,
// and cannot be acted on.
func TestOnlyAGreetingIsAnsweredAsOne(t *testing.T) {
	for _, q := range []string{
		"hi", "Hi!", "hello", "hey", "heyyy", "hiiii", "yo", "howdy", "hi there", "hello mochiii",
		"good morning", "Good evening!", "gm", "good night", "gn",
		"thanks", "thank you", "Thanks!", "thx", "ty", "thanks a lot", "thank you so much", "thank you very much",
		"thanks again", "bye", "goodbye", "cya", "see you later",
		"how are you", "how are you doing", "what's up", "whats up", "sup",
	} {
		if !isGreetingOnly(q) {
			t.Errorf("isGreetingOnly(%q) = false; this is a greeting and nothing else", q)
		}
	}
	for _, q := range []string{
		// Answers to something the assistant asked. The next turn may edit a file.
		"yes", "ok", "okay", "sure", "no", "nope", "yep", "k",
		"ok thanks", "yes please", "no thanks", "sure, thanks",
		// Follow-ups that lean on the conversation.
		"how", "you", "again", "why", "so", "good", "a lot", "there", "night",
		"continue", "go on", "do it", "and then",
		// Reactions that are small talk to retrieval, and still need the conversation.
		"cool", "nice", "great", "awesome",
		// A greeting with a task attached is a task.
		"hi, can you fix the build", "hello what does main.go do", "thanks, now run the tests",
		"hey read config.yaml", "good morning, what changed in git",
		// Not small talk at all.
		"what is a goroutine", "fix it", "run the tests", "", "   ", "?",
		"hi hi hi hi hi hi hi", // seven words: past what a greeting is
	} {
		if isGreetingOnly(q) {
			t.Errorf("isGreetingOnly(%q) = true; answering this without tools or history would lose the request", q)
		}
	}
}

// The common greetings are also small talk to retrieval, so a message spared
// the agent is never one that code is still being searched for.
func TestAGreetingIsAlsoSmallTalkToRetrieval(t *testing.T) {
	for _, q := range []string{"hi", "hello", "hey", "good morning", "thanks", "thank you", "thanks a lot", "bye", "how are you", "what's up"} {
		if isGreetingOnly(q) && !isSmallTalk(q) {
			t.Errorf("%q is a greeting but not small talk: code would still be searched for it", q)
		}
	}
}
