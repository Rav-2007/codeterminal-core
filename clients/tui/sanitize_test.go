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
