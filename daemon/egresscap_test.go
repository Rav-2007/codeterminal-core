package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// S3 / F-03: THE EGRESS CAP MUST NOT INVERT INTO "NO LIMIT".
//
// dispatchToolCall computed the per-result cap as
// min(maxResultBytes, maxTotalToolByte - toolBytes) with no floor. Once the
// turn's cumulative budget was spent the second term went negative, and
// renderToolResult only truncates when maxBytes > 0 -- so truncation switched
// OFF at exactly the moment the ceiling was reached. Measured on shipped
// defaults: 2,131,139 bytes sent in ONE iteration against a budget of 131,072,
// a 16x overrun.
//
// budgetStop cannot cover it: it runs between ITERATIONS, and one iteration may
// contain many tool calls, so the overrun happens inside the batch.

// The property, stated over the whole domain rather than at one point: for any
// amount already spent -- including more than the budget -- what a single result
// may emit is bounded.
func TestNoAmountAlreadySpentTurnsOffTruncation(t *testing.T) {
	const (
		maxResult = 32768
		maxTotal  = 131072
	)
	huge := strings.Repeat("x", 1_000_000)

	for _, spent := range []int{0, 1000, maxTotal - 1, maxTotal, maxTotal + 1, maxTotal * 16} {
		remaining := maxTotal - spent
		if remaining < 0 {
			remaining = 0
		}
		capped := maxResult
		if remaining < capped {
			capped = remaining
		}
		if capped <= 0 {
			continue // the withheld branch: nothing is rendered at all
		}
		_, _, emitted := renderToolResult(huge, capped, false, false)
		if emitted > maxResult+truncationNoticeSlack {
			t.Errorf("spent=%d: emitted %d bytes against a per-result cap of %d",
				spent, emitted, maxResult)
		}
	}
}

// truncationNoticeSlack is the notice renderToolResult appends after clipping.
// It is correct behaviour, not overrun -- calls 1 to 4 in the measured probe
// each emitted cap+67.
const truncationNoticeSlack = 512

// END TO END, and specifically with MANY CALLS IN ONE ITERATION -- which is the
// shape budgetStop cannot cover, because it runs between iterations and the
// overrun happens inside the batch. Six calls arrive in a single assistant
// message, exactly as parallel tool calling delivers them.
func TestManyLargeResultsInOneIterationRespectTheTurnBudget(t *testing.T) {
	const (
		fileSize = 60000
		maxTotal = 100000
	)
	var names []string
	for i := 1; i <= 6; i++ {
		names = append(names, fmt.Sprintf("big%d.txt", i))
	}

	base, _, _ := agentUpstream(t,
		parallelReadSSE(names),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		Budget: MCPBudgetConfig{
			MaxIterations: 10, MaxToolResultBytes: 32768, MaxTotalToolBytes: maxTotal,
		},
	})
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(s.workspace, n), []byte(strings.Repeat("x", fileSize)), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
	}

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	total := 0
	for _, a := range activity {
		total += a.ResultBytes
	}
	t.Logf("six %d-byte results in ONE iteration emitted %d bytes against a %d budget",
		fileSize, total, maxTotal)
	if total > maxTotal+truncationNoticeSlack {
		t.Fatalf("sent %d bytes of tool output against max_total_tool_bytes=%d -- the cap "+
			"stopped being enforced once it was exceeded", total, maxTotal)
	}
}

