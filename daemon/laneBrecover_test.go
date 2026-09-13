package main

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// MEMO ITEM 5 -- panic containment stops at the connection goroutine.
// VERDICT: FIX OURS ONLY, by the daemon/ owner, 2026-09-13.
//
// recover() is per-goroutine. handleConn's backstop covers the connection
// goroutine and nothing else, and the Lane B connect fan-out runs each
// mcp.Connect on a goroutine of its own. A panic decoding an untrusted
// subprocess's first bytes therefore took the whole DAEMON down, not the one
// connection that asked for it -- every other workspace and every in-flight
// turn with it.
//
// THE SDK'S FOUR ARE DELIBERATELY NOT COVERED HERE. go-sdk@v1.7.0/mcp contains
// zero recover() calls, across its stdio decoder, readIncoming, and async
// handler. Those cannot be fixed from outside the dependency, and the choice
// between vendoring a patch, filing upstream, and accepting is a
// dependency-policy call rather than a daemon one. It stays open as a register
// row with its honest unknown: whether any input actually panics go-sdk was
// never established, and establishing it means fuzzing a third-party parser.
//
// This test drives the property through a helper rather than through a real
// panicking MCP server, because a server that panics the SDK is exactly the
// thing nobody has been able to construct. What is asserted is OUR half: a
// panic raised inside the fan-out body is contained, the WaitGroup is still
// released, and the caller proceeds.
func TestLaneBConnect_PanicIsContainedAndTheWaitGroupIsReleased(t *testing.T) {
	var wg sync.WaitGroup
	var logged strings.Builder
	var mu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer recoverLaneBConnect("badserver", func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			logged.WriteString(sprintfLike(format, args...))
		})
		panic("decoder blew up on the first frame")
	}()

	// If containment fails the panic unwinds past Wait and the test binary dies,
	// which is itself the failure signal -- a crashed process is not a green
	// test. Reaching the line after Wait is the assertion.
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	got := logged.String()
	if got == "" {
		t.Fatal("vacuity floor: nothing was logged, so a contained panic would be silent — " +
			"which is indistinguishable from a server that simply did not connect")
	}
	if !strings.Contains(got, "badserver") {
		t.Errorf("the log does not name which server panicked: %q", got)
	}
	if !strings.Contains(got, "decoder blew up") {
		t.Errorf("the log does not carry the panic value: %q", got)
	}
}

// THE WIRING, asserted separately from the behaviour.
//
// The test above drives recoverLaneBConnect directly, so it proves the helper
// contains a panic and says nothing about whether the fan-out actually installs
// it. Deleting the `defer` in mcpruntime.go would leave that test green and the
// daemon uncontained -- the exact gap between "a control exists" and "a control
// is applied" that this pass keeps finding.
//
// Comments are stripped first (stripGoComments, socketauthcoverage_test.go) for
// the reason that file gives: the explanatory comment beside the defer mentions
// the function by name, and a guard that reads prose is not reading code.
func TestLaneBConnect_FanOutInstallsTheRecover(t *testing.T) {
	raw, err := os.ReadFile("mcpruntime.go")
	if err != nil {
		t.Fatalf("reading mcpruntime.go: %v", err)
	}
	code := stripGoComments(string(raw))
	if len(code) < len(raw)/4 {
		t.Fatalf("vacuity floor: stripping left %d of %d bytes; the check below would inspect almost nothing",
			len(code), len(raw))
	}
	if !strings.Contains(code, "defer recoverLaneBConnect(") {
		t.Error("the Lane B connect fan-out does not install recoverLaneBConnect. " +
			"recover() is per-goroutine: without the defer ON THAT GOROUTINE, a panic " +
			"decoding an untrusted subprocess ends the daemon, and handleConn's backstop " +
			"cannot see it.")
	}
}
