package editapply

import (
	"fmt"
	"strings"
)

// Tier B of the syntax gate: structural delimiter balance, for the languages
// this binary has an entry for but no parser.
//
// WHY THIS TIER EXISTS. checkSyntax has exactly one real parser -- go/parser --
// and every other language in extensionLanguages got
// "no syntax check applied (unsupported for .ts)". That note is honest and it
// is also the whole answer: a model that mangles a TypeScript file's braces
// gets the same silence as one that writes it perfectly. Bracket balance is not
// a parse, but it catches the single most common shape of a botched
// SEARCH/REPLACE -- a replacement that opens or closes one more block than the
// text it replaced.
//
// WHY IT IS ADVISORY, AND WHY THAT IS THE DESIGN RATHER THAN A HEDGE. Every
// part of this is a heuristic. Skip-state tracking models comments, strings and
// template literals, and a construct it does not model -- an exotic regex, a
// language feature added next year -- produces a WRONG imbalance. A wrong note
// costs a reader five seconds. A wrong REFUSAL blocks a correct edit and makes
// the file unfixable through edit blocks, which is precisely the fault the
// delta rule was written to remove from Tier A. The tier that is less sure must
// not be the tier that is more forceful.
//
// That is enforced by SIGNATURE, not by discipline: checkDelimiterTier returns
// a note and nothing else. There is no error to return, so no future edit to
// this file can make Tier B refuse without someone deliberately changing the
// function's shape and every caller of it.
//
// WHAT IT DELIBERATELY DOES NOT DO. It does not check Python indentation. That
// is where Python's real structure lives, and a checker that counted brackets
// and then claimed to have checked Python would be making the same overclaim
// this gate's history is a list of. Python gets bracket balance, and the note
// says bracket balance.

// delimKind is what one unclosed opener is.
type delimKind int

const (
	delimParen delimKind = iota
	delimBracket
	delimBrace
	// delimTemplate is a JS/TS backtick template. It is on the same stack as
	// the brackets because `${` inside one returns to CODE, and the matching
	// `}` has to return to TEMPLATE TEXT -- so the two nest arbitrarily and a
	// single stack is the thing that gets that right.
	delimTemplate
	// delimInterp is the `${` of a template interpolation, closed by `}`.
	delimInterp
)

// openDelim is one opener the scan has seen and not yet closed.
type openDelim struct {
	kind delimKind
	ch   byte // the character as written, for the message
	line int
}

// delimiterVerdict is the outcome of one scan.
//
// INDETERMINATE IS A REAL ANSWER AND NOT A FAILURE, the same way LangUnknown is
// in langtable.go and eolMixed is in lineendings.go. A file whose block comment
// or string never terminates has structure this scan cannot read, and counting
// brackets across it would produce a confident number computed from garbage.
// Saying so is worth more than saying something.
type delimiterVerdict int

const (
	delimBalanced delimiterVerdict = iota
	delimUnbalanced
	delimIndeterminate
)

// delimiterFinding is a verdict plus the one specific thing that produced it.
type delimiterFinding struct {
	Verdict delimiterVerdict
	// Detail names the construct and the line, e.g. `unclosed "{" opened at
	// line 42`. Empty when balanced.
	Detail string
}

