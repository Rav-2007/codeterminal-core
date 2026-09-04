package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// THE CORPUS. Every entry marked (measured) was reproduced reaching a real
// terminal through the real render path before this filter existed. Keep them
// here rather than in prose: a policy that is only written down is one that
// drifts.
var sanCorpus = []struct {
	name string
	in   string
	want string
}{
	{"clear screen (measured)", "before\x1b[2Jafter", "beforeafter"},
	{"cursor home (measured)", "a\x1b[Hb", "ab"},
	{"OSC 0 window title (measured)", "x\x1b]0;PWNED\x07y", "xy"},
	{"scroll region DECSTBM (measured)", "a\x1b[1;1rb", "ab"},
	{"bare CR overwrite (measured)", "harmless\rEVIL", "harmlessEVIL"},
	{"OSC 8 hyperlink (measured)", "\x1b]8;;http://evil.example\x07click\x1b]8;;\x07", "click"},
	{"BEL flood (measured)", "ding\a\a\a", "ding"},

	{"OSC 52 clipboard write", "a\x1b]52;c;ZXZpbA==\x07b", "ab"},
	{"DSR cursor query", "a\x1b[6nb", "ab"},
	{"DA device attributes query", "a\x1b[cb", "ab"},
	{"DECSET alt-screen toggle", "a\x1b[?1049hb", "ab"},
	{"DECRST alt-screen off", "a\x1b[?1049lb", "ab"},
	{"SGR 8 conceal", "a\x1b[8mhidden\x1b[0mb", "ahidden\x1b[0mb"},
	{"SGR 5 blink", "a\x1b[5mb", "ab"},
	{"SGR 6 blink", "a\x1b[6mb", "ab"},
	{"bracketed paste terminator", "a\x1b[201~b", "ab"},
	{"bracketed paste start", "a\x1b[200~b", "ab"},
	{"DCS payload", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
	{"APC payload", "a\x1b_Gf=100\x1b\\b", "ab"},
	// U+009B IS a CSI introducer on a terminal in 8-bit mode, and U+009D an
	// OSC one -- filtering only ESC leaves both as complete escape hatches.
	{"C1 CSI introducer U+009B", "a\u009b2Jb", "a2Jb"},
	{"C1 OSC introducer U+009D", "a\u009d0;title\ab", "a0;titleb"},
	{"ESC c full reset", "a\x1bcb", "ab"},
	{"DEL byte", "a\x7fb", "ab"},
	{"NUL byte", "a\x00b", "ab"},
	{"vertical tab", "a\vb", "ab"},

	// MUST SURVIVE. If any of these change, the allowlist is wrong.
	{"SGR reset", "\x1b[0mplain", "\x1b[0mplain"},
	{"SGR bold", "\x1b[1mbold\x1b[22m", "\x1b[1mbold\x1b[22m"},
	{"SGR empty params is reset", "\x1b[mx", "\x1b[mx"},
	{"SGR basic colour", "\x1b[31mred\x1b[39m", "\x1b[31mred\x1b[39m"},
	{"SGR bright colour", "\x1b[91mred\x1b[39m", "\x1b[91mred\x1b[39m"},
	{"SGR background", "\x1b[41m \x1b[49m", "\x1b[41m \x1b[49m"},
	{"SGR 256 colour", "\x1b[38;5;204mfunc\x1b[0m", "\x1b[38;5;204mfunc\x1b[0m"},
	{"SGR truecolour", "\x1b[38;2;255;128;0mwarn\x1b[0m", "\x1b[38;2;255;128;0mwarn\x1b[0m"},
	{"SGR truecolour background", "\x1b[48;2;0;0;0mbg\x1b[49m", "\x1b[48;2;0;0;0mbg\x1b[49m"},
	{"SGR compound", "\x1b[1;4;38;5;42mx\x1b[0m", "\x1b[1;4;38;5;42mx\x1b[0m"},
	{"newline and tab survive", "a\n\tb", "a\n\tb"},
	{"unicode survives", "héllo → 世界 \U0001f389", "héllo → 世界 \U0001f389"},

	// Mixed allowed and denied parameters: the whole sequence goes.
	{"allowed plus blink is rejected whole", "\x1b[1;5mx", "x"},
	{"allowed plus conceal is rejected whole", "\x1b[31;8mx", "x"},
	{"colon sub-parameter form is not recognized", "\x1b[38:5:204mx", "x"},
	{"out-of-range colour component", "\x1b[38;2;300;0;0mx", "x"},
	{"truncated extended colour", "\x1b[38;5mx", "x"},
}

func TestSanitizeCorpus(t *testing.T) {
	for _, tc := range sanCorpus {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeText(tc.in); got != tc.want {
				t.Errorf("sanitizeText(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// 2.1g: a real highlighted code block must pass through untouched, or the
// allowlist is too narrow and syntax highlighting is collateral damage.
func TestHighlightedCodeRendersByteIdentically(t *testing.T) {
	block := "\x1b[38;5;204mfunc\x1b[0m \x1b[38;5;81mmain\x1b[0m() {\n" +
		"\t\x1b[38;2;106;153;85m// a comment\x1b[0m\n" +
		"\t\x1b[1mprintln\x1b[22m(\x1b[38;5;186m\"hi\"\x1b[0m)\n}"
	if got := sanitizeText(block); got != block {
		t.Fatalf("highlighted code was altered.\n got %q\nwant %q", got, block)
	}
	// And through the transcript renderer the client actually uses.
	before := renderTranscript([]turn{{role: roleAssistant, text: block}}, 80)
	after := renderTranscript([]turn{{role: roleAssistant, text: sanitizeText(block)}}, 80)
	if before != after {
		t.Fatalf("render differs after sanitization:\n got %q\nwant %q", after, before)
	}
}

func TestSanitizeIsIdempotent(t *testing.T) {
	for _, tc := range sanCorpus {
		once := sanitizeText(tc.in)
		if twice := sanitizeText(once); twice != once {
			t.Errorf("%s: not idempotent\n once %q\ntwice %q", tc.name, once, twice)
		}
	}
}

// THE BYPASS THIS TYPE EXISTS FOR. A sequence split across two chunks must not
// pass through: the terminal reassembles what our message boundaries separated.
func TestSplitSequenceIsNotABypass(t *testing.T) {
	var z escSanitizer
	got := z.Write("\x1b[") + z.Write("2J") + z.Write("visible") + z.Flush()
	if strings.Contains(got, "\x1b") {
		t.Fatalf("a split escape survived: %q", got)
	}
	if got != "visible" {
		t.Fatalf("got %q, want %q", got, "visible")
	}
}

// Streaming any split of an input must equal sanitizing it whole.
func chunkInvariant(t *testing.T, in string, cuts []int) {
	t.Helper()
	var z escSanitizer
	var b strings.Builder
	prev := 0
	for _, c := range cuts {
		if c < prev || c > len(in) {
			continue
		}
		b.WriteString(z.Write(in[prev:c]))
		prev = c
	}
	b.WriteString(z.Write(in[prev:]))
	b.WriteString(z.Flush())
	if got, want := b.String(), sanitizeText(in); got != want {
		t.Fatalf("chunk-invariance broken for %q at cuts %v\n got %q\nwant %q", in, cuts, got, want)
	}
}

func TestChunkInvarianceEveryCutPoint(t *testing.T) {
	for _, tc := range sanCorpus {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i <= len(tc.in); i++ {
				chunkInvariant(t, tc.in, []int{i})
			}
			// byte-at-a-time, the most hostile split available
			all := make([]int, 0, len(tc.in)+1)
			for i := 0; i <= len(tc.in); i++ {
				all = append(all, i)
			}
			chunkInvariant(t, tc.in, all)
		})
	}
}

// sanEscapeViolation describes the first ESC in s that is not part of an
// allowed SGR sequence, or returns "". This is the no-escape guarantee,
// checked independently of the parser that produced s.
func sanEscapeViolation(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		if i+1 >= len(s) || s[i+1] != '[' {
			return "ESC not followed by ["
		}
		j := i + 2
		for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == ';') {
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			return "CSI sequence is not SGR"
		}
		if !sanSGRAllowed([]byte(s[i+2 : j])) {
			return "SGR parameters outside the allowlist: " + s[i+2:j]
		}
		i = j
	}
	return ""
}

func TestNoEscapeSurvivesCorpus(t *testing.T) {
	for _, tc := range sanCorpus {
		if v := sanEscapeViolation(sanitizeText(tc.in)); v != "" {
			t.Errorf("%s: %s", tc.name, v)
		}
	}
}

// 2.1e width sanity: the display width of sanitized output is computable and
// stable under re-sanitization.
func TestSanitizedWidthIsStable(t *testing.T) {
	for _, tc := range sanCorpus {
		once := sanitizeText(tc.in)
		if w1, w2 := ansi.StringWidth(once), ansi.StringWidth(sanitizeText(once)); w1 != w2 {
			t.Errorf("%s: width %d became %d under re-sanitization", tc.name, w1, w2)
		}
	}
}

// 2.1c: an unterminated OSC must neither buffer without bound nor swallow the
// rest of the conversation.
func TestUnterminatedStringIsBounded(t *testing.T) {
	var z escSanitizer
	out := z.Write("\x1b]0;" + strings.Repeat("A", sanMaxStr*4))
	out += z.Write("and the conversation continues")
	out += z.Flush()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("an escape survived: %q", out)
	}
	if !strings.Contains(out, "and the conversation continues") {
		t.Fatalf("an unterminated OSC swallowed later output: %q", out)
	}
	if z.strN > sanMaxStr {
		t.Fatalf("string payload accumulated %d bytes, ceiling is %d", z.strN, sanMaxStr)
	}
}

