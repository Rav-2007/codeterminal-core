package editapply

import "strings"

// eolStyle names the line-ending convention a piece of text uses.
//
// This exists because the matcher and the writer need OPPOSITE things from a
// line ending. match.go's normalizeEOL collapses CRLF and CR to LF so the
// SEARCH text can be FOUND in a file that writes its lines differently; the
// functions here decide what the replacement should look like when it is
// WRITTEN back. They will look like duplicates to a future reader and must not
// be merged: normalizeEOL's tolerance is deliberately confined to the search
// half, for the reason findSearch's own header records, and the moment a
// normalized rendering reaches the write path the file's endings are rewritten
// wholesale -- which is the exact failure
// TestMatchTier_LineEndingsPreservedOnWrite was added to catch.
type eolStyle int

const (
	// eolNone means the text contains no line break at all, so it has no
	// opinion about the convention. Distinct from eolMixed: "no opinion" defers
	// to the surrounding file, "two opinions" defers to nobody.
	eolNone eolStyle = iota
	eolLF            // "\n"
	eolCRLF          // "\r\n"
	// eolCR is the classic-Mac lone carriage return. Rare to the point of
	// extinct, but normalizeEOL matches it, so a file written that way can
	// reach the splice and must be re-encoded to its own convention rather
	// than silently promoted to LF.
	eolCR
	// eolMixed means more than one convention is present. There is nothing to
	// preserve, so nothing is imposed.
	eolMixed
)

func (s eolStyle) String() string {
	switch s {
	case eolLF:
		return "LF"
	case eolCRLF:
		return "CRLF"
	case eolCR:
		return "CR"
	case eolMixed:
		return "mixed"
	}
	return "none"
}

// separator returns the bytes this style writes for a line break, and false for
// the two styles that name no single convention.
func (s eolStyle) separator() (string, bool) {
	switch s {
	case eolLF:
		return "\n", true
	case eolCRLF:
		return "\r\n", true
	case eolCR:
		return "\r", true
	}
	return "", false
}

// dominantEOL reports the one convention s uses, eolMixed if it uses more than
// one, or eolNone if it has no line break at all. It stops at the first
// disagreement -- "dominant" is not a majority vote, because a file that is 90%
// CRLF and 10% LF is a file with no convention to preserve, and guessing at one
// would rewrite bytes the user never asked about.
func dominantEOL(s string) eolStyle {
	found := eolNone
	for i := 0; i < len(s); i++ {
		var style eolStyle
		switch {
		case s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n':
			style = eolCRLF
			i++
		case s[i] == '\r':
			style = eolCR
		case s[i] == '\n':
			style = eolLF
		default:
			continue
		}
		if found == eolNone {
			found = style
			continue
		}
		if found != style {
			return eolMixed
		}
	}
	return found
}

// reencodeEOL rewrites every line break in s as style. A style that names no
// single convention (eolNone, eolMixed) returns s untouched.
func reencodeEOL(s string, style eolStyle) string {
	sep, ok := style.separator()
	if !ok {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + strings.Count(s, "\n"))
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n':
			b.WriteString(sep)
			i++
		case s[i] == '\r', s[i] == '\n':
			b.WriteString(sep)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// conformReplacementEOL re-encodes a replacement's line breaks to the
// convention of the text it is about to displace, and returns the note to
// surface when it did.
//
// THE INVARIANT: an edit never changes the line-ending convention of the file
// it writes into.
//
// Without this, the splice writes the replacement's own endings verbatim, so a
// CRLF file patched with an LF replacement comes back MIXED -- lines 3-5 LF and
// the rest CRLF. That is not a corner case: git for Windows defaults to
// core.autocrlf=true, under which `git diff` emits LF while the file on disk
// stays CRLF, so the ordinary Windows workflow produces exactly the patch that
// mixes the file. The edit itself is correct and was always disclosed, but git
// then reports every line of the file as modified and the next reviewer's diff
// is unreadable.
//
// This is deliberately NOT gated on the match tier. A CRLF replacement spliced
// into an LF file can match at MatchExact -- the tier never rises, and a tier
// gate would sail straight past it. The invariant is about what gets WRITTEN,
// not about what the matcher had to forgive to find it.
//
// region is the span being replaced and outranks the file: it is the text
// actually being displaced, so if it disagrees with the rest of the file it
// still wins locally. A single-line region carries no line break of its own,
// has no opinion, and defers to the file.
//
// Three no-ops, each of which matters:
//
//   - a replacement with no line break has nothing to re-encode (this is what
//     keeps an ordinary same-line edit byte-identical to what the caller sent);
//   - a genuinely mixed file has no convention to preserve, and inventing one
//     would be a bigger change than the bug;
//   - a replacement that already agrees is returned untouched, with no note,
//     so nothing announces a normalization that did not happen.
//
// The escape hatch for a user who genuinely wants to CONVERT a file's line
// endings is the create path (an empty SEARCH section), which writes REPLACE as
// the whole file verbatim and never reaches this function. See prepareCreate.
func conformReplacementEOL(replace, region, file string) (conformed, note string) {
	have := dominantEOL(replace)
	if have == eolNone {
		return replace, ""
	}

	want := dominantEOL(region)
	if want == eolNone {
		want = dominantEOL(file)
	}
	if _, ok := want.separator(); !ok {
		return replace, ""
	}
	if have == want {
		return replace, ""
	}

	return reencodeEOL(replace, want), "replacement re-encoded to " + want.String() + " to match the file"
}
