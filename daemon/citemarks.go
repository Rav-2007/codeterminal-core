package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// REFERENCE MARKS A MODEL WRITES OUT OF HABIT, AND NOTHING HERE CAN OPEN.
//
// FOUND 2026-10-07, on the owner's screen. Asked who holds an office, the model
// searched, answered correctly, and ended its answer with
//
//	since 1967【0†L1-L4】【0†L10-L13】【1†L1-L4】【2†L1-L4】.
//
// Each mark says "source 0, lines 1 to 4" in the format of a browsing tool the
// model was trained with, where an app turns the mark into a link. Nothing in
// this product does, so on screen it is noise at the end of a sentence. The
// numbers are not even ours: web_search lists its results from 1 and these
// count from 0, so a mark cannot be turned into an address either -- guessing
// which page "0" was would put a wrong source under a claim.
//
// THEN MEASURED, before anything was built: twenty questions to the same model
// (nvidia/nemotron-3-super-120b-a12b). Eight were about current facts, and the
// model wrote the brackets in three shapes, not one:
//
//	【0†L1-L4】                                     a pointer; nothing to keep
//	【https://www.gov.uk/government/people/...】     a real address, in the same brackets
//	【0†web_content:url="https://en.wikipedia.org/wiki/Sam_Altman"】
//	                                               both: a pointer with the address inside
//
// AND TWO MORE, which the first run did not show and later runs did -- in
// answers the first version of this file had already been over:
//
//	【{"id":0,"cursor":0,"loc":0}】                   a pointer, written as an object
//	【techearl.com】【versionlog.com】                 a site's name
//
// Five shapes from one model in seven runs. A list of shapes was never going to
// be finished, so the rule is about WHOSE BRACKETS THESE ARE instead. 【 】 are
// everyday punctuation in Chinese, Japanese and Korean, and in no other
// writing: in a conversation with none of those in it, a pair of them is the
// model's habit and nothing else. So, for a pair on one line with at most
// maxCiteMarkRunes inside:
//
//   - A POINTER -- a dagger (†) inside, or an object in braces -- points into
//     something the reader cannot see. It is removed; if an address is written
//     inside it, the address is kept, in ordinary parentheses.
//   - NOTHING INSIDE BUT ONE ADDRESS: kept, in ordinary parentheses.
//   - ANYTHING ELSE IN PLAIN ASCII, when neither the user's messages nor the
//     answer so far holds a Chinese, Japanese or Korean character: the model
//     citing its own way. Digits alone are one more pointer and are removed;
//     words -- a site, a source's name -- are kept, in ordinary parentheses.
//   - Everything else is left exactly as written: 【重要】 is a heading, and
//     【PR】 or 【1】 in a Japanese answer is a label.
//
// Nothing the model wrote in words is ever dropped; only the brackets change.
// A pair this still gets wrong is shown as written, and streamWithRetry counts
// the ones it handles in the log, which is where a model's habit shows up first.
//
// The pages a turn really read are listed under the answer by the daemon, which
// knows them (websources.go); the prompt asks for sources in words. This file is
// the half that does not depend on a model doing as it was asked, which matters
// for a daemon that is pointed at any model of seventeen providers.
//
// WHAT IS NEVER TOUCHED: a fenced code block, an inline code span and an edit
// block. Those are copied or applied byte for byte -- a SEARCH that lost a
// character no longer matches its file -- and a model asked what such a mark
// means has to be able to show one.
//
// WHERE IT SITS: in streamWithRetry, the one function every model call goes
// through. So the text is clean in what is streamed to the client, in what is
// parsed for edit blocks, in what is saved, and in the assistant message the
// next call of the same turn reads -- a model that never sees its own marks has
// one less example of them to copy.

// maxCiteMarkRunes bounds what may stand between 【 and 】 for the pair to be
// read as a mark. The longest shape measured holds an address, so this is an
// address's length rather than a pointer's. It is also the most text the filter
// will ever hold back waiting for a closing bracket: past it, the text is
// released as written.
const maxCiteMarkRunes = 300

// Edit-block markers, as editapply reads them. Kept as copies because the
// originals are unexported; TestTheMarkFilterKnowsARealEditBlock fails if
// editapply stops recognising a block written with these.
const (
	editSearchLine  = "<<<<<<< SEARCH"
	editReplaceLine = ">>>>>>> REPLACE"
)

