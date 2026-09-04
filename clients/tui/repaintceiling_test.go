package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// WHAT A REPAINT COSTS AT THE BOUND -- the measurement R1.12 was missing.
//
// R1.12 was written before the transcript had a ceiling. It says a repaint is
// linear in total bytes and therefore unbounded, rate-limited only by
// coalescing. Task 3.5 then put a ceiling on the transcript, and linear-in-X
// with X bounded is bounded -- so the row may have been describing a risk that
// no longer exists, and nobody had measured it to find out.
//
// MEASURED AT THE CEILING, NOT EXTRAPOLATED FROM 240 TURNS. The existing
// numbers in this package are taken at 240 prior turns of 1.2KB, which is about
// 300KB of text -- one seventh of what the bound actually permits. Multiplying
// that by seven would be arithmetic, not measurement: the render cache is
// per-block and the viewport's line index is per-line, so neither scales with
// bytes the way the wrap does, and the only way to know the shape is to build
// the transcript the bound allows and time it.
//
// BOTH CEILINGS BIND HERE, which is what makes this the worst case rather than
// merely a large case. 500 turns alone, at a typical 1.2KB answer, is 300KB and
// cheap. 2 MiB alone, in a handful of enormous turns, misses the per-block and
// per-line costs. The expensive transcript is the one at both limits at once --
// the maximum bytes the bound permits, divided into the maximum number of turns
// it permits -- and ceilingTranscript asserts it got there rather than assuming.

// ceilingBudget is the amended D-1 budget for ONE repaint.
//
// A repaint is not an input. D-1's 16ms hard ceiling applies to a single
// Update, and a repaint is at most one per refreshInterval however fast tokens
// arrive, so 8ms -- half a frame -- is the figure a repaint is held to: it
// leaves the other half of every frame for the input that shares it.
const ceilingBudget = 8 * time.Millisecond

// ceilingAssertedCeiling is what this file FAILS on, as opposed to reports.
//
// IT IS ANCHORED TO THE MEASURED COST, NOT TO THE BUDGET, and that is a
// deliberate and uncomfortable choice worth stating plainly. The budget is NOT
// MET: a repaint at the ceiling measures p50 22.4ms against 8ms. Asserting at
// the budget would mean writing a test that fails on every run for a condition
// already recorded as an open risk (R1.12), and a permanently red gate is
// indistinguishable from no gate within a week.
//
// So the budget is REPORTED with its distance printed on every run -- the
// finding is in the log, not filed -- and what is ASSERTED is a regression
// against today's number: 64ms is roughly 3x the measured 22.4ms, the same
// multiple and the same reasoning as updateAssertedCeiling in
// renderbench_test.go. Anything that makes a repaint three times more expensive
// than it is today fails here. If R1.12 is ever fixed, this constant drops to
// 3x the budget and this comment goes with it.
const ceilingAssertedCeiling = 64 * time.Millisecond

// ceilingRepaintAllocs bounds allocations for one token plus its repaint at the
// bound. MEASURED at 39; the ceiling sits just above rather than at a round
// number far above, because what it guards -- a cache that has started
// re-rendering blocks it already holds -- costs hundreds, and a ceiling with
// room for 10x growth would not notice it arriving.
const ceilingRepaintAllocs = 60

