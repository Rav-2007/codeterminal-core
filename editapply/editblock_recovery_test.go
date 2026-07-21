package editapply

import (
	"strings"
	"testing"
	"time"
)

// This file is the before/after evidence for Fix B: ParseEditBlocks failed the
// whole RESPONSE on one malformed block. A reply carrying a good edit beside a
// bad one applied neither -- the good edit was parsed, discarded, and never
// mentioned again. That is data loss by omission: work the model did and the
// user asked for, thrown away because of an unrelated hunk.
//
// Run against the pre-fix parser, every "Mixed"/"Truncated"/"Survives" case
// FAILS by returning zero blocks. The all-valid and all-invalid cases pass
// before and after: recovery must not turn a clean response into a different
// one, nor a hopeless response into a silent success.

// TestParseRecovery_MixedValidAndInvalid is the reproduced data-loss case.
func TestParseRecovery_MixedValidAndInvalid(t *testing.T) {
	response := strings.Join([]string{
		"path: main.go",
		searchMarker,
		"func old() {}",
		separatorMarker,
		"func renamed() {}",
		replaceMarker,
		"",
		"path: notes.md",
		searchMarker,
		"alpha",
		separatorMarker,
		"beta",
		separatorMarker,
		"gamma",
		replaceMarker,
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)

	want := EditBlock{FilePath: "main.go", Search: "func old() {}", Replace: "func renamed() {}"}
	if len(blocks) != 1 || blocks[0] != want {
		t.Fatalf("DATA LOSS: blocks = %+v, want exactly the valid sibling %+v", blocks, want)
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected = %+v, want exactly one refusal", rejected)
	}
	// Surfaced, not silently dropped: the refusal has to say which block and
	// why, or the user cannot tell the difference between "refused" and
	// "never existed".
	if !strings.Contains(rejected[0].Reason, "ambiguous") {
		t.Errorf("refusal = %q, want the specific reason", rejected[0].Reason)
	}
	if rejected[0].Line != 9 {
		t.Errorf("refusal line = %d, want 9 (the bad block's SEARCH marker)", rejected[0].Line)
	}
}

// TestParseRecovery_BadBlockFirstStillYieldsTheGoodOne is the same case with
// the order reversed: recovery must work forwards from a refusal, not just
// stop at one.
func TestParseRecovery_BadBlockFirstStillYieldsTheGoodOne(t *testing.T) {
	response := strings.Join([]string{
		"path: notes.md",
		searchMarker,
		"alpha",
		separatorMarker,
		"beta",
		separatorMarker,
		"gamma",
		replaceMarker,
		"",
		"path: main.go",
		searchMarker,
		"func old() {}",
		separatorMarker,
		"func renamed() {}",
		replaceMarker,
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)
	want := EditBlock{FilePath: "main.go", Search: "func old() {}", Replace: "func renamed() {}"}
	if len(blocks) != 1 || blocks[0] != want {
		t.Fatalf("blocks = %+v, want exactly %+v", blocks, want)
	}
	if len(rejected) != 1 {
		t.Errorf("rejected = %+v, want exactly one refusal", rejected)
	}
}

// TestParseRecovery_AllValidUnchanged is the control: a clean response must
// parse to exactly what it always did, with nothing rejected.
func TestParseRecovery_AllValidUnchanged(t *testing.T) {
	response := strings.Join([]string{
		"path: a.go",
		searchMarker,
		"foo",
		separatorMarker,
		"bar",
		replaceMarker,
		"path: b.go",
		searchMarker,
		"baz",
		separatorMarker,
		"qux",
		replaceMarker,
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)
	if len(rejected) != 0 {
		t.Fatalf("rejected %+v from a clean response", rejected)
	}
	for i, want := range []EditBlock{
		{FilePath: "a.go", Search: "foo", Replace: "bar"},
		{FilePath: "b.go", Search: "baz", Replace: "qux"},
	} {
		if blocks[i] != want {
			t.Errorf("block %d = %+v, want %+v", i, blocks[i], want)
		}
	}
}

// TestParseRecovery_AllInvalidStillFailsClearly is the other control, and the
// one that keeps recovery from becoming permissiveness: a response in which
// nothing is usable must come back with no blocks and every reason, so a
// caller can tell it apart from a plain-text answer that simply had none.
func TestParseRecovery_AllInvalidStillFailsClearly(t *testing.T) {
	response := strings.Join([]string{
		"path: a.go",
		searchMarker,
		"one",
		separatorMarker,
		"two",
		separatorMarker,
		"three",
		replaceMarker,
		"",
		"path: b.go",
		searchMarker,
		"no divider here",
		replaceMarker,
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)
	if len(blocks) != 0 {
		t.Fatalf("blocks = %+v, want none", blocks)
	}
	if len(rejected) != 2 {
		t.Fatalf("rejected = %+v, want both blocks reported", rejected)
	}
	if rejected[0].Line == rejected[1].Line {
		t.Errorf("both refusals point at line %d; each block must be located separately", rejected[0].Line)
	}
	for _, bad := range rejected {
		if bad.Reason == "" {
			t.Errorf("refusal at line %d carries no reason", bad.Line)
		}
	}
}

// TestParseRecovery_TruncatedFinalBlock covers the shape a cut-off or
// token-limited response actually takes: complete blocks, then one that stops
// mid-way. The complete ones must survive.
func TestParseRecovery_TruncatedFinalBlock(t *testing.T) {
	response := strings.Join([]string{
		"path: a.go",
		searchMarker,
		"foo",
		separatorMarker,
		"bar",
		replaceMarker,
		"",
		"path: b.go",
		searchMarker,
		"baz",
		separatorMarker,
		"qux with no terminator",
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)
	want := EditBlock{FilePath: "a.go", Search: "foo", Replace: "bar"}
	if len(blocks) != 1 || blocks[0] != want {
		t.Fatalf("blocks = %+v, want the complete block %+v", blocks, want)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0].Reason, "unterminated") {
		t.Fatalf("rejected = %+v, want one 'unterminated' refusal", rejected)
	}
}

// TestParseRecovery_UnterminatedBlockDoesNotSwallowTheNextOne pins the
// structural half of recovery. A block that runs into the NEXT block's SEARCH
// marker resumes AT that marker, not past it -- otherwise one missing
// terminator would quietly consume a perfectly good sibling, which is the same
// data loss in a different costume.
func TestParseRecovery_UnterminatedBlockDoesNotSwallowTheNextOne(t *testing.T) {
	response := strings.Join([]string{
		"path: a.go",
		searchMarker,
		"foo",
		"path: b.go",
		searchMarker,
		"baz",
		separatorMarker,
		"qux",
		replaceMarker,
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)
	want := EditBlock{FilePath: "b.go", Search: "baz", Replace: "qux"}
	if len(blocks) != 1 || blocks[0] != want {
		t.Fatalf("blocks = %+v, want the intact second block %+v", blocks, want)
	}
	if len(rejected) != 1 || rejected[0].Line != 2 {
		t.Fatalf("rejected = %+v, want one refusal at line 2", rejected)
	}
}

// TestParseRecovery_CreateBesideOrdinaryEdit is Fix A and Fix B together, and
// is the exact response shape that motivated both: the create block used to be
// refused, and its refusal used to take the ordinary edit down with it.
func TestParseRecovery_CreateBesideOrdinaryEdit(t *testing.T) {
	response := strings.Join([]string{
		"path: main.go",
		searchMarker,
		"func old() {}",
		separatorMarker,
		"func renamed() {}",
		replaceMarker,
		"",
		"path: notes.md",
		searchMarker,
		separatorMarker,
		"# Notes",
		replaceMarker,
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)
	if len(rejected) != 0 {
		t.Fatalf("rejected %+v; both blocks are well-formed", rejected)
	}
	for i, want := range []EditBlock{
		{FilePath: "main.go", Search: "func old() {}", Replace: "func renamed() {}"},
		{FilePath: "notes.md", Search: "", Replace: "# Notes"},
	} {
		if blocks[i] != want {
			t.Errorf("block %d = %+v, want %+v", i, blocks[i], want)
		}
	}
}

// TestParseRecovery_TerminatesOnPathologicalInput guards the loop invariant
// recovery depends on: parseBlockAt must always report a resume index past
// where it started, or a malformed response spins forever. A bare marker with
// nothing after it is the shortest input that can violate it.
func TestParseRecovery_TerminatesOnPathologicalInput(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, response := range []string{
			searchMarker,
			searchMarker + "\n" + searchMarker,
			strings.Repeat(searchMarker+"\n", 50),
			"path: a.go\n" + searchMarker,
			strings.Repeat("path: a.go\n"+searchMarker+"\n"+separatorMarker+"\n", 50),
		} {
			ParseEditBlocks(response)
		}
	}()
	select {
	case <-done:
	case <-timeoutAfterSeconds(10):
		t.Fatal("ParseEditBlocks did not terminate: a refused block failed to advance the scan")
	}
}

// timeoutAfterSeconds is time.After, wrapped so this file needs no import of
// time for a single call site in a guard test.
func timeoutAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}