func TestUnterminatedCSIIsBounded(t *testing.T) {
	var z escSanitizer
	z.Write("\x1b[" + strings.Repeat("1;", sanMaxCSI*4))
	if z.pendN > sanMaxCSI {
		t.Fatalf("held %d bytes mid-CSI, ceiling is %d", z.pendN, sanMaxCSI)
	}
	out := z.Write("m") + z.Flush()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("an escape survived an over-long CSI: %q", out)
	}
}

// A rune cut in half by a chunk boundary must not become two replacement
// characters -- that would be silent corruption of ordinary non-ASCII text.
func TestRuneSplitAcrossChunks(t *testing.T) {
	const s = "héllo 世界"
	for i := 1; i < len(s); i++ {
		var z escSanitizer
		got := z.Write(s[:i]) + z.Write(s[i:]) + z.Flush()
		if got != s {
			t.Fatalf("split at %d corrupted the text: got %q want %q", i, got, s)
		}
	}
}

func TestFlushReleasesHeldText(t *testing.T) {
	var z escSanitizer
	if got := z.Write("answer\x1b[3"); got != "answer" {
		t.Fatalf("held bytes leaked early: %q", got)
	}
	if got := z.Flush(); got != "[3" {
		t.Fatalf("Flush = %q, want %q", got, "[3")
	}
	if z.state != sanText || z.pendN != 0 {
		t.Fatalf("Flush left state %v pendN %d", z.state, z.pendN)
	}
}