// addressInMark finds an address written inside a mark.
var addressInMark = regexp.MustCompile(`https?://[^\s"'<>{}【】†]+`)

// citeMarkFilter removes reference marks from text as it streams. feed takes
// text in pieces of any size and returns what may be shown now; flush returns
// what was still being held when the stream ended. Fed the same text in any
// pieces, it returns the same text.
type citeMarkFilter struct {
	out strings.Builder // released by the call in progress

	// A possible mark: everything from its 【. Not released until its 】 says
	// what it was, or a newline, a second 【 or the bound says it was nothing.
	held      strings.Builder
	holding   bool
	heldRunes int
	dagger    bool

	// Spaces and tabs are released one rune late, so that the one before a
	// removed mark can go with it: "since 1967 【0†L1-L4】." must not become
	// "since 1967 .". Text with no mark in it gets every space back unchanged.
	spaces  strings.Builder
	removed bool // a mark went, and nothing has been released since

	// Where the reader is, from the text as the model wrote it.
	lineStart   []byte // the first bytes of the current line
	lineHasText bool
	fence       byte // '`' or '~' inside a fenced block, else 0
	inEdit      bool // between the SEARCH and REPLACE lines of an edit block
	inCode      bool // inside an inline code span, to the end of the line at most

	partial string // the first bytes of a character the last piece ended inside

	// cjk is set once the conversation or the answer so far holds a Chinese,
	// Japanese or Korean character: from then on 【 】 may be punctuation.
	cjk bool

	marks int // how many were removed or rewritten
}

// newCiteMarkFilter returns a filter for one model call of the conversation in
// messages. Only what the USER wrote decides whether the brackets may be
// punctuation: a page a tool fetched can be in any language, and the user
// reading the answer did not choose it.
func newCiteMarkFilter(messages []chatMessage) *citeMarkFilter {
	f := &citeMarkFilter{}
	for _, m := range messages {
		if m.Role == "user" && strings.ContainsFunc(m.Content, isCJK) {
			f.cjk = true
			break
		}
	}
	return f
}

// isCJK reports a letter of a script that uses 【 】 as punctuation.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// isPlainASCII reports text made only of printable ASCII.
func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return s != ""
}

// digitsAlone matches a pointer with no dagger: "1", "0:2", "3-5".
var digitsAlone = regexp.MustCompile(`^[0-9][0-9 :,.\-]*$`)

// feed takes the next piece of the stream and returns what may be shown.
func (f *citeMarkFilter) feed(piece string) string {
	f.out.Reset()
	if f.partial != "" {
		piece, f.partial = f.partial+piece, ""
	}
	for len(piece) > 0 {
		if !utf8.FullRuneInString(piece) {
			// A character split across two pieces. Read whole, or 【 is missed.
			f.partial = piece
			break
		}
		r, size := utf8.DecodeRuneInString(piece)
		f.take(r, piece[:size])
		piece = piece[size:]
	}
	return f.out.String()
}

// flush ends the stream and returns what was still held.
func (f *citeMarkFilter) flush() string {
	f.out.Reset()
	if f.partial != "" {
		rest := f.partial
		f.partial = ""
		for len(rest) > 0 {
			r, size := utf8.DecodeRuneInString(rest)
			f.take(r, rest[:size])
			rest = rest[size:]
		}
	}
	if f.holding {
		if f.dagger || strings.HasPrefix(f.held.String(), "【{") {
			// A mark the answer was cut off inside is no more use than a whole
			// one, and an address cut short is not an address: it all goes.
			f.holding = false
			f.held.Reset()
			f.marks++
			f.removed = true
		} else {
			f.release()
		}
	}
	if !f.removed {
		f.out.WriteString(f.spaces.String())
	}
	f.spaces.Reset()
	return f.out.String()
}

// take reads one character. raw is its bytes exactly as they arrived, which is
// what is written out: a byte that is not valid text passes through untouched.
func (f *citeMarkFilter) take(r rune, raw string) {
	if f.holding {
		switch {
		case r == '】':
			f.held.WriteString(raw)
			f.resolveMark(f.held.String())
			return
		case r == '\n' || r == '【' || f.heldRunes >= maxCiteMarkRunes:
			// Not a mark after all. What was held is ordinary text, and this
			// character is read afresh below.
			f.release()
		default:
			if r == '†' {
				f.dagger = true
			}
			f.held.WriteString(raw)
			f.heldRunes++
			return
		}
	}
	if r == '【' && !f.verbatim() {
		f.holding, f.dagger, f.heldRunes = true, false, 0
		f.held.Reset()
		f.held.WriteString(raw)
		return
	}
	f.plain(r, raw)
}

