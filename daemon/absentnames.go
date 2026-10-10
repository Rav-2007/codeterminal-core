package main

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"mochiii/editapply"
)

// A NAME THAT IS NOWHERE IN THE INDEX IS SAID TO BE NOWHERE.
//
// search_code ranks by meaning, and the nearest code to a question comes back
// whatever the question named. Asked about a file or a function that is not in
// the project it answers with the chunks a vaguer question would have got, and
// nothing in them says the name was not found. A READ of a path that does not
// exist is answered plainly (missingfile.go); this is that answer for a search.
//
// The keyword index can give it exactly. It is a trigram index over every
// chunk's text and every indexed file's path, so "does this string appear at
// all" is one seek, and the answer is a fact about the index rather than a
// score with a threshold someone picked. (A threshold is not available anyway:
// the ranking is reciprocal-rank fusion, which discards relevance magnitude by
// construction -- docs/MULTI_AGENT_DESIGN.md.)
//
// ONLY WHAT WAS WRITTEN TO BE FOUND AS WRITTEN IS LOOKED UP -- a file name, an
// identifier. A plain word that happens to be missing from a small project
// says nothing about the project and is never reported; see writtenName.
//
// NOTHING IS SAID WHERE SAYING IT COULD BE WRONG:
//
//   - the index is still in its old layout, where a file's path is stored but
//     not searchable, so a name that exists only as a file name would be
//     reported missing;
//   - the keyword index holds nothing while the meaning index does (they are
//     separate files and can disagree);
//   - this turn has already changed files: the index describes the project as
//     it was, and a name the turn itself just wrote is not in it;
//   - the name is one the gates hide. The index leaves secret-shaped names out,
//     so it would call every one of them missing -- and "there is no .env here"
//     is this function answering a question the listing refuses.
//
// THE WORDS ARE ABOUT THE INDEX, on purpose. It trails a file added a moment
// ago and omits what it is told to, so "not in the index" is as far as this
// can honestly go; missingfile.go, which walks the project, is the one that
// may say "does not exist".

const (
	// absentNamesOpen and absentNamesClose bracket the line, so that the stall
	// count can set it aside when it compares what one search returned with
	// what another did (splitAbsentNamesLead).
	absentNamesOpen  = "[Note from Mochiii: this project's search index holds nothing named or containing "
	absentNamesClose = " may have nothing to do with what you asked for.]\n\n"

	// A name is looked up between these lengths: three is the shortest string a
	// trigram index can match, and nothing longer than the other is a name.
	minAbsentNameLen = 3
	maxAbsentNameLen = 64
	// maxAbsentNamesAsked bounds the seeks one search may add.
	maxAbsentNamesAsked = 8
	// maxAbsentNamesSaid bounds the names the line repeats back.
	maxAbsentNamesSaid = 3
)

// nameIndex is a keyword index that can say which names it does not hold at
// all. Asked for by type rather than added to LexicalStore: it is one caller's
// question, and a store that cannot answer it simply is not asked.
type nameIndex interface {
	namesNotHeld(ctx context.Context, names []string) []string
}

// namesNotHeld returns those of names that appear nowhere in the index, in any
// letter case: not in a chunk's text, not in a file's path. Nil when the index
// cannot say (see the list above), and nil when a seek fails -- a search that
// failed says nothing about any of them.
func (s *FTSChunkStore) namesNotHeld(ctx context.Context, names []string) []string {
	if len(names) == 0 || s.legacyLayout() {
		return nil
	}
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM chunk_index LIMIT 1`).Scan(&one); err != nil {
		return nil // nothing indexed (sql.ErrNoRows), or unreadable
	}
	var absent []string
	for _, name := range names {
		if len(name) < minAbsentNameLen {
			continue
		}
		// Quoted, as buildLexicalQuery quotes: the trigram tokenizer then
		// requires the whole string as one contiguous substring.
		err := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM code_chunks_fts WHERE code_chunks_fts MATCH ? LIMIT 1`,
			`"`+strings.ReplaceAll(name, `"`, `""`)+`"`).Scan(&one)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			absent = append(absent, name)
		case err != nil:
			return nil
		}
	}
	return absent
}