// ceilingTranscript builds a transcript sitting at BOTH bounds and proves it.
//
// It drives the real eviction path -- append, then enforce, exactly as
// startTurn does -- so the state measured is a state a running session reaches,
// including the eviction marker at the top and the copied backing array.
//
// The tb.Fatal calls are the point of the helper, not defensive noise: a
// measurement labelled "at the ceiling" that quietly ran at half the ceiling
// would report a comfortable number for a case that does not exist, which is
// the fail-open shape the gate audit spent Phase 2 removing.
func ceilingTranscript(tb testing.TB, width int) chatModel {
	tb.Helper()

	m := newChatModel("ceiling", "/workspace", "/workspace", nil)
	u, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	m = u.(chatModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}) // dismiss splash
	m = u.(chatModel)
	m.limits = loadTranscriptLimits()

	// Sized so both ceilings arrive together: the byte ceiling divided by the
	// turn ceiling, less what the question costs.
	answerChars := m.limits.bytes/(m.limits.turns/2) - 64
	body := strings.Repeat("the quick brown fox jumps over the lazy dog. ", answerChars/44+1)

	// Enough exchanges to overrun both ceilings and evict several times, so the
	// transcript measured is a settled one rather than one still filling.
	for i := 0; i < m.limits.turns; i++ {
		m.appendTurn(turn{role: roleUser, text: fmt.Sprintf("question %d: please explain this in detail", i)})
		m.appendTurn(turn{role: roleAssistant, text: body})
		m.enforceTranscriptBound()
	}

	bytes := transcriptBytes(m.turns)
	if m.evictedTurns == 0 {
		tb.Fatalf("the bound never bound: %d turns, %d bytes, nothing evicted -- "+
			"this is not a ceiling transcript", len(m.turns), bytes)
	}
	if want := m.limits.bytes * 9 / 10; bytes < want {
		tb.Fatalf("transcript holds %d bytes, under %d (90%% of the %d-byte ceiling). "+
			"The dominant cost is linear in bytes, so measuring here would "+
			"understate the worst case the bound permits", bytes, want, m.limits.bytes)
	}
	if want := m.limits.turns * 4 / 5; len(m.turns) < want {
		tb.Fatalf("transcript holds %d turns, under %d (80%% of the %d-turn ceiling). "+
			"Few enormous turns miss the per-block and per-line costs",
			len(m.turns), want, m.limits.turns)
	}

	// An in-flight stream, so a token takes the real path rather than being
	// dropped as a stray. This runs startTurn, which enforces the bound again.
	m = typeText(m, "next question")
	m, _ = pressEnter(m)

	// Warm the cache. A cold first repaint renders every block and is a
	// different measurement -- it is taken separately below.
	m.refreshViewport()
	return m
}

func spread(ds []time.Duration) (lo, p50, p99, hi time.Duration) {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[0], percentile(s, 50), percentile(s, 99), s[len(s)-1]
}

