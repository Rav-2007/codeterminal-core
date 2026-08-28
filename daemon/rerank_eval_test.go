//go:build eval

// See eval_test.go's header for why this is gated behind the "eval" build
// tag (real BGE model + onnxruntime + a freshly built helper binary — not
// part of the fast, offline `go test ./...` path). Run it explicitly with:
//
//	go test -tags eval -run TestRerankEvalRetrievalRanking -v ./...
//
// This is the acceptance test for retrieval RANKING specifically: it indexes
// the actual CodeTerminal repo (not a curated testdata/ subset — the
// measured failures this fixes were found against this real repo, and
// expected files/chunks are real repo-relative paths/line ranges) and runs
// 49 queries against it.
//
// ── WHY 49 AND NOT 9 ───────────────────────────────────────────────────────
//
// It ran 9 queries until 2026-08-28, and on 2026-08-27 that cost a shipped
// regression. A structure-aware chunking change took real-repo recall from 8/9
// to 4/9; `make check` was green, the small offline fixture eval reported
// 14/15, this eval was schedule-only, and the change landed. Reverted in
// b552daf. Nine queries also cannot resolve ordinary retrieval work: one flip
// is 11pp, and the 256→512 embed-window change measured 8/9 on both sides.
//
// The expansion was verified by re-applying the reverted chunking in a scratch
// worktree and running both evals against it. MEASURED 2026-08-28:
//
//	                          fixture eval      this eval
//	HEAD (fixed windows)      14/15  PASS       24/49 (49.0%)  PASS
//	structure-aware chunks    14/15  PASS       17/49 (34.7%)  FAIL
//
// The fixture eval cannot see the regression -- it scores identically on both
// -- which is what it did in real life. This eval fails on it by 5.3pp under
// the floor.
//
// The per-shape breakdown then reproduced, unprompted, the diagnosis that had
// taken a manual investigation: def-vs-use queries collapsed from 6/7 to 1/7,
// while implementation-seeking queries actually IMPROVED, 8/24 to 10/24.
// Cutting cleanly at construct boundaries separates a declaration from the code
// that uses it, and a query like "where is the ZDR refusal string matched"
// wants the matching code. Better-formed chunks, worse retrieval.
//
// ── WHAT THE NUMBER MEANS ──────────────────────────────────────────────────
//
// The GATED number is DELIVERED: what survives retrieval, merging, and the
// character budget, because only that reaches the model. MEASURED 2026-08-28 at
// k=10 / 16000 chars: 28/49 (57.1%) delivered, 30/49 (61.2%) retrieved,
// 43/49 (87.8%) file-level. Of the misses, 13 are the RIGHT FILE with the wrong
// forty lines of it and 6 are the right file never retrieved at all -- so most
// of the remaining failure mass is granularity, not ranking.
//
// Do not compare any of this to the old 8/9 = 89%. That set was built out of
// failures that had already been fixed; it was measuring questions retrieval
// had been taught to answer.
//
// ── THE BUDGET WAS THE BINDING CONSTRAINT, NOT THE RANKER ──────────────────
//
// Measured 2026-08-28 through the full production path, which is what prompted
// k=5 -> 10 and 8000 -> 16000 chars:
//
//	k=5,  budget=8000 (shipped until now)   23/49 (46.9%)  truncated on 27/49
//	k=10, budget=8000                       24/49 (49.0%)  truncated on 49/49
//	k=10, budget=12000                      27/49 (55.1%)  truncated on 49/49
//	k=10, budget=16000 (now)                30/49 (61.2%)  truncated on 21/49
//
// Raising k alone is nearly a no-op and briefly delivers FEWER spans (3.2 vs
// 3.7), because merging folds the window overlap and the survivors are bigger.
// The two constants are one decision. This eval reads both from production
// (defaultK, defaultContextBudgetChars) rather than hardcoding them, so it
// cannot drift away from the shipped configuration the way it did when
// displayK was written as a literal 5.
//
// CHUNK-LEVEL, not file-level: exactChunks below names the specific
// chunk(s) (file:startLine-endLine) that actually contain the relevant
// symbol/logic, and TestRerankEvalRetrievalRanking's hit criterion checks
// THAT, not merely whether some chunk from the expected file(s) appears.
// This distinction is load-bearing, not cosmetic — file-level checking is
// exactly what hid the original defect (see below).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// rerankEvalQuery is one query from the measured stress tests, with the
// repo-relative file(s) that count as a file-level match (reported for
// context only) and the specific chunk ID(s) — file:startLine-endLine,
// matching Chunk.ID from chunker.go's chunkContent — that actually contain
// the symbol/logic in question. exactChunks is the real, gating check;
// expectedFiles exists only so the eval's printed table can show the old,
// looser signal alongside the new one.
// anchor is a literal substring of the code that actually answers the query. It
// is what makes exactChunks CHECKABLE rather than merely asserted: the harness
// verifies, before running any query, that the anchor really does appear inside
// one of the named chunks.
//
// This exists because the 2026-07-30 launch-gate review found this eval RED at
// 4/9 and the cause was not retrieval at all -- it was that exactChunks had gone
// STALE. Chunk IDs are file:startLine-endLine, so every edit to a named file
// shifts them, and five of the nine queries were pointing at line ranges whose
// contents had moved. Query 8 ("where is the ZDR refusal string matched")
// expected daemon/provider.go:91-130, which holds the chatCompletionChunk struct;
// the ZDR matching it names lives ~150 lines further down. Retrieval was
// returning the correct chunk and being scored wrong.
//
// A stale expectation and a retrieval regression are indistinguishable in the
// pass/fail signal but demand opposite responses, so the harness now tells them
// apart by name. Without this the eval measures how much the repo has been
// edited since the expectations were written.
type rerankEvalQuery struct {
	query string
	// expectedFiles are the files that legitimately answer this query. DECLARED,
	// because a file changes name only when something is deliberately moved, and
	// that is a change a human should have to acknowledge here.
	expectedFiles []string
	// anchors are literal substrings of the code that actually answers the
	// query -- at least one per expected file that has a chunk-level answer.
	// DECLARED, because what counts as the answer is a judgement, not a fact the
	// harness can derive.
	anchors []string
	// shape is which KIND of question this is. Declared, because the value of
	// knowing it is exactly that a human decided it.
	//
	// It exists because an aggregate recall number tells you a regression
	// happened and nothing about where to look. The 2026-08-27 chunking
	// regression cost five queries; had the set been labelled, the report
	// would have said the loss was concentrated in one class instead of
	// leaving "recall fell" as the whole diagnosis. The per-shape breakdown
	// printed by TestRerankEvalRetrievalRanking is not gated -- a per-class
	// floor on 1-10 queries per class would be noise -- it is there to point
	// at the cause once the gated overall rate has already fired.
	shape string
}

