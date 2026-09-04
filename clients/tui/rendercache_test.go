package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/muesli/termenv"
)

// EVERY WAY THE TRANSCRIPT CHANGES, ASSERTED.
//
// The enumeration in rendercache.go is the deliverable; this is what holds it
// true. A missed invalidation does not crash and does not corrupt anything --
// it draws text that is no longer what the model said, and keeps drawing it.
// No gate catches that except a comparison against a render that has no cache
// at all, which is what every case below does:
//
//	cache.render(turns, width)  ==  renderTranscript(turns, width)
//
// asserted AFTER the mutation, with a cache that was warmed BEFORE it. A case
// that skipped the warm-up would pass against an empty cache and prove nothing,
// so each one renders first and mutates second.
func TestEveryTranscriptMutationIsCaughtByTheCache(t *testing.T) {
	base := func() []turn {
		return []turn{
			{role: roleUser, text: "first question"},
			{role: roleAssistant, text: "first answer", reasoning: "thinking"},
			{role: roleSystem, text: "a note"},
			{role: roleUser, text: "second question"},
			{role: roleAssistant, text: "second answer"},
			{role: roleSevered, text: "never sent"},
		}
	}

	cases := []struct {
		name   string
		mutate func(turns []turn) []turn
		width  int // 0 means "same width as the warm-up"
	}{
		{
			// 1. appendTurn.
			name:   "a turn is appended",
			mutate: func(ts []turn) []turn { return append(ts, turn{role: roleUser, text: "third"}) },
		},
		{
			// 2. tokenMsg / reasoningMsg growing the streaming turn.
			name: "the last turn's text grows, as a token does",
			mutate: func(ts []turn) []turn {
				ts[len(ts)-1].text += " and more"
				return ts
			},
		},
		{
			// 2b. the reasoning stream, which is a separate field on one turn.
			name: "the last turn's reasoning grows",
			mutate: func(ts []turn) []turn {
				ts[1].reasoning += " and more thinking"
				return ts
			},
		},
		{
			// 3. endStream's sanitizer flush, which mutates a turn that is no
			// longer the one being streamed into.
			name: "a turn that is not the last one is appended to",
			mutate: func(ts []turn) []turn {
				ts[1].text += "<flushed tail>"
				return ts
			},
		},
		{
			// 4. THE ONE AN INVALIDATION-HOOK DESIGN WOULD MISS. recordActivity
			// rewrites m.turns[idx].text for an idx looked up by tool-call id,
			// which can be any earlier turn.
			name: "an arbitrary earlier turn is rewritten in place",
			mutate: func(ts []turn) []turn {
				ts[2].text = "⚙ tool finished"
				return ts
			},
		},
		{
			// 4b. and the same rewrite to a string of the SAME LENGTH, which a
			// cheaper key (a length, a count, a dirty bit) would not notice.
			name: "an earlier turn is rewritten to the same length",
			mutate: func(ts []turn) []turn {
				ts[2].text = strings.Repeat("x", len(ts[2].text))
				return ts
			},
		},
		{
			// 4c. and a role change with the text left alone.
			name: "a turn changes role but keeps its text",
			mutate: func(ts []turn) []turn {
				ts[2].role = roleSevered
				return ts
			},
		},
		{
			// 5. /compact: turns are dropped from the FRONT, so every surviving
			// turn changes index and the cache faces a different turn at each.
			name:   "the transcript is compacted from the front",
			mutate: func(ts []turn) []turn { return append([]turn(nil), ts[len(ts)-2:]...) },
		},
		{
			// 7. eviction (3.5) is the same shape as /compact. Asserted in
			// advance so 3.5 cannot land a variant this design misses.
			name:   "one turn is evicted from the front",
			mutate: func(ts []turn) []turn { return append([]turn(nil), ts[1:]...) },
		},
		{
			// 6. /clear and clearConversation.
			name:   "the transcript is cleared",
			mutate: func(ts []turn) []turn { return nil },
		},
		{
			// 8. a WindowSizeMsg. renderSevered draws its rail to the edge, so
			// a rendered block is width-dependent.
			name:   "the terminal is resized",
			mutate: func(ts []turn) []turn { return ts },
			width:  37,
		},
		{
			name:   "the terminal is resized to one column",
			mutate: func(ts []turn) []turn { return ts },
			width:  1,
		},
		{
			// 11. incomplete changes no rendered byte -- but it IS a mutation of
			// a cached turn, and the reason it is safe is a fact about
			// renderTranscript that could stop being true.
			name: "a turn is marked incomplete",
			mutate: func(ts []turn) []turn {
				ts[1].incomplete = "provider_error"
				return ts
			},
		},
		{
			// 12. an error path that refreshes with the same turns must not
			// change anything, and must not discard the cache either.
			name:   "nothing changes at all",
			mutate: func(ts []turn) []turn { return ts },
		},
	}

	const warmWidth = 40
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cache transcriptCache
			turns := base()

			// Warm it. Without this every case would run against an empty cache
			// and could not tell a correct cache from an absent one.
			warm := cache.render(turns, warmWidth)
			if warm != renderTranscript(turns, warmWidth) {
				t.Fatal("the cache was already wrong before the mutation")
			}

			turns = c.mutate(turns)
			width := warmWidth
			if c.width != 0 {
				width = c.width
			}

			got := cache.render(turns, width)
			want := renderTranscript(turns, width)
			if got != want {
				t.Errorf("the cache served stale content after: %s\ncached: %q\nfresh:  %q", c.name, got, want)
			}
		})
	}
}

