// What a chunk's vector is allowed to know that its forty lines do not say.
//
// ── THE ONE THING THAT WORKED, AND THE SIX THAT DID NOT ────────────────────
//
// A chunk is a fixed 40-line window (chunker.go). Nothing in the text handed to
// the embedder said WHERE that window came from, so a query naming a subsystem
// had to match on body statements alone. Prefixing the file path to the embedded
// text -- and nothing else -- is worth more than every structural idea tried
// before it put together.
//
// MEASURED 2026-08-28, 49 queries, k=10, budget 16,000. DELIVERED is the gated
// number: what survives retrieval, expansion, merging and the character budget.
//
// The six arms, all on one corpus (523 files / 4,459 chunks):
//
//	arm                                   delivered  retrieved  semantic  impl
//	no prefix (the baseline)              30/49      28         23        11/24
//	enclosing declaration                 28         28         24        12/24
//	enclosing declaration + doc line      27         26         25        10/24
//	   ...on every mid-body chunk         29         27         22        11/24
//	declaration + doc + FILE PATH         31         32         30        14/24
//	FILE PATH ALONE (this)                33/49      32         30        14/24
//
// Then re-measured head-to-head on the tree that actually ships (522 files /
// 4,452 chunks), because a ship decision should not rest on a comparison across
// corpora -- and it moved: the baseline is 31 here, not 30, so the real gain is
// TWO queries, not three.
//
//	                    delivered  retrieved  semantic  file-level
//	no prefix           31/49      28         24        44
//	file path (ships)   33/49      32         31        45
//
// Per shape, that is impl 13->15, cross 6->7, doc 0->1, against defuse 6->5 and
// multi 4->3. The bar agreed in advance was >=32 delivered with no shape falling
// by more than one, and both halves are met.
//
// ── WHY THE STRUCTURAL ARMS LOSE, CONFIRMED FROM BOTH DIRECTIONS ───────────
//
// Read the last two rows of the six together. They differ by exactly one thing:
// whether the enclosing construct's name is prepended alongside the path. Adding
// it COSTS TWO QUERIES. The structural half is not merely unhelpful here, it is
// negative, and the arms without a path (28, 27, 29) all sit BELOW their
// baseline of 30.
//
// This is the same mechanism that killed construct-aligned BOUNDARIES three
// times in 2026-08-27/28 (the table at the top of chunkContent). Both make a
// single 384-dimensional vector describe one narrower thing. A file path does
// the opposite: it is coarse, it is identical for every chunk of a file, and it
// says which document this is rather than which function.
//
// The header arms leave a signature that says it out loud. Multi-answer queries
// -- the ones with more than one legitimate file -- fall from 4/4 to 2/4 in
// EVERY arm that names a construct, and hold at 3/4 or 4/4 in the two that do
// not. Naming one construct makes its sibling chunks resemble each other, and
// one file then crowds out the others the query needed.
//
// So: six measured attempts to put program structure INTO the vector, six
// losses. Do not attempt a seventh. If structure is to help retrieval on this
// corpus it has to act somewhere it cannot narrow a vector -- on what is
// DELIVERED once retrieval has already found the file, which is untried.
//
// The symbol layer at the bottom of this file is kept for exactly that: it is
// recovered verbatim from the reverted 880df70:daemon/chunkboundary.go, and
// graded against go/parser by TestTheHeuristicAgreesWithTheCompiler. What is NOT
// kept is that commit's snapping machinery, which is the part the corpus refused.
package main

import "strings"

// embedPrefixPolicy selects what is prepended to a chunk's text before it is
// embedded. Two values, because two is what is measured: the shipped one and
// the baseline it has to beat.
type embedPrefixPolicy string

const (
	// prefixNone is the pre-2026-08-28 behaviour -- the embedder sees exactly
	// the chunk's own lines. Kept so the eval can re-measure the baseline in the
	// tree that ships, rather than comparing against a number from a past run.
	prefixNone embedPrefixPolicy = "none"
	// prefixPath prepends the workspace-relative file path.
	prefixPath embedPrefixPolicy = "path"
)

// activeEmbedPrefix is interpolated into chunkerID, so changing it invalidates
// every existing index rather than leaving vectors built under one policy to be
// queried under another.
const activeEmbedPrefix = prefixPath

// embedPrefixFor returns the text prepended to a chunk of relPath before
// embedding, including its trailing newline, or "" under prefixNone.
//
// EVERY CHUNK GETS IT, which is the property that distinguishes this from the
// construct headers that lost. A prefix only some chunks carry introduces an
// asymmetry between them; a prefix all of them carry adds a dimension the
// embedder can use to tell files apart.
func embedPrefixFor(relPath string) string {
	if activeEmbedPrefix == prefixNone || relPath == "" {
		return ""
	}
	return relPath + "\n"
}

// embedTextOf is THE ONE FUNNEL through which a chunk becomes embedder input,
// and embedTextsFor is it applied to a batch.
//
// THEY EXIST RATHER THAN FOUR `.Content` ASSIGNMENTS because there were exactly
// four, and two of them are the INSTRUMENTS: indexWorkspace and reindexFile are
// production, but indexRepoExcludingSelfReference (rerank_eval_test.go) and the
// token-efficiency eval build their own indexes the same way. Changing only the
// two production sites would have left both evals embedding the OLD
// representation and grading a product that no longer exists -- the same fault
// as `const displayK = 5`, and as the character budget going unmeasured until
// 2026-08-28. One funnel is what stops a fifth site from drifting.
//
// THE FALLBACK IS LOAD-BEARING. A chunk read back out of the vector store has no
// EmbedText unless Existing reconstructed it (vectorstore.go), so anything
// re-embedding a stored chunk would otherwise embed the empty string and write a
// vector describing nothing.
func embedTextOf(c Chunk) string {
	if c.EmbedText != "" {
		return c.EmbedText
	}
	return c.Content
}

