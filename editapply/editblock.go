package editapply

import (
	"fmt"
	"strings"
)

// Markers delimiting one SEARCH/REPLACE edit block, as instructed in
// daemon/prompts/system.txt. The path line must appear on the line
// immediately before searchMarker.
const (
	pathPrefix      = "path:"
	searchMarker    = "<<<<<<< SEARCH"
	separatorMarker = "======="
	replaceMarker   = ">>>>>>> REPLACE"
)

// EditBlock is one parsed SEARCH/REPLACE instruction targeting a single file.
type EditBlock struct {
	FilePath string
	Search   string
	Replace  string
}

// ParseEditBlocks scans a completed model response for SEARCH/REPLACE edit
// blocks and returns them in order. A response with no blocks is valid and
// returns an empty, nil-error result — plain-text answers are expected to
// contain none. Malformed blocks (missing path line, unterminated markers,
// ambiguous dividers) produce a descriptive error naming the line number; the
// parser never panics on malformed input.
//
// A block with an EMPTY SEARCH section is well-formed: it is the create
// instruction, and it parses into a block whose Search is empty for the engine
// to act on (Fix A). Rejecting it here is what kept file creation unreachable
// from every shipped client even after the engine grew the capability.
func ParseEditBlocks(response string) ([]EditBlock, error) {
	lines := strings.Split(response, "\n")

	var blocks []EditBlock
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) != searchMarker {
			i++
			continue
		}
		lineNum := i + 1

		if i == 0 {
			return nil, fmt.Errorf("line %d: %q with no preceding %q line", lineNum, searchMarker, pathPrefix)
		}
		path, ok := parsePathLine(lines[i-1])
		if !ok {
			return nil, fmt.Errorf("line %d: missing %q line immediately before %q", lineNum, pathPrefix, searchMarker)
		}
		if path == "" {
			return nil, fmt.Errorf("line %d: %q line has an empty path", lineNum, pathPrefix)
		}

		// Find this block's OWN terminator first, then look for the divider
		// strictly inside it (Fix 4). The scan used to run the other way round —
		// take the first "=======" anywhere after the SEARCH marker, then find a
		// REPLACE marker after that — which cut the block at the first separator
		// line even when that line was part of the content being searched for.
		// The result was a block that looked perfectly well-formed while
		// carrying the wrong SEARCH and the wrong REPLACE, and it applied
		// cleanly: silent file corruption, the one failure this parser must
		// never produce.
		replEndIdx, hitNextBlock := scanUntil(lines, i+1, replaceMarker)
		if replEndIdx == -1 || hitNextBlock {
			return nil, fmt.Errorf("line %d: unterminated block (no %q found before end of response)", lineNum, replaceMarker)
		}

		seps := findSeparators(lines, i+1, replEndIdx)
		switch len(seps) {
		case 0:
			return nil, fmt.Errorf("line %d: unterminated SEARCH block (no %q found before %q)", lineNum, separatorMarker, replaceMarker)
		case 1:
			// Unambiguous: exactly one divider between the markers. This is
			// also the ONLY shape a create block may take (Fix A) — see the
			// empty-SEARCH note below the switch.
		default:
			// Genuinely undecidable HERE. Whether a given "=======" is the
			// divider or content depends on the file the block targets, and
			// this parser deliberately does no I/O — it is called on a raw
			// model response, before any path has been resolved. Guessing is
			// what produced the corruption, so refuse, and name the colliding
			// lines so the caller can re-issue a SEARCH that avoids them.
			return nil, fmt.Errorf(
				"line %d: ambiguous block — %d %q lines before %q (lines %s); cannot tell the divider from content, so refusing rather than guessing. Re-issue with a SEARCH section that does not contain a bare %q line",
				lineNum, len(seps), separatorMarker, replaceMarker, formatLineNumbers(seps), separatorMarker)
		}
		sepIdx := seps[0]

		// An empty SEARCH section is an explicit CREATE intent, and is passed
		// through to the engine, which decides what it means against the file
		// on disk (see IsEmptySearch and PrepareEdit: write REPLACE as the whole
		// content, creating the file if absent, refusing if it is already there
		// with content in it). The parser used to reject it outright, which made
		// the entire creation capability unreachable from every shipped client —
		// the CLI, the daemon's EditProposals, and the TUI all enter here.
		//
		// This does NOT weaken the Fix-4 collision refusal, because it changes
		// nothing about how the divider is chosen. The create reading is only
		// ever reached through the single-separator case above: with exactly one
		// divider in the block, an empty SEARCH has precisely one reading. A
		// block whose first separator sits immediately after the SEARCH marker
		// but which carries FURTHER separators before its REPLACE marker stays
		// ambiguous — it could be a create whose content contains a bare
		// "=======", or an edit whose SEARCH begins with one — and the default
		// branch above still refuses it rather than guessing. That refusal is
		// now per-block (Fix B), so it costs the response's other blocks
		// nothing; the cost is that creating a file whose content contains a
		// bare seven-character "=======" line is refused, not supported.
		search := strings.Join(lines[i+1:sepIdx], "\n")

		replace := strings.Join(lines[sepIdx+1:replEndIdx], "\n")

		blocks = append(blocks, EditBlock{FilePath: path, Search: search, Replace: replace})
		i = replEndIdx + 1
	}

	return blocks, nil
}

// parsePathLine extracts the path from a line like "path: some/file.go".
func parsePathLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, pathPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, pathPrefix)), true
}

// findSeparators returns the indexes of every separator line in [start, end).
// The comparison is exact against a trimmed line, so only a bare "=======" of
// exactly seven characters counts — the setext underlines, rule lines and
// changelog dividers that occur in real documentation are other lengths and
// remain ordinary content.
func findSeparators(lines []string, start, end int) []int {
	var found []int
	for j := start; j < end; j++ {
		if strings.TrimSpace(lines[j]) == separatorMarker {
			found = append(found, j)
		}
	}
	return found
}

// formatLineNumbers renders 0-indexed line positions as the 1-indexed numbers
// the rest of this parser's errors use.
func formatLineNumbers(idxs []int) string {
	parts := make([]string, len(idxs))
	for i, idx := range idxs {
		parts[i] = fmt.Sprintf("%d", idx+1)
	}
	return strings.Join(parts, ", ")
}

// scanUntil returns the index of the first line at or after start that
// equals marker once trimmed. If a new block's searchMarker line is found
// first instead, it returns that index with hitNextBlock set, so the caller
// can report the current block as unterminated instead of silently
// attributing the next block's markers to it. Returns -1 if neither is
// found before the end of lines.
func scanUntil(lines []string, start int, marker string) (idx int, hitNextBlock bool) {
	for j := start; j < len(lines); j++ {
		switch strings.TrimSpace(lines[j]) {
		case marker:
			return j, false
		case searchMarker:
			return j, true
		}
	}
	return -1, false
}