// verbatim reports whether the reader is somewhere text is kept byte for byte.
func (f *citeMarkFilter) verbatim() bool {
	return f.fence != 0 || f.inEdit || f.inCode
}

// resolveMark decides what a held 【...】 was.
func (f *citeMarkFilter) resolveMark(text string) {
	inside := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "【"), "】"))
	address := addressInMark.FindString(inside)
	// The model's own brackets, not a writer's: see the top of this file.
	habit := !f.cjk && isPlainASCII(inside)
	// A pointer into something the reader cannot see.
	pointer := f.dagger ||
		(strings.HasPrefix(inside, "{") && strings.HasSuffix(inside, "}")) ||
		(habit && digitsAlone.MatchString(inside))

	keep := ""
	switch {
	case pointer:
		keep = address // "" for a pointer and nothing else
	case address != "" && inside == address:
		keep = address
	case habit:
		keep = inside
	default:
		f.release()
		return
	}
	f.holding = false
	f.held.Reset()
	f.marks++
	if keep == "" {
		f.removed = true
		return
	}
	f.rewrite("(" + keep + ")")
}

// rewrite puts text where a mark stood, as ordinary words of the sentence: one
// space before it unless it opens the line or a space is already waiting.
func (f *citeMarkFilter) rewrite(text string) {
	if f.lineHasText && f.spaces.Len() == 0 {
		f.plain(' ', " ")
	}
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		f.plain(r, text[:size])
		text = text[size:]
	}
}

// release writes held text out as the ordinary text it turned out to be.
func (f *citeMarkFilter) release() {
	held := f.held.String()
	f.holding = false
	f.held.Reset()
	for len(held) > 0 {
		r, size := utf8.DecodeRuneInString(held)
		f.plain(r, held[:size])
		held = held[size:]
	}
}

// plain writes one character of ordinary text.
func (f *citeMarkFilter) plain(r rune, raw string) {
	if r == ' ' || r == '\t' {
		if f.removed && (f.spaces.Len() > 0 || !f.lineHasText) {
			// The space after a removed mark, when the one before it (or the
			// start of the line) already separates the words.
			return
		}
		f.spaces.WriteString(raw)
		f.track(r, raw)
		return
	}
	if f.spaces.Len() > 0 {
		if !(f.removed && endsAClause(r)) {
			f.out.WriteString(f.spaces.String())
		}
		f.spaces.Reset()
	}
	f.removed = false
	if !f.cjk && r >= 0x1100 && isCJK(r) {
		f.cjk = true
	}
	f.out.WriteString(raw)
	f.track(r, raw)
}

// endsAClause reports a character that a space never stands before, so the
// space left behind by a removed mark goes too.
func endsAClause(r rune) bool {
	return strings.ContainsRune("\n.,;:!?)]}。，、；：！？）", r)
}

// track follows the text as written, to know when it is inside code.
func (f *citeMarkFilter) track(r rune, raw string) {
	if r == '\n' {
		f.lineStart = f.lineStart[:0]
		f.lineHasText = false
		f.inCode = false
		return
	}
	if r != ' ' && r != '\t' {
		f.lineHasText = true
	}
	if len(f.lineStart) < len(editReplaceLine) {
		f.lineStart = append(f.lineStart, raw...)
		if f.readLineStart() {
			return
		}
	}
	if r == '`' && f.fence == 0 && !f.inEdit {
		f.inCode = !f.inCode
	}
}

// readLineStart acts on the line's opening the moment it becomes a fence or an
// edit-block marker, and reports whether it just did.
func (f *citeMarkFilter) readLineStart() bool {
	line := string(f.lineStart)
	if f.inEdit {
		if line == editReplaceLine {
			f.inEdit = false
			return true
		}
		return false
	}
	// A fence may be indented by up to three spaces.
	mark := strings.TrimLeft(line, " ")
	if len(line)-len(mark) <= 3 && (mark == "```" || mark == "~~~") {
		switch f.fence {
		case 0:
			f.fence = mark[0]
		case mark[0]:
			f.fence = 0
		}
		f.inCode = false
		return true
	}
	if f.fence == 0 && line == editSearchLine {
		f.inEdit = true
		return true
	}
	return false
}