func FuzzSanitizeNoEscapeSurvives(f *testing.F) {
	for _, tc := range sanCorpus {
		f.Add(tc.in)
	}
	f.Add("\x1b[38;2;1;2;3m\x1b]8;;x\x07\x1b[?1049h2J")
	// The length-ceiling shapes, seeded so the fuzzer starts at the boundary
	// rather than having to build a 66-byte sequence by chance.
	for _, n := range []int{65, 66, 67} {
		f.Add(sgrOfLength(n))
		f.Add(sgrOfLength(n) + "\x1b[31mx")
		f.Add(sgrOfLength(n) + "\x1b[2Jvisible")
		f.Add(sgrOfLength(n) + sgrOfLength(n) + "tail")
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := sanitizeText(in)
		if v := sanEscapeViolation(out); v != "" {
			t.Fatalf("%s\nin  %q\nout %q", v, in, out)
		}
		if twice := sanitizeText(out); twice != out {
			t.Fatalf("not idempotent\nin    %q\nonce  %q\ntwice %q", in, out, twice)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("output is not valid UTF-8: %q", out)
		}
	})
}

// The split points are where bypasses live, so they are what gets fuzzed.
func FuzzSanitizeChunkInvariance(f *testing.F) {
	for _, tc := range sanCorpus {
		f.Add(tc.in, 1)
	}
	f.Add("\x1b[2J", 2)
	// Cut points AT the ceiling are where a resume offset one byte late would
	// show up, so the boundary shapes are seeded here too.
	for _, n := range []int{65, 66, 67} {
		f.Add(sgrOfLength(n)+"\x1b[2Jvisible", 64)
		f.Add(sgrOfLength(n)+"\x1b[31mx", 65)
		f.Add(sgrOfLength(n)+sgrOfLength(n)+"tail", 66)
	}
	f.Fuzz(func(t *testing.T, in string, cut int) {
		if len(in) == 0 {
			return
		}
		c := cut % (len(in) + 1)
		if c < 0 {
			c += len(in) + 1
		}
		var z escSanitizer
		got := z.Write(in[:c]) + z.Write(in[c:]) + z.Flush()
		if want := sanitizeText(in); got != want {
			t.Fatalf("cut %d of %q\n got %q\nwant %q", c, in, got, want)
		}
	})
}