// 9. The colour profile. It cannot change in production -- it is pinned at
// startup -- but it does change in tests, which sweep profiles deliberately. A
// cache that ignored it would hand those tests bytes from the wrong profile and
// they would pass.
func TestTheCacheNoticesAColorProfileChange(t *testing.T) {
	turns := []turn{
		{role: roleUser, text: "a question"},
		{role: roleAssistant, text: "an answer"},
	}
	var cache transcriptCache

	var atAscii, atANSI256 string
	withColorProfile(t, termenv.Ascii, func() { atAscii = cache.render(turns, 40) })
	withColorProfile(t, termenv.ANSI256, func() { atANSI256 = cache.render(turns, 40) })

	if atAscii == atANSI256 {
		t.Fatal("the cache served Ascii bytes at ANSI256 -- a profile change is not invalidating it")
	}
	withColorProfile(t, termenv.ANSI256, func() {
		if want := renderTranscript(turns, 40); atANSI256 != want {
			t.Errorf("after the profile changed:\ncached: %q\nfresh:  %q", atANSI256, want)
		}
	})
}

// A cache that never hits is always correct and entirely pointless, so the
// cases above cannot be the only assertion. This is the one that would fail if
// render started missing on every lookup: reusing a warm cache with no mutation
// must allocate a small, FLAT number of times whatever the transcript length.
//
// Flat is the acceptance signal rather than small: the defect was cost
// proportional to conversation length, and a constant factor improvement that
// stayed proportional would be the same defect at a lower price.
func TestWarmCacheAllocationsDoNotGrowWithTheTranscript(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	measure := func(turns int) float64 {
		ts := make([]turn, 0, turns)
		for i := 0; i < turns; i++ {
			ts = append(ts, turn{role: roleAssistant, text: strings.Repeat("answer text ", 100)})
		}
		var cache transcriptCache
		cache.render(ts, 80)
		return testing.AllocsPerRun(50, func() { cache.render(ts, 80) })
	}

	small, large := measure(30), measure(400)
	t.Logf("warm re-render: %.0f allocs at 30 turns, %.0f at 400", small, large)

	// A per-turn allocation would show as roughly 370 more at 400 than at 30.
	// The slack allowed here is the builder's own geometric growth, which is
	// logarithmic in the transcript size, not linear.
	if large > small+8 {
		t.Errorf("warm re-render allocates %.0f at 400 turns against %.0f at 30. "+
			"That is growth with transcript length, which means blocks are being "+
			"re-rendered and the cache is not doing what it claims.", large, small)
	}
}

