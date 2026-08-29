package editapply

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Floors that would have caught the hole in the original round-trip corpus.
//
// That corpus mutated by replacing one line with one line, so every hunk it
// generated was n-for-n and the cumulative line drift was identically zero.
// Its "4 ambiguous in 554" was therefore not a sample but the theoretical
// MAXIMUM, achieved by construction — and it was structurally incapable of
// showing what happens once earlier hunks move later ones.
//
// minPostDriftHunks is the load-bearing one. A file with two hunks still
// produces zero drift if the first is n-for-n, so counting multi-hunk files is
// not enough: the floor has to count hunks that are actually DOWNSTREAM of a
// size change.
const (
	minDriftingHunks  = 50
	minMultiHunkFiles = 30
	minPostDriftHunks = 50
)

// mutateWithDrift is a sibling of mutate, never a replacement for it: the
// original measurement must stay reproducible. Where mutate swaps one line for
// one line, this inserts, deletes, and then edits BELOW both — so the third
// hunk in every file sits downstream of a real cumulative delta.
func mutateWithDrift(content string, fileIdx int) (out string, hunks, drifting, postDrift int) {
	lines := strings.Split(content, "\n")
	if len(lines) < 40 {
		return content, 0, 0, 0
	}

	at1 := len(lines) / 5
	at2 := len(lines) / 2
	at3 := 4 * len(lines) / 5
	if at2-at1 < 10 || at3-at2 < 10 {
		return content, 0, 0, 0
	}

	// Rebuilt bottom-up so the earlier indexes stay valid.
	tail := append([]string{}, lines[at3:]...)
	mid := append([]string{}, lines[at2:at3]...)
	head := append([]string{}, lines[:at2]...)

	// Hunk 3 (lowest): an ordinary n-for-n edit, downstream of both deltas.
	if strings.TrimSpace(tail[0]) != "" {
		tail[0] = fmt.Sprintf("DRIFTED-%d-3 downstream edit", fileIdx)
		hunks++
		postDrift++
	}
	// Hunk 2: a DELETION — three lines collapse to one.
	if len(mid) > 4 {
		mid = append([]string{fmt.Sprintf("DRIFTED-%d-2 collapsed", fileIdx)}, mid[3:]...)
		hunks++
		drifting++
	}
	// Hunk 1 (highest): an INSERTION — one line becomes five.
	if at1 > 0 && strings.TrimSpace(head[at1]) != "" {
		grown := []string{
			fmt.Sprintf("DRIFTED-%d-1a inserted", fileIdx),
			fmt.Sprintf("DRIFTED-%d-1b inserted", fileIdx),
			fmt.Sprintf("DRIFTED-%d-1c inserted", fileIdx),
			fmt.Sprintf("DRIFTED-%d-1d inserted", fileIdx),
			fmt.Sprintf("DRIFTED-%d-1e inserted", fileIdx),
		}
		head = append(head[:at1], append(grown, head[at1+1:]...)...)
		hunks++
		drifting++
	}

	return strings.Join(append(head, append(mid, tail...)...), "\n"), hunks, drifting, postDrift
}

// TestUnifiedDiff_DriftCorpusMeasurement is the measurement the original corpus
// could not produce: how the reader behaves once hunks genuinely displace one
// another. It asserts correctness (every hunk either applies or is refused for
// a named reason) and REPORTS the numbers rather than gating on a ratio.
func TestUnifiedDiff_DriftCorpusMeasurement(t *testing.T) {
	requireGit(t)

	sources := collectRealSources(t)
	if len(sources) > 200 {
		sources = sources[:200]
	}
	if len(sources) < minRoundTripFiles {
		t.Fatalf("collected only %d source files (floor %d)", len(sources), minRoundTripFiles)
	}

	root := realTempDir(t)
	git(t, root, "init", "-q", ".")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "test")

	originals := make([]string, len(sources))
	wanted := make([]string, len(sources))
	var wantHunks, drifting, postDrift, multiHunkFiles int

	for i, src := range sources {
		originals[i] = src
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte(src), 0644)
		m, h, d, pd := mutateWithDrift(src, i)
		wanted[i] = m
		wantHunks += h
		drifting += d
		postDrift += pd
		if h >= 2 {
			multiHunkFiles++
		}
	}

	if drifting < minDriftingHunks {
		t.Fatalf("only %d size-changing hunks (floor %d): this corpus is line-count-preserving and therefore cannot measure drift — the exact hole that made the original 4/554 measurement uninformative", drifting, minDriftingHunks)
	}
	if multiHunkFiles < minMultiHunkFiles {
		t.Fatalf("only %d files contribute >= 2 hunks (floor %d)", multiHunkFiles, minMultiHunkFiles)
	}
	if postDrift < minPostDriftHunks {
		t.Fatalf("only %d hunks sit downstream of a size change (floor %d): coexisting with a delta is not the same as being displaced by one", postDrift, minPostDriftHunks)
	}

	git(t, root, "add", "-A")
	git(t, root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base")
	for i := range sources {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte(wanted[i]), 0644)
	}
	patch := git(t, root, "diff", "--unified=3")
	if strings.TrimSpace(patch) == "" {
		t.Skip("git produced no diff")
	}
	for i := range sources {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte(originals[i]), 0644)
	}

	payload := ParseEditPayload(patch)
	if payload.Format != FormatUnifiedDiff {
		t.Fatalf("Format = %v", payload.Format)
	}

	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}

	var applied, ambiguous, notFound int
	other := map[string]int{}
	for _, block := range payload.Blocks {
		prepared, err := PrepareEdit(root, block)
		if err != nil {
			switch {
			case strings.Contains(err.Error(), "ambiguous"):
				ambiguous++
			case strings.Contains(err.Error(), "not found"):
				notFound++
			default:
				other[err.Error()]++
			}
			continue
		}
		if err := Apply(root, prepared, backupDir); err != nil {
			t.Fatalf("Apply %s: %v", block.FilePath, err)
		}
		applied++
	}

	exact := 0
	for i := range sources {
		got, _ := os.ReadFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)))
		if string(got) == wanted[i] {
			exact++
		}
	}

	t.Logf("DRIFT MEASUREMENT: files=%d hunks=%d size_changing=%d downstream_of_a_delta=%d | applied=%d AMBIGUOUS=%d not_found=%d other=%v reader_refusals=%d | byte_identical=%d/%d",
		len(sources), len(payload.Blocks), drifting, postDrift,
		applied, ambiguous, notFound, other, len(payload.Rejected), exact, len(sources))

	if len(other) > 0 {
		t.Errorf("hunks refused for reasons that are neither ambiguity nor staleness: %v", other)
	}
	if notFound > 0 {
		t.Errorf("%d hunk(s) produced SEARCH text absent from the file — the reader mis-assembled them under drift", notFound)
	}
}

