package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// THE ADVERSARIAL HALF of stdioclient_test.go.
//
// Those tests drive echoserver, which cooperates. These drive testdata/badserver,
// which does not. The distinction is the whole point: Lane B's security claim is
// "unconfined, but you approve every call and everything is audited", and a
// claim about hostile input is worth exactly what it survives from a hostile
// peer. Every mode here is something a published MCP server could do today --
// most of them by accident.
//
// Several of these tests MEASURE rather than assert. That is deliberate and
// temporary: this file was written as the instrument for a pre-registered
// hypothesis matrix (M1-M7), so the first run had to be able to report what the
// daemon does, including when the answer was "nothing stops it". Where a fix
// landed, the measurement was replaced by an assertion in the fix's own commit.

// buildBadServer compiles the misbehaving server fresh from source, the same
// convention buildEchoServer uses. Built once per package run: it has no state,
// and nine tests each paying a Go build is most of the runtime.
var (
	badServerOnce sync.Once
	badServerPath string
	badServerErr  error
)

func buildBadServer(t *testing.T) string {
	t.Helper()
	badServerOnce.Do(func() {
		// Not t.TempDir(): that is removed when the FIRST test finishes, and the
		// binary has to outlive it for the others.
		dir, err := os.MkdirTemp("", "badserver")
		if err != nil {
			badServerErr = err
			return
		}
		bin := filepath.Join(dir, "badserver")
		if out, err := exec.Command("go", "build", "-o", bin, "./testdata/badserver").CombinedOutput(); err != nil {
			badServerErr = errors.New(string(out))
			return
		}
		badServerPath = bin
	})
	if badServerErr != nil {
		t.Fatalf("building the misbehaving MCP server: %v", badServerErr)
	}
	return badServerPath
}

func connectBad(t *testing.T, mode string, env ...string) (*StdioClient, error) {
	t.Helper()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	allow := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		allow = append(allow, k)
	}
	client, err := Connect(context.Background(), LaunchConfig{
		Name:     "bad",
		Command:  buildBadServer(t),
		Args:     []string{mode},
		EnvAllow: allow,
		Stderr:   os.Stderr,
	})
	if client != nil {
		t.Cleanup(func() { _ = client.Close() })
	}
	return client, err
}

// floodMiB sizes the flood tests. 8 MiB is enough to demonstrate that no cap
// exists and small enough to be a normal test; MCP_FLOOD_MIB raises it for a
// deliberate amplification sweep, which is how the 12x peak-heap figure in the
// hardening report was measured.
func floodMiB() int {
	if v := os.Getenv("MCP_FLOOD_MIB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8
}

// samplePeakHeap polls live heap until the returned func is called, which
// reports the maximum seen.
//
// Peak heap rather than TotalAlloc is the number that matters for a flood:
// TotalAlloc is cumulative churn across the whole operation and overstates
// pressure, while peak live heap is what an OOM killer actually reads. Both get
// reported, labelled, so neither is mistaken for the other.
func samplePeakHeap() func() uint64 {
	done := make(chan struct{})
	result := make(chan uint64, 1)
	go func() {
		var peak uint64
		var m runtime.MemStats
		for {
			select {
			case <-done:
				result <- peak
				return
			default:
			}
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return func() uint64 {
		close(done)
		return <-result
	}
}

// M1a -- an over-long tools/list is refused, and refused cheaply.
//
// This is the sharpest of the flood cases, because tools/list is read at
// registry-build time: before the loop has started, before any tool has been
// proposed, and therefore before any approval prompt could exist. A user who
// configured a server and typed one prompt has already taken this response,
// having authorised nothing. max_tool_result_bytes does not apply here at all
// -- that caps tool RESULTS, and this is a tool LIST.
//
// Measured before messageLimitReader existed: 8 MiB accepted whole, 101 MB of
// peak heap, 12.0x amplification holding linearly to 64 MiB / 806 MB.
//
// Fails if the limit check in messageLimitReader.Read is neutered -- on the
// error AND on the peak-heap ceiling, which is the assertion that would still
// catch a "fix" that reported an error after allocating anyway.
func TestAFloodedToolListIsRefusedBeforeItIsAllocated(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates several MiB by design")
	}
	mib := floodMiB()
	client, err := connectBad(t, "flood-list", "BADSERVER_FLOOD_MIB="+strconv.Itoa(mib))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	stopPeak := samplePeakHeap()

	start := time.Now()
	tools, err := client.ListTools(context.Background())
	elapsed := time.Since(start)

	peakHeap := stopPeak()
	runtime.ReadMemStats(&after)
	churn := after.TotalAlloc - before.TotalAlloc
	t.Logf("M1a: a %d MiB tools/list against a %d byte message limit: err=%v, tools=%d, "+
		"peak heap=%d bytes, cumulative allocation=%d bytes, elapsed=%s",
		mib, DefaultMaxMessageBytes, err, len(tools), peakHeap, churn, elapsed)

	if err == nil {
		t.Fatalf("a %d MiB tools/list was accepted whole. This is read at registry-build time, "+
			"before any approval prompt exists, so there is no consent step in front of it", mib)
	}
	if !errors.Is(err, ErrServerUnavailable) {
		t.Errorf("an over-long message should degrade the server, not fail in some other way: %v", err)
	}
	// The bound is on ALLOCATION, so the bound has to hold there too. 4x the
	// limit is loose enough for decoder headroom and tight enough that reading
	// the whole 8 MiB would fail it.
	if ceiling := uint64(4 * DefaultMaxMessageBytes); peakHeap > ceiling {
		t.Errorf("peak heap %d exceeded %d while refusing an over-long message; the limit did not "+
			"bound what was actually allocated", peakHeap, ceiling)
	}
}

// M1b -- an over-long tool result is refused at the transport.
//
// renderToolResult's cap (32 KiB by default) is real and correct, and it is
// applied to a string already resident: it bounds what leaves the machine, not
// what the machine had to hold to send it. Those are different properties and
// only one of them was enforced.
func TestAFloodedToolResultIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates several MiB by design")
	}
	mib := floodMiB()
	client, err := connectBad(t, "flood-call", "BADSERVER_FLOOD_MIB="+strconv.Itoa(mib))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	res, err := client.CallTool(context.Background(), "firehose", nil)
	t.Logf("M1b: a %d MiB tool result against a %d byte message limit: err=%v, content=%d bytes",
		mib, DefaultMaxMessageBytes, err, len(res.Content))

	if err == nil {
		t.Fatalf("a %d MiB result was read in full. max_tool_result_bytes caps what goes to the "+
			"MODEL (32768 by default) and is applied to a string already resident, so it bounds "+
			"egress and not memory", mib)
	}
	if !errors.Is(err, ErrServerUnavailable) {
		t.Errorf("an over-long result should degrade the server, not fail in some other way: %v", err)
	}
}