// writtenNamePattern finds the runs a name could be in: letters, digits and
// the marks that join the parts of a path or an identifier.
var writtenNamePattern = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./\-]*`)

// writtenNames returns the names in query that were written to be found as
// written, each once whatever its case, in the order they appear. A path is
// looked up by its last element: a file asked for in the wrong folder is in
// the index all the same, and the search itself ranks its path first.
func writtenNames(query string) []string {
	var names []string
	seen := map[string]bool{}
	for _, run := range writtenNamePattern.FindAllString(query, -1) {
		name := strings.TrimRight(run[strings.LastIndexByte(run, '/')+1:], ".-")
		if len(name) < minAbsentNameLen || len(name) > maxAbsentNameLen || !writtenName(name) {
			continue
		}
		if editapply.MatchesSecretName(name) || editapply.IsProtectedDirName(name) {
			continue
		}
		if key := strings.ToLower(name); !seen[key] {
			seen[key] = true
			names = append(names, name)
		}
		if len(names) == maxAbsentNamesAsked {
			break
		}
	}
	return names
}

// writtenName reports a string that is a name and not a word: it is joined
// with an underscore, mixes capitals into its middle (parseConfig, HTTPServer),
// carries a digit after a letter (sha256), or has a dot with a real word on
// each side (settings.yaml, fmt.Println).
//
// TIGHTER THAN IT MIGHT BE, because the cost of a miss is nothing -- the search
// answers as it always did -- and the cost of a false one is a note telling the
// model that "please" is not in the project. So a capital only at the front is
// a sentence's first word; capitals throughout are emphasis; "1st" and "e.g"
// are English.
func writtenName(name string) bool {
	var lower, innerUpper, digit, underscore, letter bool
	for i, r := range name {
		switch {
		case r == '_':
			underscore = true
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsUpper(r):
			letter = true
			innerUpper = innerUpper || i > 0
		case unicode.IsLower(r):
			letter, lower = true, true
		}
	}
	if !letter {
		return false
	}
	if underscore || (innerUpper && lower) {
		return true
	}
	if digit && len(name) >= 4 && unicode.IsLetter(rune(name[0])) {
		return true
	}
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		return wordLike(name[:dot]) && wordLike(name[dot+1:])
	}
	return false
}

// wordLike is one side of a dotted name: two characters or more, a letter
// among them. It is what keeps "e.g" and "3.14" from being file names.
func wordLike(part string) bool {
	return len(part) >= 2 && strings.IndexFunc(part, unicode.IsLetter) >= 0
}

// absentNamesLead is the line search_code opens with when query names
// something the index does not hold, or "" when there is nothing to say or
// nothing that can be said (see the top of this file).
func (s *Server) absentNamesLead(ctx context.Context, query string) string {
	index, ok := s.lexicalStore.(nameIndex)
	if !ok {
		return ""
	}
	if st := stageFromCtx(ctx); st != nil && len(st.touched) > 0 {
		return ""
	}
	absent := index.namesNotHeld(ctx, writtenNames(query))
	if len(absent) == 0 {
		return ""
	}
	more := ""
	if n := len(absent) - maxAbsentNamesSaid; n > 0 {
		absent = absent[:maxAbsentNamesSaid]
		more = " and " + strconv.Itoa(n) + " more"
	}
	return absentNamesOpen + `"` + strings.Join(absent, `", "`) + `"` + more +
		". The code below is only the nearest by meaning, and" + absentNamesClose
}

// splitAbsentNamesLead separates the line above from what the search returned.
// A result without one, or one whose line was cut by the result cap, comes
// back whole as body.
func splitAbsentNamesLead(rendered string) (lead, body string) {
	if !strings.HasPrefix(rendered, absentNamesOpen) {
		return "", rendered
	}
	end := strings.Index(rendered, absentNamesClose)
	if end < 0 {
		return "", rendered
	}
	end += len(absentNamesClose)
	return rendered[:end], rendered[end:]
}
