package tui

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mochiii/protocol"
)

// A LAUNCH STARTS A NEW CHAT (freshchat.go). The owner, 2026-11-14: "every
// time it is just opening again and again. It should be fresh."

var heldChat = []protocol.Turn{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}

// What the daemon was holding is erased, and the screen opens with nothing in
// it, level with the new chat's revision.
//
// Neuter check: make startFresh return held unchanged -- the chat comes back.
func TestFreshStart_ErasesWhatTheDaemonHolds(t *testing.T) {
	f := serveChatDaemon(t, unsavedChat())
	f.resetRev = "r2"
	turns, rev := startFresh("test-client", heldChat, "r1")
	if len(turns) != 0 {
		t.Errorf("the launch still has %d turns of the old chat", len(turns))
	}
	if rev != "r2" {
		t.Errorf("revision = %q, want the new chat's r2", rev)
	}
	if _, resets := f.counts(); resets != 1 {
		t.Errorf("the daemon was reset %d times, want once", resets)
	}
}

// An empty daemon is not troubled.
func TestFreshStart_AsksNothingWhenNothingIsHeld(t *testing.T) {
	f := serveChatDaemon(t, nil)
	turns, rev := startFresh("test-client", nil, "r1")
	if len(turns) != 0 || rev != "r1" {
		t.Errorf("turns=%d rev=%q; want the launch left as it was", len(turns), rev)
	}
	if _, resets := f.counts(); resets != 0 {
		t.Errorf("an empty chat was reset %d times", resets)
	}
}

// IF THE ERASE FAILS, THE CHAT IS SHOWN AS IT WAS: the daemon would answer from
// it whatever the screen said.
//
// Neuter check: return nil on the error path -- a blank screen over a full chat.
func TestFreshStart_AFailedEraseKeepsTheChatOnScreen(t *testing.T) {
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	turns, rev := startFresh("test-client", heldChat, "r1")
	if len(turns) != len(heldChat) || rev != "r1" {
		t.Errorf("turns=%d rev=%q; want the chat and its revision untouched", len(turns), rev)
	}
}

// A request that never answers costs a launch, or a quit, only the deadline.
//
// The slow request is a function the test controls, not a daemon that goes
// silent: withDeadline abandons its worker on a timeout BY DESIGN, and a worker
// left running into the test's cleanup raced it (found by -race, 2026-11-14:
// the abandoned goroutine was still reading the lockfile path the cleanup was
// restoring). Here the test waits for the worker, so nothing outlives it.
//
// Neuter check: make withDeadline wait for f -- the call takes as long as the
// request does, and the elapsed-time check fails.
func TestWithDeadline_GivesUpOnARequestThatDoesNotAnswer(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	slow := func() (string, error) {
		defer close(finished)
		<-release
		return "late", nil
	}

	start := time.Now()
	rev, err := withDeadline(100*time.Millisecond, slow)
	took := time.Since(start)
	if !errors.Is(err, errResetTimeout) || rev != "" {
		t.Errorf("got (%q, %v); want no answer and errResetTimeout", rev, err)
	}
	if took > 2*time.Second {
		t.Errorf("gave up after %v, want about the 100ms deadline", took)
	}
	close(release)
	<-finished // the abandoned worker is done before the test is
}

// An answer that arrives in time is returned as it is, and so is an error.
func TestWithDeadline_ReturnsWhatTheRequestAnswers(t *testing.T) {
	if rev, err := withDeadline(time.Second, func() (string, error) { return "r7", nil }); rev != "r7" || err != nil {
		t.Errorf("got (%q, %v), want (r7, nil)", rev, err)
	}
	boom := errors.New("boom")
	if _, err := withDeadline(time.Second, func() (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Errorf("got %v, want the request's own error", err)
	}
}
