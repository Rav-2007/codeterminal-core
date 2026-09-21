package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mochiii/protocol"
)

// REGISTER ITEM 35: a language server is stopped once it sits idle.
//
// One lookup used to keep gopls -- 295 MB RSS, measured -- for the daemon's
// whole lifetime, and a polyglot project could hold three. These tests hold the
// definition of "idle" (nothing in flight AND unused for the timeout), prove
// the janitor that acts on it is wired rather than merely written, and prove
// that a stopped server's restart goes back through item 32's launch prompt.
//
// The clock is synthetic wherever it can be: evictIdle takes `now`, so the real
// ten-minute constant is tested without waiting ten minutes.

// A server is kept until the timeout and stopped at it -- the real constant, a
// synthetic clock. Neuter check: compare against twice the timeout, and the
// second call keeps a server that is due.
func TestAServerIsStoppedOnlyAfterTheIdleTimeout(t *testing.T) {
	b, _ := newFakeBridge(t, "ok")
	srv, err := b.GetServer(approvedLaunch("go"), "go")
	if err != nil {
		t.Fatal(err)
	}
	handedOut := time.Now()

	if got := b.evictIdle(handedOut.Add(lspIdleTimeout - time.Second)); len(got) != 0 {
		t.Fatalf("stopped %v a second before the idle timeout", got)
	}
	if !b.Running("go") {
		t.Fatal("the server is gone before its timeout")
	}

	got := b.evictIdle(handedOut.Add(lspIdleTimeout))
	if len(got) != 1 || got[0] != "gopls" {
		t.Fatalf("at the idle timeout evictIdle stopped %v, want [gopls]", got)
	}
	if b.Running("go") || srv.alive() {
		t.Error("the server was reported stopped but is still running")
	}
}

// A SERVER ANSWERING A REQUEST IS BUSY, however long ago it was handed out. The
// fake's "silent" mode accepts a request and never answers, holding one in
// flight for as long as the test likes.
//
// Neuter check: drop the len(pending) test in retireIfIdle and the server is
// killed with a request outstanding.
func TestARequestInFlightIsNeverStoppedForIdleness(t *testing.T) {
	b, _ := newFakeBridge(t, "silent")
	srv, err := b.GetServer(approvedLaunch("go"), "go")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = srv.CallContext(ctx, "textDocument/definition", map[string]any{})
	}()
	t.Cleanup(func() { cancel(); <-done })

	// Wait until the request is actually registered, or the check below proves
	// nothing about in-flight requests.
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.pending)
		srv.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("vacuity: the request never went in flight, so this would test nothing")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := b.evictIdle(time.Now().Add(100 * lspIdleTimeout)); len(got) != 0 {
		t.Fatalf("stopped %v with a request in flight", got)
	}
	if !b.Running("go") {
		t.Error("the server was stopped with a request outstanding")
	}
}

// A call is a use. Neuter check: drop the touch() calls in CallContext and the
// server is stopped two minutes after being asked something.
func TestACallResetsTheIdleClock(t *testing.T) {
	b, _ := newFakeBridge(t, "ok")
	srv, err := b.GetServer(approvedLaunch("go"), "go")
	if err != nil {
		t.Fatal(err)
	}
	// As though it was handed out nine minutes ago and not touched since.
	srv.lastUsed.Store(time.Now().Add(-9 * time.Minute).UnixNano())

	if _, err := srv.Call("textDocument/definition", map[string]any{}); err != nil {
		t.Fatal(err)
	}

	// Eleven minutes after the handout, two after the call.
	if got := b.evictIdle(time.Now().Add(2 * time.Minute)); len(got) != 0 {
		t.Fatalf("stopped %v two minutes after it was last asked something", got)
	}
}