// TestPositionHintWouldPickTheWrongOccurrence is a NEGATIVE CONTROL, and the
// reason unified-diff ingestion does not disambiguate by line number.
//
// The obvious fix for an ambiguous hunk is to use the line the "@@" header
// declares — the datum is right there and `git apply` relies on it. It cannot
// be made safe here, because blocks apply SEQUENTIALLY: once an earlier hunk
// changes a file's line count, every later hunk's declared line is stale by
// that delta. When the drift happens to equal the SPACING between the duplicate
// occurrences, the stale line matches the WRONG occurrence — uniquely, so a
// "select iff exactly one candidate is at the declared line" rule accepts it,
// and the edit lands in the wrong place with no gate able to notice: the file
// is right, the bytes are byte-identical to the intended target, and
// VerifyUnchanged only checks that the file has not changed since PrepareEdit.
//
// This is not a rare coincidence. In test files the unit of duplication and the
// unit of insertion are both "a function", so drift and spacing are routinely
// the same quantity.
//
// This test constructs exactly that collision and pins the two facts that make
// the design unsafe. If anyone later adds positional disambiguation, this test
// must be confronted deliberately rather than discovered in production.
func TestPositionHintWouldPickTheWrongOccurrence(t *testing.T) {
	block := []string{
		"\tsrv := setup(t)",
		"\tdefer srv.Close()",
		"\trun(srv)",
	}
	spacer := []string{"", "func between() {", "\tnoop()", "}", ""}

	var lines []string
	lines = append(lines, "package main", "")
	occ1Start := len(lines) + 1 // 1-indexed
	lines = append(lines, block...)
	lines = append(lines, spacer...)
	occ2Start := len(lines) + 1
	lines = append(lines, block...)
	lines = append(lines, "")

	spacing := occ2Start - occ1Start

	// An earlier hunk inserts exactly `spacing` lines above both occurrences.
	inserted := make([]string, spacing)
	for i := range inserted {
		inserted[i] = fmt.Sprintf("// inserted %d", i)
	}
	drifted := strings.Join(append(append([]string{}, inserted...), lines...), "\n")

	// A hunk targeting occurrence 2 declares occ2Start, measured against the
	// ORIGINAL file. That is what a real "@@ -N" header carries.
	declared := occ2Start

	// In the drifted file, occurrence 1 has moved to exactly that line.
	driftedLines := strings.Split(drifted, "\n")
	if got := driftedLines[declared-1]; got != block[0] {
		t.Fatalf("fixture is wrong: line %d of the drifted file is %q, want the start of an occurrence (%q)", declared, got, block[0])
	}
	if occ1Start+spacing != declared {
		t.Fatalf("fixture is wrong: occurrence 1 (line %d) + drift %d != declared line %d", occ1Start, spacing, declared)
	}

	// FACT 1: the declared line now names occurrence ONE, not the intended two.
	// A positional rule would select it uniquely and write to the wrong place.
	if occ2Start+spacing == declared {
		t.Fatal("fixture is wrong: the intended occurrence must NOT be at the declared line, or there is no collision to demonstrate")
	}

	// FACT 2: today the engine refuses, and that refusal is the only thing
	// standing between this input and a silent wrong write.
	root := realTempDir(t)
	writeTempFile(t, root, "dup.txt", drifted+"\n")
	search := strings.Join(block, "\n")
	_, err := PrepareEdit(root, EditBlock{FilePath: "dup.txt", Search: search, Replace: "\tCHANGED"})
	if err == nil {
		t.Fatal("PrepareEdit accepted a SEARCH that occurs twice; the ambiguity refusal is what makes positional disambiguation unnecessary")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %v, want an ambiguity refusal", err)
	}
	// And the refusal must tell the user how to get unstuck.
	if !strings.Contains(err.Error(), "more surrounding context") {
		t.Errorf("err = %v, want it to name the remedy — widening the context is what makes the passage unique", err)
	}

	t.Logf("COUNTEREXAMPLE: occurrences at lines %d and %d (spacing %d); an earlier hunk inserting %d lines moves occurrence 1 onto line %d, which is exactly what the hunk targeting occurrence 2 declares",
		occ1Start, occ2Start, spacing, spacing, declared)
}