// The query shapes. These are the kinds of question the product actually gets
// asked, plus the two failure modes this repo has measured and recorded.
const (
	// shapeImpl is "where is X done" -- the dominant real shape.
	shapeImpl = "impl"
	// shapeDefUse is a query whose anchor exists at BOTH a declaration and a
	// use site, so retrieval has to pick the one that answers the question.
	// This is the mechanism behind the 2026-08-27 regression: splitting chunks
	// on construct boundaries cleanly separated declarations from the code
	// that uses them, and the wrong half started winning.
	shapeDefUse = "defuse"
	// shapeTest is a genuinely test-seeking query. It is the paired guard for
	// rerank.go's FileClassTest down-weight: any change that helps
	// implementation-seeking queries by pushing tests down must not push them
	// out of reach of someone who is actually asking about a test.
	shapeTest = "test"
	// shapeCross is a query whose answer lives in a different Go module from
	// the component the question names. These catch seam moves -- query 1's
	// expectation was silently wrong for months after net.Listen moved from
	// daemon to protocol.
	shapeCross = "cross"
	// shapeDoc is the documented doc-comment-vs-implementation gap: a prose
	// comment that contains the query's words and none of its code outranks
	// the code. Only query 1 is labelled this, and it is the one known miss.
	shapeDoc = "doc"
	// shapeMulti is a query with more than one legitimate home, where
	// expectedFiles is a set rather than a single file.
	shapeMulti = "multi"
)

// evalQueryShapeOrder fixes the order shapes are reported in, so the printed
// breakdown is diffable between runs. A shape present on a query but missing
// here is a compile-time-invisible mistake, so the summary counts what it
// prints and fails if the totals do not add up.
var evalQueryShapeOrder = []string{shapeImpl, shapeDefUse, shapeTest, shapeCross, shapeDoc, shapeMulti}

