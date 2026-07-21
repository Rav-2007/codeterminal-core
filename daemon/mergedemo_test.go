package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestDemo_RenderedPromptBeforeAndAfterMerge is the Fix 11 acceptance
// artifact: the ACTUAL rendered <retrieved_context> block for the observed
// case (daemon/server.go's two consecutive indexer windows), before and after
// merging, from the real file's real chunks.
func TestDemo_RenderedPromptBeforeAndAfterMerge(t *testing.T) {
	content, err := os.ReadFile("server.go")
	if err != nil {
		t.Skipf("server.go unreadable: %v", err)
	}
	all := chunkContent(content, "daemon/server.go")

	// The two consecutive windows the finding names.
	var before []Chunk
	for _, c := range all {
		if c.StartLine == 661 || c.StartLine == 691 {
			before = append(before, c)
		}
	}
	if len(before) != 2 {
		t.Skipf("server.go no longer produces windows at 661/691 (got %d); the demo needs the observed shape", len(before))
	}

	after := mergeAdjacentChunks(before)

	const q = "why does the daemon persist an empty turn?"
	beforeMsg := buildAugmentedUserMessage(q, before, false)
	afterMsg := buildAugmentedUserMessage(q, after, false)

	t.Logf("\n########## BEFORE: %d fragments, %d bytes ##########\n%s", len(before), len(beforeMsg), outline(beforeMsg))
	t.Logf("\n########## AFTER: %d span, %d bytes ##########\n%s", len(after), len(afterMsg), outline(afterMsg))

	// A distinctive, non-blank line from the 10-line seam the two windows share.
	var seam string
	for _, line := range strings.Split(before[1].Content, "\n") {
		if t := strings.TrimSpace(line); len(t) > 20 {
			seam = line
			break
		}
	}
	if seam == "" {
		t.Fatal("no distinctive seam line found in the overlap")
	}

	t.Logf("\n########## MEASURED ##########\nseam line: %q\n  occurrences BEFORE: %d\n  occurrences AFTER:  %d\nrendered bytes: before=%d after=%d reclaimed=%d (%.1f%%)",
		seam,
		strings.Count(beforeMsg, seam), strings.Count(afterMsg, seam),
		len(beforeMsg), len(afterMsg), len(beforeMsg)-len(afterMsg),
		100*float64(len(beforeMsg)-len(afterMsg))/float64(len(beforeMsg)))

	if strings.Count(beforeMsg, seam) != 2 {
		t.Errorf("premise broken: seam line should appear twice before merging, appeared %d times", strings.Count(beforeMsg, seam))
	}
	if strings.Count(afterMsg, seam) != 1 {
		t.Errorf("seam line appears %d times after merging, want exactly 1", strings.Count(afterMsg, seam))
	}
}

// outline renders a message with its chunk-label lines intact and each chunk's
// body collapsed to its first and last line, so the structural change
// (fragments with a duplicated seam vs. one contiguous span) is readable
// without dumping a thousand lines of source into the test log.
func outline(msg string) string {
	var b strings.Builder
	body := []string{}
	flush := func() {
		if len(body) == 0 {
			return
		}
		b.WriteString("    " + body[0] + "\n")
		if len(body) > 2 {
			b.WriteString("    ... " + strconv.Itoa(len(body)-2) + " lines ...\n")
		}
		if len(body) > 1 {
			b.WriteString("    " + body[len(body)-1] + "\n")
		}
		body = body[:0]
	}
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "<") {
			flush()
			b.WriteString(line + "\n")
			continue
		}
		body = append(body, line)
	}
	flush()
	return b.String()
}
