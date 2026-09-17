//go:build eval

package main

import (
	"path/filepath"
	"testing"
)

// THE EVAL'S OWN GROUND-TRUTH CHECK, WITHOUT THE MODEL.
//
// resolveExactChunks does two things that have nothing to do with retrieval: it
// refuses an anchor that resolves to more than three chunks ("the ground truth
// quietly became 'anywhere in the file'"), and it refuses an anchor that resolves
// to none ("a DECLARED fact that stopped being true"). Both are properties of the
// CORPUS and the query table, not of the ranker.
//
// It needed neither the embedder nor the index for either of them -- only
// ScanWorkspace's chunks, which are pure Go and take a fraction of a second. Yet
// the only thing that ran it was TestRerankEvalRetrievalRanking, behind twenty-two
// minutes of BGE embedding in a schedule- and path-triggered workflow.
//
// WHY IT EXISTS, and it is a defect I committed. On 2026-09-16 a comment written to
// explain a fix in chunker.go spelled the qualified symbol
// `editapply.MatchesSecretName`, which is query 4's anchor. It became the second
// occurrence in that file, the chunker's overlapping windows put it in two chunks,
// and the eval went red: "resolves to 4 chunks, past the 3 ceiling". Cost: one
// 23-minute CI run and a blocked merge, for a defect this test finds in under a
// second.
//
//	A comment ABOUT a symbol is not an ANSWER to a query about it, and the eval
//	cannot tell those apart. So the ground truth pays for the explanation.
//
// WHAT WAS TRIED FIRST, AND REJECTED ON EVIDENCE. A textual check -- "no eval
// anchor may appear in a comment" -- looked cheaper still and is the wrong
// instrument. Measured: only 5 of 101 anchors are qualified symbols at all, so the
// rule covers 5% of the set; on the full set it fired on ordinary prose, because
// `scrub` and `bm25` are words; and even restricted to qualified symbols it had
// four pre-existing legitimate hits. A gate that needs four birth-defect
// exemptions for correct code is one people switch off. The right check already
// existed and was simply being run too late.
//
// WHAT THIS DOES NOT CHECK: recall, ranking, budget, or anything a model decides.
// It checks that the eval's ground truth is still well-formed against today's
// corpus, and nothing else.
func TestEvalGroundTruthResolvesWithoutTheModel(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	scan, err := ScanWorkspace(root)
	if err != nil {
		t.Fatalf("scanning workspace: %v", err)
	}

	// Anti-vacuity, both halves. resolveExactChunks reports an anchor that matches
	// NOTHING as a stale declared fact -- so an empty or tiny scan would produce a
	// flood of false "the code was moved" errors, and a scan that somehow returned
	// one enormous chunk would make every anchor resolve to exactly 1 and pass.
	if scan.FilesScanned < 100 {
		t.Fatalf("vacuity floor: scanned %d files; this repository has hundreds, so the scan is "+
			"broken and every verdict below is about nothing", scan.FilesScanned)
	}
	if len(scan.Chunks) < 1000 {
		t.Fatalf("vacuity floor: %d chunks from %d files. The corpus has thousands; a chunker "+
			"returning too few would make every anchor resolve to one chunk and pass",
			len(scan.Chunks), scan.FilesScanned)
	}
	if len(rerankEvalQueries) == 0 {
		t.Fatal("vacuity floor: the query table is empty")
	}

	// resolveExactChunks does the asserting; this is the driver that gives it a
	// corpus. Its messages are the eval's own, so a failure here reads exactly as
	// it would after twenty-three minutes.
	resolveExactChunks(t, scan.Chunks)

	t.Logf("ground truth well-formed: %d quer(ies) resolved against %d chunks over %d files, "+
		"no anchor past the 3-chunk ceiling and none resolving to zero",
		len(rerankEvalQueries), len(scan.Chunks), scan.FilesScanned)
}