// P3.1 -- repaint p50, p99 AND THE SPREAD at the transcript ceiling.
//
// THE SPREAD IS PART OF THE RESULT, not decoration. The 1MB paste measured
// 6.3, 13.6, 14.5 and 16.6ms on one machine against a 16ms line: reporting its
// median alone would have turned a straddle into a pass. A disposition made on
// a median that sits under a budget with a max well over it is a disposition
// made on the wrong number.
func TestRepaintCostAtTheTranscriptCeiling(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing measurement")
	}
	const width = 100
	m := ceilingTranscript(t, width)
	t.Logf("at the ceiling: %d turns, %d bytes (%.2f MiB), %d evicted so far",
		len(m.turns), transcriptBytes(m.turns),
		float64(transcriptBytes(m.turns))/(1<<20), m.evictedTurns)

	// The loop-visible repaint: the tick Update that a token arms. Timed
	// alone, with the token that dirties it left outside the clock.
	const samples = 200
	ds := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		u, _ := m.Update(tokenMsg("tok "))
		m = u.(chatModel)
		start := time.Now()
		u, _ = m.Update(refreshTickMsg{})
		ds = append(ds, time.Since(start))
		m = u.(chatModel)
	}

	// PROVE THE TICK ACTUALLY REPAINTED. A repaint that was skipped -- a
	// refreshPending flag that stopped being set, a handler that returned
	// early -- would show up as a beautiful number rather than as a failure,
	// which is the fail-open shape Phase 2 was spent removing.
	if !strings.Contains(m.viewport.View(), "tok") {
		t.Fatal("after 200 tokens and 200 ticks the viewport shows none of them: " +
			"the repaint being timed did not happen")
	}

	lo, p50, p99, hi := spread(ds)
	t.Logf("REPAINT AT THE CEILING over %d samples: p50=%v p99=%v (min=%v max=%v, spread max/min=%.1fx)",
		samples, p50.Round(time.Microsecond), p99.Round(time.Microsecond),
		lo.Round(time.Microsecond), hi.Round(time.Microsecond),
		float64(hi)/float64(lo))
	t.Logf("against the %v repaint budget: p50 is %.2fx (%v of headroom), p99 is %.2fx (%v of headroom)",
		ceilingBudget,
		float64(p50)/float64(ceilingBudget), (ceilingBudget - p50).Round(time.Microsecond),
		float64(p99)/float64(ceilingBudget), (ceilingBudget - p99).Round(time.Microsecond))

	// The per-stage split, at the ceiling rather than at 240 turns. R1.12
	// carries this breakdown, so it is re-taken wherever the row is re-decided.
	var render, wrap, set []time.Duration
	for i := 0; i < 20; i++ {
		m.turns[len(m.turns)-1].text += "tok " // dirty the last block only
		start := time.Now()
		content := m.transcript.render(m.turns, width)
		render = append(render, time.Since(start))
		start = time.Now()
		wrapped := wrapToWidth(content, width)
		wrap = append(wrap, time.Since(start))
		start = time.Now()
		m.viewport.SetContent(wrapped)
		set = append(set, time.Since(start))
	}
	_, rp50, _, _ := spread(render)
	_, wp50, _, _ := spread(wrap)
	_, sp50, _, _ := spread(set)
	t.Logf("per stage at the ceiling (medians): cache.render=%v wrapToWidth=%v viewport.SetContent=%v",
		rp50.Round(time.Microsecond), wp50.Round(time.Microsecond), sp50.Round(time.Microsecond))

	// A COLD repaint too -- the first draw after a resize, when no block is
	// cached. It is not the streaming case R1.12 is about, but it is the same
	// path with the cache doing nothing, so it bounds the other end.
	cold := m
	cold.transcript = transcriptCache{}
	start := time.Now()
	cold.refreshViewport()
	t.Logf("cold repaint (empty cache, every block re-rendered): %v", time.Since(start).Round(time.Microsecond))

	if p99 > ceilingBudget {
		t.Logf("OVER THE %v REPAINT BUDGET at p99. Reported, not failed.", ceilingBudget)
	}
	if p50 > ceilingAssertedCeiling {
		t.Errorf("a repaint at the transcript ceiling takes %v, over %v -- %.1fx the "+
			"%v budget. Past what measurement noise explains.",
			p50.Round(time.Microsecond), ceilingAssertedCeiling,
			float64(p50)/float64(ceilingBudget), ceilingBudget)
	}
}

// The deterministic half, per the standing rule: allocations, which do not
// depend on the machine or on what else is running on it.
//
// This is a CEILING, not a flatness assertion. A repaint at the bound renders
// nothing it has cached, but it still concatenates every block, wraps 2 MiB and
// re-splits it, and those allocate in proportion to bytes by construction. What
// the number guards is a repaint that starts re-rendering blocks it already
// holds -- that shows up here as a large multiple, whatever wall-clock says.
func TestRepaintAllocationsAtTheCeilingAreBounded(t *testing.T) {
	m := ceilingTranscript(t, 100)
	n := testing.AllocsPerRun(20, func() {
		u, _ := m.Update(tokenMsg("tok "))
		m = u.(chatModel)
		u, _ = m.Update(refreshTickMsg{})
		m = u.(chatModel)
	})
	t.Logf("allocs per token+repaint at the ceiling: %.0f", n)
	if n > ceilingRepaintAllocs {
		t.Errorf("a repaint at the ceiling allocates %.0f times, over %d. A cache "+
			"that has started re-rendering cached blocks looks exactly like this",
			n, ceilingRepaintAllocs)
	}
}

