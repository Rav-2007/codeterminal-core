package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"mochiii/editapply"
)

// These tests are about an answer as the user reads it. The marks in them are
// the ones a real model wrote on 2026-10-07 (citemarks.go has the story), and
// every "left alone" case is text the filter must not improve.

// filtered runs text through the filter in one piece.
func filtered(text string) string {
	f := &citeMarkFilter{}
	return f.feed(text) + f.flush()
}

// filteredIn runs text through the filter cut at the given byte offsets.
func filteredIn(text string, cuts ...int) string {
	f := &citeMarkFilter{}
	var out strings.Builder
	last := 0
	for _, c := range cuts {
		out.WriteString(f.feed(text[last:c]))
		last = c
	}
	out.WriteString(f.feed(text[last:]))
	out.WriteString(f.flush())
	return out.String()
}

// markCases is what goes in and what must come out.
var markCases = []struct{ name, in, want string }{
	// The three shapes measured, as they were written.
	{"the owner's screen",
		"the party in power since 1967【0†L1-L4】【0†L10-L13】【1†L1-L4】【2†L1-L4】.",
		"the party in power since 1967."},
	{"an address in the same brackets",
		"after succeeding Keir Starmer【https://www.gov.uk/government/people/andy-burnham】.",
		"after succeeding Keir Starmer (https://www.gov.uk/government/people/andy-burnham)."},
	{"a pointer with the address inside",
		"is **Sam Altman**【0†web_content:url=\"https://en.wikipedia.org/wiki/Sam_Altman\"】.",
		"is **Sam Altman** (https://en.wikipedia.org/wiki/Sam_Altman)."},
	{"a list, a mark closing each line",
		"- Wikipedia states he heads the ministry【1†L1-L4】.\n- The portal lists him【2†L1-L4】.\n",
		"- Wikipedia states he heads the ministry.\n- The portal lists him.\n"},
	{"the file-search form", "see the design notes【4:0†design notes.pdf】 for why", "see the design notes for why"},
	// The fourth shape, as two real answers ended on 2026-10-07 -- after the
	// first version of the filter had passed over them.
	{"a pointer written as an object",
		"This marks Spain's second World Cup victory.【{\"id\":0,\"cursor\":0,\"loc\":0}】【{\"id\":1,\"cursor\":0,\"loc\":0}】\n\nNext",
		"This marks Spain's second World Cup victory.\n\nNext"},
	{"an object pointer inside a sentence",
		"including the launch of ChatGPT【{\"id\":0,\"cursor\":0,\"loc\":0}】.",
		"including the launch of ChatGPT."},
	{"an object pointer holding an address",
		"the downloads page【{\"id\":0,\"url\":\"https://go.dev/dl/\"}】 lists it",
		"the downloads page (https://go.dev/dl/) lists it"},
	{"an answer cut off inside an object pointer", "victory.【{\"id\":0,\"cur", "victory."},
	// The fifth, from the run made to confirm the fourth was handled.
	{"a site's name",
		"will transition to LTS later in October 2026.【techearl.com】【versionlog.com】\n\nNext",
		"will transition to LTS later in October 2026. (techearl.com) (versionlog.com)\n\nNext"},
	{"a source's name", "the party in power since 1967【Wikipedia】.", "the party in power since 1967 (Wikipedia)."},
	{"digits alone", "the party in power since 1967【1】【2:0】.", "the party in power since 1967."},
	{"words and an address", "【see https://go.dev for more】", "(see https://go.dev for more)"},
	{"two addresses in one pair", "【https://a.example https://b.example】", "(https://a.example https://b.example)"},
	{"braces with words round them", "【see {name} here】", "(see {name} here)"},

	// The space a mark leaves behind.
	{"a space before the mark, a full stop after", "since 1967 【0†L1-L4】.", "since 1967."},
	{"a space on both sides", "since 1967 【0†L1-L4】 and still", "since 1967 and still"},
	{"no space before, one after", "since 1967【0†L1-L4】 and still", "since 1967 and still"},
	{"marks with spaces between them", "since 1967 【0†L1】 【1†L2】 and still", "since 1967 and still"},
	{"a mark opening a line", "【0†L1-L4】 Tamil Nadu has", "Tamil Nadu has"},
	{"a mark ending a line", "is the answer 【0†L1-L4】\nNext line", "is the answer\nNext line"},
	{"a mark ending the answer", "is the answer 【0†L1-L4】", "is the answer"},
	{"a mark inside a parenthesis", "(see 【0†L1-L4】)", "(see)"},
	{"an address opening a line", "【https://go.dev/dl/】 lists it", "(https://go.dev/dl/) lists it"},
	{"a pointer then an address", "the release notes【0†L1】【https://go.dev/doc/devel/release】 say", "the release notes (https://go.dev/doc/devel/release) say"},
	{"an answer cut off inside a mark", "since 1967【0†L1-", "since 1967"},

	// Left exactly as written.
	{"brackets as punctuation", "【重要】この変更は互換性がありません。", "【重要】この変更は互換性がありません。"},
	{"a label in a Japanese sentence", "この記事は【PR】です。【NEW】も同じ。", "この記事は【PR】です。【NEW】も同じ。"},
	{"numbered steps in Chinese", "步骤如下：【1】安装 【2】运行", "步骤如下：【1】安装 【2】运行"},
	{"a label in Korean", "이 글은 【AD】 입니다", "이 글은 【AD】 입니다"},
	{"a pointer is a pointer in any language", "東京は首都です【0†L1-L4】。", "東京は首都です。"},
	{"words that are not plain ASCII", "【café – menu】", "【café – menu】"},
	{"a dagger with no brackets", "Ada Lovelace† wrote the first program", "Ada Lovelace† wrote the first program"},
	{"an opening bracket never closed", "the 【 character opens a title", "the 【 character opens a title"},
	{"a brace that is not an object", "【{ は開き括弧です】", "【{ は開き括弧です】"},
	{"a dagger pair across two lines", "【0†L1\n-L4】", "【0†L1\n-L4】"},
	{"cut off inside brackets with no dagger", "see 【https://go.dev/d", "see 【https://go.dev/d"},
	{"a space before a full stop the model wrote", "x = a ? b : c .", "x = a ? b : c ."},
	{"two trailing spaces, a Markdown line break", "first line  \nsecond line", "first line  \nsecond line"},
	{"nothing at all", "", ""},
}

