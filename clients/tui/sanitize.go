package main

import (
	"strings"
	"unicode/utf8"
)

// TERMINAL ESCAPE SANITIZATION FOR UNTRUSTED TEXT.
//
// Everything this client displays that it did not write itself -- model tokens,
// reasoning, tool-activity lines, tool-call ARGUMENTS on the approval screen,
// proposed edits and the file contents inside them, daemon error strings, and
// history hydrated from a previous session -- arrives as bytes chosen by
// something that is not the user. Written straight to a terminal those bytes
// are not text, they are commands.
//
// MEASURED, before this file existed, by rendering each payload through the
// real transcript path: \x1b[2J (clear screen), \x1b[H (home cursor), OSC 0
// (rewrite the window title), \x1b[1;1r (lock the scroll region), a bare \r
// (overwrite the line just drawn), OSC 8 (make any text a hyperlink to
// anywhere) and a BEL flood ALL reached the terminal unchanged. The approval
// panel is the one that matters most: it renders model-authored arguments in
// full, and it is the screen where the user decides whether a tool may run. A
// sequence that repaints that screen is not a display bug, it is forged
// consent.
//
// THE POLICY IS AN ALLOWLIST, AND REJECT-BY-DEFAULT. Only SGR (CSI ... m) --
// the sequences that set colour and weight -- survives, and only with
// parameters named below. Anything the parser does not positively recognize is
// removed. A deny-list was rejected outright: the set of dangerous sequences is
// open-ended and grows with every terminal emulator, and one missed entry is a
// hole. The set of sequences we WANT is small, closed, and enumerable.
//
// Colour is allowlisted rather than dropped because a coding assistant emits
// syntax-highlighted code, and that highlighting is SGR. Stripping all escapes
// would have made every code block monochrome; escaping them visibly (^[) would
// have corrupted every code block with literal garbage. See TestHighlightedCode
// RendersByteIdentically, which pins that a real highlighted block is passed
// through unchanged.
//
// DENIED EVEN INSIDE SGR: 5 and 6 (blink), and 8 (conceal). Conceal is a
// spoofing primitive -- it makes text present in the buffer, and in anything
// copied out of it, while invisible on screen. On an approval panel that is
// precisely the attack.

// Sequence-length ceilings. A pending escape is held across chunk boundaries
// (see escSanitizer.Write), so without a ceiling an unterminated sequence is an
// unbounded memory sink fed by the far end. On overflow the parser DROPS what
// it held and resumes reading text at the offending byte rather than staying in
// the sequence: the alternative -- keep consuming -- lets one unterminated OSC
// swallow the entire rest of the conversation, which is its own denial of
// service. Leaked parameter bytes render as harmless literal text; no ESC can
// survive, because a new ESC re-enters this parser.
const (
	sanMaxCSI = 64  // ESC [ ... final; a truecolour SGR pair is about 35 bytes
	sanMaxStr = 256 // OSC/DCS/APC/PM/SOS payload, which is never displayed
)

type sanState uint8

const (
	sanText   sanState = iota // ordinary text
	sanEsc                    // seen ESC, waiting to learn what kind
	sanCSI                    // seen ESC [, accumulating parameters
	sanStr                    // inside OSC/DCS/APC/PM/SOS, discarding
	sanStrEsc                 // seen ESC inside such a string; maybe ST
	sanUTF8                   // holding a multi-byte rune split by a chunk edge
)

// escSanitizer filters untrusted text. It is STATEFUL ACROSS CHUNKS, and that
// is the whole reason it is a type rather than a function.
//
// A stateless per-chunk filter is the classic bypass: the far end sends "\x1b["
// in one token and "2J" in the next, neither chunk contains a complete
// sequence, each passes the filter untouched, and the terminal -- which does
// not care where our message boundaries were -- reassembles and executes it.
// Tokens arrive one JSON message at a time (stream.go), so the attacker picks
// the split points for free.
//
// So an incomplete tail is HELD here, emitting nothing, and resolved when the
// next chunk arrives. Flush releases whatever is still held, as literal-safe
// text, when the stream ends.
//
// It is a value type with no reference fields, so copying a chatModel (Bubble
// Tea passes the model by value through every Update) copies the parser state
// rather than sharing it.
// A FIXED ARRAY, NOT A SLICE OR A STRING. An array is copied by value with
// the model, so no two copies of a chatModel can ever share held bytes; and
// appending a byte to it does not allocate, which the streaming path does once
// per byte of every escape sequence.
type escSanitizer struct {
	state sanState
	pend  [sanMaxCSI]byte // raw bytes of the sequence in progress
	pendN int             // how many of them are live
	strN  int             // bytes seen in a string payload; counted, never retained
}

// hold appends one byte to the pending sequence, reporting false when the
// ceiling is reached and the caller must abandon it.
func (z *escSanitizer) hold(c byte) bool {
	if z.pendN >= len(z.pend) {
		return false
	}
	z.pend[z.pendN] = c
	z.pendN++
	return true
}

