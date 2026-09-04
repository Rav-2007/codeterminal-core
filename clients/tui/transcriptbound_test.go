package main

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// soakModel builds a model with DISTINCT text in every turn.
//
// benchTranscript reuses one body string for every assistant turn, so two
// thousand turns share a single allocation. That is fine for measuring
// per-token CPU and useless for measuring memory -- it would have reported a
// transcript that costs almost nothing however long it gets.
func soakModel(t testing.TB, priorTurns, answerChars int) chatModel {
	t.Helper()
	m := newChatModel("soak", "/workspace", "/workspace", nil)
	u, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = u.(chatModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = u.(chatModel)
	for i := 0; i < priorTurns/2; i++ {
		m.appendTurn(turn{role: roleUser, text: fmt.Sprintf("question %d: please explain this in detail", i)})
		var b strings.Builder
		for b.Len() < answerChars {
			fmt.Fprintf(&b, "turn %d chunk %d: the quick brown fox jumps over the lazy dog. ", i, b.Len())
		}
		m.appendTurn(turn{role: roleAssistant, text: b.String()})
	}
	return m
}

func isEvictionMarker(tn turn) bool {
	return tn.role == roleSystem && strings.HasPrefix(tn.text, evictionMarkerPrefix)
}

// 3.5a: the count ceiling.
func TestTheTurnCountCeilingIsEnforced(t *testing.T) {
	m := soakModel(t, 300, 100)
	m.limits = transcriptLimits{turns: 40, bytes: 1 << 30} // bytes deliberately out of the way
	m.enforceTranscriptBound()

	if len(m.turns) > 40 {
		t.Errorf("the transcript holds %d turns against a ceiling of 40", len(m.turns))
	}
	if m.evictedTurns == 0 {
		t.Fatal("nothing was evicted, so this test asserted nothing")
	}
}

// 3.5a: the byte ceiling, which the count ceiling cannot substitute for -- a
// model answering in large blocks stays under any turn count while holding
// megabytes.
func TestTheByteCeilingIsEnforcedIndependentlyOfTheCount(t *testing.T) {
	m := soakModel(t, 60, 20000) // 30 answers of 20KB: far under any turn count
	m.limits = transcriptLimits{turns: 100000, bytes: 128 << 10}
	m.enforceTranscriptBound()

	if got := transcriptBytes(m.turns); got > 128<<10 {
		t.Errorf("the transcript holds %s against a ceiling of 128 KB", humanBytes(got))
	}
	if len(m.turns) >= 100000 {
		t.Fatal("the turn ceiling was the binding one; this test is not about bytes")
	}
	if m.evictedTurns == 0 {
		t.Fatal("nothing was evicted, so this test asserted nothing")
	}
}

// 3.5b: eviction is never silent.
func TestEvictionLeavesAnUnmistakableMarker(t *testing.T) {
	m := soakModel(t, 200, 500)
	m.limits = transcriptLimits{turns: 20, bytes: 1 << 30}
	m.enforceTranscriptBound()

	if len(m.turns) == 0 || !isEvictionMarker(m.turns[0]) {
		t.Fatalf("no eviction marker at the top of the transcript; first turn is %+v", m.turns[0])
	}
	notice := m.turns[0].text
	for _, want := range []string{fmt.Sprint(m.evictedTurns), "dropped", "/compact", "/clear"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the eviction notice does not mention %q: %q", want, notice)
		}
	}
	if !strings.Contains(notice, humanBytes(m.evictedBytes)) {
		t.Errorf("the eviction notice does not say how many bytes went: %q", notice)
	}
}

// ...and repeated eviction accumulates into that ONE marker rather than
// stacking a line of chrome on top of every conversation.
func TestRepeatedEvictionAccumulatesIntoOneMarker(t *testing.T) {
	m := soakModel(t, 100, 500)
	m.limits = transcriptLimits{turns: 20, bytes: 1 << 30}
	m.enforceTranscriptBound()
	first := m.evictedTurns

	for round := 0; round < 5; round++ {
		for i := 0; i < 20; i++ {
			m.appendTurn(turn{role: roleAssistant, text: fmt.Sprintf("later answer %d-%d", round, i)})
		}
		m.enforceTranscriptBound()
	}

	markers := 0
	for _, tn := range m.turns {
		if isEvictionMarker(tn) {
			markers++
		}
	}
	if markers != 1 {
		t.Errorf("the transcript carries %d eviction markers, want exactly 1", markers)
	}
	if m.evictedTurns <= first {
		t.Errorf("the running total did not grow across evictions: %d then %d", first, m.evictedTurns)
	}
	if !strings.Contains(m.turns[0].text, fmt.Sprint(m.evictedTurns)) {
		t.Errorf("the marker does not report the accumulated total %d: %q", m.evictedTurns, m.turns[0].text)
	}
}