// resolveExactChunks computes each query's chunk-level ground truth FROM THE
// INDEX, rather than reading it from a list written by hand.
//
// WHY THE GROUND TRUTH IS NOW DERIVED. It used to be a literal list of chunk
// IDs, and a chunk ID is file:startLine-endLine -- so every edit to a named
// file shifted the ranges and the expectation silently stopped describing the
// code. The 2026-07-30 launch-gate review found this eval red at 4/9 with
// retrieval working perfectly; the answer key had rotted. A staleness CHECK was
// added then, which was the right first move: it made the harness say which of
// the two it was. But a check only converts a wrong number into a chore, and
// the chore came due again five queries at a time -- on 2026-08-07 the scheduled
// job was red with STALE EXPECTATION on 5 of 9, one of them because a startup
// refactor had moved the code the first query points at.
//
// Deriving removes the failure mode instead of reporting it. expectedFiles and
// anchors are stable facts about the codebase, and the volatile part -- which
// chunk holds the anchor today -- is computed from the same index the queries
// are run against, every run.
//
// It is NOT a weakening. The check is still chunk-level, which is the whole
// point of this eval (file-level checking is what hid the original defect): the
// derived set is the chunks that contain the anchor, not every chunk in the
// file. The three guards below are what keep it that way.
func resolveExactChunks(t *testing.T, chunks []Chunk) [][]string {
	t.Helper()

	// PER ANCHOR, not per query, because "is this anchor specific?" is the
	// property that matters and it does not get weaker just because a query has
	// three expected files.
	//
	// A single line falls inside at most two chunks (40-line windows on a
	// 30-line stride), so an anchor occurring once yields 1-2. Three allows for
	// one that legitimately appears twice, and refuses to let the ground truth
	// quietly become "anywhere in the file" -- which would make every query pass
	// and mean nothing.
	const maxChunksPerAnchor = 3

	resolved := make([][]string, len(rerankEvalQueries))
	for i, q := range rerankEvalQueries {
		if len(q.anchors) == 0 {
			t.Errorf("query %d (%q) declares no anchors, so it has no checkable "+
				"chunk-level ground truth at all", i+1, q.query)
			continue
		}

		seen := make(map[string]bool)
		var ids []string
		for _, a := range q.anchors {
			var here []string
			elsewhere := make(map[string]bool)
			for _, c := range chunks {
				if !strings.Contains(c.Content, a) {
					continue
				}
				if matchesAny(c.FilePath, q.expectedFiles) {
					here = append(here, chunkID(c))
				} else {
					elsewhere[c.FilePath] = true
				}
			}

			if len(here) == 0 {
				t.Errorf("query %d (%q): anchor %q appears in no indexed chunk of %v. This is a "+
					"DECLARED fact that stopped being true -- the code was moved or renamed -- "+
					"and it is NOT a retrieval regression. It currently appears in: %v",
					i+1, q.query, a, q.expectedFiles, sortedFileNames(elsewhere))
				continue
			}
			if len(here) > maxChunksPerAnchor {
				t.Errorf("query %d (%q): anchor %q resolves to %d chunks (%v), past the %d "+
					"ceiling. An anchor that broad makes the ground truth 'somewhere in the "+
					"file', which is the file-level check this eval exists to be stricter than.",
					i+1, q.query, a, len(here), here, maxChunksPerAnchor)
			}
			// Reported, never failed. A shared helper name legitimately appears
			// in several callers; it matters only if one of them should have been
			// declared in expectedFiles.
			if len(elsewhere) > 0 {
				t.Logf("query %d (%q): anchor %q also appears outside expectedFiles, in %v -- "+
					"harmless unless one of those is a better answer than what is declared",
					i+1, q.query, a, sortedFileNames(elsewhere))
			}

			for _, id := range here {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		sort.Strings(ids)
		resolved[i] = ids
	}
	return resolved
}

func sortedFileNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var rerankEvalQueries = []rerankEvalQuery{
	// THE ONE REMAINING KNOWN GAP (not gated -- see the note after mustHit).
	// The top-ranked chunk for this query is a package doc comment that reads as
	// an excellent semantic match AND contains the same words the real call
	// does, without containing the call.
	//
	// The answer used to be in daemon/main.go and is now in protocol: the
	// transport seam moved every net.Listen behind protocol.Listen so the
	// Windows named-pipe implementation could sit beside it. The old
	// expectation pointed at daemon/main.go:181-220 and had been scoring
	// correct retrieval as a miss ever since.
	{"where does the daemon open the unix socket",
		[]string{"protocol/transport_unix.go"},
		[]string{`net.Listen("unix"`}, shapeDoc},

	// editblock.go moved from daemon/ to editapply/ in an earlier, unrelated
	// refactor (c516479).
	{"how are edit blocks parsed from the model response",
		[]string{"editapply/editblock.go"},
		[]string{"func ParseEditBlocks"}, shapeCross},

	// The tier decision is made in Route, not in the const/type block at the top
	// of the file -- which is what the file-level check could not tell apart.
	{"where is the model tier routing decided",
		[]string{"daemon/router.go"},
		[]string{"func Route(cfg *Config"}, shapeImpl},

	// The actual secret-skipping decision is editapply.MatchesSecretName, called
	// from shouldSkipFile. index_cmd.go only REPORTS skip counts, so it stays an
	// acceptable file-level match with no chunk-level answer of its own.
	{"how does secret skipping work during indexing",
		[]string{"daemon/chunker.go", "daemon/index_cmd.go"},
		[]string{"editapply.MatchesSecretName"}, shapeMulti},

	// Two things legitimately answer this: where the DB lives, and its schema.
	// The second anchor was "CREATE TABLE IF NOT EXISTS", which memory.go uses
	// for four tables -- so it resolved to 4 chunks against the ceiling of 3 and
	// failed this eval on the BASELINE, before any retrieval change. A fixture
	// fault, not retrieval: named the specific table instead.
	{"where are conversation turns stored in sqlite",
		[]string{"daemon/memory.go"},
		[]string{"func OpenMemoryStore"}, shapeImpl},

	// Added for the _test.go down-weight fix (FileClassTest, rerank.go): the
	// measured live-repo failure this fix targets -- provider.go never made the
	// top-5 at all because provider_test.go/config_test.go out-ranked it despite
	// identical class weight. Implementation-seeking, so the test down-weight
	// must apply here. One of the two queries that motivated hybrid retrieval:
	// the answer is the substring TABLE, which the file-level check could not
	// tell apart from provider.go's other chunks (the ErrZDRRefused sentinel and
	// its doc comment) that rank better semantically and contain none of it.
	{"where in the code is the ZDR refusal string matched, and what substring does it match on?",
		[]string{"daemon/provider.go"},
		[]string{"zdrRefusalSubstrings"}, shapeDefUse},

	// The paired regression guard for the same fix: a genuinely test-seeking
	// query must still find the test files, or it would start failing the moment
	// the down-weight above is added. Two anchors, because two files carry a
	// real chunk-level answer.
	{"how is the ZDR refusal detection logic tested end to end",
		[]string{"daemon/provider_test.go", "daemon/config_test.go"},
		[]string{
			"func TestIsZDRRoutingRefusal_MatchesKnownPhrasings",
			"func TestZDRConfig_ZeroValueResolvesToStrictEnforcement",
		}, shapeTest},

	{"where is the ZDR refusal string matched",
		[]string{"daemon/provider.go"},
		[]string{"zdrRefusalSubstrings"}, shapeDefUse},

	// The second symbol/string query that motivated hybrid retrieval: a
	// natural-language question with essentially no shared vocabulary with any
	// single chunk's dominant semantic content, whose three real answer chunks
	// (the struct, its search implementation, and its server-side dispatch) were
	// measured at raw semantic ranks #273, #352 and #164 out of 708 -- nowhere
	// near the ~30-candidate rerank pool for k=5.
	{"what files does SearchRequest touch",
		[]string{"protocol/protocol.go", "daemon/search.go", "daemon/server.go"},
		[]string{
			"type SearchRequest struct",
			"func (s *MemoryStore) SearchTurns",
			// BOTH halves of the server-side dispatch. isSearchRequest is the
			// sniffer that routes the message and handleSearch is what runs it;
			// the expectation this replaces named two adjacent server.go chunks
			// for exactly that reason, and listing only the handler was a
			// transcription slip that scored a correct hit as a miss.
			"func isSearchRequest",
			"func (s *Server) handleSearch",
		}, shapeMulti},

	// ------------------------------------------------------------------
	// CORPUS EXPANSION, 2026-08-28. Everything above this line is the
	// original nine; everything below was added to make this eval able to
	// resolve the effects retrieval work actually turns on.
	//
	// WHY. On 2026-08-27 a chunking change (structure-aware boundaries) took
	// real-repo recall from 8/9 to 4/9 while `make check` was fully green and
	// the small offline fixture eval reported 14/15. It shipped, and was
	// reverted in b552daf. At nine queries one flip is 11pp, so this eval
	// could not have distinguished a real 1-query regression from noise
	// either; the embed-window change measured 8/9 both before and after.
	// BACKLOG recorded the same thing as open debt ("the locate-eval
	// saturation flag").
	//
	// Every anchor below was verified to resolve to 1-3 chunks against a real
	// index before being committed -- the same bar resolveExactChunks
	// enforces at run time -- so this set starts honest rather than starting
	// red and being ignored.
	//
	// NOT tuning. No retrieval code changed with this expansion; the score
	// moves only because more questions are being asked.

	{"how does the daemon decide an index is stale and refuse to use it",
		[]string{"daemon/embedderstamp.go"},
		[]string{"func checkEmbedderStamp"}, shapeImpl},

	// Anchor is a constant that exists at its declaration and at the branch
	// that returns it, which is the distinction this shape is here to watch.
	{"where does the daemon ask the user to approve a tool call",
		[]string{"daemon/toolapproval.go"},
		[]string{"ApprovalCancelTurn"}, shapeDefUse},

	{"how are secrets stripped out of retrieved code before it is sent to the model",
		[]string{"daemon/scrub.go"},
		[]string{"func scrub(text string"}, shapeImpl},

	{"where does the agent loop stop iterating",
		[]string{"daemon/agentloop.go"},
		[]string{"maxTurnIterations"}, shapeDefUse},

	{"how does the daemon notice the client disappeared in the middle of a turn",
		[]string{"daemon/agentturn.go"},
		[]string{"clientGone = true"}, shapeImpl},

	{"where are overlapping retrieved chunks folded into one span",
		[]string{"daemon/chunkmerge.go"},
		[]string{"func mergeAdjacentChunks"}, shapeImpl},

	// Asks about "the daemon"; the answer is in the protocol module, behind
	// the same transport seam that broke query 1's expectation.
	{"how does the daemon check who is on the other end of the unix socket",
		[]string{"protocol/peerauth_linux.go"},
		[]string{"SO_PEERCRED"}, shapeCross},

	{"where is a tool result truncated when it is too big",
		[]string{"daemon/toolresult.go"},
		[]string{"func renderToolResult"}, shapeImpl},

	{"how does the proxy limit how many requests a key can make",
		[]string{"proxy/ratelimit.go"},
		[]string{"func (l *rateLimiter) allow("}, shapeCross},

	{"where does the workspace watcher avoid following a symlink out of the project",
		[]string{"daemon/watcher.go"},
		[]string{"IsLinkLike"}, shapeImpl},

	{"how is a file classified as test or documentation for ranking",
		[]string{"daemon/fileclass.go"},
		[]string{"func classifyFile"}, shapeImpl},

	{"where does the daemon write the per-workspace lockfile the clients look for",
		[]string{"protocol/paths.go", "protocol/protocol.go"},
		[]string{"func LockPathFor"}, shapeMulti},

	{"where does the daemon build the map of the workspace it shows the planner",
		[]string{"daemon/repomap.go"},
		[]string{"func buildRepoMap"}, shapeImpl},

	{"how is the repository map kept inside its byte budget",
		[]string{"daemon/repomap.go"},
		[]string{"pathBudgetTotal"}, shapeDefUse},

	{"where do the specialist roles for a multi-agent turn get defined",
		[]string{"daemon/roles.go"},
		[]string{"var roleResearcher"}, shapeImpl},

	{"how does one phase hand its conclusions to the next",
		[]string{"daemon/orchestrator.go"},
		[]string{"func truncateHandoff"}, shapeImpl},

	{"where does the daemon run a third-party MCP server as a subprocess",
		[]string{"daemon/mcp/stdioclient.go"},
		[]string{"cmd.Env = ServerEnv"}, shapeCross},

	{"what stops an MCP server from receiving our API keys",
		[]string{"daemon/mcp/mcp.go"},
		[]string{"func ValidateEnvAllowList"}, shapeCross},

	{"where is the sandbox command line actually assembled for bubblewrap",
		[]string{"daemon/mcp/sandbox.go"},
		[]string{"func WrapCommand"}, shapeCross},

	{"how does the tui know which daemon socket to dial",
		[]string{"clients/tui/daemonconn.go"},
		[]string{"lockPathFunc"}, shapeCross},

	{"where does the daemon decide a search query needs the live web",
		[]string{"daemon/livequestion.go"},
		[]string{"func looksLikeLiveWorldQuestion"}, shapeImpl},

	{"how is an outbound web search query stripped of secrets",
		[]string{"daemon/websearch.go"},
		[]string{"scrub"}, shapeImpl},

	{"where does an edit get written to disk atomically",
		[]string{"editapply/atomicwrite.go"},
		[]string{"func writeFileAtomicNoFollow"}, shapeCross},

	{"how does undo restore a file that the edit had created",
		[]string{"daemon/apply_cmd.go"},
		[]string{"func stageRestore"}, shapeImpl},

	{"where is the git ignore file parsed for the indexer",
		[]string{"daemon/chunker.go"},
		[]string{"func newGitignoreMatcher"}, shapeImpl},

	{"how does the lexical tier score a match",
		[]string{"daemon/lexicalstore.go"},
		[]string{"bm25"}, shapeImpl},

	{"where are the two retrieval tiers combined into one ranking",
		[]string{"daemon/search.go", "daemon/rerank.go"},
		[]string{"func fuseRRF"}, shapeMulti},

	// Test-seeking, and deliberately about a client rather than the daemon:
	// the paired guard for rerank.go's FileClassTest down-weight has to hold
	// outside the one package the down-weight was tuned against.
	{"how is the interrupt behaviour of the chat client tested",
		[]string{"clients/tui/interrupt_test.go"},
		[]string{"func TestEscStopsTheTurnAndKeepsTheSession"}, shapeTest},

	{"what test proves a lane B server is never reported as confined",
		[]string{"daemon/mcp/mcp_test.go"},
		[]string{"func TestLaneBToolsAreNeverConfined"}, shapeTest},

	{"where is the prompt assembled with retrieved context before it goes to the model",
		[]string{"daemon/context.go"},
		[]string{"func buildAugmentedUserMessage"}, shapeImpl},

	{"how does the daemon count tokens it has spent in a turn",
		[]string{"daemon/counters.go"},
		[]string{"func (c *counters)"}, shapeImpl},

	{"where does the config get read off disk and defaulted",
		[]string{"daemon/config.go"},
		[]string{"func LoadConfig"}, shapeImpl},

	{"what does the user get told when cross-session memory is unavailable",
		[]string{"daemon/degraded.go"},
		[]string{"detailMemoryDown"}, shapeImpl},

	{"where is a proposed edit turned into a reviewable diff",
		[]string{"daemon/mcpbuiltin.go"},
		[]string{"func (s *Server) builtinProposeEdit"}, shapeImpl},

	{"how does the helper subprocess get restarted if it dies",
		[]string{"daemon/helperproc.go"},
		[]string{"func (h *HelperProcess) spawnLocked"}, shapeImpl},

	// Same file as "where does the agent loop stop iterating", different
	// stopping rule -- iteration count versus wall clock. Two questions that
	// are near-identical in embedding space and must not collapse onto the
	// same chunk.
	{"where does the daemon stop a runaway tool loop by wall clock",
		[]string{"daemon/agentloop.go"},
		[]string{"deadlineCap.Before"}, shapeDefUse},

	{"how are chat turns rendered for the terminal transcript",
		[]string{"clients/tui/chat.go"},
		[]string{"func renderTranscript"}, shapeCross},

	{"where does the proxy decide a model is allowed for this key",
		[]string{"proxy/main.go"},
		[]string{"func (p *proxy) modelAllowed"}, shapeCross},

	{"how is the workspace index built and written to disk",
		[]string{"daemon/index_cmd.go"},
		[]string{"func buildIndex"}, shapeImpl},

	{"where does the daemon refuse to index a file that is too large",
		[]string{"daemon/chunker.go"},
		[]string{"SkipTooLarge"}, shapeDefUse},
}

// evalSelfReferenceFiles are repo-relative paths this eval test itself must
// exclude from the indexed corpus: they store the literal query strings
// above (or, for lexicalstore_test.go, one used as a unit-test fixture) as
// Go string literals, so when the real repo is indexed they'd otherwise
// contribute a chunk that's a near-perfect lexical (and often semantic)
// match for its own query — an oracle-leaks-into-the-corpus problem, not a
// property of the retrieval design under test. rerank_test.go's
// TestLooksTestSeeking already documents the same concern for the semantic
// tier alone ("this file is itself indexed by that real-repo eval harness,
// and a near-verbatim copy of an eval query embeds as a near-duplicate of
// it"); the lexical tier makes the effect far stronger (an exact substring
// match beats any embedding similarity), so exclusion — not just rewording
// — is required for this test to measure the real defect rather than its
// own reflection.
var evalSelfReferenceFiles = map[string]bool{
	"daemon/rerank_eval_test.go":  true,
	"daemon/lexicalstore_test.go": true,

	// Found by TestNoIndexedFileEchoesAnEvalQuery on 2026-08-28, not by
	// anybody noticing. Each of these quotes eval queries verbatim for a
	// legitimate reason -- a sibling eval reusing the query set, and three
	// write-ups that quote what was measured -- and each was therefore a
	// free lexical bullseye for the query it quotes.
	//
	// Excluding a doc is cheap: no query in this set declares a .md file as
	// its answer, so nothing real is lost from the corpus. Excluding PRODUCT
	// SOURCE would not be, which is why rerank.go -- which also quoted a
	// query, and is itself a declared answer for the RRF-fusion query -- was
	// reworded instead of listed here.
	"daemon/token_efficiency_eval_test.go":         true,
	"BACKLOG.md":                                   true,
	"docs/QA_LAUNCH_GATE_2026-07-30.md":            true,
	"docs/RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md": true,
}

// TestNoIndexedFileEchoesAnEvalQuery fails if any file in the indexed corpus
// contains one of this eval's query strings verbatim, other than the files
// evalSelfReferenceFiles already excludes.
//
// WHY THIS EXISTS. evalSelfReferenceFiles was the right idea with a hole in it:
// it is a hand-maintained list, so it only covers leaks somebody thought of. On
// 2026-08-28, while expanding this eval, three captured `go test` output files
// turned out to be COMMITTED at the repo root -- and test_output.txt (added in
// 3ff9ee2, 2026-08-11) was a transcript of a TestRerankEvalRetrievalRanking run.
// It contained every query string verbatim, each one sitting a few lines above
// the chunk IDs of that query's correct answers. Seventeen days of eval runs
// scored against a corpus containing their own answer key, and nothing said so.
//
// A hand-maintained exclusion list cannot catch that; a check can. This is the
// cheap half of the eval -- ScanWorkspace only, no model, well under a second --
// so it also runs as a fast standalone signal rather than only inside the
// ten-minute job.
//
// It deliberately checks the QUERY strings and not the anchors: an anchor is a
// literal substring of the product's own source and is SUPPOSED to appear in
// the corpus (resolveExactChunks logs where, and does not fail). A query string
// is prose that exists nowhere but this eval, so a second copy of one is always
// either a leak or a file that should not be committed.
func TestNoIndexedFileEchoesAnEvalQuery(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	scan, err := ScanWorkspace(root)
	if err != nil {
		t.Fatalf("scanning workspace: %v", err)
	}

	leaks := map[string][]int{}
	for _, c := range scan.Chunks {
		if evalSelfReferenceFiles[c.FilePath] {
			continue
		}
		for i, q := range rerankEvalQueries {
			if strings.Contains(c.Content, q.query) && !slices.Contains(leaks[c.FilePath], i+1) {
				leaks[c.FilePath] = append(leaks[c.FilePath], i+1)
			}
		}
	}
	if len(leaks) == 0 {
		t.Logf("clean: %d files / %d chunks scanned, no file outside evalSelfReferenceFiles "+
			"echoes any of the %d eval queries", scan.FilesScanned, len(scan.Chunks), len(rerankEvalQueries))
		return
	}
	for _, f := range sortedFileNames(func() map[string]bool {
		m := map[string]bool{}
		for f := range leaks {
			m[f] = true
		}
		return m
	}()) {
		t.Errorf("%s is in the indexed corpus and contains eval quer%s %v verbatim. That file is a "+
			"near-perfect lexical match for its own query, so the eval would score it as retrieval "+
			"working. Three remedies, in order of preference: reword the file so it does not quote "+
			"the query verbatim (the only option if it is product source that some query declares "+
			"as an ANSWER -- excluding it would delete a real answer from the corpus); delete the "+
			"file (if it is captured test output -- see .gitignore); or add it to "+
			"evalSelfReferenceFiles (fine for docs and sibling evals, which no query answers)",
			f, map[bool]string{true: "y", false: "ies"}[len(leaks[f]) == 1], leaks[f])
	}
}

// indexRepoExcludingSelfReference scans root, drops any chunk whose
// FilePath is in evalSelfReferenceFiles, and embeds/upserts the rest into
// store and lexicalStore in indexEmbedBatchSize-sized batches — the same
// batching buildIndex (index_cmd.go) uses, duplicated here only because
// buildIndex has no hook to filter chunks between scanning and embedding.
func indexRepoExcludingSelfReference(ctx context.Context, root string, embedder Embedder, store VectorStore, lexicalStore LexicalStore, logger *log.Logger) (*ScanResult, error) {
	scan, err := ScanWorkspace(root)
	if err != nil {
		return nil, fmt.Errorf("scanning workspace: %w", err)
	}

	filtered := scan.Chunks[:0]
	for _, c := range scan.Chunks {
		if !evalSelfReferenceFiles[c.FilePath] {
			filtered = append(filtered, c)
		}
	}
	scan.Chunks = filtered

	for start := 0; start < len(scan.Chunks); start += indexEmbedBatchSize {
		end := min(start+indexEmbedBatchSize, len(scan.Chunks))
		batch := scan.Chunks[start:end]

		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding batch [%d:%d]: %w", start, end, err)
		}
		for i := range batch {
			batch[i].Vector = vecs[i]
		}

		if err := store.Upsert(ctx, batch); err != nil {
			return nil, fmt.Errorf("upserting batch [%d:%d]: %w", start, end, err)
		}
		if err := lexicalStore.Upsert(ctx, batch); err != nil {
			return nil, fmt.Errorf("upserting lexical batch [%d:%d]: %w", start, end, err)
		}
	}
	return scan, nil
}

func matchesAny(path string, candidates []string) bool {
	for _, c := range candidates {
		if path == c {
			return true
		}
	}
	return false
}

// chunkID mirrors chunker.go's chunkContent ID format exactly.
func chunkID(c Chunk) string {
	return fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine)
}

