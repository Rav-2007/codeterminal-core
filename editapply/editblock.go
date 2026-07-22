package editapply

import (
	"fmt"
	"strings"
)

// Markers delimiting one SEARCH/REPLACE edit block, as instructed in
// daemon/prompts/system.txt. The path line must appear before searchMarker,
// separated from it by nothing more than blank lines or a markdown code fence
// (see findPathLine).
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

// BlockError is one block the parser refused, carrying the same descriptive,
// line-numbered reason ParseEditBlocks used to return as its whole-response
// error. Callers MUST surface these: a refused block is a block the user asked
// for and did not get, and dropping it silently is the failure mode this type
// exists to make impossible.
type BlockError struct {
	Line   int    // 1-indexed line of the block's SEARCH marker
	Reason string // already begins "line N: ", matching the parser's convention
}

func (e BlockError) Error() string { return e.Reason }

// ParseEditBlocks scans a completed model response for SEARCH/REPLACE edit
// blocks and returns the ones it could read, in order, alongside one
// BlockError per block it refused. A response with no blocks at all is valid
// and returns two empty results — plain-text answers are expected to contain
// none. The parser never panics on malformed input.
//
// Recovery is PER BLOCK (Fix B). A malformed block used to fail the whole
// response: a two-block reply carrying one good edit and one bad block applied
// neither, so a model's mistake in one hunk silently threw away work the user
// had already been shown in another. Each block is now parsed independently,
// and the scan resumes past the bad one — a refusal costs that block and
// nothing else.
//
// Both return values matter, and a caller that reads only the first is wrong:
// blocks and rejections can be non-empty at the same time, and an empty block
// list with a non-empty rejection list is a failure, not a plain-text answer.
//
// A block with an EMPTY SEARCH section is well-formed: it is the create
// instruction, and it parses into a block whose Search is empty for the engine
// to act on (Fix A). Rejecting it here is what kept file creation unreachable
// from every shipped client even after the engine grew the capability.
func ParseEditBlocks(response string) ([]EditBlock, []BlockError) {
	lines := strings.Split(response, "\n")

	var blocks []EditBlock
	var rejected []BlockError
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) != searchMarker {
			i++
			continue
		}
		block, resume, err := parseBlockAt(lines, i)
		if err != nil {
			rejected = append(rejected, BlockError{Line: i + 1, Reason: err.Error()})
		} else {
			blocks = append(blocks, block)
		}
		i = resume
	}

	return blocks, rejected
}

// parseBlockAt reads the single block whose searchMarker is at lines[i].
//
// It ALWAYS returns a resume index strictly greater than i, on the error path
// as much as the success path — that is what makes per-block recovery possible
// and what keeps the caller's loop finite. Resuming is deliberately structural
// rather than heuristic: past this block's own REPLACE marker when it has one,
// at the next block's SEARCH marker when this one ran into it, and at the end
// of the response when neither exists. A malformed block therefore never
// consumes a well-formed sibling, and never re-parses one either.
//
// The refusal reasons and their precedence are exactly what the whole-response
// parser produced; only their blast radius changed.
func parseBlockAt(lines []string, i int) (EditBlock, int, error) {
	lineNum := i + 1

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
	resume := len(lines)
	switch {
	case hitNextBlock:
		resume = replEndIdx // this index IS the next block's SEARCH marker
	case replEndIdx != -1:
		resume = replEndIdx + 1
	}

	path, found, empty := findPathLine(lines, i)
	if !found {
		return EditBlock{}, resume, fmt.Errorf("line %d: missing %q line before %q", lineNum, pathPrefix, searchMarker)
	}
	if empty {
		return EditBlock{}, resume, fmt.Errorf("line %d: %q line has an empty path", lineNum, pathPrefix)
	}

	if replEndIdx == -1 || hitNextBlock {
		return EditBlock{}, resume, fmt.Errorf("line %d: unterminated block (no %q found before end of response)", lineNum, replaceMarker)
	}

	seps := findSeparators(lines, i+1, replEndIdx)
	switch len(seps) {
	case 0:
		return EditBlock{}, resume, fmt.Errorf("line %d: unterminated SEARCH block (no %q found before %q)", lineNum, separatorMarker, replaceMarker)
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
		return EditBlock{}, resume, fmt.Errorf(
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

	return EditBlock{FilePath: path, Search: search, Replace: replace}, resume, nil
}

// findPathLine locates the path line belonging to the block whose SEARCH
// marker is at lines[i], scanning BACKWARDS from it. found reports whether a
// path line was there at all; empty reports that one was found but named
// nothing.
//
// It scans back rather than reading lines[i-1] because models routinely put
// something harmless in between (Fix 14). The rule used to be "the path line
// must be the line IMMEDIATELY before SEARCH", and every violation of it cost
// the user a real edit the model had correctly produced:
//
//	path: server.go            path: server.go
//	                           ```go
//	<<<<<<< SEARCH             <<<<<<< SEARCH
//
// Neither of those is a different intent — they are the same edit with a blank
// line or a markdown fence in the way. Only lines that carry no instruction of
// their own are skipped (see ignorableBeforeSearch), so the scan still stops
// dead at a previous block's REPLACE marker, at prose, or at anything else
// meaningful: a block genuinely missing its path line is still refused.
func findPathLine(lines []string, i int) (path string, found, empty bool) {
	for j := i - 1; j >= 0; j-- {
		if p, ok := parsePathLine(lines[j]); ok {
			return p, true, p == ""
		}
		if !ignorableBeforeSearch(lines[j]) {
			return "", false, false
		}
	}
	return "", false, false
}

// ignorableBeforeSearch reports whether a line between the path line and the
// SEARCH marker carries no instruction of its own: a blank line, or a markdown
// code-fence line. These are formatting the model wrapped its answer in, not
// content, and neither changes what the block means.
func ignorableBeforeSearch(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// pathLinePrefixes are the labels accepted on a path line. "path:" is what
// prompts/system.txt asks for; "file:" is the variant models emit anyway, often
// enough to be worth accepting rather than losing the edit over. Matched
// case-insensitively, so "Path:" and "File:" work too.
var pathLinePrefixes = []string{pathPrefix, "file:"}

// parsePathLine extracts the path from a line like "path: some/file.go",
// tolerating the decorations models actually produce (Fix 14): a "File:" label
// instead of "path:", any capitalization of either, markdown emphasis around
// the label, and a path wrapped in backticks or quotes. The path itself is
// returned bare, exactly as the strict form would have returned it — no
// downstream gate sees any difference, so PrepareEdit's confinement and
// protected-target checks apply identically however the line was dressed up.
func parsePathLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	// Markdown emphasis around the whole label, e.g. "**path:** a.go".
	trimmed = strings.TrimLeft(trimmed, "*_ \t")

	lower := strings.ToLower(trimmed)
	rest := ""
	matched := false
	for _, prefix := range pathLinePrefixes {
		if strings.HasPrefix(lower, prefix) {
			rest = trimmed[len(prefix):]
			matched = true
			break
		}
	}
	if !matched {
		return "", false
	}

	// Closing emphasis from "**path:** a.go", then the path's own wrapping.
	rest = strings.TrimLeft(rest, "*_")
	rest = strings.TrimSpace(rest)
	rest = strings.Trim(rest, "`\"'")
	return strings.TrimSpace(rest), true
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
