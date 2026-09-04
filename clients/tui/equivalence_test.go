package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/muesli/termenv"
)

// THE EQUIVALENCE GATE.
//
//	cache.render(turns, width)  ==  renderTranscript(turns, width)
//
// byte for byte, for randomised transcripts and randomised widths, after
// randomised mutations. This is what makes the render cache safe rather than
// merely fast: everything else measures that it is quicker, and a cache can be
// quicker and wrong at the same time. Wrong here means a transcript that shows
// text the model did not send, with nothing to signal it.
//
// THE LOAD-BEARING ASSUMPTION, stated because it is an assumption and not a
// fact of the code under test: the only escape sequences the renderer can ever
// see in turn text are the SGR forms the sanitizer permits. Everything reaching
// the transcript goes through appendTurn or the streaming sanitizers, which
// strip every other class -- all other CSI, all OSC, DCS, APC, PM, SOS, SS2/SS3,
// every C1, every C0 but \n and \t. The generators below therefore only ever
// produce SGR, and if sanitization changed to permit something else this gate
// would be testing a smaller alphabet than the renderer actually receives.
//
// TestTheRendererOnlyEverSeesSGR is what makes that assumption fail loudly
// instead of quietly: it pushes the sanitizer's own hostile corpus through the
// door the transcript is written by and requires that nothing but SGR survives.

// sgrPalette is the set the sanitizer permits, in every form it permits: the
// simple attributes, their resets, the 8 standard and 8 bright colours in
// foreground and background, and both extended forms.
var sgrPalette = []string{
	"\x1b[0m", "\x1b[1m", "\x1b[2m", "\x1b[3m", "\x1b[4m", "\x1b[7m", "\x1b[9m",
	"\x1b[22m", "\x1b[23m", "\x1b[24m", "\x1b[27m", "\x1b[29m",
	"\x1b[30m", "\x1b[37m", "\x1b[39m", "\x1b[40m", "\x1b[47m", "\x1b[49m",
	"\x1b[90m", "\x1b[97m", "\x1b[100m", "\x1b[107m",
	"\x1b[38;5;205m", "\x1b[48;5;17m",
	"\x1b[38;2;255;95;175m", "\x1b[48;2;0;0;0m",
	"\x1b[1;38;5;44;4m",
}

// textFragments covers the widths and cluster shapes that break naive wrapping.
var textFragments = []string{
	"", " ", "\t", "\n", "a", "hello world ",
	"日本語のテキスト",           // wide, unambiguous
	"→±·※",               // ambiguous width
	"🎉", "👩‍👩‍👧‍👦", "🇯🇵", // emoji, ZWJ sequence, regional indicator pair
	"é", "e\u0301", "a\u0300\u0301\u0302", // precomposed, combining, stacked combining
	"\u200b", "\u00a0", // zero-width space, non-breaking space
	strings.Repeat("x", 300),                          // one unbroken token far past any width
	"https://example.com/" + strings.Repeat("a/", 60), // the URL case
	"line one\nline two\n\nline four",
	"trailing spaces    ",
	"\ttabbed\tcolumns\t",
}

// randomTurn builds one turn out of the fragments and the permitted SGR.
func randomTurn(rng *rand.Rand) turn {
	build := func() string {
		var b strings.Builder
		for n := rng.Intn(6); n > 0; n-- {
			if rng.Intn(3) == 0 {
				b.WriteString(sgrPalette[rng.Intn(len(sgrPalette))])
			}
			b.WriteString(textFragments[rng.Intn(len(textFragments))])
		}
		return b.String()
	}
	t := turn{
		role: []turnRole{roleUser, roleAssistant, roleSystem, roleSevered}[rng.Intn(4)],
		text: build(),
	}
	if t.role == roleAssistant && rng.Intn(3) == 0 {
		t.reasoning = build()
	}
	return t
}

var equivalenceWidths = []int{1, 2, 3, 20, 40, 80, 200, 0}

// THE PROPERTY TEST. Deterministic, seeded, and run by an ordinary `go test`
// so it is part of the -race gate rather than something someone remembers to
// fuzz.
func TestIncrementalRenderEqualsFullRenderUnderRandomMutation(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			var cache transcriptCache
			var turns []turn
			width := equivalenceWidths[rng.Intn(len(equivalenceWidths))]

			for step := 0; step < 40; step++ {
				switch rng.Intn(10) {
				case 0, 1, 2, 3: // append, the common case
					turns = append(turns, randomTurn(rng))
				case 4, 5: // grow a turn, as a token does
					if len(turns) > 0 {
						i := rng.Intn(len(turns))
						turns[i].text += textFragments[rng.Intn(len(textFragments))]
					}
				case 6: // rewrite an arbitrary turn, as recordActivity does
					if len(turns) > 0 {
						turns[rng.Intn(len(turns))] = randomTurn(rng)
					}
				case 7: // compact or evict, a drop from the front
					if len(turns) > 2 {
						turns = append([]turn(nil), turns[rng.Intn(len(turns)-1)+1:]...)
					}
				case 8: // clear
					turns = nil
				case 9: // resize
					width = equivalenceWidths[rng.Intn(len(equivalenceWidths))]
				}

				got := cache.render(turns, width)
				want := renderTranscript(turns, width)
				if got != want {
					t.Fatalf("step %d, width %d, %d turns:\ncached: %q\nfresh:  %q",
						step, width, len(turns), got, want)
				}
			}
		})
	}
}

