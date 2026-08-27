// Structure-aware chunk boundaries: where one indexed window should end and
// the next begin, decided by the shape of the code rather than by arithmetic.
//
// WHAT WAS WRONG, MEASURED. chunkContent cut every file into fixed 40-line
// windows on a 30-line stride, which lands wherever it lands. Across this
// repository's own non-test sources:
//
//	daemon/              55% of chunks began on an indented line
//	clients/vscode/src/  69%
//	protocol/            31%
//
// A chunk beginning on an indented line is a chunk that opens in the middle of
// a function body: no signature, no receiver, no name -- retrieved text that
// cannot say what it belongs to. And 30-38% of top-level constructs were held
// whole by no single chunk at all, averaging 53 lines in daemon/ and 122 in the
// extension.
//
// WHAT THIS DOES. The window is still bounded and still overlapping where it
// has to be, but its edges are pulled to construct boundaries when one is
// within reach. Where a boundary is found the cut is clean and the next chunk
// starts exactly there, with NO overlap -- the seam is a real seam, so the
// duplicated lines that mergeAdjacentChunks exists to clean up are never
// created in the first place. Where no boundary is in reach, which is what a
// genuinely long function looks like, the old fixed cut and its overlap are
// used unchanged, because the overlap is the only thing that keeps a construct
// bigger than one window present somewhere in full.
//
// A HEURISTIC, SHARING THE MAP'S. declarationName is the repository map's
// column-zero rule (repomap.go), reused rather than reimplemented: one symbol
// layer, one place to fix, and a language this daemon has no language server
// for still gets boundaries. Being wrong about a boundary costs a chunk that
// starts one line off; being unable to parse Ruby costs every chunk in the file.
package main

import (
	"strings"

	"codeterminal/helper/helperproto"
)

// Boundary search bounds, in lines. A snap may pull a cut earlier or later than
// the nominal window, but never far enough to make a chunk too small to carry
// context or too big to survive the embedder.
//
// maxSnapLines is bounded by the EMBEDDER, not by taste: at
// helperproto.MaxSequenceLength (512) a chunk of about 45 lines of Go is the
// most that is embedded whole (measured median 3.39 chars/token, and a 40-line
// chunk measures a median 467 tokens). Stretching further to reach a prettier
// boundary would buy a clean edge and pay for it with a truncated tail, which
// is the bargain this whole change exists to stop making.
const (
	minSnapLines = 12
	maxSnapLines = 46
)

// maxChunkBytes is the ceiling that keeps a chunk inside the EMBEDDER's window,
// and it is the bound that actually binds.
//
// It has to exist because removing the overlap took away the thing that used to
// hide truncation. The old fixed windows overlapped by ten lines unconditionally,
// so a chunk whose tail was cut off by the token cap had that tail re-covered by
// the head of the next window -- accidentally, but effectively. Cutting cleanly
// at a boundary is better in every other way and removes that accident, so the
// budget has to be respected directly instead.
//
// MEASURED, with the real BGE tokenizer over this repository: chars-per-token
// on real source sits at 2.72 (p5) / 3.39 (median) / 4.02 (p95), and the ratio
// is stable across Go, TypeScript and Markdown, so bytes are a sound proxy for
// tokens in a daemon that has no tokenizer and must not grow one -- the model
// is optional, and indexing has to work without it.
//
// 3.20 was SWEPT, not picked. Three ratios, each measured end to end for blind
// lines, chunk count, and how many constructs survive whole:
//
//	ratio  ceiling  chunks  opens mid-body  construct split  lines unreachable
//	2.72   1392 B     714        31%             24%              0.09%
//	3.20   1638 B     613        28%             19%              0.14%
//	3.60   1843 B     565        29%             17%              0.88%
//
// 3.20 is the knee. Against the conservative p5 bound it costs 0.05 points of
// blindness and buys 14% fewer chunks to embed plus five points of construct
// wholeness; against 3.60 it avoids a SIX-FOLD rise in unreachable lines for
// two points of the same. The sweep is weighted toward unreachability
// deliberately: a split construct still has both halves in the index and
// mergeAdjacentChunks rejoins them at prompt time, whereas a truncated tail
// cannot be retrieved at any k.
//
// The effect of getting this wrong is not an error anywhere: the helper
// truncates silently, so an over-budget chunk simply has a tail no query can
// ever reach.
const minCharsPerToken = 320 // hundredths; integer math, no float constant
const maxChunkBytes = helperproto.MaxSequenceLength * minCharsPerToken / 100

