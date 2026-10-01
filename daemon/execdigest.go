package main

import (
	"fmt"
	"regexp"
	"strings"
)

// A LONG COMMAND OUTPUT KEEPS WHAT MATTERS.
//
// A tool result longer than max_tool_result_bytes is clipped to its FIRST bytes
// (renderToolResult), which for a build or a test run is the wrong end: the
// head is banner and progress, and the verdict -- which tests failed, the
// compiler's error, the stack trace -- is at the end or scattered through the
// middle. A long failing test run therefore reached the model as thirty-two
// kilobytes of passing tests and no failure.
//
// So an output that would be clipped is digested instead: its first lines,
// every line that reports a failure with one line either side, and its last
// lines, in their original order, with each gap counted. An output that fits
// goes through whole, byte for byte, as it always did.

var failureLine = regexp.MustCompile(`(?i)(^\s*--- FAIL|^FAIL\b|^\s*FAIL:|panic:|^\s*error\b|\berror:|\bfailed\b|^E\s|AssertionError|Traceback|^\s*not ok\b|✗|✖)`)

const (
	digestHeadLines   = 15
	digestTailLines   = 80
	digestMaxMarked   = 60
	digestLineRunes   = 400
	digestGapTemplate = "[... %d line(s) left out ...]\n"
)

// execDigestLimit is the size past which a command's output is digested: the
// per-result cap the loop clips every tool result to, less room for the
// result's own first line and the loop's markers.
func (s *Server) execDigestLimit() int {
	limit := defaultMaxToolResultBytes
	if s.cfg != nil {
		limit = s.cfg.MCP.Budget.resolvedMaxToolResultBytes()
	}
	return limit - 1024
}

// commandDigest returns output whole when it is at most limit bytes, and a
// digest of at most about limit bytes otherwise.
func commandDigest(output string, limit int) string {
	if limit <= 0 || len(output) <= limit {
		return output
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	keep := make([]bool, len(lines))
	// Room for the header and for every gap line there could be (one before
	// each kept run: at most one per failure, plus the head and the tail), so
	// the digest never outgrows limit -- if it did, the clip that follows
	// would cut its END, the part this exists to keep.
	budget := limit - 200 - (digestMaxMarked+2)*len(fmt.Sprintf(digestGapTemplate, len(lines)))

	take := func(i int) bool {
		if keep[i] {
			return true
		}
		cost := len(truncateRunes(lines[i], digestLineRunes)) + 1
		if cost > budget {
			return false
		}
		budget -= cost
		keep[i] = true
		return true
	}
	// The end first: it is where a test run's verdict and a build's last
	// error are, and it must survive whatever else does not.
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-digestTailLines; i-- {
		if !take(i) {
			break
		}
	}
	// Then every failure line, in order, with one line either side.
	marked := 0
	for i := range lines {
		if marked >= digestMaxMarked || !failureLine.MatchString(lines[i]) {
			continue
		}
		marked++
		for j := max(0, i-1); j <= min(len(lines)-1, i+1); j++ {
			take(j)
		}
	}
	// Then the start, while room is left.
	for i := 0; i < len(lines) && i < digestHeadLines; i++ {
		if !take(i) {
			break
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[long output, %d lines: showing its start, every line that reports a failure, and its end]\n", len(lines))
	gap := 0
	for i, l := range lines {
		if !keep[i] {
			gap++
			continue
		}
		if gap > 0 {
			fmt.Fprintf(&b, digestGapTemplate, gap)
			gap = 0
		}
		b.WriteString(truncateRunes(l, digestLineRunes))
		b.WriteByte('\n')
	}
	if gap > 0 {
		fmt.Fprintf(&b, digestGapTemplate, gap)
	}
	return b.String()
}