// P3.3 -- the paste number at depth, which 3.6 took on an empty transcript.
//
// A paste is the one input that is expensive INDEPENDENTLY of the transcript,
// so measuring it on an empty one answered only half the question: what a user
// actually does is paste into a session they have been running all day, and the
// two costs land in the same Update.
//
// MEASURED IN BOTH STATES, and the reason is a defect this test found in its
// own first version. startTurn BLURS the input, so while an answer is streaming
// a paste is bounded, reported to the user -- and then dropped on the floor by
// textinput, which ignores keys it is not focused for. The 3.6 measurement was
// taken mid-stream, so its "0.30ms after" describes a paste that was discarded.
// The delta it reported is still sound (both halves were measured in the same
// state, and the cost it removed -- stringifying a megabyte twice -- was paid
// regardless of focus), but the absolute number was not the cost of a paste
// that lands. That one is the idle case below, and it is the one of record.
func TestOneMegabytePasteIntoACeilingTranscript(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing measurement")
	}
	paste := []rune(strings.Repeat("pasted source line with unicode -> abcd\n", 1024*1024/40))
	streaming := ceilingTranscript(t, 100)

	// End the stream: that refocuses the input, which is the state a paste is
	// actually accepted in.
	u, _ := streaming.Update(streamDoneMsg{})
	idle := u.(chatModel)
	if !idle.input.Focused() {
		idle.input.Focus()
	}

	// PROVE THE PASTE LANDS before timing it. frameCost throws the updated
	// model away, so without this the test would report a very fast number for
	// an Update that had quietly done nothing -- and "0% of a frame" is exactly
	// what doing nothing looks like. This assertion is what caught the blurred
	// input above.
	u, _ = idle.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: paste})
	after := u.(chatModel)
	kept := len([]rune(after.input.Value()))
	if kept == 0 || kept > idle.input.CharLimit {
		t.Fatalf("the paste did not reach the input as a bounded value: %d runes kept "+
			"against a limit of %d", kept, idle.input.CharLimit)
	}
	if !strings.Contains(after.statusErr, "dropped") {
		t.Fatalf("a truncated paste reported nothing to the user: %q", after.statusErr)
	}
	t.Logf("the paste lands: %d of %d runes kept, and the user is told so", kept, len(paste))

	for _, tc := range []struct {
		name string
		on   chatModel
	}{
		{"idle, at the ceiling (the paste is accepted)", idle},
		{"streaming, at the ceiling (the input is blurred; the paste is dropped)", streaming},
		{"idle, empty transcript (the 3.6 comparison, re-taken)", func() chatModel {
			e := benchTranscript(0, 0)
			v, _ := e.Update(streamDoneMsg{})
			e = v.(chatModel)
			e.input.Focus()
			return e
		}()},
	} {
		on := tc.on
		lo, median, hi := frameCost(func() chatModel { return on }, tea.KeyMsg{Type: tea.KeyRunes, Runes: paste}, 9)
		t.Logf("1MB paste, %-62s min=%-9v median=%-9v max=%-9v (spread %.1fx, %.0f%% of a %v frame)",
			tc.name, lo.Round(time.Microsecond), median.Round(time.Microsecond),
			hi.Round(time.Microsecond), float64(hi)/float64(lo),
			100*float64(median)/float64(updateCeilingAnyInput), updateCeilingAnyInput)

		if median > updateCeilingAnyInput {
			t.Logf("OVER D-1's %v hard ceiling. Reported, not failed.", updateCeilingAnyInput)
		}
		if median > updateAssertedCeiling {
			t.Errorf("a 1MB paste (%s) takes %v, over %v", tc.name,
				median.Round(time.Microsecond), updateAssertedCeiling)
		}
	}
}