// Write filters one chunk, returning the text that is safe to display now.
// Bytes belonging to an unfinished sequence are held, not returned.
func (z *escSanitizer) Write(s string) string {
	// A rune split across chunks is resolved by putting it back in front of the
	// new chunk. At most three bytes, so this is not a copy worth avoiding.
	if z.state == sanUTF8 {
		s = string(z.pend[:z.pendN]) + s
		z.state, z.pendN = sanText, 0
	}
	// The common case is a token with nothing to sanitize, on the hot streaming
	// path, so it returns the input unchanged and allocates nothing.
	if z.state == sanText && z.pendN == 0 && !sanNeedsWork(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch z.state {

		case sanText:
			switch {
			case c == 0x1b:
				z.state, z.pendN = sanEsc, 0
				z.hold(0x1b)
				i++
			case c < utf8.RuneSelf:
				// C0 controls are dropped except the two that are layout:
				// newline and tab. A bare \r is dropped rather than converted,
				// because its only effect here is to overwrite the line the
				// user just read. DEL goes too.
				if c == '\n' || c == '\t' || (c >= 0x20 && c != 0x7f) {
					b.WriteByte(c)
				}
				i++
			default:
				r, size := utf8.DecodeRuneInString(s[i:])
				if r == utf8.RuneError && size == 1 {
					// Either a genuinely invalid byte, or a valid rune the
					// chunk boundary cut in half. Holding the second case is
					// what keeps chunk-invariance true for non-ASCII text.
					if n := sanTruncatedRune(s[i:]); n > 0 {
						z.state, z.pendN = sanUTF8, 0
						for k := 0; k < n; k++ {
							z.hold(s[i+k])
						}
						i = len(s)
						continue
					}
					b.WriteRune(utf8.RuneError)
					i++
					continue
				}
				// C1 controls (U+0080-U+009F) are single-rune equivalents of
				// the ESC forms -- U+009B IS a CSI introducer on a terminal in
				// 8-bit mode. Dropped as runes, never as bytes: 0x80-0x9F are
				// also ordinary UTF-8 continuation bytes, and dropping those
				// would corrupt every non-ASCII character.
				if r >= 0x80 && r <= 0x9f {
					i += size
					continue
				}
				b.WriteString(s[i : i+size])
				i += size
			}

		case sanEsc:
			switch c {
			case '[':
				z.hold('[')
				z.state = sanCSI
				i++
			case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS
				z.state, z.pendN, z.strN = sanStr, 0, 0
				i++
			case 0x1b:
				i++ // a second ESC restarts the sequence
			default:
				// Two-byte escapes: ESC c (full reset), ESC 7/8 (save and
				// restore cursor), ESC N/O (single shifts), and the rest.
				// None are display, so the pair is dropped whole.
				z.state, z.pendN = sanText, 0
				i++
			}

		case sanCSI:
			switch {
			case (c >= 0x30 && c <= 0x3f) || (c >= 0x20 && c <= 0x2f):
				if !z.hold(c) {
					z.state, z.pendN = sanText, 0
					continue // reprocess this byte as text; see the ceilings above
				}
				i++
			case c >= 0x40 && c <= 0x7e: // final byte: the sequence ends here
				body := z.pend[2:z.pendN]
				if c == 'm' && sanIsParams(body) && sanSGRAllowed(body) {
					b.Write(z.pend[:z.pendN])
					b.WriteByte(c)
				}
				z.state, z.pendN = sanText, 0
				i++
			default:
				// Malformed (a control byte inside the sequence). Abandon it
				// and read this byte as text.
				z.state, z.pendN = sanText, 0
			}

		case sanStr:
			switch {
			case c == 0x07: // BEL terminates an OSC
				z.state, z.strN = sanText, 0
				i++
			case c == 0x1b:
				z.state = sanStrEsc
				i++
			default:
				// COUNTED, NOT KEPT. The payload is never displayed, so there
				// is no reason to hold attacker bytes in memory at all.
				z.strN++
				if z.strN > sanMaxStr {
					z.state, z.strN = sanText, 0
					continue
				}
				i++
			}

		case sanStrEsc:
			if c == '\\' { // ST
				z.state, z.strN = sanText, 0
				i++
				continue
			}
			z.state = sanStr // not a terminator; still inside the string

		default:
			z.state = sanText
		}
	}
	return b.String()
}

// Flush releases whatever is still held when the stream ends, so a truncated
// answer does not silently lose its last characters. A sequence that never
// completed was never an allowed SGR, so what comes back is its printable
// remainder with the ESC removed -- literal text, not a command. String
// payloads (OSC and friends) are never displayed and so return nothing.
func (z *escSanitizer) Flush() string {
	state, n := z.state, z.pendN
	pend := z.pend
	z.state, z.pendN, z.strN = sanText, 0, 0
	switch state {
	case sanUTF8:
		// One replacement per held byte, which is exactly what the text path
		// does for an invalid byte -- so a one-shot sanitize of the whole
		// input and a streamed one still agree.
		return strings.Repeat(string(utf8.RuneError), n)
	case sanEsc, sanCSI:
		return sanLiteral(pend[:n])
	default:
		return ""
	}
}