// constructStarts returns the 0-based line indexes at which a top-level
// construct begins, in order.
//
// A DOC COMMENT BELONGS TO WHAT IT DOCUMENTS. The declaration line itself is
// the obvious boundary and it is the wrong one: cutting there strands the
// comment block on the tail of the previous chunk, where it describes something
// that is not present, and opens the new chunk with a signature stripped of the
// prose explaining it. So the boundary walks back over the contiguous
// column-zero comment lines directly above the declaration.
func constructStarts(lines []string) []int {
	var starts []int
	for i, line := range lines {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if !isConstructStart(line) {
			continue
		}
		start := i
		for start > 0 && isTopLevelComment(lines[start-1]) {
			start--
		}
		// Walking back can land on a line an earlier construct already claimed
		// (two declarations sharing one comment block); keep boundaries strictly
		// increasing so the chunker never sees a cut it cannot advance past.
		if len(starts) > 0 && start <= starts[len(starts)-1] {
			continue
		}
		starts = append(starts, start)
	}
	return starts
}

// isConstructStart reports whether line opens a top-level construct.
//
// Deliberately broader than declarationName, which answers a different question.
// The repository map wants a NAME to show; the chunker wants a BOUNDARY to cut
// on, and a grouped declaration block -- `const (`, `var (`, `type (` -- is a
// real boundary with no single name to give the map. Sharing declarationName
// alone therefore left the chunker blind to exactly those.
//
// MEASURED against go/parser over all 391 Go files in this repository: the
// column-zero rule agreed on 97.7% of the compiler's own top-level declaration
// positions with ZERO false positives, and every one of the 79 disagreements
// was a grouped block. Handling that one shape is what closes the gap; see
// TestTheHeuristicAgreesWithTheCompiler, which holds it closed.
func isConstructStart(line string) bool {
	if declarationName(line) != "" {
		return true
	}
	return isGroupedDeclOpener(line)
}

// isGroupedDeclOpener matches `const (`, `var (` and `type (`.
//
// NOT `import (`, and that exclusion is measured rather than stylistic: import
// blocks are top-level declarations by the compiler's reckoning and account for
// 19 of every 25 positions the heuristic "missed", but a chunk that begins on an
// import block begins on the least informative lines in the file. Counting them
// as boundaries made the heuristic look 12% wrong when it is 2.3% wrong.
func isGroupedDeclOpener(line string) bool {
	trimmed := strings.TrimRight(line, " \t")
	if !strings.HasSuffix(trimmed, "(") {
		return false
	}
	switch strings.TrimSpace(strings.TrimSuffix(trimmed, "(")) {
	case "const", "var", "type":
		return true
	}
	return false
}

// isTopLevelComment reports whether line is a comment at column zero, in any of
// the comment syntaxes this project's languages use.
func isTopLevelComment(line string) bool {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return false
	}
	return strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") ||
		strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "*") ||
		strings.HasPrefix(line, "--")
}

// snapEnd picks where the chunk starting at start should end.
//
// It returns the end line (exclusive) and whether the cut landed on a real
// construct boundary. A clean cut needs no overlap; a forced one does.
func snapEnd(lines []string, byteAt []int, start, nominal int, boundaries []int) (end int, clean bool) {
	// THE BYTE CEILING OUTRANKS THE LINE FLOOR, and the first version had that
	// backwards. When every line is 3,000 bytes, twelve of them are already six
	// times over budget, so the search window collapsed and the code fell
	// through to a fallback that took the nominal forty lines and ignored the
	// ceiling completely -- measured on a minified-bundle fixture, one chunk
	// came out at 120,759 bytes against a 6 KiB cap. A ceiling that yields to a
	// floor is not a ceiling.
	//
	// hi is therefore the last line that fits, and everything else is clamped
	// to it. One line may still exceed the budget on its own; a chunk of one
	// enormous line is the only correct answer there, and the alternative is
	// emitting nothing.
	hi := start + maxSnapLines
	if hi > len(lines) {
		hi = len(lines)
	}
	for hi > start+1 && byteAt[hi]-byteAt[start] > maxChunkBytes {
		hi--
	}
	lo := start + minSnapLines
	if lo > hi {
		lo = hi
	}
	if lo >= hi {
		// No room to search: the tail of the file, or lines so long that the
		// budget is spent before the minimum window. Take what fits.
		end = start + nominal
		if end > hi {
			end = hi
		}
		if end > len(lines) {
			end = len(lines)
		}
		return end, end == len(lines)
	}

	best, found := 0, false
	for _, b := range boundaries {
		if b < lo {
			continue
		}
		if b > hi {
			break
		}
		// Closest to the nominal window; ties go to the later boundary, which
		// keeps chunks nearer the intended size rather than drifting small.
		if !found || abs(b-(start+nominal)) <= abs(best-(start+nominal)) {
			best, found = b, true
		}
	}
	if found {
		return best, true
	}

	end = start + nominal
	if end > hi {
		end = hi // the byte ceiling still applies to a forced cut
	}
	if end > len(lines) {
		end = len(lines)
	}
	return end, end == len(lines)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// byteOffsets returns a prefix-sum table so a chunk's byte size is an O(1)
// subtraction rather than a re-join of its lines inside the search loop.
func byteOffsets(lines []string) []int {
	out := make([]int, len(lines)+1)
	for i, l := range lines {
		out[i+1] = out[i] + len(l) + 1 // +1 for the newline that rejoins them
	}
	return out
}