func TestReferenceMarksAreRemovedFromAnAnswer(t *testing.T) {
	for _, c := range markCases {
		if got := filtered(c.in); got != c.want {
			t.Errorf("%s:\n   in  %q\n   got %q\n  want %q", c.name, c.in, got, c.want)
		}
	}
}

// Whose brackets they are is read from what the USER wrote. A Japanese question
// can be answered with a line that opens 【PR】, before one Japanese character of
// the answer has gone by, and that label is not the model's habit. A page a
// tool fetched does not count: the user did not choose its language.
func TestTheUsersLanguageDecidesWhetherBracketsArePunctuation(t *testing.T) {
	const answer = "【PR】this line opens with a label"
	run := func(messages []chatMessage) string {
		f := newCiteMarkFilter(messages)
		return f.feed(answer) + f.flush()
	}
	japanese := []chatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "この記事を要約してください"}}
	if got := run(japanese); got != answer {
		t.Errorf("a Japanese user's answer had its label rewritten: %q", got)
	}
	english := []chatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "summarise this article"}}
	if got := run(english); got != "(PR)this line opens with a label" {
		t.Errorf("in an English conversation the pair is the model's habit; got %q", got)
	}
	// A Chinese page in a tool result is not the user speaking.
	fetched := append(english, chatMessage{Role: "tool", Content: "<web_content>北京是首都</web_content>"},
		chatMessage{Role: "assistant", Content: "東京"}, chatMessage{Role: "system", Content: "日本語"})
	if got := run(fetched); got != "(PR)this line opens with a label" {
		t.Errorf("text the user did not write decided the language; got %q", got)
	}
}

// A model's answer arrives in pieces of the provider's choosing, and a mark is
// as likely to be cut in half as not. The pieces must not matter -- including a
// cut inside a character, which is where 【 would otherwise be missed.
func TestTheSameTextComesOutHoweverTheAnswerIsCut(t *testing.T) {
	for _, c := range markCases {
		for cut := 0; cut <= len(c.in); cut++ {
			if got := filteredIn(c.in, cut); got != c.want {
				t.Fatalf("%s, cut at byte %d:\n   got %q\n  want %q", c.name, cut, got, c.want)
			}
		}
		// And one byte at a time, the worst a stream can do.
		cuts := make([]int, 0, len(c.in))
		for i := 1; i < len(c.in); i++ {
			cuts = append(cuts, i)
		}
		if got := filteredIn(c.in, cuts...); got != c.want {
			t.Errorf("%s, a byte at a time:\n   got %q\n  want %q", c.name, got, c.want)
		}
	}
}

