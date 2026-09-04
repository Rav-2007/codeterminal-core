package main

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"codeterminal/protocol"
)

// NOTHING OUTLIVES THE SESSION (I5), CHECKED RATHER THAN ASSERTED IN A COMMENT.
//
// The TUI starts goroutines in four places: the signal listener
// (exitsignals_unix.go), streamPrompt and resetHistoryOnDaemon (stream.go), and
// the context watchdog inside streamPrompt that closes the socket on cancel.
// Every one of them is parked on a channel or a socket for most of its life,
// which is exactly the shape that leaks quietly: a parked goroutine costs a
// stack and a socket and produces no symptom until a long session has
// accumulated hundreds.
//
// One such leak is already in the record -- streamPrompt parking forever on a
// send to a UI that stopped reading, one goroutine per interrupt, for the life
// of the session (TestStreamPromptExitsWhenAStoppedUIQuitsReading). It was
// found because someone added a stop key and went looking, not because anything
// failed. This is the gate that would have failed.
//
// WRITTEN HERE RATHER THAN TAKEN FROM go.uber.org/goleak, deliberately. goleak
// is the better library and this is a smaller thing than goleak, but adding a
// dependency to the client that talks to a socket and draws a terminal is a
// decision with a supply-chain cost, and what is needed here is roughly eighty
// lines: snapshot goroutine ids, run the work, and name what is new and still
// running. Task 2.5 asks for "goleak or a goroutine-count assertion"; this is
// the second.

// goroutineSnapshot is the set of goroutine ids alive right now. Ids are unique
// and never reused, so anything absent from a snapshot and present later was
// started in between -- which is a stronger statement than a count, and one a
// count cannot make when something exits while something else starts.
type goroutineSnapshot map[int]bool

func snapshotGoroutines() goroutineSnapshot {
	s := goroutineSnapshot{}
	for _, block := range goroutineBlocks() {
		if id, ok := goroutineID(block); ok {
			s[id] = true
		}
	}
	return s
}

func goroutineBlocks() []string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Split(strings.TrimSpace(string(buf[:n])), "\n\n")
		}
		if len(buf) >= 1<<24 {
			return strings.Split(string(buf), "\n\n")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// goroutineID reads the id out of a "goroutine 17 [running]:" header. The
// runtime also emits headers like "goroutine 0 gp=0x... [idle]:" for its own
// threads, which is why this parses the second field rather than assuming a
// shape -- an earlier version of a very similar parse missed exactly those and
// reported a dump as empty.
func goroutineID(block string) (int, bool) {
	line, _, _ := strings.Cut(block, "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "goroutine" {
		return 0, false
	}
	id, err := strconv.Atoi(fields[1])
	return id, err == nil
}

// ignoredGoroutines are the ones a leak check must not count: the test
// framework's own, and Go's single signal-delivery loop, which os/signal starts
// on the first Notify and never stops.
var ignoredGoroutines = []string{
	"testing.tRunner",
	"testing.(*T).Run",
	"testing.runTests",
	"testing.(*M).Run",
	"os/signal.loop",
	"os/signal.signal_recv",
	"runtime.gcBgMarkWorker",
	"runtime.bgsweep",
	"runtime.bgscavenge",
	"runtime.forcegchelper",
	"runtime.runfinq",
	"created by runtime.init",
}

// assertNoNewGoroutines fails if anything started since before is still running.
//
// It POLLS rather than sampling once. A goroutine on its way out is not a leak,
// and the difference between "leaked" and "had not finished yet" is a few
// milliseconds of scheduling, so a single sample would make this flaky in the
// direction that gets a gate deleted.
func assertNoNewGoroutines(t *testing.T, before goroutineSnapshot) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var leaked []string
	for {
		leaked = newGoroutinesSince(before)
		if len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(leaked) == 0 {
		return
	}
	t.Errorf("%d goroutine(s) outlived the work that started them:\n\n%s",
		len(leaked), strings.Join(leaked, "\n\n"))
}

func newGoroutinesSince(before goroutineSnapshot) []string {
	var leaked []string
	for _, block := range goroutineBlocks() {
		id, ok := goroutineID(block)
		if !ok || before[id] {
			continue
		}
		if ignoredGoroutine(block) {
			continue
		}
		leaked = append(leaked, block)
	}
	return leaked
}

func ignoredGoroutine(block string) bool {
	for _, frame := range ignoredGoroutines {
		if strings.Contains(block, frame) {
			return true
		}
	}
	return false
}

// THE DETECTOR ITSELF IS TESTED, because a leak checker that cannot see a leak
// is worse than none: it turns an unexamined risk into a checked one on paper.
// This starts a goroutine parked on a channel nobody will ever send to -- the
// exact shape of the streamPrompt leak -- and requires the checker to name it.
func TestTheGoroutineCheckerActuallySeesALeak(t *testing.T) {
	before := snapshotGoroutines()

	release := make(chan struct{})
	go func() { <-release }()
	defer close(release)

	fake := &testing.T{}
	assertNoNewGoroutines(fake, before)
	if !fake.Failed() {
		t.Fatal("a goroutine parked forever on a channel was not reported as a leak")
	}
}

// And it must not report a goroutine that simply finished.
func TestTheGoroutineCheckerIgnoresWorkThatFinished(t *testing.T) {
	before := snapshotGoroutines()
	done := make(chan struct{})
	go func() { close(done) }()
	<-done
	assertNoNewGoroutines(t, before)
}

// ---------------------------------------------------------------------------
// The real lifecycles.

// A turn that runs to completion must leave nothing behind: not streamPrompt,
// and not the context watchdog it starts to close the socket on cancel.
func TestACompletedTurnLeavesNoGoroutineBehind(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 8)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	before := snapshotGoroutines()

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 16)
	go streamPrompt(ctx, "test-client", "", "hello", "", "", "", nil, nil, ch)

	deadline := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case msg := <-ch:
			if _, ok := msg.(streamDoneMsg); ok {
				done = true
			}
		case <-deadline:
			t.Fatal("the turn never finished")
		}
	}
	cancel()
	assertNoNewGoroutines(t, before)
}

// AND THE CASE THAT ACTUALLY LEAKED. The UI stops reading ch the moment a turn
// is interrupted, so a send in streamPrompt has nobody to hand a token to. Once
// that was a goroutine parked for the life of the session, one per interrupt.
func TestAnInterruptedTurnLeavesNoGoroutineBehind(t *testing.T) {
	lockPath, cleanup := fakeDaemonStreamingForever(t)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	before := snapshotGoroutines()

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg) // unbuffered, exactly as startTurn makes it
	go streamPrompt(ctx, "test-client", "", "hello", "", "", "", nil, nil, ch)

	select {
	case <-ch: // the stream is genuinely flowing
	case <-time.After(5 * time.Second):
		t.Fatal("the fake daemon never streamed anything")
	}
	// Nobody reads ch from here. Let the socket buffer fill so streamPrompt is
	// parked on the SEND rather than on the wire when the cancel lands.
	time.Sleep(200 * time.Millisecond)
	cancel()

	assertNoNewGoroutines(t, before)
}

// The signal listener is joined by finish(), which is what makes "no goroutine
// outlives the session" true on the exit path rather than merely likely.
func TestTheSignalListenerDoesNotOutliveTheSession(t *testing.T) {
	before := snapshotGoroutines()
	finish := installExitSignals(func() {})
	finish()
	assertNoNewGoroutines(t, before)
}