// hitsExactChunk reports whether any of hits' first n entries has an ID
// matching one of exactChunks.
func hitsExactChunk(hits []Chunk, n int, exactChunks []string) bool {
	for _, h := range hits[:min(n, len(hits))] {
		if matchesAny(chunkID(h), exactChunks) {
			return true
		}
	}
	return false
}

// runEvalPass runs every query in rerankEvalQueries through retrieveTopK
// with the given lexicalStore (nil for semantic-only, a real store for
// hybrid) and returns, per query, whether the exact chunk containing the
// relevant symbol/logic reached the final top-k (k=displayK=5, the real
// production default — not an arbitrary top-3 subset of a wider fetch,
// since what matters is whether the chunk actually gets injected into the
// prompt).
func runEvalPass(ctx context.Context, t *testing.T, embedder Embedder, store VectorStore, lexicalStore LexicalStore, exact [][]string, label string) (chunkHits, fileHits, deliveredHits []bool) {
	t.Helper()
	// BOUND TO PRODUCTION, not written as 5. It was `const displayK = 5`, and on
	// 2026-08-28 production moved to 10 -- at which point a hardcoded 5 would
	// have left this eval reporting a number for a configuration that no longer
	// ships, green, forever. That is the same fault as every other one this file
	// documents: the instrument quietly stopping measuring the product.
	displayK := defaultK

	chunkHits = make([]bool, len(rerankEvalQueries))
	fileHits = make([]bool, len(rerankEvalQueries))
	deliveredHits = make([]bool, len(rerankEvalQueries))
	fmt.Println()
	fmt.Printf("=== Retrieval ranking eval: %s ===\n", label)
	for i, q := range rerankEvalQueries {
		hits, err := retrieveTopK(ctx, q.query, displayK, embedder, store, lexicalStore, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%q): %v", q.query, err)
		}

		exactHit := hitsExactChunk(hits, displayK, exact[i])
		fileHit := false
		for _, h := range hits[:min(displayK, len(hits))] {
			if matchesAny(h.FilePath, q.expectedFiles) {
				fileHit = true
			}
		}
		chunkHits[i] = exactHit
		fileHits[i] = fileHit

		// Guardrail for Fix 11: production folds same-file overlapping chunks
		// into contiguous spans before rendering (mergeAdjacentChunks), so this
		// re-scores the SAME hits through that fold. It must never be worse
		// than the unmerged verdict -- a merged span only ever covers MORE
		// lines, so a query that hit before must still hit. Graded by
		// containment (rankOfChunk, edit_eval_test.go), because merging changes
		// chunk IDs by design.
		mergedHit := rankOfChunk(mergeAdjacentChunks(hits), exact[i]) != 0
		if exactHit && !mergedHit {
			t.Errorf("query %q: hit before merging and MISSES after -- Fix 11 regressed question-shaped recall", q.query)
		}

		// DELIVERED: the whole production tail, not just retrieval. buildContext
		// (context.go) does retrieve -> fuseDirectSpans -> truncateToBudget, and
		// only what survives all three is in the prompt the model reads.
		//
		// This exists because until 2026-08-28 this eval stopped at retrieval and
		// was therefore blind to the budget -- and the budget turned out to be
		// the binding constraint. At the shipped 8000 chars it was truncating 27
		// of these 49 queries and costing one outright, and no assertion in this
		// file could see any of it. Someone could have halved the budget and left
		// this eval green.
		//
		// Direct spans are nil: those come from file:line references in the
		// prompt, and these queries are natural-language questions with none.
		// That is the harder path, not a shortcut -- direct spans would only add
		// context.
		delivered, _ := truncateToBudget(
			fuseDirectSpans(nil, hits, displayK, false).Chunks,
			defaultContextBudgetChars, false)
		deliveredHit := rankOfChunk(delivered, exact[i]) != 0
		deliveredHits[i] = deliveredHit

		mark := "MISS"
		if exactHit {
			mark = "hit"
		}
		fmt.Printf("\n%d. [%s] query=%q\n   expected files=%v exact chunks=%v\n   chunk-level=%s file-level=%t merged-path=%t DELIVERED=%t\n", i+1, q.shape, q.query, q.expectedFiles, exact[i], mark, fileHit, mergedHit, deliveredHit)
		for j, h := range hits {
			fmt.Printf("   %d. %-40s class=%-6s raw=%.4f weighted=%.4f\n", j+1, chunkID(h), h.Class, h.RawScore, h.Score)
		}
	}
	return chunkHits, fileHits, deliveredHits
}