// THE ATOMICITY GUARD. Once retireIfIdle says yes, no new request may be
// admitted -- even in the moment before Close kills the process, while the
// server is still alive and WOULD answer. That is what makes it safe to decide
// "idle" and then close as two steps.
//
// Neuter check: drop the s.closed test at the top of CallContext. The request
// then reaches the still-running fake, which answers it, and this fails.
func TestARetiredServerAdmitsNoNewRequest(t *testing.T) {
	b, _ := newFakeBridge(t, "ok")
	srv, err := b.GetServer(approvedLaunch("go"), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !srv.retireIfIdle(time.Now().Add(2*lspIdleTimeout), lspIdleTimeout) {
		t.Fatal("an unused server was not judged idle")
	}
	// Deliberately NOT closed yet: the process is alive and would answer.
	if _, err := srv.Call("textDocument/definition", map[string]any{}); !errors.Is(err, errServerGone) {
		t.Fatalf("a retired server admitted a request: err = %v, want errServerGone", err)
	}
}

// THE JANITOR IS WIRED, not merely written: with a short timeout the server
// stops by itself, and says so. Neuter check: remove the startJanitor call in
// GetServer and it never stops.
func TestTheJanitorStopsAnIdleServerOnItsOwn(t *testing.T) {
	b, _ := newFakeBridge(t, "ok")
	b.idleTimeout = 100 * time.Millisecond
	var mu sync.Mutex
	var lines []string
	b.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}

	if _, err := b.GetServer(approvedLaunch("go"), "go"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.Running("go") {
		if time.Now().After(deadline) {
			t.Fatal("the server was never stopped for idleness: the janitor is not running")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 || !strings.Contains(lines[0], "stopped gopls") ||
		!strings.Contains(lines[0], "will ask to start it again") {
		t.Errorf("the stop was not logged in words a user can act on: %q", lines)
	}
}

// No goroutine outlives the bridge. Neuter check: stop closing b.stop in Close
// and the janitor never exits.
func TestCloseEndsTheJanitor(t *testing.T) {
	b, _ := newFakeBridge(t, "ok")
	b.idleTimeout = time.Hour // the janitor runs, and has nothing to do
	if _, err := b.GetServer(approvedLaunch("go"), "go"); err != nil {
		t.Fatal(err)
	}
	b.Close()
	select {
	case <-b.janitorDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the janitor goroutine is still running after Close")
	}
}

// THE COMPOSITION WITH ITEM 32. A server stopped for idleness is restarted only
// through a prompt that says STARTS gopls -- never silently, and never on the
// strength of a grant given before it stopped.
func TestAnIdleStopIsFollowedByAFreshLaunchPrompt(t *testing.T) {
	responses := [][]string{
		toolCallSSE("c1", defQualified, defArgs),
		toolCallSSE("c2", defQualified, defArgs),
		textSSE("done"),
	}
	b, home := newFakeBridge(t, "ok")
	stopped := make(chan []string, 1)
	var n atomic.Int64
	base := rawSSEServerFunc(t, func([]byte) []string {
		i := int(n.Add(1)) - 1
		if i == 1 {
			// Between the two calls: the user steps away past the timeout.
			stopped <- b.evictIdle(time.Now().Add(lspIdleTimeout + time.Minute))
			_ = os.Remove(filepath.Join(home, "env.dump"))
		}
		if i >= len(responses) {
			return textSSE("done")
		}
		return responses[i]
	})
	s, _ := launchLoopServerOn(t, base, nil, b)
	appr := &observingApprover{home: home, scriptedApprover: scriptedApprover{answers: []approvalDecision{
		says(protocol.ApprovalApproveForTurn), says(protocol.ApprovalApprove),
	}}}

	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	select {
	case got := <-stopped:
		if len(got) != 1 || got[0] != "gopls" {
			t.Fatalf("the idle stop between calls stopped %v, want [gopls]", got)
		}
	default:
		t.Fatal("vacuity: the idle stop never ran between the calls")
	}
	if len(appr.asked) != 2 || !appr.asked[1].LaunchesSubprocess {
		t.Fatalf("after an idle stop the next call must ask to START gopls again; prompts: %+v", appr.asked)
	}
	if appr.startedWhenAsk[1] {
		t.Error("gopls restarted before the user was asked")
	}
	if !fakeServerStarted(home) {
		t.Error("the approved restart did not start gopls")
	}
}
