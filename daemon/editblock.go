package main

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
// empty SEARCH) produce a descriptive error naming the line number; the
// parser never panics on malformed input.
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

		sepIdx, hitNextBlock := scanUntil(lines, i+1, separatorMarker)
		if sepIdx == -1 || hitNextBlock {
			return nil, fmt.Errorf("line %d: unterminated SEARCH block (no %q found before end of response)", lineNum, separatorMarker)
		}

		search := strings.Join(lines[i+1:sepIdx], "\n")
		if strings.TrimSpace(search) == "" {
			return nil, fmt.Errorf("line %d: SEARCH block is empty", lineNum)
		}

		replEndIdx, hitNextBlock := scanUntil(lines, sepIdx+1, replaceMarker)
		if replEndIdx == -1 || hitNextBlock {
			return nil, fmt.Errorf("line %d: unterminated block (no %q found before end of response)", lineNum, replaceMarker)
		}
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
