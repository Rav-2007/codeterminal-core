package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chunkerBoundaryFingerprint hashes the LINE RANGES chunkContent produces over
// a fixed input. Content is deliberately excluded: this pins where the cuts
// fall, which is the only thing chunkerID claims to describe.
func chunkerBoundaryFingerprint() (string, int) {
	var b strings.Builder
	for _, n := range []int{1, 7, 40, 41, 95, 200} {
		var src strings.Builder
		for i := 1; i <= n; i++ {
			// Mixed shapes so a structure-aware chunker would cut differently
			// here than a fixed-window one does.
			switch i % 5 {
			case 0:
				fmt.Fprintf(&src, "func f%d() {\n", i)
			case 1:
				fmt.Fprintf(&src, "\treturn %d\n", i)
			case 2:
				src.WriteString("}\n")
			case 3:
				fmt.Fprintf(&src, "// comment %d\n", i)
			default:
				fmt.Fprintf(&src, "type T%d struct{}\n", i)
			}
		}
		for _, c := range chunkContent([]byte(src.String()), "fixture.go") {
			fmt.Fprintf(&b, "%d:%d-%d;", n, c.StartLine, c.EndLine)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8]), strings.Count(b.String(), ";")
}

// chunkerEmbedTextFingerprint hashes the TEXT THAT WOULD BE EMBEDDED, which the
// boundary fingerprint above deliberately excludes.
//
// The two are separate because they fail for opposite reasons and demand
// opposite fixes. Boundaries move when the cutting changes; embedded text moves
// when the REPRESENTATION changes -- a prefix added, a body elided, a comment
// stripped -- at identical cuts. On 2026-08-28 the representation changed
// exactly that way (chunkcontext.go) and this file could not see it: it hashes
// line ranges and says so. One fingerprint would have had to be re-recorded for
// either kind of change, which tells the next person nothing about which one
// they made.
func chunkerEmbedTextFingerprint() (string, int) {
	var b strings.Builder
	var n int
	for _, lines := range []int{1, 7, 40, 41, 95, 200} {
		var src strings.Builder
		for i := 1; i <= lines; i++ {
			switch i % 5 {
			case 0:
				fmt.Fprintf(&src, "func f%d() {\n", i)
			case 1:
				fmt.Fprintf(&src, "\treturn %d\n", i)
			case 2:
				src.WriteString("}\n")
			case 3:
				fmt.Fprintf(&src, "// comment %d\n", i)
			default:
				fmt.Fprintf(&src, "type T%d struct{}\n", i)
			}
		}
		for _, c := range chunkContent([]byte(src.String()), "fixture.go") {
			b.WriteString(embedTextOf(c))
			b.WriteString("\x00")
			n++
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8]), n
}

// THE EMBEDDED TEXT IS PART OF THE INDEX'S IDENTITY TOO, and until 2026-08-28
// nothing said so. bgeEmbedderID covers the model and its sequence length;
// currentIndexSchemaVersion covers the shape of what is STORED; the fingerprint
// below covers where the cuts fall. Change what is handed to the embedder at
// unchanged cuts, unchanged model and unchanged stored shape -- which is exactly
// what prefixing the file path did -- and all three stay identical while every
// vector in the index means something different.
func TestTheChunkerIDChangesWhenTheEmbeddedTextDoes(t *testing.T) {
	const (
		// Recorded 2026-08-28 for chunkerID "fixed-window/v2/lines=40/overlap=10/prefix=path".
		wantFingerprint = "4e0a5e30bd6c9c9d"
		wantTexts       = 16
	)
	got, texts := chunkerEmbedTextFingerprint()

	// ANTI-VACUITY: a chunkContent returning nothing would hash a constant.
	if texts < 10 {
		t.Fatalf("the fixture produced only %d chunks; the fingerprint below would pin almost nothing", texts)
	}
	if got != wantFingerprint || texts != wantTexts {
		t.Fatalf(`the text handed to the EMBEDDER changed: fingerprint %s (%d chunks), recorded %s (%d chunks).

The boundaries may well be untouched -- TestTheChunkerIDChangesWhenTheBoundariesDo
answers that separately. What moved is what each chunk is embedded AS, and every
vector in every existing index was built from the old version.

chunkerID is currently %q. If this change was intended, make sure chunkerID moves
with it (it interpolates activeEmbedPrefix, so a new policy is covered; a change
to how an existing policy renders is NOT), then update the values above.`,
			got, texts, wantFingerprint, wantTexts, chunkerID)
	}
}