func embedTextsFor(chunks []Chunk) []string {
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = embedTextOf(c)
	}
	return texts
}

// ── The symbol layer ───────────────────────────────────────────────────────
//
// Recovered from 880df70:daemon/chunkboundary.go. Nothing on the indexing path
// uses it: it is kept because it is the only measured, language-agnostic way
// this daemon can find a construct, and because delivery-time structure is the
// one place structure has not yet been tried. TestTheHeuristicAgreesWithThe
// Compiler grades it against go/parser on every run.

// constructStarts returns the 0-based line indexes at which a top-level
// construct begins, in order.
//
// A DOC COMMENT BELONGS TO WHAT IT DOCUMENTS. The declaration line itself is
// the obvious boundary and it is the wrong one: it would leave the comment block
// attributed to whatever precedes it. So the start walks back over the
// contiguous column-zero comment lines directly above the declaration.
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
		// (two declarations sharing one comment block); keep starts strictly
		// increasing so a consumer never sees a table it cannot advance through.
		if len(starts) > 0 && start <= starts[len(starts)-1] {
			continue
		}
		starts = append(starts, start)
	}
	return starts
}

// constructExtents returns each top-level construct's line range, 1-indexed and
// INCLUSIVE, in source order.
//
// THE END IS INFERRED, NOT PARSED: a construct runs to the line before the next
// one starts, with trailing blank lines trimmed off. There is no brace matching
// here and deliberately so -- this daemon indexes languages it has no parser
// for, and the whole value of the column-zero heuristic is that it degrades to
// "slightly wrong" rather than "absent" on Ruby.
//
// MEASURED against brace counting over all 3,944 top-level Go constructs in this
// repository: exact on 87.2%, OVER-extends on 12.7% (median 9 lines, max 61),
// and under-extends on 0.1%. That asymmetry is what makes it usable. Expansion
// widens a retrieved chunk to a construct, so an extent that is slightly too
// LARGE delivers a few extra lines, while one that is too SMALL silently drops
// the part of the function the query was about. A near-superset is the safe
// direction, and TestConstructExtentsNeverStopShortOfTheCompiler holds it there.
//
// (The 12.7% is mostly file headers: the "construct" starting at line 1 of a Go
// file is the package clause, and its extent absorbs the import block.)
func constructExtents(lines []string) [][2]int {
	starts := constructStarts(lines)
	out := make([][2]int, 0, len(starts))
	for i, s := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		for end > s+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		out = append(out, [2]int{s + 1, end})
	}
	return out
}

// isConstructStart reports whether line opens a top-level construct.
//
// Deliberately broader than declarationName, which answers a different question.
// The repository map wants a NAME to show; this wants to know a construct BEGAN,
// and a grouped declaration block -- `const (`, `var (`, `type (` -- is a real
// construct with no single name to give the map. Sharing declarationName alone
// therefore left this blind to exactly those.
//
// MEASURED against go/parser over every Go file in this repository: the
// column-zero rule agreed on 97.7% of the compiler's own top-level declaration
// positions with ZERO false positives, and every one of the 79 disagreements was
// a grouped block. Handling that one shape is what closes the gap.
// trimCR removes the carriage return a CRLF line keeps after splitLines, which
// splits on "\n" alone (chunker.go).
//
// STRUCTURE ONLY, NEVER CONTENT. It is applied inside the predicates that ask
// what a line IS, and nowhere near the text that gets chunked, embedded or shown
// to the model -- so a Windows checkout is still indexed with the bytes it has on
// disk. The alternative, stripping in splitLines, would quietly change every
// chunk's text on such a tree and with it every embedding.
//
// WHAT IT COSTS TO OMIT, measured on a CRLF checkout of this repository: the
// heuristic missed 87 of 3745 top-level declarations, all of them grouped
// declarations, because isGroupedDeclOpener asks whether the line ENDS in "(" and
// "const (\r" does not. Chunk expansion then widens to the wrong region, so
// retrieval is worse on Windows than on Linux for the same source.
//
// repomap.go never had this problem and that is not luck: declarationsIn reads
// through bufio.Scanner, whose ScanLines drops the CR for free. Only the
// hand-rolled split needed telling.
func trimCR(line string) string {
	return strings.TrimSuffix(line, "\r")
}

func isConstructStart(line string) bool {
	line = trimCR(line)
	if declarationName(line) != "" {
		return true
	}
	return isGroupedDeclOpener(line)
}

// isGroupedDeclOpener matches `const (`, `var (` and `type (`.
//
// NOT `import (`, and that exclusion is measured rather than stylistic: import
// blocks are top-level declarations by the compiler's reckoning and account for
// 19 of every 25 positions the heuristic "missed", but an import block is the
// least informative thing in a file to name a region after. Counting them made
// the heuristic look 12% wrong when it is 2.3% wrong.
func isGroupedDeclOpener(line string) bool {
	trimmed := strings.TrimRight(trimCR(line), " \t")
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
	line = trimCR(line)
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return false
	}
	return strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") ||
		strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "*") ||
		strings.HasPrefix(line, "--")
}