// THE OVER-LONG CSI BOUNDARY, PINNED BY SHAPE RATHER THAN LEFT TO RANDOM
// SEARCH.
//
// On overflow the parser drops what it held and REPROCESSES the offending byte
// as text (see the ceilings in sanitize.go). The failure mode that trade
// invites is a resume offset one byte off: land late and the stream is
// re-entered in the middle of an escape, which is the bypass the ceiling was
// supposed to prevent. Random fuzzing would need a long time to build a
// 66-byte sequence with an ESC at exactly the resume point, so the shapes are
// written down here and seeded into the fuzzers.
//
// sanMaxCSI counts from the ESC, so "\x1b[" occupies two of it: 62 parameter
// bytes fit (65 total with the final byte), 63 overflow.
// sgrOfLength builds a VALID, fully-allowlisted SGR sequence of exactly total
// bytes, so a test can walk the length ceiling without tripping any other
// rule. Getting this wrong is how the first version of this test failed: it
// emitted thirty-odd parameters and hit the 24-parameter cap, which looked
// like a length failure and was not.
func sgrOfLength(total int) string {
	p := total - 3 // "\x1b[" and the final "m"
	if p < 1 {
		p = 1
	}
	// Widths are 1, 2 or 3 digits; n parameters cost sum(widths) + n-1
	// separators. Take the fewest parameters that can reach p.
	n := (p + 4) / 4
	if n < 1 {
		n = 1
	}
	widths := make([]int, n)
	for i := range widths {
		widths[i] = 1
	}
	for i, extra := 0, (p-(n-1))-n; i < n && extra > 0; i++ {
		add := 2
		if extra < 2 {
			add = extra
		}
		widths[i] += add
		extra -= add
	}
	// One allowed parameter per width: reset, default-foreground, bright-bg.
	byWidth := map[int]string{1: "0", 2: "39", 3: "100"}
	parts := make([]string, n)
	for i, w := range widths {
		parts[i] = byWidth[w]
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

// The generator is load-bearing, so it is checked rather than trusted.
func TestSGROfLengthGeneratesWhatItClaims(t *testing.T) {
	for total := 8; total <= 70; total++ {
		s := sgrOfLength(total)
		if len(s) != total {
			t.Fatalf("sgrOfLength(%d) is %d bytes: %q", total, len(s), s)
		}
		if !sanIsParams([]byte(s[2:len(s)-1])) || !sanSGRAllowed([]byte(s[2:len(s)-1])) {
			t.Fatalf("sgrOfLength(%d) is not an allowed SGR: %q", total, s)
		}
	}
}

func TestOverLongCSIBoundary(t *testing.T) {
	const validSGR = "\x1b[31m"
	cases := []struct {
		name    string
		in      string
		survive string // a substring the output must contain, "" for none
	}{
		{"63 bytes, fits", sgrOfLength(63), sgrOfLength(63)},
		{"64 bytes, fits", sgrOfLength(64), sgrOfLength(64)},
		{"65 bytes, the last that fits", sgrOfLength(65), sgrOfLength(65)},
		{"66 bytes, the first that overflows", sgrOfLength(66), ""},
		{"67 bytes, overflows", sgrOfLength(67), ""},

		// The recovery case: whatever follows an over-long sequence must be
		// read as a fresh sequence, not as a continuation of the dead one.
		{"valid SGR immediately after an over-long CSI", sgrOfLength(66) + validSGR + "x", validSGR},
		{"valid SGR after an over-long CSI with no final byte", "\x1b[" + strings.Repeat("1;", 60) + validSGR + "x", validSGR},

		// ESC exactly at the resume point, by both routes into it: a control
		// byte inside the sequence (the malformed branch) and the length
		// ceiling (the overflow branch).
		{"ESC arrives mid-CSI, before the ceiling", "\x1b[1;2;3\x1b[2Jvisible", ""},
		{"ESC arrives mid-CSI, at the ceiling", "\x1b[" + strings.Repeat("1;", 31) + "\x1b[2Jvisible", ""},
		{"ESC is the byte after an over-long CSI ends", sgrOfLength(66) + "\x1b[2Jvisible", ""},

		{"two over-long sequences back to back", sgrOfLength(66) + sgrOfLength(66) + "tail", ""},
		{"two over-long sequences then a valid one", sgrOfLength(70) + sgrOfLength(70) + validSGR + "tail", validSGR},
		{"over-long CSI then an OSC", sgrOfLength(66) + "\x1b]0;title\x07tail", ""},
		{"over-long OSC then a valid SGR", "\x1b]0;" + strings.Repeat("A", sanMaxStr*2) + "\x07" + validSGR + "x", validSGR},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sanitizeText(tc.in)
			if v := sanEscapeViolation(out); v != "" {
				t.Fatalf("%s\nin  %q\nout %q", v, tc.in, out)
			}
			if tc.survive != "" && !strings.Contains(out, tc.survive) {
				t.Fatalf("the sequence after the over-long one was lost\nin  %q\nout %q", tc.in, out)
			}
			// The recovery must not eat later text either.
			if strings.Contains(tc.in, "visible") && !strings.Contains(out, "visible") {
				t.Fatalf("an over-long sequence swallowed later output: %q", out)
			}
			if strings.Contains(tc.in, "tail") && !strings.Contains(out, "tail") {
				t.Fatalf("an over-long sequence swallowed later output: %q", out)
			}
			// And the boundary must behave identically however it is chunked,
			// which is where a one-byte-late resume would show up.
			for i := 0; i <= len(tc.in); i++ {
				chunkInvariant(t, tc.in, []int{i})
			}
			all := make([]int, 0, len(tc.in)+1)
			for i := 0; i <= len(tc.in); i++ {
				all = append(all, i)
			}
			chunkInvariant(t, tc.in, all)
		})
	}
}

// The held buffer must never exceed its ceiling at any point during any of the
// boundary shapes, whatever the chunking.
func TestHeldBytesNeverExceedTheCeiling(t *testing.T) {
	for _, n := range []int{63, 64, 65, 66, 67, 200} {
		in := sgrOfLength(n) + "\x1b]0;" + strings.Repeat("A", 600) + "\x07tail"
		var z escSanitizer
		for i := 0; i < len(in); i++ {
			z.Write(in[i : i+1])
			if z.pendN > sanMaxCSI {
				t.Fatalf("n=%d: held %d bytes, ceiling is %d", n, z.pendN, sanMaxCSI)
			}
			if z.strN > sanMaxStr {
				t.Fatalf("n=%d: string payload %d bytes, ceiling is %d", n, z.strN, sanMaxStr)
			}
		}
		z.Flush()
	}
}