// THE POINT OF THIS TEST IS THAT chunkerID CANNOT BE FORGOTTEN.
//
// chunkerID interpolates chunkLines and overlapLines, so tuning either of those
// changes the identity by itself. It cannot cover the case that matters most:
// a change to the ALGORITHM at unchanged parameters. AST-aware chunk boundaries
// are precisely that -- same 40/10 constants, entirely different cuts -- and
// under that change chunkerID would not move, the embedder stamp would accept
// every existing index, and each stale boundary would sit in the store
// competing in retrieval with content that no longer describes its file.
//
// currentIndexSchemaVersion already demonstrates the failure mode this avoids:
// it is a hand-bumped integer, and a hand-bumped integer is a promise that
// somebody will remember. This pins BEHAVIOUR instead. Change how chunkContent
// cuts anything and this fails, naming what to do.
func TestTheChunkerIDChangesWhenTheBoundariesDo(t *testing.T) {
	const (
		// Recorded 2026-08-28 for chunkerID "fixed-window/v1/lines=40/overlap=10".
		wantFingerprint = "f10b2fcf653647fc"
		wantCuts        = 16
	)
	got, cuts := chunkerBoundaryFingerprint()

	// ANTI-VACUITY: a chunkContent that returned nothing would hash a constant
	// empty string and match forever.
	if cuts < 10 {
		t.Fatalf("the fixture produced only %d chunks; the fingerprint below would be pinning almost nothing", cuts)
	}
	if got != wantFingerprint || cuts != wantCuts {
		t.Fatalf(`chunk boundaries changed: fingerprint %s (%d chunks), recorded %s (%d chunks).

chunkerID is currently %q, and it did NOT change with them -- it interpolates
chunkLines and overlapLines, so it cannot see an algorithm change at unchanged
parameters. An index built by the old chunker would therefore still be accepted,
and every boundary this new chunker does not regenerate would stay in the store
serving content that no longer describes its file.

If the change was intended: bump the version segment of chunkerID (chunker.go),
then update wantFingerprint/wantCuts above to the values printed here.`,
			got, cuts, wantFingerprint, wantCuts, chunkerID)
	}
}

// The identity must actually move when the parameters do, or the interpolation
// is decoration. Asserted on the format rather than by mutating a const.
func TestTheChunkerIDCarriesItsParameters(t *testing.T) {
	for _, want := range []string{
		fmt.Sprintf("lines=%d", chunkLines),
		fmt.Sprintf("overlap=%d", overlapLines),
		fmt.Sprintf("prefix=%s", activeEmbedPrefix),
	} {
		if !strings.Contains(chunkerID, want) {
			t.Errorf("chunkerID %q does not carry %q, so changing that constant would not "+
				"invalidate indexes built with the old value", chunkerID, want)
		}
	}
}

// The stamp must REFUSE an index whose boundaries came from a different
// chunker. Without this, chunkerID is a string nobody reads: pruneOrphanedChunks
// clears stale boundaries, but only once a rebuild happens, and nothing else
// would ever ask for one.
//
// Written because neutering found the gap: disabling the check in
// checkEmbedderStamp failed no test at all.
func TestAnIndexBuiltByADifferentChunkerIsRefused(t *testing.T) {
	dir := t.TempDir()
	emb := &fakeEmbedder{dim: 8}
	if err := writeEmbedderStamp(dir, emb); err != nil {
		t.Fatal(err)
	}
	// storeIsEmpty=false: an empty store has nothing stale to refuse.
	if err := checkEmbedderStamp(dir, emb, false); err != nil {
		t.Fatalf("a stamp this binary just wrote must be accepted: %v", err)
	}

	// Rewrite the stamp as an older chunker would have: same embedder, same
	// schema version, different boundaries.
	raw, err := os.ReadFile(filepath.Join(dir, embedderStampFileName))
	if err != nil {
		t.Fatal(err)
	}
	var stamp embedderStamp
	if err := json.Unmarshal(raw, &stamp); err != nil {
		t.Fatal(err)
	}
	if stamp.ChunkerID != chunkerID {
		t.Fatalf("writeEmbedderStamp did not record the chunker: got %q, want %q", stamp.ChunkerID, chunkerID)
	}
	for _, other := range []string{
		"fixed-window/v1/lines=40/overlap=5", // a tuned parameter
		"ast-aware/v1/lines=40/overlap=10",   // a changed algorithm
		"",                                   // an index that predates the field
	} {
		stamp.ChunkerID = other
		out, err := json.Marshal(stamp)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), out, 0o644); err != nil {
			t.Fatal(err)
		}
		err = checkEmbedderStamp(dir, emb, false)
		if err == nil {
			t.Errorf("an index built by chunker %q was accepted by a binary that chunks as %q; "+
				"its stored line ranges no longer describe the files", other, chunkerID)
			continue
		}
		if !strings.Contains(err.Error(), "re-index") {
			t.Errorf("the refusal for chunker %q does not tell the user what to do: %v", other, err)
		}
	}
}