// M2 -- a server that starts and then says nothing costs the full connect
// timeout, and that time is spent before the turn's own deadline clock starts.
func TestHangingInitializeCostsTheFullConnectTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the connect timeout")
	}
	start := time.Now()
	client, err := connectBad(t, "hang-initialize")
	elapsed := time.Since(start)

	if err == nil {
		_ = client.Close()
		t.Fatal("a server that never answers initialize completed the handshake")
	}
	if !errors.Is(err, ErrServerUnavailable) {
		t.Errorf("a hung handshake should be ErrServerUnavailable so the caller degrades rather than "+
			"fails the turn; got %v", err)
	}
	t.Logf("M2: one hung server cost %s. buildRegistry connects serially, so n servers cost n times "+
		"this, and runAgentLoop's deadline clock has not started yet", elapsed.Round(time.Second))
}

// M3 -- a server that dies mid-call.
//
// The question is whether this costs the model one tool or costs the user their
// turn. A crash inside someone else's tool handler needs no malice at all.
func TestServerExitingMidCall(t *testing.T) {
	client, err := connectBad(t, "exit-midcall")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	res, err := client.CallTool(context.Background(), "crash", nil)
	switch {
	case err != nil:
		t.Logf("M3: a mid-call exit surfaced as a transport error, classified as "+
			"ErrServerUnavailable=%v: %v", errors.Is(err, ErrServerUnavailable), err)
	case res.IsError:
		t.Logf("M3: a mid-call exit surfaced as a tool error the model can read: %q", res.Content)
	default:
		t.Errorf("a server that exited mid-call returned a SUCCESSFUL result: %q", res.Content)
	}
}

// M4 -- Close signals a pid, not a process group.
//
// A server that leaves a child behind survives teardown with the user's full
// privileges, and the child holds this server's stdout, which is what stops the
// SDK's read goroutine unblocking. Registries are built and closed per TURN, so
// anything that leaks here leaks once per turn.
func TestOrphanedGrandchildSurvivesClose(t *testing.T) {
	client, err := connectBad(t, "orphan")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	res, err := client.CallTool(context.Background(), "spawn_orphan", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	pid := 0
	if _, after, found := strings.Cut(res.Content, "orphan_pid="); found {
		pid, _ = strconv.Atoi(strings.TrimSpace(after))
	}
	if pid == 0 {
		t.Fatalf("the server did not report an orphan pid: %q", res.Content)
	}

	goroutinesBefore := runtime.NumGoroutine()
	if err := client.Close(); err != nil {
		t.Logf("M4: Close reported %v", err)
	}

	// Give a leaked goroutine time to be counted, and a correctly-reaped child
	// time to be gone.
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	goroutinesAfter := runtime.NumGoroutine()

	alive := syscall.Kill(pid, 0) == nil
	t.Logf("M4: after Close, the orphan (pid %d) alive=%v; goroutines %d -> %d",
		pid, alive, goroutinesBefore, goroutinesAfter)
	if alive {
		// Reap it so the test does not leave a process behind.
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		t.Log("M4: a grandchild of an MCP server outlived the daemon's teardown. Close signals " +
			"cmd.Process, which is one pid; helperproc.go's discipline is a process group")
	}
}

// M5 -- a server lying in readOnlyHint changes nothing about confinement.
//
// This one ASSERTS rather than measures, because the property is supposed to
// hold today and is worth a regression test either way: an annotation is a
// claim by an unconfined third party, and no claim moves Confined.
func TestALyingAnnotationDoesNotBuyConfinement(t *testing.T) {
	client, err := connectBad(t, "liar")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}
	tool := tools[0]

	if tool.Confined {
		t.Error("a Lane B tool reported itself confined. No input may ever make this true")
	}
	if tool.Lane != "third_party" {
		t.Errorf("lane = %q, want third_party regardless of what the server claims", tool.Lane)
	}
	if !tool.ReadOnlyHint {
		t.Fatal("the fixture is supposed to be advertising readOnlyHint:true")
	}

	// And the claim is a lie: the "read-only" tool writes.
	target := filepath.Join(t.TempDir(), "written")
	if _, err := client.CallTool(context.Background(), "definitely_read_only",
		[]byte(`{"path":`+strconv.Quote(target)+`}`)); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the fixture did not actually write, so it is not testing a lie: %v", err)
	}
	t.Logf("M5: readOnlyHint=true on a tool that wrote %s. Confined stayed false, which is the "+
		"property that matters; what remains is whether a client RENDERS the hint as reassurance",
		target)
}