// The same property with the colour profile moving underneath it, which is the
// one input the cache keys on that is not visible in the turns.
func TestIncrementalRenderEqualsFullRenderAcrossProfiles(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	var cache transcriptCache
	turns := []turn{randomTurn(rng), randomTurn(rng), randomTurn(rng)}

	for _, p := range []termenv.Profile{termenv.Ascii, termenv.ANSI, termenv.ANSI256, termenv.TrueColor, termenv.ANSI} {
		withColorProfile(t, p, func() {
			for _, w := range equivalenceWidths {
				if got, want := cache.render(turns, w), renderTranscript(turns, w); got != want {
					t.Fatalf("profile %v width %d:\ncached: %q\nfresh:  %q", p, w, got, want)
				}
			}
		})
	}
}

// THE ASSUMPTION, MADE TO FAIL LOUDLY. Every hostile payload the sanitizer
// corpus knows about, plus the CSI length-boundary shapes, pushed through
// appendTurn -- the one door the transcript is written by -- and required to
// leave nothing but permitted SGR behind.
//
// If sanitization is ever relaxed, this fails, and the generators above are
// then known to be testing a smaller alphabet than the renderer receives.
func TestTheRendererOnlyEverSeesSGR(t *testing.T) {
	var m chatModel
	for _, c := range sanCorpus {
		m.appendTurn(turn{role: roleAssistant, text: c.in, reasoning: c.in})
	}
	for _, n := range []int{63, 64, 65, 66, 67, 200} {
		m.appendTurn(turn{role: roleAssistant, text: sgrOfLength(n)})
	}
	if len(m.turns) == 0 {
		t.Fatal("no payloads were pushed through appendTurn; this test asserted nothing")
	}

	for i, tn := range m.turns {
		for _, s := range []string{tn.text, tn.reasoning} {
			if rest, ok := nonSGREscape(s); ok {
				t.Errorf("turn %d reached the renderer carrying a non-SGR escape %q.\n"+
					"The equivalence gate's generators only produce SGR, so they are no "+
					"longer covering what the renderer actually sees. Either restore the "+
					"sanitizer's allowlist or widen sgrPalette to match it.", i, rest)
			}
		}
	}
}

// nonSGREscape returns the first escape sequence in s that is not a CSI ... m,
// and whether one was found.
func nonSGREscape(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		if i+1 >= len(s) || s[i+1] != '[' {
			return sample(s[i:]), true
		}
		j := i + 2
		for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			return sample(s[i:]), true
		}
		i = j
	}
	return "", false
}

func sample(s string) string {
	if len(s) > 24 {
		return s[:24]
	}
	return s
}

// FuzzIncrementalRenderMatchesFull is the same property left open-ended, seeded
// from the sanitizer's own corpus so the escapes it explores are the ones that
// actually reach the transcript.
func FuzzIncrementalRenderMatchesFull(f *testing.F) {
	for _, c := range sanCorpus {
		f.Add(c.in, 40)
	}
	for _, s := range sgrPalette {
		f.Add(s+"text", 20)
	}
	for _, s := range textFragments {
		f.Add(s, 1)
	}
	for _, n := range []int{63, 65, 66, 67} {
		f.Add(sgrOfLength(n), 80)
	}

	f.Fuzz(func(t *testing.T, payload string, width int) {
		if width < 0 || width > 500 {
			t.Skip()
		}
		// Through the real door, so the fuzzer explores what the renderer can
		// actually receive rather than arbitrary bytes it never sees.
		var m chatModel
		m.appendTurn(turn{role: roleUser, text: payload})
		m.appendTurn(turn{role: roleAssistant, text: payload, reasoning: payload})
		m.appendTurn(turn{role: roleSevered, text: payload})

		var cache transcriptCache
		if got, want := cache.render(m.turns, width), renderTranscript(m.turns, width); got != want {
			t.Fatalf("width %d:\ncached: %q\nfresh:  %q", width, got, want)
		}
		// And again after a mutation, which is where a cache goes stale.
		m.turns[1].text += payload
		if got, want := cache.render(m.turns, width), renderTranscript(m.turns, width); got != want {
			t.Fatalf("after mutation, width %d:\ncached: %q\nfresh:  %q", width, got, want)
		}
	})
}
