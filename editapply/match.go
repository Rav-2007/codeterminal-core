package editapply

import (
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// MatchTier names how much tolerance was needed to locate a SEARCH block in
// the file. Matching used to be byte-exact and nothing else, which hard-refused
// an edit whenever the model's SEARCH text differed from the file in a way that
// is invisible or semantically irrelevant: CRLF line endings (every edit in a
// Windows-authored repo), trailing whitespace, tabs vs spaces (the most common
// whitespace mistake an LLM makes), smart quotes, and unicode differences where
// the SEARCH text is VISUALLY IDENTICAL to what is in the file. "search text
// not found" for text the user can see is right there is what makes a tool feel
// broken.
//
// The tiers are cumulative — each applies its own normalization plus every
// looser one above it — and are tried in order, so a file that needs CRLF,
// trailing-whitespace AND indentation tolerance all at once matches at
// MatchIndentation.
type MatchTier int

const (
	// MatchExact is byte-for-byte. Always tried first, always preferred:
	// tolerance is a fallback, never a preference.
	MatchExact MatchTier = iota
	// MatchLineEndings treats CRLF, CR and LF as the same line break.
	MatchLineEndings
	// MatchTrailingSpace ignores spaces and tabs at the end of a line.
	MatchTrailingSpace
	// MatchIndentation ignores spaces and tabs at the start of a line, which is
	// what makes tabs-vs-spaces and 2-vs-4-space indentation match.
	MatchIndentation
	// MatchUnicode folds the invisible and lookalike differences: NFC/NFD
	// normalization, zero-width characters, byte-order marks, non-breaking
	// spaces, and smart quotes/dashes.
	MatchUnicode
)

// matchTiers is the ladder in the order it is attempted.
var matchTiers = []MatchTier{MatchExact, MatchLineEndings, MatchTrailingSpace, MatchIndentation, MatchUnicode}

func (t MatchTier) String() string {
	switch t {
	case MatchExact:
		return "exact"
	case MatchLineEndings:
		return "line-endings"
	case MatchTrailingSpace:
		return "trailing-space"
	case MatchIndentation:
		return "indentation"
	case MatchUnicode:
		return "unicode"
	}
	return fmt.Sprintf("tier(%d)", int(t))
}

// Note returns the human-readable explanation surfaced to the user when a match
// needed tolerance, and "" for an exact match — the ordinary case must not
// start announcing a normalization that did not happen.
func (t MatchTier) Note() string {
	switch t {
	case MatchLineEndings:
		return "matched after normalizing line endings (CRLF/CR vs LF)"
	case MatchTrailingSpace:
		return "matched after normalizing line endings and trailing whitespace"
	case MatchIndentation:
		return "matched after normalizing line endings, trailing whitespace and leading indentation (tabs vs spaces)"
	case MatchUnicode:
		return "matched after normalizing whitespace and unicode (NFC, zero-width characters, smart quotes)"
	}
	return ""
}

// mapped is a normalized rendering of some source text together with the map
// back to it. off has len(text)+1 entries: off[i] is the byte index in the
// source where the i'th normalized byte begins, and off[len(text)] is len(src).
//
// Using "where the next kept byte begins" as an exclusive end is what makes
// dropped characters fall on the correct side of a match boundary. When
// trailing-whitespace normalization deletes the tab in "beta\t", the match for
// "beta" ends at the offset of the following newline, so the replacement
// consumes the tab instead of stranding it after the new text.
type mapped struct {
	text string
	off  []int
}

// identity is the tier-0 rendering: the source unchanged, mapping to itself.
func identity(src string) mapped {
	off := make([]int, len(src)+1)
	for i := range off {
		off[i] = i
	}
	return mapped{text: src, off: off}
}

// builder accumulates a normalized rendering while recording, for every byte it
// keeps, the source offset it came from.
type builder struct {
	buf strings.Builder
	off []int
}

// keep appends s as output produced by the source bytes starting at srcIdx.
func (b *builder) keep(s string, srcIdx int) {
	for range len(s) {
		b.off = append(b.off, srcIdx)
	}
	b.buf.WriteString(s)
}

func (b *builder) done(srcLen int) mapped {
	return mapped{text: b.buf.String(), off: append(b.off, srcLen)}
}

// normalizeEOL collapses CRLF and lone CR to LF. A CRLF pair maps to the index
// of its "\r", so a match ending at that line break consumes both bytes.
func normalizeEOL(m mapped) mapped {
	var b builder
	for i := 0; i < len(m.text); i++ {
		switch {
		case m.text[i] == '\r' && i+1 < len(m.text) && m.text[i+1] == '\n':
			b.keep("\n", m.off[i])
			i++
		case m.text[i] == '\r':
			b.keep("\n", m.off[i])
		default:
			b.keep(m.text[i:i+1], m.off[i])
		}
	}
	return b.done(m.off[len(m.text)])
}

// normalizeTrailingSpace drops spaces and tabs that sit immediately before a
// line break or the end of the text.
func normalizeTrailingSpace(m mapped) mapped {
	var b builder
	for i := 0; i < len(m.text); i++ {
		if c := m.text[i]; c == ' ' || c == '\t' {
			j := i
			for j < len(m.text) && (m.text[j] == ' ' || m.text[j] == '\t') {
				j++
			}
			if j == len(m.text) || m.text[j] == '\n' {
				i = j - 1 // whole run is trailing: drop it
				continue
			}
		}
		b.keep(m.text[i:i+1], m.off[i])
	}
	return b.done(m.off[len(m.text)])
}

// normalizeIndentation drops spaces and tabs at the start of every line. This
// is deliberately "strip", not "expand tabs to N spaces": there is no single
// correct tab width, and stripping is what makes tab-vs-space and 2-vs-4-space
// indentation compare equal. It is the loosest whitespace tier, which is why it
// sits below the other two and why the uniqueness check matters — a SEARCH that
// becomes ambiguous once indentation is ignored is refused, not guessed at.
func normalizeIndentation(m mapped) mapped {
	var b builder
	atLineStart := true
	for i := 0; i < len(m.text); i++ {
		c := m.text[i]
		if atLineStart && (c == ' ' || c == '\t') {
			continue
		}
		atLineStart = c == '\n'
		b.keep(m.text[i:i+1], m.off[i])
	}
	return b.done(m.off[len(m.text)])
}

// invisibleRunes are dropped outright: they carry no meaning for matching and
// are exactly the characters that make a SEARCH block look identical to the
// file while comparing unequal.
var invisibleRunes = map[rune]bool{
	'\u200b': true, // zero-width space
	'\u200c': true, // zero-width non-joiner
	'\u200d': true, // zero-width joiner
	'\ufeff': true, // byte-order mark / zero-width no-break space
	'\u2060': true, // word joiner
}

// lookalikeRunes fold typographic characters to the ASCII a model most often
// writes instead. Smart quotes are the common case (an editor or a docs
// pipeline "helpfully" curls them); dashes and the ellipsis come along for the
// same reason.
var lookalikeRunes = map[rune]string{
	'\u2018': "'", '\u2019': "'", '\u201a': "'", '\u201b': "'",
	'\u201c': `"`, '\u201d': `"`, '\u201e': `"`, '\u201f': `"`,
	'\u2013': "-", '\u2014': "-", '\u2212': "-",
	'\u00a0': " ", '\u202f': " ", '\u2007': " ", // non-breaking spaces
	'\u2026': "...",
}

// normalizeUnicode folds the invisible and lookalike differences, then applies
// NFC so decomposed sequences (e + combining acute) compare equal to their
// composed form (e-acute). NFC runs segment by segment over the SOURCE so every
// output byte can still be traced back to the source offset its segment began
// at.
func normalizeUnicode(m mapped) mapped {
	var folded builder
	for i, r := range m.text {
		if invisibleRunes[r] {
			continue
		}
		if repl, ok := lookalikeRunes[r]; ok {
			folded.keep(repl, m.off[i])
			continue
		}
		folded.keep(string(r), m.off[i])
	}
	f := folded.done(m.off[len(m.text)])

	var b builder
	for i := 0; i < len(f.text); {
		n := norm.NFC.NextBoundaryInString(f.text[i:], true)
		if n <= 0 {
			n = len(f.text) - i
		}
		b.keep(norm.NFC.String(f.text[i:i+n]), f.off[i])
		i += n
	}
	return b.done(f.off[len(f.text)])
}

// normalizeTo renders src at the given tier, applying every normalization from
// MatchExact down to tier inclusive.
func normalizeTo(src string, tier MatchTier) mapped {
	m := identity(src)
	if tier >= MatchLineEndings {
		m = normalizeEOL(m)
	}
	if tier >= MatchTrailingSpace {
		m = normalizeTrailingSpace(m)
	}
	if tier >= MatchIndentation {
		m = normalizeIndentation(m)
	}
	if tier >= MatchUnicode {
		m = normalizeUnicode(m)
	}
	return m
}

// searchMatch is where a SEARCH block was found in the original file content,
// as a byte range into that original, plus how much tolerance it took.
type searchMatch struct {
	Start int // byte offset into the original content
	End   int // exclusive
	Tier  MatchTier
}

// errAmbiguousMatch formats the refusal used when a SEARCH block matches in
// more than one place. Shared by every tier so the message a user sees does not
// depend on which normalization happened to find the duplicates.
func errAmbiguousMatch(count int, relPath string, tier MatchTier) error {
	if tier == MatchExact {
		return fmt.Errorf("search text found %d times in %s; ambiguous, refusing", count, relPath)
	}
	return fmt.Errorf("search text found %d times in %s once %s differences are ignored; ambiguous, refusing", count, relPath, tier)
}

// findSearch locates search within original, walking the tolerance ladder and
// stopping at the first tier that finds anything.
//
// Two properties hold at every tier, and they are what keep the tolerance
// honest:
//
//   - More than one match is a refusal, never a silent pick. A looser tier can
//     make two previously-distinct passages compare equal; that is exactly when
//     the tool must stop and ask rather than choose.
//   - The byte range returned is verified: the original's own bytes over that
//     range are re-normalized and required to equal the normalized search text.
//     However the offset map behaves, a range that does not round-trip is not
//     returned, so a mis-mapped boundary becomes a refusal rather than a
//     wrongly-spliced file.
//
// The range is into the ORIGINAL bytes, so splicing the replacement into it
// leaves everything outside the match byte-identical — a CRLF file matched at
// the line-ending tier keeps every CRLF it did not replace.
func findSearch(original, search, relPath string) (searchMatch, error) {
	if search == "" {
		return searchMatch{}, fmt.Errorf("search text is empty for %s", relPath)
	}

	for _, tier := range matchTiers {
		haystack := normalizeTo(original, tier)
		needle := normalizeTo(search, tier).text
		if needle == "" {
			continue // normalized away entirely (e.g. whitespace-only SEARCH)
		}

		count := strings.Count(haystack.text, needle)
		if count == 0 {
			continue
		}
		if count > 1 {
			return searchMatch{}, errAmbiguousMatch(count, relPath, tier)
		}

		idx := strings.Index(haystack.text, needle)
		start, end := haystack.off[idx], haystack.off[idx+len(needle)]
		if start < 0 || end > len(original) || start > end {
			continue
		}
		if normalizeTo(original[start:end], tier).text != needle {
			continue // offset map did not round-trip; refuse this tier
		}
		return searchMatch{Start: start, End: end, Tier: tier}, nil
	}

	return searchMatch{}, fmt.Errorf("search text not found in %s", relPath)
}
