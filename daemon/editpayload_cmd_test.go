package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/editapply"
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

// TestApplyExitsNonZeroOnUnreadableEditPayload covers what is STILL unreadable
// now that unified diffs are ingested.
//
// The original silence was this: ParseEditBlocks returns (nil, nil) for a
// plain-text answer AND for edit-shaped input it cannot read, so the command
// printed "no edit blocks found in input" and exited 0 either way. Every script
// and every reader takes exit 0 as success. Ingestion removed the diff from
// that set; these shapes remain in it, and must still fail loudly.
func TestApplyExitsNonZeroOnUnreadableEditPayload(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{
			// A diff header with no hunk. Recognised as edit-shaped but never
			// parsed, deliberately: requiring a hunk header is what keeps a
			// markdown rule from being read as a patch.
			name:  "diff header with no hunk",
			input: "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n",
			want:  "unified diff",
		},
		{
			name:  "markers that missed the grammar",
			input: "path: a.txt\n<<<<<<<SEARCH\nold line\n=======\nnew line\n>>>>>>>REPLACE\n",
			want:  "conflict-style edit markers",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runEditsApply(t, tt.input)
			if err == nil {
				t.Fatal("accepted silently with a zero exit status; nothing was applied and the caller was told everything was fine")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to name what it saw (%q) so the user knows what to re-send", err, tt.want)
			}
			if !strings.Contains(err.Error(), "nothing applied") {
				t.Errorf("error = %q, want it to say plainly that nothing reached disk", err)
			}
		})
	}
}

// TestUnifiedDiffReachesTheConfirmPrompt is the Stage 1 headline at the CLI
// boundary: a real patch is no longer refused, it is offered.
func TestUnifiedDiffReachesTheConfirmPrompt(t *testing.T) {
	if err := runEditsApply(t, unifiedDiffPayload); err != nil {
		t.Fatalf("a readable patch was rejected: %v", err)
	}
}

// TestUnifiedDiffAppliesThroughTheSameGates drives the whole CLI path with a
// scripted confirmation, so the proof is a changed file rather than an absent
// error. The blocks come from ParseEditPayload and are handed to the SAME
// applyEditBlocks a SEARCH/REPLACE response uses -- that shared path is the
// point of translating rather than adding a second apply route.
func TestUnifiedDiffAppliesThroughTheSameGates(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "a.txt", "old line\n")

	payload := editapply.ParseEditPayload(unifiedDiffPayload)
	if payload.Format != editapply.FormatUnifiedDiff {
		t.Fatalf("Format = %v, want FormatUnifiedDiff (rejections: %v)", payload.Format, payload.Rejected)
	}

	var out bytes.Buffer
	if err := applyEditBlocks(root, payload.Blocks, payload.Rejected, strings.NewReader("y\n"), &out, discardLogger()); err != nil {
		t.Fatalf("applyEditBlocks: %v", err)
	}

	if got := readFileString(t, filepath.Join(root, "a.txt")); got != "new line\n" {
		t.Errorf("file content = %q, want the patch applied byte for byte", got)
	}
	if !strings.Contains(out.String(), "1 applied") {
		t.Errorf("summary = %q, want 1 applied", out.String())
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