// scanDelimiters walks content once and reports the FIRST structural problem it
// finds, or that it found none.
//
// First, not all: a single unmatched brace usually cascades into a list of
// consequences, and the first one is the one a reader can act on.
func scanDelimiters(lang Language, content string) delimiterFinding {
	jsFamily := lang == LangTypeScript || lang == LangJavaScript
	python := lang == LangPython

	var stack []openDelim
	line := 1

	// prevCode is the last significant code byte, used only for the regex
	// heuristic below. Zero means "start of file, or start of a line, or just
	// after a construct" -- all positions where a `/` begins a regex rather
	// than a division.
	var prevCode byte

	unterminated := func(what string, at int) delimiterFinding {
		return delimiterFinding{
			Verdict: delimIndeterminate,
			Detail:  fmt.Sprintf("unterminated %s starting at line %d", what, at),
		}
	}

	i := 0
	for i < len(content) {
		c := content[i]

		// TEMPLATE TEXT. Decided by the stack rather than a separate flag, so
		// `${ {a: `x`} }` nests correctly however deep it goes.
		if len(stack) > 0 && stack[len(stack)-1].kind == delimTemplate {
			switch {
			case c == '\\' && i+1 < len(content):
				if content[i+1] == '\n' {
					line++
				}
				i += 2
			case c == '`':
				stack = stack[:len(stack)-1]
				i++
			case c == '$' && i+1 < len(content) && content[i+1] == '{':
				stack = append(stack, openDelim{kind: delimInterp, ch: '{', line: line})
				i += 2
			case c == '\n':
				line++
				i++
			default:
				i++
			}
			continue
		}

		switch {
		case c == '\n':
			line++
			i++
			prevCode = 0

		case c == ' ' || c == '\t' || c == '\r':
			i++

		// Comments. Checked before anything else that starts with the same
		// byte, so a `//` never reaches the regex heuristic.
		case !python && jsFamily && strings.HasPrefix(content[i:], "//"),
			python && c == '#':
			for i < len(content) && content[i] != '\n' {
				i++
			}

		case jsFamily && strings.HasPrefix(content[i:], "/*"):
			start := line
			end := strings.Index(content[i+2:], "*/")
			if end < 0 {
				return unterminated("block comment", start)
			}
			line += strings.Count(content[i:i+2+end+2], "\n")
			i += 2 + end + 2
			prevCode = 0

		// Python triple-quoted strings, before the single-quote case. A
		// docstring full of braces is the most common false positive there is,
		// and it is one `if` to remove it.
		case python && (strings.HasPrefix(content[i:], `"""`) || strings.HasPrefix(content[i:], "'''")):
			q := content[i : i+3]
			start := line
			end := strings.Index(content[i+3:], q)
			if end < 0 {
				return unterminated("triple-quoted string", start)
			}
			line += strings.Count(content[i:i+3+end+3], "\n")
			i += 3 + end + 3
			prevCode = 0

		case c == '"' || c == '\'':
			start := line
			next, nl, ok := skipQuoted(content, i, c)
			if !ok {
				return unterminated(fmt.Sprintf("%c-quoted string", c), start)
			}
			line += nl
			i = next
			prevCode = c

		case jsFamily && c == '`':
			stack = append(stack, openDelim{kind: delimTemplate, ch: '`', line: line})
			i++

		// A JS/TS regex literal, which can contain any bracket at all. `/` is
		// division or a regex depending on what precedes it, and this is the
		// standard expression-position heuristic. It is also exactly the kind
		// of guess that makes this tier advisory: get it wrong and the note is
		// wrong, which is survivable, where a refusal would not be.
		case jsFamily && c == '/' && startsRegex(prevCode):
			next, nl, ok := skipRegex(content, i)
			if !ok {
				// Not a regex after all (an unterminated one is far more likely
				// to be a division we misread). Treat it as ordinary code
				// rather than claiming the file is broken.
				i++
				prevCode = '/'
				break
			}
			line += nl
			i = next
			prevCode = '/'

		case c == '(' || c == '[' || c == '{':
			stack = append(stack, openDelim{kind: openerKind(c), ch: c, line: line})
			i++
			prevCode = c

		case c == ')' || c == ']' || c == '}':
			want := closerKind(c)
			if len(stack) == 0 {
				return delimiterFinding{
					Verdict: delimUnbalanced,
					Detail:  fmt.Sprintf("%q at line %d closes nothing", string(c), line),
				}
			}
			top := stack[len(stack)-1]
			// An interpolation's `}` closes the `${` and hands control back to
			// the template text, which the loop head picks up from the stack.
			if top.kind == delimInterp && c == '}' {
				stack = stack[:len(stack)-1]
				i++
				prevCode = c
				break
			}
			if top.kind != want {
				return delimiterFinding{
					Verdict: delimUnbalanced,
					Detail: fmt.Sprintf("%q at line %d does not close %q opened at line %d",
						string(c), line, string(top.ch), top.line),
				}
			}
			stack = stack[:len(stack)-1]
			i++
			prevCode = c

		default:
			i++
			prevCode = c
		}
	}

	if len(stack) > 0 {
		top := stack[len(stack)-1]
		if top.kind == delimTemplate {
			return unterminated("template literal", top.line)
		}
		return delimiterFinding{
			Verdict: delimUnbalanced,
			Detail:  fmt.Sprintf("unclosed %q opened at line %d", string(top.ch), top.line),
		}
	}
	return delimiterFinding{Verdict: delimBalanced}
}