// sanitizeText filters a COMPLETE, already-bounded value: a tool argument, an
// error string, a proposed edit, a line of history. Defined in terms of the
// streaming parser rather than beside it, so the two can never disagree --
// chunk-invariance is then true by construction, and the fuzz test says so.
func sanitizeText(s string) string {
	var z escSanitizer
	out := z.Write(s)
	return out + z.Flush()
}

// sanNeedsWork reports whether s contains anything the parser must act on. It
// exists for the streaming path, where it runs once per token: a chunk of
// ordinary text returns false and is passed through with no allocation.
func sanNeedsWork(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\n' || c == '\t':
		case c < 0x20 || c == 0x7f:
			return true
		case c == 0xc2:
			// Lead byte of U+0080-U+00FF: might be a C1 control, might be an
			// ordinary character like ¢. Conservative -- let the parser decide.
			return true
		}
	}
	// Invalid or truncated UTF-8 needs the slow path to replace or hold it.
	return !utf8.ValidString(s)
}

// sanTruncatedRune returns the length of a trailing sequence that is a valid
// PREFIX of a multi-byte rune, or 0. Distinguishing "cut in half by the chunk
// boundary" from "genuinely invalid" is what lets Write hold the former.
func sanTruncatedRune(s string) int {
	if len(s) == 0 || len(s) >= utf8.UTFMax {
		return 0
	}
	var need int
	switch c := s[0]; {
	case c&0xe0 == 0xc0:
		need = 2
	case c&0xf0 == 0xe0:
		need = 3
	case c&0xf8 == 0xf0:
		need = 4
	default:
		return 0
	}
	if len(s) >= need {
		return 0 // complete, or invalid for some other reason
	}
	for i := 1; i < len(s); i++ {
		if s[i]&0xc0 != 0x80 {
			return 0
		}
	}
	return len(s)
}

// sanLiteral strips every control rune, leaving text that is safe to print and
// that re-sanitizes to itself.
func sanLiteral(b0 []byte) string {
	var b strings.Builder
	b.Grow(len(b0))
	for _, r := range string(b0) {
		if r == 0x1b || r == 0x7f || (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sanIsParams reports whether body consists only of numeric parameters and
// separators. It rejects the private-marker bytes (< = > ?), the colon
// sub-parameter form, and the intermediate bytes -- none of which appear in
// the SGR forms this allowlist names, so none are recognized.
func sanIsParams(body []byte) bool {
	for _, c := range body {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

// sanSGRAllowed reports whether every parameter in an SGR body is allowed.
//
// ALL-OR-NOTHING, deliberately: a sequence carrying one disallowed parameter is
// dropped entire rather than rewritten to keep the allowed ones. Reject-by-
// default means the question is "is this whole sequence one we recognize", and
// a filter that edits parameters mid-sequence is a far easier thing to get
// subtly wrong than one that says no.
func sanSGRAllowed(body []byte) bool {
	if len(body) == 0 {
		return true // ESC[m is ESC[0m, a plain reset
	}
	// PARSED IN PLACE, into a fixed array. This runs once per escape sequence
	// on the streaming path, and splitting on ";" allocated a slice of strings
	// per token of syntax-highlighted output.
	var p [24]int
	n, val, digits := 0, 0, 0
	for i := 0; i <= len(body); i++ {
		if i == len(body) || body[i] == ';' {
			if digits > 3 || n == len(p) {
				return false
			}
			p[n] = val // an empty parameter is zero, which is what a terminal does
			n++
			val, digits = 0, 0
			continue
		}
		c := body[i]
		if c < '0' || c > '9' {
			return false
		}
		val = val*10 + int(c-'0')
		digits++
	}
	for i := 0; i < n; i++ {
		switch v := p[i]; {
		case v == 0, v == 1, v == 2, v == 3, v == 4, v == 7, v == 9, // set
			v == 22, v == 23, v == 24, v == 27, v == 29, // their resets
			v >= 30 && v <= 37, v == 39, // foreground, default
			v >= 40 && v <= 47, v == 49, // background, default
			v >= 90 && v <= 97, v >= 100 && v <= 107: // bright
			// allowed on its own

		case v == 38 || v == 48: // extended colour, and only in two forms
			if i+1 >= n {
				return false
			}
			switch p[i+1] {
			case 5: // 256-colour: 38;5;N
				if i+2 >= n || p[i+2] > 255 {
					return false
				}
				i += 2
			case 2: // truecolour: 38;2;R;G;B
				if i+4 >= n {
					return false
				}
				for k := i + 2; k <= i+4; k++ {
					if p[k] > 255 {
						return false
					}
				}
				i += 4
			default:
				return false
			}

		default:
			// Everything unnamed, which includes 5 and 6 (blink) and 8
			// (conceal) -- see the note at the top of this file.
			return false
		}
	}
	return true
}