func TestRerankEvalRetrievalRanking(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	logger := log.New(os.Stderr, "rerank-eval: ", log.LstdFlags)

	modelCacheDir, err := defaultModelCacheDir()
	if err != nil {
		t.Fatalf("defaultModelCacheDir: %v", err)
	}
	modelDir, err := EnsureModelFiles(context.Background(), modelCacheDir, bgeModelAssets, logger)
	if err != nil {
		t.Fatalf("EnsureModelFiles (downloads only if not already cached): %v", err)
	}

	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		t.Fatalf("defaultONNXRuntimeCacheDir: %v", err)
	}
	onnxRuntimeLib, err := EnsureONNXRuntimeLib(context.Background(), ortCacheDir, logger)
	if err != nil {
		t.Fatalf("EnsureONNXRuntimeLib (downloads only if not already cached): %v", err)
	}

	helperBin := buildRealHelperBinary(t)
	helper := NewHelperProcess(helperBin, modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		t.Fatalf("starting real embedder helper: %v", err)
	}
	defer helper.Stop()

	embedder := NewBgeEmbedder(helper)

	// Index the actual repo root (one level up from daemon/), not a
	// curated subset — this is what makes the test faithful to the
	// measured, real-repo failures. Both the semantic (chromem) and lexical
	// (FTS5) stores are built from the same chunks, exactly as production
	// indexing does — except this test scans and filters manually instead
	// of calling buildIndex directly, to drop self-referential chunks (see
	// evalSelfReferenceFiles) before they're ever embedded/upserted.
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	indexDir := filepath.Join(t.TempDir(), "index")
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	lexicalStore, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer lexicalStore.Close()

	ctx := context.Background()
	scan, err := indexRepoExcludingSelfReference(ctx, repoRoot, embedder, store, lexicalStore, logger)
	if err != nil {
		t.Fatalf("indexing repo: %v", err)
	}
	t.Logf("indexed repo root %s: scanned=%d chunks=%d (self-referential chunks excluded)", repoRoot, scan.FilesScanned, len(scan.Chunks))

	// The chunk-level ground truth, computed from the index that was just built
	// rather than read from a list of line ranges written weeks ago. See
	// resolveExactChunks.
	exact := resolveExactChunks(t, scan.Chunks)

	semanticOnlyHits, _, _ := runEvalPass(ctx, t, embedder, store, nil, exact, "SEMANTIC-ONLY (lexicalStore=nil, today's behavior)")
	hybridHits, hybridFileHits, hybridDelivered := runEvalPass(ctx, t, embedder, store, lexicalStore, exact, "HYBRID (semantic + lexical, fused via RRF)")

	fmt.Println()
	fmt.Println("=== Before/after summary (chunk-level hit = exact symbol-containing chunk reached top-5) ===")
	fmt.Printf("%-3s %-7s %-62s %-10s %-8s\n", "#", "shape", "query", "semantic", "hybrid")
	var semanticOnlyCount, hybridCount int
	var regressions []string
	shapeTotal := map[string]int{}
	shapeHybridHits := map[string]int{}
	var fileLevelCount, rightFileWrongChunk, deliveredCount, retrievedButBudgeted int
	for i, q := range rerankEvalQueries {
		shapeTotal[q.shape]++
		if hybridDelivered[i] {
			deliveredCount++
		} else if hybridHits[i] {
			retrievedButBudgeted++
		}
		if hybridFileHits[i] {
			fileLevelCount++
			if !hybridHits[i] {
				rightFileWrongChunk++
			}
		}
		if semanticOnlyHits[i] {
			semanticOnlyCount++
		}
		if hybridHits[i] {
			hybridCount++
		}
		if hybridDelivered[i] {
			shapeHybridHits[q.shape]++
		}
		before := "MISS"
		if semanticOnlyHits[i] {
			before = "hit"
		}
		after := "MISS"
		if hybridHits[i] {
			after = "hit"
		}
		fmt.Printf("%-3d %-7s %-62s %-10s %-8s\n", i+1, q.shape, truncateEval(q.query, 62), before, after)

		if semanticOnlyHits[i] && !hybridHits[i] {
			regressions = append(regressions, q.query)
		}
	}
	total := len(rerankEvalQueries)
	deliveredRate := float64(deliveredCount) / float64(total)
	fmt.Println()
	fmt.Printf("semantic-only chunk-level recall: %d/%d (%.1f%%)\n", semanticOnlyCount, total, 100*float64(semanticOnlyCount)/float64(total))
	fmt.Printf("hybrid chunk-level recall:        %d/%d (%.1f%%)  (retrieval only)\n", hybridCount, total, 100*float64(hybridCount)/float64(total))
	fmt.Printf("DELIVERED to the prompt:          %d/%d (%.1f%%)  <- THE GATED NUMBER\n", deliveredCount, total, 100*deliveredRate)
	fmt.Printf("  retrieved then BUDGETED OUT:    %d  (k=%d, budget=%d chars)\n", retrievedButBudgeted, defaultK, defaultContextBudgetChars)
	fmt.Println()

	// The chunk-level number alone cannot tell "retrieval had no idea" apart
	// from "retrieval named the right file and handed over the wrong forty
	// lines of it", and those two want completely different fixes -- the first
	// is ranking or embedding, the second is chunking and granularity.
	//
	// MEASURED 2026-08-28, and it is the most useful thing this expansion
	// surfaced: of 27 chunk-level misses, 14 were right-file-wrong-chunk. Over
	// half the failure mass sits on the chunk boundary, not in the ranker.
	// That is also why the 2026-08-27 chunking change was able to do so much
	// damage so quickly: it was moving the boundary that half the failures
	// already turn on.
	fmt.Printf("hybrid file-level recall:         %d/%d (%.1f%%)\n", fileLevelCount, total, 100*float64(fileLevelCount)/float64(total))
	fmt.Printf("  right file, WRONG chunk:        %d  (granularity failures -- chunking, not ranking)\n", rightFileWrongChunk)
	fmt.Printf("  right file never retrieved:     %d  (ranking/embedding failures)\n", total-fileLevelCount)
	fmt.Println()

	// Per-shape breakdown. NOT gated -- with 1-10 queries in a class a single
	// flip is 10-100pp, which is noise, and gating on it would make this eval
	// the flaky thing nobody trusts. It is diagnosis: when the gated overall
	// rate below fires, this says which KIND of question broke, which is the
	// difference between "recall fell" and "declarations stopped beating their
	// use sites".
	fmt.Println("per-shape hybrid recall (diagnostic, not gated):")
	var shapeAccounted int
	for _, s := range evalQueryShapeOrder {
		n := shapeTotal[s]
		if n == 0 {
			continue
		}
		shapeAccounted += n
		fmt.Printf("  %-8s %d/%d (%.0f%%)\n", s, shapeHybridHits[s], n, 100*float64(shapeHybridHits[s])/float64(n))
	}
	fmt.Println()

	// A query carrying a shape that evalQueryShapeOrder does not list would be
	// silently dropped from the breakdown above -- invisible, because a typo in
	// a string constant still compiles. Counting what was printed catches it.
	if shapeAccounted != total {
		var unknown []string
		for s := range shapeTotal {
			if !slices.Contains(evalQueryShapeOrder, s) {
				unknown = append(unknown, s)
			}
		}
		sort.Strings(unknown)
		t.Errorf("the per-shape breakdown accounted for %d of %d queries: shape(s) %v are set on "+
			"queries but missing from evalQueryShapeOrder, so those queries are absent from the "+
			"diagnostic above", shapeAccounted, total, unknown)
	}

	// REPORTED, NOT FAILED -- changed 2026-08-28, and this is a real loosening,
	// so here is the evidence for it.
	//
	// This was `t.Errorf` and it demanded that fusion lose NO query the
	// semantic tier alone found. That bar was set against nine queries which
	// had been selected, at the time, as the ones hybrid retrieval was built to
	// rescue -- so pointwise dominance held, and looked like a property.
	//
	// It is not one. RRF fuses two rankings; it cannot dominate either input
	// pointwise, and demanding that it does is demanding something the method
	// cannot supply. Measured on the 49-query set on 2026-08-28: hybrid took
	// recall from 19/49 to 22/49 while losing 3 queries it gained 6 -- a clear
	// net win that the old rule would have called a failure, permanently, on
	// every run. A gate that can never be green is a gate people delete.
	//
	// The intent behind the rule survives as the AGGREGATE check below
	// (semanticOnlyCount > hybridCount), which is the honest form of the same
	// question: fusion must not lose more than it gains. Per-query trades are
	// still printed here, because WHICH queries fusion trades away is worth
	// looking at even when the net is positive.
	if len(regressions) > 0 {
		t.Logf("fusion traded away %d quer(ies) the semantic tier alone found (net is still "+
			"%+d): %v", len(regressions), hybridCount-semanticOnlyCount, regressions)
	}

	// The two queries that actually motivated this feature (see the design
	// doc/PR description) MUST hit under hybrid retrieval — this is the
	// feature's real acceptance bar, not "every query in the set,
	// unconditionally". Indices: 5 = the original verbose ZDR-string
	// phrasing, 7 = the live-failing short ZDR-string phrasing, 8 =
	// SearchRequest cross-file.
	mustHit := []int{5, 7, 8}
	for _, i := range mustHit {
		if hybridDelivered[i] {
			continue
		}
		// Two different failures wear the same red. Say which.
		if hybridHits[i] {
			t.Errorf("query %d (%q) — one of the measured live failures this feature exists to fix — "+
				"IS retrieved but does not survive the %d-char budget, so the model never sees it",
				i+1, rerankEvalQueries[i].query, defaultContextBudgetChars)
			continue
		}
		t.Errorf("query %d (%q) — one of the two measured live failures this feature exists to fix — is still MISS under hybrid retrieval", i+1, rerankEvalQueries[i].query)
	}

	// A floor on overall recall, so a regression that spares the three gated
	// queries is still caught.
	//
	// A RATE, not a tally, since 2026-08-28. It was `hybridCount < 8` against a
	// nine-query set, where one flip is 11pp -- coarser than the effects
	// retrieval changes actually produce, and the reason a chunking change that
	// cost five queries was indistinguishable from noise until it had already
	// shipped. A rate also survives the set growing again: the next person to
	// add queries does not have to remember to raise an integer.
	if deliveredRate < evalChunkRecallFloor {
		t.Errorf("DELIVERED chunk-level recall %d/%d = %.1f%% is below the floor of %.1f%% measured on "+
			"2026-08-28. Read resolveExactChunks' output first. If it reported an anchor that "+
			"appears in no chunk of its expectedFiles, the DECLARED half of the ground truth "+
			"has gone stale (code was moved or renamed) and retrieval is fine. If it reported "+
			"nothing, this is a real retrieval regression: the derived half cannot go stale, "+
			"because it is computed from the index this run just built. The per-shape breakdown "+
			"above says which kind of question lost. If chunk-level recall held and only "+
			"DELIVERED fell, the loss is in the budget or the merge, not in the ranker",
			deliveredCount, total, 100*deliveredRate, 100*evalChunkRecallFloor)
	}
	if semanticOnlyCount > hybridCount {
		t.Errorf("hybrid recall %d/%d is WORSE than semantic-only %d/%d -- fusion is losing "+
			"results the semantic tier alone finds", hybridCount, total, semanticOnlyCount, total)
	}

	// Query 1 (index 0, "where does the daemon open the unix socket") is a
	// KNOWN, PRE-EXISTING, SEPARATE gap discovered while upgrading this
	// harness to chunk-level checking (see that query's comment above): the
	// package doc comment in main.go:1-40 literally contains "Unix" and
	// "socket", so it wins on BOTH the semantic tier (reads as an excellent
	// paraphrase of the query) AND the lexical tier (contains the same
	// substrings the real net.Listen("unix", ...) call does) — hybrid
	// retrieval cannot distinguish "a comment describing the concept" from
	// "the code that does it" when both literally contain the same words.
	// Fixing that needs chunk-level content classification (not just
	// per-file classification by extension, fileclass.go's current design),
	// which is out of this feature's scope (retrieval MERGE, not
	// reclassification granularity) — flagged here, deliberately not gated,
	// so it isn't silently lost.
	if hybridDelivered[0] {
		t.Logf("NOTE: query 1 HITS. Measured 2026-08-28 at k=10: its answer chunk " +
			"(protocol/transport_unix.go:31-70) came back at RANK 7, and the top five were all " +
			"real transport code with no package doc comment among them -- so on this run the " +
			"gap was k=5 truncation, not the semantic doc-vs-code confusion described above. " +
			"NOT added to mustHit on one run: the same one-run inference was made about this " +
			"query during the 2026-08-27 embed-window work and did not reproduce. Add it after " +
			"it holds across several runs on different corpus states.")
	} else {
		t.Logf("KNOWN GAP (not gated, pre-existing, out of scope): query 1 (%q) still misses — see comment above mustHit for why", rerankEvalQueries[0].query)
	}
}