// parallelReadSSE is one assistant message carrying N tool calls, the way a
// model that supports parallel tool calling actually replies. toolCallSSE emits
// a single call at index 0, so it can only ever produce one call per iteration
// -- which is why a bug that lives INSIDE a batch was invisible to every test
// built on it.
func parallelReadSSE(paths []string) []string {
	var lines []string
	for i, p := range paths {
		args, _ := json.Marshal(fmt.Sprintf(`{"path":%q}`, p))
		lines = append(lines, fmt.Sprintf(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":%d,"id":"p%d","type":"function","function":{"name":"builtin__read_file","arguments":%s}}]}}]}`,
			i, i, string(args)))
	}
	lines = append(lines,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`)
	return lines
}

// ---------------------------------------------------------------------------
// sandbox_exec output, bounded AT SOURCE.
// ---------------------------------------------------------------------------

// cmd.CombinedOutput() grows a buffer until the process stops. The downstream
// egress cap bounds what reaches the MODEL -- the privacy quantity -- but not
// what this daemon ALLOCATES, and a build can produce output at whatever rate
// it likes for the full 30-second timeout.
func TestCommandOutputIsBoundedAtSource(t *testing.T) {
	var b tailBuffer
	b.max = 1000

	for i := 0; i < 100; i++ {
		if _, err := b.Write([]byte(strings.Repeat("a", 500))); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if len(b.buf) > b.max {
		t.Fatalf("buffer holds %d bytes against a cap of %d", len(b.buf), b.max)
	}
	if !strings.Contains(b.String(), "dropped") {
		t.Error("dropped output was not disclosed, so a truncated build reads as a complete one")
	}
}

// A single write larger than the whole cap must not blow past it either -- that
// is one `go test -v` line away from realistic.
func TestOneOversizedWriteIsStillBounded(t *testing.T) {
	var b tailBuffer
	b.max = 100
	if _, err := b.Write([]byte(strings.Repeat("z", 50000))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(b.buf) > b.max {
		t.Fatalf("a single %d-byte write left %d bytes buffered against a cap of %d", 50000, len(b.buf), b.max)
	}
}

// THE TAIL IS KEPT, NOT THE HEAD. A build's error is at the end; the head is
// banner text and dependency resolution. Keeping the head would reliably discard
// the only part the model needs.
func TestTheEndOfTheOutputIsWhatSurvives(t *testing.T) {
	var b tailBuffer
	b.max = 20
	if _, err := b.Write([]byte("banner banner banner ERROR: the real failure")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(b.String(), "the real failure") {
		t.Fatalf("the tail was discarded; kept %q", b.String())
	}
}

// exec.Cmd kills the process with io.ErrShortWrite if a writer reports fewer
// bytes than it was given, so a bounding writer must report the full length.
func TestTheBoundedWriterNeverReportsAShortWrite(t *testing.T) {
	var b tailBuffer
	b.max = 10
	n, err := b.Write([]byte(strings.Repeat("q", 999)))
	if err != nil || n != 999 {
		t.Fatalf("Write returned (%d, %v); a short write makes exec.Cmd kill the command", n, err)
	}
}

// THE HANDLER MUST ACTUALLY WIRE THE CAP, which the tailBuffer unit tests above
// cannot show -- they construct their own buffer, so they pass whether or not
// builtinSandboxExec uses one.
//
// This drives the real tool: a real Makefile, through the real allow-list, the
// real sandbox selection and the real subprocess. It therefore also answers the
// question the unit tests never do -- whether sandbox_exec runs at all.
func TestTheSandboxExecHandlerBoundsARealCommandsOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real subprocess")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}

	s := builtinTestServer(t)
	s.logger = discardLogger()

	// Two megabytes, against a 1 MiB cap. `yes` is in coreutils; head bounds it
	// so the recipe cannot run away if the cap is broken.
	makefile := "spew:\n\t@yes 0123456789012345678901234567890123456789 | head -c 2000000\n"
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatalf("writing Makefile: %v", err)
	}

	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make spew"}`))
	if err != nil {
		t.Fatalf("builtinSandboxExec returned a Go error rather than a tool result: %v", err)
	}
	if res.Content == "" {
		t.Fatal("the command produced no output at all, so this test proves nothing about the cap")
	}
	if len(res.Content) > execMaxOutputBytes+4096 {
		t.Fatalf("the handler returned %d bytes from a 2 MB command against a cap of %d",
			len(res.Content), execMaxOutputBytes)
	}
	t.Logf("a 2 MB command returned %d bytes (cap %d)", len(res.Content), execMaxOutputBytes)
}