// Code is copied and edit blocks are applied, so both are kept byte for byte --
// and the filter goes back to work on the line after.
func TestCodeAndEditBlocksKeepEveryCharacter(t *testing.T) {
	const mark = "【0†L1-L4】"
	for _, c := range []struct{ name, in, want string }{
		{"a fenced block",
			"Before" + mark + ".\n```go\ns := \"" + mark + "\"\n```\nAfter" + mark + ".\n",
			"Before.\n```go\ns := \"" + mark + "\"\n```\nAfter.\n"},
		{"a fence of tildes, indented",
			"  ~~~\n" + mark + "\n  ~~~\nAfter" + mark + ".",
			"  ~~~\n" + mark + "\n  ~~~\nAfter."},
		{"a tilde line inside a backtick fence does not close it",
			"```\n~~~\n" + mark + "\n```\nAfter" + mark + ".",
			"```\n~~~\n" + mark + "\n```\nAfter."},
		{"an inline code span",
			"The mark `" + mark + "` means source 0" + mark + ".",
			"The mark `" + mark + "` means source 0."},
		{"a stray backtick is forgotten at the end of its line",
			"a ` alone " + mark + "\nnext" + mark + ".",
			"a ` alone " + mark + "\nnext."},
		{"an edit block",
			"Changed" + mark + ":\n\npath: notes.md\n" + editSearchLine + "\nsee " + mark + "\n=======\nsee the notes\n" + editReplaceLine + "\n\nDone" + mark + ".",
			"Changed:\n\npath: notes.md\n" + editSearchLine + "\nsee " + mark + "\n=======\nsee the notes\n" + editReplaceLine + "\n\nDone."},
	} {
		if got := filtered(c.in); got != c.want {
			t.Errorf("%s:\n   got %q\n  want %q", c.name, got, c.want)
		}
		for cut := 0; cut <= len(c.in); cut++ {
			if got := filteredIn(c.in, cut); got != c.want {
				t.Fatalf("%s, cut at byte %d:\n   got %q\n  want %q", c.name, cut, got, c.want)
			}
		}
	}
}

// The two marker lines are copies of editapply's, which does not export them.
// If editapply ever reads a block differently, an edit block would stop being
// protected here and nothing else would say so.
func TestTheMarkFilterKnowsARealEditBlock(t *testing.T) {
	block := "path: notes.md\n" + editSearchLine + "\nold line\n=======\nnew line\n" + editReplaceLine + "\n"
	payload := editapply.ParseEditPayload(block)
	if len(payload.Blocks) != 1 || len(payload.Rejected) != 0 {
		t.Fatalf("editapply read %d block(s) and rejected %d from a block written with this file's markers; "+
			"editSearchLine/editReplaceLine no longer match the format", len(payload.Blocks), len(payload.Rejected))
	}
}

// Text with no 【 in it is not this filter's business, down to the last space
// and the last byte that is not text at all.
func TestAnAnswerWithNoMarkIsReturnedByteForByte(t *testing.T) {
	for _, in := range []string{
		"plain words.",
		"  leading and trailing spaces  ",
		"tabs\tand  double  spaces \n \n",
		"a ? b : c ;\n\n\n",
		"```\ncode\n```",
		"日本語の文章。「引用」です。",
		"half a character at the end \xe3\x80",
		"bytes that are not text \xff\xfe\x00 here",
		"】 a closing bracket alone, and a dagger † alone",
		strings.Repeat("word ", 500),
	} {
		if got := filtered(in); got != in {
			t.Errorf("changed text that has no mark in it:\n   in  %q\n   got %q", in, got)
		}
		for cut := 0; cut <= len(in) && cut < 64; cut++ {
			if got := filteredIn(in, cut); got != in {
				t.Fatalf("cut at byte %d changed text that has no mark in it:\n   in  %q\n   got %q", cut, in, got)
			}
		}
	}
}

// The filter holds text back while it waits for a closing bracket. That wait is
// bounded: an opening bracket that is never closed delays what follows by at
// most maxCiteMarkRunes characters, and loses none of it.
func TestAnUnclosedBracketHoldsBackOnlySoMuch(t *testing.T) {
	f := &citeMarkFilter{}
	shown := f.feed("before 【")
	if shown != "before" {
		t.Fatalf("shown %q before the bracket; want %q (the space waits with the bracket)", shown, "before")
	}
	body := strings.Repeat("x", maxCiteMarkRunes)
	if got := f.feed(body); got != "" {
		t.Fatalf("released %d byte(s) while the bracket could still close", len(got))
	}
	// One more character and it can no longer be a mark.
	got := f.feed("y")
	if want := " 【" + body + "y"; got != want {
		t.Fatalf("after the bound: got %d byte(s), want all %d back", len(got), len(want))
	}
}

