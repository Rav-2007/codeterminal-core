package editapply

import (
	"strings"
	"testing"
)

// TestHunkHeaderIsRequiredBeforeAnythingIsParsedAsADiff pins the guard in
// ParseEditPayload that stops the diff reader being handed input it should
// never see.
//
// The guard looked redundant at first -- a document with no hunks yields no
// blocks anyway -- and a neuter that removed it changed nothing observable.
// This is the case where it is load-bearing, and it is the reason the guard
// stays.
//
// The reader's file-level markers ("rename from ", "deleted file mode ",
// "GIT binary patch") are matched as line PREFIXES wherever they appear, so a
// sentence that happens to begin "rename from " produces a refusal. On its own
// that is unreachable: prose does not look like an edit payload. But a document
// containing a horizontal rule immediately followed by a "+++" line DOES trip
// the sniffer -- and without the hunk-header requirement, the prose below it
// would then be read as a patch and reported to the user as a refused rename.
// Documentation would become a failed edit.
func TestHunkHeaderIsRequiredBeforeAnythingIsParsedAsADiff(t *testing.T) {
	doc := strings.Join([]string{
		"# Migration notes",
		"",
		"--- a section rule",
		"+++ another one",
		"",
		"rename from the old scheme to the new one before upgrading.",
	}, "\n")

	p := ParseEditPayload(doc)
	if p.Format == FormatUnifiedDiff {
		t.Fatalf("prose was read as a unified diff and produced %d block(s) and %d refusal(s): %v",
			len(p.Blocks), len(p.Rejected), p.Rejected)
	}
	if len(p.Blocks) != 0 || len(p.Rejected) != 0 {
		t.Fatalf("prose produced %d block(s) and %d refusal(s); it must produce neither", len(p.Blocks), len(p.Rejected))
	}
	// It is still honestly reported as edit-shaped-and-unreadable, because the
	// ---/+++ pair genuinely is that shape. Saying so is Stage 0's job.
	if p.Format != FormatUnrecognised {
		t.Errorf("Format = %v, want FormatUnrecognised", p.Format)
	}
}

// TestFormatNoneCarriesNothing is the companion: an ordinary answer must reach
// the "nothing to do" state with empty everything, or a caller that renders
// Blocks without checking Format would show a phantom edit.
func TestFormatNoneCarriesNothing(t *testing.T) {
	for _, answer := range []string{
		"The retrieval budget is 24000 characters.",
		"Option one.\n\n---\n\nOption two.",
		"a: 1\n---\nb: 2",
		"",
	} {
		p := ParseEditPayload(answer)
		if p.Format != FormatNone {
			t.Errorf("Format = %v for %q, want FormatNone", p.Format, answer)
		}
		if len(p.Blocks) != 0 || len(p.Rejected) != 0 {
			t.Errorf("FormatNone carried %d block(s) and %d refusal(s)", len(p.Blocks), len(p.Rejected))
		}
	}
}