// skipQuoted consumes a single- or double-quoted string starting at content[i]
// (which is the quote). It reports the index just past the closing quote, how
// many newlines it crossed, and whether it found a close at all.
//
// A newline ends the string in every language this tier serves -- an unescaped
// newline inside a ” or "" literal is a syntax error in Go, JS, TS and Python
// alike -- so hitting one is "unterminated", not "keep going".
func skipQuoted(content string, i int, quote byte) (next, newlines int, ok bool) {
	for j := i + 1; j < len(content); j++ {
		switch content[j] {
		case '\\':
			if j+1 < len(content) {
				if content[j+1] == '\n' {
					newlines++
				}
				j++
			}
		case quote:
			return j + 1, newlines, true
		case '\n':
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// skipRegex consumes a JS/TS regex literal starting at the `/` at content[i],
// honouring escapes and character classes (where `/` is literal).
func skipRegex(content string, i int) (next, newlines int, ok bool) {
	inClass := false
	for j := i + 1; j < len(content); j++ {
		switch content[j] {
		case '\\':
			j++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				return j + 1, newlines, true
			}
		case '\n':
			// A regex literal cannot span a line. This is a division.
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// startsRegex reports whether a `/` following prev begins a regex literal
// rather than a division.
//
// The rule is expression position: a regex can only start where a VALUE can
// start. After an identifier, a number, or a closing bracket, `/` is division.
// prevCode is zeroed at a newline and after comments and brackets-that-close-a
// -block, which is what makes the common cases land right.
func startsRegex(prev byte) bool {
	switch prev {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '~', '^', '<', '>', '%':
		return true
	}
	return false
}

func openerKind(c byte) delimKind {
	switch c {
	case '(':
		return delimParen
	case '[':
		return delimBracket
	default:
		return delimBrace
	}
}

func closerKind(c byte) delimKind {
	switch c {
	case ')':
		return delimParen
	case ']':
		return delimBracket
	default:
		return delimBrace
	}
}

// checkDelimiterTier is Tier B's entry point into the syntax gate.
//
// IT RETURNS A NOTE AND NOTHING ELSE. There is no error in the signature, which
// is the enforcement of "advisory": a caller cannot turn this into a refusal
// without changing the function's shape, and the compiler will find every place
// that has to change if anyone tries.
//
// It applies the SAME DELTA RULE Tier A does, for the same reason. A file that
// was already unbalanced and is still unbalanced after an edit tells the reader
// nothing about the edit -- and the mirrored asymmetry matters more here,
// because Tier B's false positives are exactly the files that would otherwise
// generate the same useless note on every subsequent edit forever.
func checkDelimiterTier(lang Language, relPath string, prior *string, content string) string {
	after := scanDelimiters(lang, content)

	if after.Verdict == delimBalanced {
		return fmt.Sprintf("delimiters balanced (advisory: no parser for %s)", lang)
	}

	kind := "unbalanced"
	if after.Verdict == delimIndeterminate {
		kind = "unreadable"
	}

	if prior != nil {
		if before := scanDelimiters(lang, *prior); before.Verdict != delimBalanced {
			return fmt.Sprintf("delimiters %s (%s); this file was already %s before this edit "+
				"— advisory only, %s is not parsed by this build",
				kind, after.Detail, kind, lang)
		}
	}

	return fmt.Sprintf("delimiters %s: %s — ADVISORY ONLY, the edit was applied; "+
		"%s is not parsed by this build, so this is a bracket count and not a syntax error",
		kind, after.Detail, lang)
}