// markStream serves one completion whose content arrives as the given pieces.
func markStream(t *testing.T, pieces ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, p := range pieces {
			content, _ := json.Marshal(p)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", content)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The wiring: every model call goes through streamWithRetry, so that is where
// an answer is cleaned -- before the client, the edit parser, the saved chat
// and the next call of the same turn see it. This drives the real function
// against a real stream, with the marks cut across pieces as a provider would.
func TestAModelCallsMarksNeverLeaveTheStream(t *testing.T) {
	srv := markStream(t,
		"C. Joseph Vijay is the Chief Minister", "【0", "†L1-L4】【1†", "L1-L4】",
		", according to the state portal", "【https://www.tn.gov.in/", "ministers】", ". ")

	var tokens []string
	var logged bytes.Buffer
	_, err := streamWithRetry(context.Background(), srv.URL, "k", "m", buildChatMessages("sys", nil, "who"), nil,
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, log.New(&logged, "", 0))
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	got := strings.Join(tokens, "")
	want := "C. Joseph Vijay is the Chief Minister, according to the state portal (https://www.tn.gov.in/ministers). "
	if got != want {
		t.Errorf("the client was sent\n   %q\nwant\n   %q", got, want)
	}
	for _, tok := range tokens {
		if tok == "" {
			t.Error("an empty token was sent; a piece the filter held back entirely must send nothing")
		}
	}
	// Said out loud, once per call: it is the only way anyone learns that a
	// model has this habit.
	if !strings.Contains(logged.String(), "3 reference mark(s)") {
		t.Errorf("the log does not count the marks; it says %q", logged.String())
	}
}

// And the conversation reaches the filter: the real stream, a Japanese user.
// The label stays, the pointer goes -- a pointer is a pointer in any language.
func TestAJapaneseConversationKeepsItsLabelsThroughTheRealStream(t *testing.T) {
	srv := markStream(t, "【PR】", "this article is sponsored", "【0†L1-L4】", ".")
	var tokens []string
	_, err := streamWithRetry(context.Background(), srv.URL, "k", "m", buildChatMessages("sys", nil, "この記事は広告ですか"), nil,
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if got, want := strings.Join(tokens, ""), "【PR】this article is sponsored."; got != want {
		t.Errorf("the client was sent %q, want %q", got, want)
	}
}

// A retry is safe only while the client has seen nothing. Text the filter is
// still holding has not been seen, so a stream that dies there is retried --
// and the first attempt's half-mark is not carried into the second.
func TestTextTheFilterIsHoldingDoesNotCloseTheDoorOnARetry(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if requests.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"【0†L1\"}}]}\n\n")
			w.(http.Flusher).Flush()
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"-L4】 the second answer\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var tokens []string
	_, err := streamWithRetry(context.Background(), srv.URL, "k", "m", buildChatMessages("sys", nil, "hello"), nil,
		ZDRConfig{}.resolvedProviderRouting(), collectTokens(&tokens), nil, nil, nil, discardLogger())
	if err != nil {
		t.Fatalf("want the second attempt's answer, got: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("upstream saw %d request(s), want 2: nothing had reached the client, so the call was safe to repeat", requests.Load())
	}
	// "-L4】" is ordinary text to a filter that never saw the first attempt's "【0†L1".
	if got := strings.Join(tokens, ""); got != "-L4】 the second answer" {
		t.Errorf("tokens = %q; the second attempt must be read on its own", got)
	}
}

// FuzzAnAnswerLosesOnlyItsMarks holds the filter to what it promises about text
// it was not written for: the pieces never matter, text with no opening bracket
// comes back unchanged, and nothing is ever added.
func FuzzAnAnswerLosesOnlyItsMarks(f *testing.F) {
	for _, c := range markCases {
		f.Add(c.in, uint16(len(c.in)/2), uint16(3))
	}
	f.Add("```\n【0†L1】\n```\n【0†L1】", uint16(5), uint16(1))
	f.Add("a `【0†L1】` b 【0†L1】", uint16(2), uint16(7))
	f.Add("【【0†a】0†b】", uint16(4), uint16(2))
	f.Add("x \xe3\x80【\xe3", uint16(3), uint16(1))
	f.Add(editSearchLine+"\n【0†L1】\n"+editReplaceLine+"\n【0†L1】", uint16(9), uint16(4))

	f.Fuzz(func(t *testing.T, in string, cut, step uint16) {
		whole := filtered(in)

		// Cut once, anywhere.
		if c := int(cut) % (len(in) + 1); filteredIn(in, c) != whole {
			t.Fatalf("cut at byte %d of %q:\n   got %q\n  whole %q", c, in, filteredIn(in, c), whole)
		}
		// Cut into pieces of a fixed size.
		if n := int(step)%7 + 1; len(in) > n {
			var cuts []int
			for i := n; i < len(in); i += n {
				cuts = append(cuts, i)
			}
			if got := filteredIn(in, cuts...); got != whole {
				t.Fatalf("in pieces of %d byte(s), %q:\n   got %q\n  whole %q", n, in, got, whole)
			}
		}
		if !strings.Contains(in, "【") && whole != in {
			t.Fatalf("no opening bracket in %q, and it came back as %q", in, whole)
		}
		// A mark is removed, or its brackets become " (" and ")": never longer.
		if len(whole) > len(in) {
			t.Fatalf("%q grew to %q", in, whole)
		}
	})
}
