package main

import (
	"context"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"codeterminal/protocol"
)

// THE ACCEPTANCE TARGETS THAT NOTHING ELSE IN THIS PACKAGE ASSERTS.
//
// The per-token budget, the equivalence gate, the soak and the paste bound all
// have their own files. What is here is the three that were named as targets
// and had no home: a resize storm, a whole answer at depth against the same
// answer with no history, and file descriptors.

// A RESIZE STORM. Dragging a terminal's corner emits a WindowSizeMsg per frame,
// each of which re-wraps the entire transcript at a new width -- every cached
// block invalidated, every time, because block rendering is width-dependent.
// It is the worst input the render cache has: a sustained sequence in which the
// cache never hits.
func TestAResizeStormStaysWithinTheFrameBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing measurement")
	}
	m := benchTranscript(240, 1200)
	widths := []int{120, 118, 100, 84, 63, 41, 30, 41, 63, 84, 100, 118, 120}

	var ds []time.Duration
	for round := 0; round < 8; round++ {
		for _, w := range widths {
			start := time.Now()
			u, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
			ds = append(ds, time.Since(start))
			m = u.(chatModel)
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	median, p99, worst := ds[len(ds)/2], ds[len(ds)*99/100], ds[len(ds)-1]
	t.Logf("resize storm at 240 turns, %d resizes: median=%v p99=%v worst=%v (%.0f%% of a %v frame)",
		len(ds), median.Round(time.Microsecond), p99.Round(time.Microsecond),
		worst.Round(time.Microsecond), 100*float64(worst)/float64(updateCeilingAnyInput),
		updateCeilingAnyInput)

	if worst > updateAssertedCeiling {
		t.Errorf("the slowest resize took %v, over %v", worst.Round(time.Microsecond), updateAssertedCeiling)
	}
	if median > updateCeilingAnyInput {
		t.Errorf("the median resize took %v, over D-1's %v frame ceiling",
			median.Round(time.Microsecond), updateCeilingAnyInput)
	}
}

// A WHOLE ANSWER AT DEPTH, which is the shape the original defect actually had:
// per-token cost proportional to transcript length is QUADRATIC per answer, so
// the honest measure is the whole answer rather than one token.
//
// Target: an 800-token answer at 400 prior turns within 2x the same answer at
// none. Measured on allocations, which are deterministic, with wall-clock
// reported beside it.
func TestAWholeAnswerAtDepthCostsNoMoreThanTwiceAtTheTop(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two long transcripts")
	}
	answer := func(prior int) (allocs uint64, elapsed time.Duration) {
		m := benchTranscript(prior, 1200)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		for i := 0; i < 800; i++ {
			m = deliverToken(m, "token ")
		}
		elapsed = time.Since(start)
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(m)
		return after.Mallocs - before.Mallocs, elapsed
	}

	atTop, topTime := answer(0)
	atDepth, depthTime := answer(400)
	t.Logf("800-token answer, A REPAINT FORCED PER TOKEN (pessimistic): "+
		"%d allocations at 0 prior turns (%v), %d at 400 (%v)",
		atTop, topTime.Round(time.Millisecond), atDepth, depthTime.Round(time.Millisecond))

	// AND THE SAME ANSWER AT THE REAL CADENCE, reported because the number
	// above is not what anyone experiences and reporting it alone would be
	// misleading in the other direction. deliverToken forces a repaint per
	// token; the running client repaints at most once per refreshInterval
	// however fast tokens arrive, so the honest wall-clock figure paces the
	// tick by the clock exactly as the event loop does.
	if !raceEnabled {
		paced := func(prior int) (time.Duration, int) {
			m := benchTranscript(prior, 1200)
			repaints := 0
			last := time.Now()
			start := time.Now()
			for i := 0; i < 800; i++ {
				u, _ := m.Update(tokenMsg("token "))
				m = u.(chatModel)
				if time.Since(last) >= refreshInterval {
					u, _ = m.Update(refreshTickMsg{})
					m = u.(chatModel)
					repaints++
					last = time.Now()
				}
			}
			u, _ := m.Update(refreshTickMsg{}) // the guaranteed final repaint
			m = u.(chatModel)
			runtime.KeepAlive(m)
			return time.Since(start), repaints + 1
		}
		pacedTop, repaintsTop := paced(0)
		pacedDepth, repaintsDepth := paced(400)
		t.Logf("800-token answer, TICK-PACED (what the client actually does): "+
			"%v at 0 prior turns (%d repaints), %v at 400 (%d repaints)",
			pacedTop.Round(time.Millisecond), repaintsTop,
			pacedDepth.Round(time.Millisecond), repaintsDepth)
	}

	if atTop == 0 {
		t.Fatal("the baseline allocated nothing; this measurement is broken")
	}
	if ratio := float64(atDepth) / float64(atTop); ratio > 2 {
		t.Errorf("an 800-token answer at 400 prior turns allocates %.2fx the same answer "+
			"at none (%d vs %d). The target is 2x; above that the per-answer cost is "+
			"still growing with the conversation.", ratio, atDepth, atTop)
	}
}

// FILE DESCRIPTORS, flat across many turns. Each turn opens a fresh connection
// to the daemon -- the wire protocol is one prompt per connection -- so a leak
// here is one descriptor per exchange, invisible until a long session hits the
// process limit and every subsequent turn fails to connect for reasons that
// look like the daemon's fault.
func TestFileDescriptorsDoNotGrowAcrossTurns(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc/self/fd on this platform")
	}
	requests := make(chan protocol.PromptRequest, 256)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	// One turn is one connection: the wire protocol is one prompt per
	// connection, so a descriptor leak here is one per exchange.
	turn := func() {
		ch := make(chan tea.Msg, 32)
		streamPrompt(context.Background(), "fd-test", "", "hello", "", "", "", nil, nil, ch)
		for msg := range ch {
			if _, done := msg.(streamDoneMsg); done {
				return
			}
		}
	}

	for i := 0; i < 5; i++ { // warm: the first connections allocate what they allocate once
		turn()
	}
	before := openFDs(t)
	for i := 0; i < 25; i++ {
		turn()
	}
	after := openFDs(t)

	t.Logf("open file descriptors: %d before 25 turns, %d after", before, after)
	if after > before+2 {
		t.Errorf("25 turns left %d extra file descriptors open (%d -> %d). "+
			"One per turn is a session that eventually cannot connect at all.",
			after-before, before, after)
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