// 3.5c: EVICTION THROUGH THE CACHE, which is the entry most likely to be
// missed. Eviction changes the transcript from the FRONT while the cache is
// indexed from the front, so every surviving turn faces a cache entry rendered
// from a different turn. A cache that trusted its index would draw the
// conversation shifted -- old answers under new questions, and nothing to
// signal it.
func TestEvictionDoesNotLeaveTheCacheStale(t *testing.T) {
	m := soakModel(t, 120, 300)
	m.limits = transcriptLimits{turns: 30, bytes: 1 << 30}

	// Warm the cache on the FULL transcript first. Without this the cache would
	// be empty at eviction time and the test could not tell a correct cache
	// from an absent one.
	warm := m.transcript.render(m.turns, 80)
	if warm != renderTranscript(m.turns, 80) {
		t.Fatal("the cache was wrong before eviction")
	}

	m.enforceTranscriptBound()
	if m.evictedTurns == 0 {
		t.Fatal("nothing was evicted, so this test asserted nothing")
	}

	got := m.transcript.render(m.turns, 80)
	want := renderTranscript(m.turns, 80)
	if got != want {
		t.Errorf("the cache served a stale transcript after eviction:\ncached: %q\nfresh:  %q", got, want)
	}
	if !strings.Contains(got, evictionMarkerPrefix) {
		t.Error("the rendered transcript does not show the eviction marker")
	}
}

// The whole path, through the real event loop rather than by calling the
// bounding function directly: many turns really do evict, and the user sees it.
func TestALongSessionEvictsThroughTheRealTurnPath(t *testing.T) {
	m := soakModel(t, 0, 0)
	m.limits = transcriptLimits{turns: 24, bytes: 1 << 30}

	for i := 0; i < 40; i++ {
		m = typeText(m, fmt.Sprintf("question %d", i))
		m, _ = pressEnter(m)
		u, _ := m.Update(tokenMsg(fmt.Sprintf("answer %d ", i)))
		m = u.(chatModel)
		u, _ = m.Update(streamDoneMsg{})
		m = u.(chatModel)
	}

	if len(m.turns) > 24+2 { // +2: the marker, and the in-flight pair
		t.Errorf("40 exchanges left %d turns against a ceiling of 24", len(m.turns))
	}
	if !strings.Contains(m.viewport.View(), evictionMarkerPrefix) &&
		!isEvictionMarker(m.turns[0]) {
		t.Fatalf("a session that evicted shows no marker. Turns: %d, evicted: %d", len(m.turns), m.evictedTurns)
	}
}

// A re-slice would keep the whole original backing array alive behind a shorter
// view -- every evicted turn still resident, which is the bug this fix exists
// to prevent, hidden inside the fix.
func TestEvictionDoesNotRetainTheOldBackingArray(t *testing.T) {
	m := soakModel(t, 400, 2000)
	m.limits = transcriptLimits{turns: 20, bytes: 1 << 30}
	m.enforceTranscriptBound()

	if cap(m.turns) > 4*len(m.turns)+8 {
		t.Errorf("after eviction the slice keeps capacity %d for %d turns, so the "+
			"evicted turns are still reachable", cap(m.turns), len(m.turns))
	}
}

// Configuration must not be able to turn the bound off by accident.
func TestABadCeilingFromTheEnvironmentIsIgnored(t *testing.T) {
	for _, bad := range []string{"", "0", "-1", "nonsense", "3"} {
		t.Setenv(maxTurnsEnv, bad)
		t.Setenv(maxBytesEnv, bad)
		got := loadTranscriptLimits()
		if got.turns != defaultMaxTurns || got.bytes != defaultMaxTranscriptBytes {
			t.Errorf("%q produced limits %+v, want the defaults. A ceiling read from a "+
				"mistyped variable must not be honoured in the unsafe direction.", bad, got)
		}
	}
	t.Setenv(maxTurnsEnv, "50")
	t.Setenv(maxBytesEnv, "200000")
	if got := loadTranscriptLimits(); got.turns != 50 || got.bytes != 200000 {
		t.Errorf("a valid override was not applied: %+v", got)
	}
}