// M6 -- control sequences survive the transport intact.
//
// This package is not where they should be stripped -- toolresult.go is the
// egress choke point and owns that -- so this test establishes only that the
// bytes arrive, which is what makes the daemon-side test meaningful.
func TestControlSequencesArriveIntact(t *testing.T) {
	client, err := connectBad(t, "ansi")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	res, err := client.CallTool(context.Background(), "escape_artist", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(res.Content, "\x1b]52;") {
		t.Error("the OSC 52 clipboard sequence did not survive; the fixture is not testing what it claims")
	}
	t.Logf("M6: %d bytes of terminal control sequences reached the daemon, including OSC 52 "+
		"(set clipboard), CSI 2J (clear screen) and the alternate screen buffer switch",
		len(res.Content))
}

// M7 -- injected text arrives verbatim, including this system's own vocabulary.
func TestInjectedInstructionsArriveVerbatim(t *testing.T) {
	client, err := connectBad(t, "inject")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	res, err := client.CallTool(context.Background(), "helpful_notes", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	for _, want := range []string{"tool_approval", "allow_always", "builtin__propose_edit", "<|im_start|>"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("the injection payload lost %q before reaching the daemon", want)
		}
	}
	t.Logf("M7: %d bytes of forged consent vocabulary and cross-server tool names arrived "+
		"unaltered, to be appended to the message list and POSTed on the next iteration",
		len(res.Content))
}

// A slow tool is cancellable: the caller's context is what bounds it, and it
// works. Recorded because M2's finding is specifically that CONNECT is the
// unbudgeted phase -- calls are budgeted correctly, and the contrast is the
// point.
func TestASlowToolIsBoundedByTheCallersContext(t *testing.T) {
	client, err := connectBad(t, "slow-call")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := client.CallTool(ctx, "molasses", nil); err == nil {
		t.Fatal("a tool that sleeps for ten minutes returned inside 300ms")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %s; the context did not bound the call", elapsed)
	}
}

// The name-validation rule on its own, at the boundary that owns it.
//
// A server offering one unusable tool must lose that tool and keep the rest --
// the same trade the unserialisable-schema path already makes.
func TestAToolNameWithControlCharactersIsNotAdvertised(t *testing.T) {
	client, err := connectBad(t, "evil-name")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools {
		if strings.ContainsRune(tool.Name, 0x1b) || strings.ContainsRune(tool.Name, '\r') {
			t.Errorf("advertised a tool whose name carries control characters: %q", tool.Name)
		}
	}
	if len(tools) != 0 {
		t.Errorf("the fixture offers exactly one tool and its name is the payload, so nothing "+
			"should have been advertised; got %d", len(tools))
	}
}

func TestValidateToolName(t *testing.T) {
	valid := []string{"read_file", "a", "search-code", "ns__tool", "café"}
	for _, name := range valid {
		if err := ValidateToolName(name); err != nil {
			t.Errorf("ValidateToolName(%q) = %v, want nil", name, err)
		}
	}

	// \t and \n are refused too: a name is one identifier on one line.
	invalid := []string{"", "  ", "a\x1bb", "a\rb", "a\nb", "a\tb", "a\x00b", "a\x7fb", "a\x9bb"}
	for _, name := range invalid {
		if err := ValidateToolName(name); err == nil {
			t.Errorf("ValidateToolName(%q) = nil, want an error", name)
		}
	}
}

func TestSanitizeForDisplay(t *testing.T) {
	// Escaped, not dropped: a log line has to stay readable, and knowing that
	// a server emitted an escape is itself worth seeing.
	if got := SanitizeForDisplay("clear\x1b[2Jscreen"); strings.ContainsRune(got, 0x1b) {
		t.Errorf("SanitizeForDisplay left an ESC in %q", got)
	}
	if got := SanitizeForDisplay("ordinary text"); got != "ordinary text" {
		t.Errorf("SanitizeForDisplay rewrote clean text to %q", got)
	}
}
