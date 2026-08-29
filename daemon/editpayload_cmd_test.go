package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runEditsApply drives the real `edits apply <file>` entry point against a
// temp workspace, returning the error it would hand to main.go (where a
// non-nil error becomes logger.Fatal, i.e. exit status 1 / exitFailure).
func runEditsApply(t *testing.T, input string) error {
	t.Helper()
	root := realTempDir(t)
	writeTempFile(t, root, "a.txt", "old line\n")

	payload := filepath.Join(root, "response.txt")
	if err := os.WriteFile(payload, []byte(input), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logger := log.New(os.Stderr, "", 0)
	return runEditsApplyCommand([]string{"--workspace", root, payload}, logger)
}

const unifiedDiffPayload = `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-old line
+new line
`

// TestApplyExitsNonZeroOnUnreadableEditPayload is the headline Stage 0
// regression.
//
// ParseEditBlocks returns (nil, nil) for a plain-text answer AND for a
// response full of unified-diff hunks it cannot read. This command reported
// both identically -- it printed "no edit blocks found in input" and returned
// nil, so `edits apply < some.patch` exited 0. Every script and every reader
// takes exit 0 as success. Nothing was applied and nothing said so.
func TestApplyExitsNonZeroOnUnreadableEditPayload(t *testing.T) {
	err := runEditsApply(t, unifiedDiffPayload)
	if err == nil {
		t.Fatal("a unified diff was accepted silently with a zero exit status; nothing was applied and the caller was told everything was fine")
	}
	if !strings.Contains(err.Error(), "unified diff") {
		t.Errorf("error = %q, want it to name what it saw so the user knows what to re-send", err)
	}
	if !strings.Contains(err.Error(), "nothing applied") {
		t.Errorf("error = %q, want it to say plainly that nothing reached disk", err)
	}
}

// TestPlainTextAnswerStillExitsZero is the over-correction guard. Answering a
// question is not a failure, and scripts depend on exit 0 here.
func TestPlainTextAnswerStillExitsZero(t *testing.T) {
	answers := []string{
		"The retrieval budget is 24000 characters.",
		"Here are the options:\n\n---\n\nOption one.\nOption two.",
		"Overview\n--------\n\nThe daemon streams tokens.",
		"Run git diff to see what changed.",
		"",
	}
	for _, answer := range answers {
		if err := runEditsApply(t, answer); err != nil {
			t.Errorf("plain answer %q was reported as a failure: %v", answer, err)
		}
	}
}

// TestMalformedMarkersDoNotExitZero covers the third silent shape: a model that
// typed the conflict markers slightly wrong. ParseEditBlocks anchors on the
// exact trimmed marker line, so "<<<<<<<SEARCH" produces no block AND no
// rejection -- it vanishes exactly like a diff did.
func TestMalformedMarkersDoNotExitZero(t *testing.T) {
	input := "path: a.txt\n<<<<<<<SEARCH\nold line\n=======\nnew line\n>>>>>>>REPLACE\n"
	if err := runEditsApply(t, input); err == nil {
		t.Fatal("markers that missed the grammar were accepted silently with a zero exit status")
	}
}

// TestWellFormedBlocksAreUnaffected is the precondition the two tests above
// depend on: the sniffer must not have changed what a readable response does.
// A parser rejection is a RECOGNISED block that failed, and it already reaches
// the user by line number -- the sniffer must never claim it.
func TestWellFormedBlocksAreUnaffected(t *testing.T) {
	// Unterminated: ParseEditBlocks produces a rejection, so the sniffer branch
	// is not entered at all, and the command still fails for the ORIGINAL
	// reason rather than a sniffed one.
	err := runEditsApply(t, "path: a.txt\n<<<<<<< SEARCH\nold line\n")
	if err == nil {
		t.Fatal("an unterminated block should still be an error")
	}
	if strings.Contains(err.Error(), "nothing applied") {
		t.Errorf("error = %q; a parser rejection must keep its own line-numbered reason, not be re-diagnosed by the sniffer", err)
	}
}