// 3.5e: THE SOAK. Two thousand turns of realistic size, driven through the real
// turn path, with the bound doing its job.
// soakExchanges is how long the soak runs, and it is SHORTER UNDER -race.
//
// MEASURED, and this is why: the loop is single-goroutine -- it drives Update
// directly and starts nothing -- so the race detector has no concurrency to
// examine here and finds nothing by construction. What it does is cost 9x. The
// full 2000 exchanges take 29.9s on the reference machine and 270.9s under
// -race; on a CI runner that became 7m38s, and the package hit `go test`'s 10
// minute timeout with this test still running.
//
// SHORTENING IT UNDER -race WOULD BE WEAKENING THE GATE IF THAT WERE ALL, so it
// is not all. build.yml's `go` job gains a second, UNRACED run of this one test
// for clients/tui, which is ~40s and executes the full 2000. The race build runs
// the reduced form for the memory-safety coverage it does add; the full length
// still runs on every push, in CI, where the number in the readiness statement
// can be reproduced.
//
// 400 rather than a round fraction: the turn ceiling is 500 turns = 250
// exchanges, so 400 spends 150 exchanges in sustained eviction. The assertion
// that eviction actually happened is at the bottom of this test and applies to
// both lengths, so a reduced count that stopped exercising the bound fails
// rather than passes quietly.
func soakExchanges() int {
	if raceEnabled {
		return 400
	}
	return 2000
}

func TestTwoThousandTurnSoakStaysWithinItsBounds(t *testing.T) {
	if testing.Short() {
		t.Skip("soak")
	}
	n := soakExchanges()
	if raceEnabled {
		t.Logf("REDUCED FORM: %d exchanges, not 2000, because -race costs 9x on a loop with "+
			"no concurrency in it. build.yml runs this same test unraced at full length.", n)
	}
	m := soakModel(t, 0, 0)

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	allocsEarly, allocsLate := 0.0, 0.0
	for i := 0; i < n; i++ {
		m = typeText(m, fmt.Sprintf("question number %d about the code", i))
		m, _ = pressEnter(m)
		body := strings.Repeat(fmt.Sprintf("answer %d chunk. ", i), 70) // ~1.2KB
		u, _ := m.Update(tokenMsg(body))
		m = u.(chatModel)
		u, _ = m.Update(refreshTickMsg{})
		m = u.(chatModel)
		u, _ = m.Update(streamDoneMsg{})
		m = u.(chatModel)

		if !raceEnabled && (i == 100 || i == n-100) {
			// MID-STREAM, on a fork of the model. A tokenMsg arriving after
			// streamDoneMsg is discarded as a stray -- an earlier version of
			// this measurement sampled exactly that and reported 3 allocations
			// per token at every transcript length, which is the number for
			// doing nothing at all.
			mm := m
			mm = typeText(mm, "sampling prompt")
			mm, _ = pressEnter(mm)
			if !mm.turnInFlight() {
				t.Fatal("the allocation sample is not running against a live stream")
			}
			a := testing.AllocsPerRun(20, func() { mm = deliverToken(mm, "tok ") })
			if i == 100 {
				allocsEarly = a
			} else {
				allocsLate = a
			}
		}
	}

	// The declared bounds.
	if len(m.turns) > defaultMaxTurns+2 {
		t.Errorf("after %d exchanges the transcript holds %d turns, ceiling %d", n, len(m.turns), defaultMaxTurns)
	}
	if got := transcriptBytes(m.turns); got > defaultMaxTranscriptBytes {
		t.Errorf("after %d exchanges the transcript holds %s, ceiling %s",
			n, humanBytes(got), humanBytes(defaultMaxTranscriptBytes))
	}
	if len(m.transcript.blocks) > len(m.turns) {
		t.Errorf("the render cache holds %d blocks for %d turns", len(m.transcript.blocks), len(m.turns))
	}

	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("%d exchanges: %d turns kept, %s of text, %d cache blocks, heap-in-use %.1f MB, evicted %d turns / %s",
		n, len(m.turns), humanBytes(transcriptBytes(m.turns)), len(m.transcript.blocks),
		float64(after.HeapInuse)/(1<<20), m.evictedTurns, humanBytes(m.evictedBytes))

	// APPLIES TO BOTH LENGTHS, which is what makes the reduced form safe: a
	// shortened soak that no longer reaches the ceiling fails here rather than
	// passing quietly on an assertion it never exercised.
	if m.evictedTurns == 0 {
		t.Fatalf("%d exchanges evicted nothing; the soak is not exercising the bound", n)
	}
	if !raceEnabled {
		t.Logf("allocs per token: %.0f at turn 100, %.0f at turn %d", allocsEarly, allocsLate, n-100)
		if allocsLate > allocsEarly*2 {
			t.Errorf("allocations per token grew from %.0f to %.0f over the soak; "+
				"something is still proportional to session length", allocsEarly, allocsLate)
		}
	}
}