// evalChunkRecallFloor is the minimum chunk-level recall RATE
// TestRerankEvalRetrievalRanking accepts, in the same named-constant style as
// evalTop3RecallThreshold in eval_test.go.
//
// IT GATES THE **DELIVERED** NUMBER -- what survives retrieval AND merging AND
// the character budget -- not what retrieval returned. Those differ, and the
// difference is not decorative: at the configuration shipped until 2026-08-28
// (k=5, 8000 chars) the budget was truncating 27 of these 49 query sets and
// costing a query outright, and an eval that stopped at retrieval could not see
// any of it.
//
// MEASURED 2026-08-28 at k=10 / 16000 chars: DELIVERED 28/49 (57.1%), against
// 30/49 (61.2%) retrieved -- 2 found and then budgeted out. The configuration
// this replaced (k=5 / 8000) delivers 23/49 (46.9%) on the same corpus.
//
// THAT NUMBER IS NOT A REGRESSION FROM 8/9, and reading it as one is the single
// most likely misreading of this file. The nine-query set scored 89% because
// its queries were derived FROM measured failures that were then fixed -- it
// was, by construction, a set of questions retrieval had been taught to answer.
// 44.9% is the first measurement against questions it was not tuned on, and it
// is the real number. File-level recall on the same run is 36/49 (73.5%): the
// right file usually IS retrieved, and 14 of the 27 chunk-level misses are the
// right file with the wrong forty lines of it.
//
// THAT SPREAD IS NOT NOISE, AND IT WAS CHECKED RATHER THAN ASSUMED. Indexing
// once and running the identical pass three times over that one index gives
// byte-identical hit vectors, so retrieval is deterministic. The two runs
// differed because the CORPUS did -- 4381 chunks vs 4386, five chunks added by
// edits to files that no flipped query even names. Retrieval here is
// deterministic and corpus-sensitive: a 0.1% change in the corpus moved the
// gated number by two queries. Every near-miss sits close to the top-5 cut, so
// small changes in what it is competing against decide it.
//
// THE FLOOR IS 49%, WHICH IS DELIBERATELY TIGHT, and the window is narrow
// enough to be worth writing down. It needs 25 of 49. That is:
//
//   - three below the measured 28, which is one more than the +/-2 corpus band
//     above -- so ordinary churn should not trip it, but there is not much room;
//   - ABOVE the 23/49 (46.9%) that k=5 / 8000 delivers, so reverting either
//     constant fails this gate rather than quietly halving what the model sees.
//
// The second property is why the floor is not looser. Both constants are read
// from production here (defaultK, defaultContextBudgetChars), so a change to
// either moves this number, and that is exactly what should be caught.
// Resolution is 2.0pp per query, against 11pp on the old nine-query set.
//
// RAISE THIS when a real improvement makes a higher rate the new normal, and
// re-measure the spread when you do. Do NOT raise it to whatever the last run
// printed: given the corpus sensitivity above, a floor with no slack fails on
// an unrelated edit.
const evalChunkRecallFloor = 0.49

func truncateEval(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