// The same property through the real event loop, which is where the budget is
// written: allocations per streamed token, flat with respect to prior turns.
func TestPerTokenAllocationsAreFlatInTranscriptLength(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	var at [2]float64
	for i, prior := range []int{0, 400} {
		m := benchTranscript(prior, 1200)
		at[i] = testing.AllocsPerRun(50, func() {
			u, _ := m.Update(tokenMsg("tok "))
			m = u.(chatModel)
		})
	}
	t.Logf("allocs per token: %.0f at 0 prior turns, %.0f at 400", at[0], at[1])

	// MEASURED with the cache neutered so every lookup misses -- which is how
	// this was checked to be load-bearing rather than merely green:
	// 23 / 195 / 692 / 1353 / 2234 at 0 / 30 / 120 / 240 / 400 prior turns.
	// A ratio of 97x. With the cache live it is 18 / 25 / 27 / 28 / 29.
	if ratio := at[1] / at[0]; ratio > 3 {
		t.Errorf("allocations per token grew %.1fx between 0 and 400 prior turns "+
			"(%.0f -> %.0f). The transcript prefix is being rebuilt somewhere.",
			ratio, at[0], at[1])
	}
}

// A DIFFERENT KIND OF STALENESS: a cache entry that survives being handed a
// turn it was not rendered from. The model is copied on every Bubble Tea
// message, so two copies share this slice's backing array; the guarantee is
// that an entry is only ever used when it matches the turn in front of it.
func TestACacheEntryIsNeverUsedForADifferentTurn(t *testing.T) {
	var cache transcriptCache
	original := []turn{
		{role: roleUser, text: "aaa"},
		{role: roleAssistant, text: "bbb"},
	}
	cache.render(original, 40)

	// Hand it an entirely different transcript of the same length at the same
	// width -- the shape a shared backing array between two model copies makes
	// possible.
	swapped := []turn{
		{role: roleUser, text: "ccc"},
		{role: roleAssistant, text: "ddd"},
	}
	if got, want := cache.render(swapped, 40), renderTranscript(swapped, 40); got != want {
		t.Fatalf("a cached block was reused for a different turn:\ncached: %q\nfresh:  %q", got, want)
	}
}

// Widths the terminal can actually be, including the ones that break wrapping
// arithmetic, swept against the uncached render.
func TestTheCacheMatchesAtEveryWidth(t *testing.T) {
	turns := []turn{
		{role: roleUser, text: "question with 日本語 and an emoji 🎉"},
		{role: roleAssistant, text: strings.Repeat("a long answer that must wrap. ", 6), reasoning: "thought"},
		{role: roleSevered, text: "severed"},
		{role: roleSystem, text: "note"},
	}
	var cache transcriptCache
	for _, w := range []int{0, 1, 2, 3, 20, 40, 80, 200, 1000} {
		if got, want := cache.render(turns, w), renderTranscript(turns, w); got != want {
			t.Errorf("width %d:\ncached: %q\nfresh:  %q", w, got, want)
		}
		// And again at the same width, which is the path that actually hits.
		if got, want := cache.render(turns, w), renderTranscript(turns, w); got != want {
			t.Errorf("width %d on the second render:\ncached: %q\nfresh:  %q", w, got, want)
		}
	}
}

// Growing one turn a token at a time, checked after every token. This is the
// streaming path in miniature, and the case where a stale block would be least
// visible: the text only ever gets longer, so a stale block is a correct
// prefix of the right answer.
func TestTheCacheStaysCorrectThroughAStream(t *testing.T) {
	turns := []turn{
		{role: roleUser, text: "explain"},
		{role: roleAssistant, text: ""},
	}
	var cache transcriptCache
	for i := 0; i < 60; i++ {
		turns[1].text += fmt.Sprintf("token%d ", i)
		if got, want := cache.render(turns, 40), renderTranscript(turns, 40); got != want {
			t.Fatalf("token %d:\ncached: %q\nfresh:  %q", i, got, want)
		}
	}
}
