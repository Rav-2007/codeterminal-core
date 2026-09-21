package main

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"mochiii/protocol"
)

// Delimiter tags wrapping retrieved context and the user's own request in
// the live prompt path. Retrieved content is untrusted (it's whatever text
// happens to live in the user's workspace), so it must never be mistaken
// for a real boundary or for instructions — see neutralizeDelimiters below
// and the system prompt paragraph in prompts/system.txt that tells the
// model how to treat this block.
const (
	retrievedContextOpenTag  = "<retrieved_context>"
	retrievedContextCloseTag = "</retrieved_context>"
	userRequestOpenTag       = "<user_request>"
	userRequestCloseTag      = "</user_request>"
)

// buildTagVariantPattern returns a regexp source matching any tag-like
// occurrence of the given letters (e.g. "retrievedcontext"), tolerant of:
//   - case (Retrieved_Context, RETRIEVEDCONTEXT, ...)
//   - underscore-vs-space-vs-nothing between letters (retrieved context,
//     retrievedcontext, retrieved_context all match)
//   - arbitrary whitespace, including newlines, anywhere inside the tag
//     (\s already matches \n in Go's regexp)
//   - a closing slash separated from '<' and the letters by whitespace
//     (< / retrieved_context >)
//   - trailing junk before the closing '>' (fake attributes, extra text)
//
// It deliberately does NOT match on '<'/'>' alone, so real code (channel
// operators, comparisons, generics) is left untouched — only sequences that
// spell out one of our exact tag names, however sloppily, are matched.
func buildTagVariantPattern(letters string) string {
	var b strings.Builder
	b.WriteString(`<\s*/?\s*`)
	for i, r := range letters {
		if i > 0 {
			b.WriteString(`[\s_]*`)
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	b.WriteString(`[^<>]*>`)
	return "(?i)" + b.String()
}

// protectedTagFamilies IS THE REGISTRY, and it is a registry rather than a
// hand-written list of two because the hand-written list of two was wrong.
//
// WHAT WENT WRONG. webContentEnvelope (websearch.go) documented, as one of the
// three things that make its fence hold, that "renderToolResult runs
// neutralizeDelimiters over this on the way out, so a page that writes
// </web_content> cannot close its own envelope and continue as though it were
// the daemon talking". That was false for as long as the envelope existed:
// neutralizeDelimiters knew "retrievedcontext" and "userrequest" and had never
// heard of "webcontent". A fetched page -- text written by anyone on the open
// internet, at a URL the model can be talked into visiting -- could close the
// fence the system prompt tells the model to trust, and continue outside it.
//
// The defect is not that someone forgot a line. It is that the set of protected
// families was expressed as two `var` declarations and a two-line function, so
// ADDING A THIRD ENVELOPE ANYWHERE ELSE IN THE DAEMON did not require touching
// this file, did not fail to compile, and did not fail a test. The fence was
// opt-in and nothing said so.
//
// So: every delimiter family this daemon writes around untrusted text is named
// here, neutralizeDelimiters loops over all of them, and
// TestEveryEnvelopeTagIsNeutralized walks the real tag constants and fails if
// any one of them survives a round trip. Adding an envelope without registering
// it is now a red build rather than a silent hole.
var protectedTagFamilies = []string{
	// The prompt-level fence: retrieved workspace content, and the boundary
	// that separates it from what the user actually asked for.
	"retrievedcontext",
	"userrequest",
	// A page fetched from the open internet. The most exposed of the four --
	// its content has no relationship to the user at all.
	"webcontent",
	// Output from a third-party MCP server (see toolresult.go). Written by
	// whoever wrote the server, which the user configured but did not audit.
	"laneboutput",
}

var protectedTagPatterns = compileProtectedTagPatterns()

func compileProtectedTagPatterns() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(protectedTagFamilies))
	for _, family := range protectedTagFamilies {
		out = append(out, regexp.MustCompile(buildTagVariantPattern(family)))
	}
	return out
}

// protectedTagPattern returns the compiled pattern for one registered family,
// so a caller asking "does this text carry retrieved-context markup?" reads the
// registry rather than compiling a second copy that could drift from it.
// Panics on an unregistered family: the argument is always a literal in this
// package, so a wrong one is a programming error and should not be reachable at
// runtime.
func protectedTagPattern(family string) *regexp.Regexp {
	for i, registered := range protectedTagFamilies {
		if registered == family {
			return protectedTagPatterns[i]
		}
	}
	panic("no protected tag family named " + family)
}

// envelopeTagsInUse holds one COMPLETE example of every delimiter this daemon
// writes around untrusted text.
//
// TestEveryEnvelopeTagIsNeutralized drives each one through
// neutralizeDelimiters and fails if it survives. That is a BEHAVIOURAL check
// rather than a membership check on purpose: it does not ask "is this family in
// the registry", it asks "could a hostile document forge this exact tag", which
// is the question that was answered wrongly for <web_content>. A new envelope
// added without a registry entry fails here even if its author never read
// protectedTagFamilies.
//
// COMPLETE, and the word is load-bearing. The two opening tags that carry
// attributes are stored as PREFIXES elsewhere (webContentOpenTagPrefix ends at
// the quote, because the URL comes next), and a prefix is not a tag:
// buildTagVariantPattern requires a closing '>', so a bare prefix cannot match
// and a test asserting on one would fail for a reason that has nothing to do
// with the defence. Each entry below is therefore the prefix plus a minimal
// attribute value and terminator -- still derived from the constants, so a
// renamed tag still moves this list, but now shaped like something an attacker
// would actually have to write.
var envelopeTagsInUse = []string{
	retrievedContextOpenTag,
	retrievedContextCloseTag,
	userRequestOpenTag,
	userRequestCloseTag,
	webContentOpenTagPrefix + `https://example.invalid/">`,
	webContentCloseTag,
	laneBOutputOpenTagPrefix + `some-server">`,
	laneBOutputCloseTag,
}

// neutralizeDelimiters defuses any tag-like text inside untrusted retrieved
// content that could otherwise be mistaken for one of our real delimiter
// boundaries (e.g. a source comment containing "</retrieved_context>" to
// try to forge an early close and inject a fake <user_request> after it).
// Matches are labeled AND have their angle brackets replaced with visually
// similar lookalike characters (‹ ›) — not just wrapped in descriptive text
// — so the exact tag substring can never survive into the rendered message.
// Wrapping alone isn't enough: it would still leave "</retrieved_context>"
// present verbatim inside the "neutralized" text, which a literal substring
// check (or a sufficiently literal-minded reader) would still count as a
// real closing tag.
func neutralizeDelimiters(s string) string {
	for _, pattern := range protectedTagPatterns {
		// MATCH BEFORE REPLACING, and the guard is not premature optimisation.
		//
		// ReplaceAllStringFunc allocates a fresh copy of the whole input
		// whether or not anything matched -- it appends the remaining source
		// to a buffer and returns string(buf) -- so N patterns meant N copies
		// of every retrieved chunk and every tool result, on a path that runs
		// for each of them on every turn. Registering the two missing families
		// took this from 2 copies to 4 and was measured at 33.4us -> 58.7us
		// per 2 KB chunk, with allocations exactly doubled (6 -> 12).
		//
		// MatchString allocates nothing. The overwhelmingly common input is
		// ordinary source code containing no tag at all, so the guard turns
		// four allocating copies into four non-allocating scans and zero
		// copies. The hostile path is unchanged: a chunk that does contain a
		// forged tag still takes exactly the same replacement it always did.
		if pattern.MatchString(s) {
			s = pattern.ReplaceAllStringFunc(s, neutralizeMatch)
		}
	}
	return s
}

var angleBracketLookalikes = strings.NewReplacer("<", "‹", ">", "›")

func neutralizeMatch(match string) string {
	return "[neutralized tag-like text found in retrieved source: " + angleBracketLookalikes.Replace(match) + "]"
}

// retrievalOutcome records what happened when the live path tried to gather
// context for a prompt, for logging and for deciding what (if anything) to
// inject into the model request.
type retrievalOutcome struct {
	Skipped   bool
	Reason    string // set only when Skipped
	Chunks    []Chunk
	Truncated bool

	// MergedFrom and MergeSavedBytes describe what mergeAdjacentChunks
	// (chunkmerge.go) did to the retrieved set before it was budgeted:
	// how many chunks went in (similarity hits plus any directly-resolved
	// spans), and how many rendered bytes folding their duplicated overlaps
	// away reclaimed. Both are zero when nothing merged. DirectRefSpans is
	// how many of those inputs came from resolving a file:line pointer in the
	// prompt (Fix 12, fileref.go). All three are for the log line, not the
	// wire.
	MergedFrom      int
	MergeSavedBytes int
	DirectRefSpans  int
}

// gatherContext retrieves context for prompt using the exact same
// retrieveTopK the CLI `retrieve` command uses (no duplicated retrieval
// logic), then applies the server's configured char budget. It never
// returns an error: any failure — no embedder/store configured, a
// transient retrieval error, zero hits — becomes a Skipped outcome with a
// human-readable Reason, because retrieval must never prevent generation.
func (s *Server) gatherContext(ctx context.Context, prompt string) retrievalOutcome {
	// Exact pointers the prompt already contains ("foo.go:142: undefined: bar")
	// are resolved straight off disk and placed at the top, rather than left to
	// similarity search to rediscover (Fix 12, fileref.go). Best-effort by
	// construction: a bad path or an out-of-range line is skipped and normal
	// retrieval still happens.
	//
	// THIS RUNS BEFORE THE EMBEDDER/STORE CHECK, and that is the point.
	// Resolving "foo.go:142" opens a file and reads forty lines around a line
	// number. It needs no embedder, no vector store, and no index — it is not
	// search, it is a lookup the user already did for us by naming the line.
	// Sitting below the early return meant the one case where the user was most
	// precise (pasting a compiler error) was the one case that returned nothing,
	// for a user who had not indexed their workspace — likely the newest user
	// there is. Confinement is unchanged and lives in fileref.go: EvalSymlinks
	// on the root, workspace-relative resolution, the gitignore matcher, and
	// readReferencedSpan's own bounds. This makes an already-confined resolver
	// reachable in a state it previously was not; it does not widen what it may
	// read.
	direct := resolveFileLineRefs(prompt, s.workspace, s.logger)

	similar, reason := s.similarChunks(ctx, prompt)
	if reason != "" && len(direct) == 0 {
		// Nothing resolved and similarity could not run: this is the same
		// Skipped outcome as before, with the same wording.
		return retrievalOutcome{Skipped: true, Reason: reason}
	}

	// Fuse and fold: fuseDirectSpans puts the direct spans first and runs the
	// whole set through mergeAdjacentChunks (Fix 11, chunkmerge.go), which is
	// what makes fusion dedupe — a direct span and a similarity chunk covering
	// the same lines become one span, not two overlapping copies — and what
	// reclaims the indexer's deliberate window overlap. Merging happens BEFORE
	// budgeting so the reclaimed bytes are actually usable: a lower-ranked
	// chunk that used to be dropped can now fit.
	// Widen the top hits into the regions around them before fusing. This sits
	// here, not inside similarChunks, because it must run BEFORE
	// mergeAdjacentChunks -- fuseDirectSpans is what folds a chunk and its new
	// siblings into one contiguous span, and that fold is the whole mechanism.
	// See chunkexpand.go for the measurement.
	similar = expandToNeighbours(similar, s.workspace, s.resolvedExpandPolicy())

	fused := fuseDirectSpans(direct, similar, s.retrievalTopK, s.noScrub())
	if len(fused.Chunks) == 0 {
		// Prefer the specific cause when there is one. "No relevant chunks found
		// in index" is a lie to a user who has no index.
		if reason == "" {
			reason = "no relevant chunks found in index"
		}
		return retrievalOutcome{Skipped: true, Reason: reason}
	}

	kept, truncated := truncateToBudget(fused.Chunks, s.contextBudgetChars, s.noScrub())
	return retrievalOutcome{
		Chunks:          kept,
		Truncated:       truncated,
		MergedFrom:      fused.InputCount,
		MergeSavedBytes: fused.SavedBytes,
		DirectRefSpans:  fused.DirectSpans,
	}
}

// similarChunks runs similarity retrieval, returning a non-empty reason string
// when it could not run or failed. It returns no error, deliberately: retrieval
// must never prevent generation, so every failure is a reason the caller may
// choose to override with whatever it did manage to gather.
func (s *Server) similarChunks(ctx context.Context, prompt string) ([]Chunk, string) {
	if s.embedder == nil || s.store == nil {
		// Report the cause setupRetrieval actually recorded, not a guess at it
		// (Fix 8). The fallback only covers a Server built without going through
		// setupRetrieval at all, which in practice means a test.
		reason := s.retrievalDisabledReason
		if reason == "" {
			reason = "retrieval unavailable for this daemon"
		}
		return nil, reason
	}

	// REPORTED, NOT REFUSED. A prompt over the lexical bound still gets a turn:
	// retrieveTopK's lexical tier declines it and the answer is grounded by
	// semantic similarity alone, which is strictly better than what this used to
	// do -- a 400,000-character paste spent 12.5 s in the keyword tier and
	// returned zero hits, because one enormous token matches nothing. Skipping
	// it costs the user nothing and saves the wait. Said out loud because a
	// reduced tier that says nothing is the exact failure degraded.go exists to
	// stop; see maxLexicalQueryChars for where the number comes from.
	//
	// Guarded on the tier EXISTING. "Skipped because the prompt is too long" and
	// "there is no keyword tier on this daemon" are different states, and a
	// daemon with no lexical store announcing that it skipped one would be
	// reporting a degradation it does not have.
	if s.lexicalStore != nil && lexicalQueryTooLong(prompt) {
		s.logger.Printf("retrieval: prompt is %d chars, over the %d-char lexical bound; the keyword tier is skipped for this turn and grounding is by semantic similarity alone",
			len(prompt), maxLexicalQueryChars)
	}

	similar, err := retrieveTopK(ctx, prompt, s.retrievalTopK, s.embedder, s.store, s.lexicalStore, !s.rerankDisabled)
	if err != nil {
		return nil, fmt.Sprintf("retrieval error: %v", err)
	}
	return similar, ""
}

// noScrub reports whether the --no-scrub escape hatch is set, tolerating a
// Server built without a Config at all (which in practice means a test that
// exercises retrieval on its own). Nil is read as "scrub", the safe default —
// the one case where guessing is allowed is guessing in favour of redaction.
func (s *Server) noScrub() bool { return s.cfg != nil && s.cfg.NoScrub }

// truncateToBudget keeps chunks (already ranked best-first by the vector
// store) in rank order while their rendered size stays within budget chars.
// The top hit is never sacrificed to make room for a lower-ranked one.
// Reports whether anything was dropped.
//
// IT SKIPS AN OVERSIZED SPAN RATHER THAN STOPPING AT IT, and that one word is
// worth a query. This used to `return` on the first span that did not fit,
// which threw away the entire remaining tail — so a small span ranked eighth was
// discarded because a large one ranked fifth had not fitted, even with thousands
// of characters of budget still free. Measured over the locate eval by replaying
// one run's retrieval through this function, continuing instead of returning
// delivers one more query for 2.6% more context, and it is a precondition for
// the construct widening in chunkexpand.go: widening makes some spans much
// bigger, and stopping at the first big one would forfeit everything behind it.
//
// RANK ORDER AMONG SURVIVORS IS UNCHANGED. This only ever ADDS a span that would
// otherwise have been dropped; it never promotes one above a better-ranked span
// that was kept.
func truncateToBudget(chunks []Chunk, budget int, scrubDisabled bool) ([]Chunk, bool) {
	var kept []Chunk
	used := 0
	dropped := false
	for _, c := range chunks {
		size := len(renderChunk(len(kept)+1, c, scrubDisabled))
		if len(kept) > 0 && used+size > budget {
			dropped = true
			continue
		}
		kept = append(kept, c)
		used += size
	}
	return kept, dropped
}

// renderChunk formats one chunk as it will appear inside the
// <retrieved_context> block: an index, its source label, and its content,
// which is first secret-scrubbed and then delimiter-neutralized.
//
// This is the single retrieval-time choke point for chunk secret scrubbing
// (Option A, CHUNK_SCRUB_DESIGN.md §4): every chunk that reaches the prompt
// is rendered here, and truncateToBudget sizes chunks through this same
// function, so redacting here keeps the outbound bytes and the budget
// accounting self-consistent. scrub() is the existing precision-first,
// structural-signature span-redactor already run on the user's typed prompt
// (scrub.go); reusing it on chunk Content adds no new false-positive surface.
// It is span-level: only the matched secret substring becomes
// [REDACTED:<kind>], the surrounding code is preserved. scrubDisabled honors
// the same --no-scrub escape hatch that governs prompt scrubbing.
//
// Structural signatures only. Opaque/novel secrets with no recognizable
// prefix are NOT closed here — those await the entropy decision, which awaits
// the warn-mode fire-rate data (see logChunkScrub / chunkscrub.go). scrub is
// applied before neutralizeDelimiters so secret detection sees the original
// bytes (PEM/AKIA/etc. carry no angle brackets, so order is immaterial for
// them, but detecting on raw content is the safe order).
func renderChunk(index int, c Chunk, scrubDisabled bool) string {
	cleaned, _ := scrub(c.Content, scrubDisabled)
	return fmt.Sprintf("[%d] %s:%d-%d\n%s\n", index, c.FilePath, c.StartLine, c.EndLine, neutralizeDelimiters(cleaned))
}

// buildAugmentedUserMessage renders retrieved chunks and the raw user
// prompt into a single user-role message, wrapped in explicit, labeled
// delimiters. This is the only place retrieved (untrusted) content is
// combined with a request; it never touches the system role (see
// buildChatMessages in provider.go, which places this string as-is into the
// "user" message). If there are no chunks, the prompt passes through
// unchanged — identical to today's behavior with retrieval off.
func buildAugmentedUserMessage(prompt string, chunks []Chunk, scrubDisabled bool) string {
	if len(chunks) == 0 {
		return prompt
	}

	var b strings.Builder
	b.WriteString(retrievedContextOpenTag)
	b.WriteString("\n")
	for i, c := range chunks {
		b.WriteString(renderChunk(i+1, c, scrubDisabled))
	}
	b.WriteString(retrievedContextCloseTag)
	b.WriteString("\n\n")
	b.WriteString(userRequestOpenTag)
	b.WriteString("\n")
	b.WriteString(prompt)
	b.WriteString("\n")
	b.WriteString(userRequestCloseTag)
	return b.String()
}

// logChunkScrub measures and logs secret-scrubbing activity over exactly the
// chunks that will be folded into the outbound prompt (the budget-kept set),
// so the fire-rate numbers describe what is actually sent, not chunks that
// were dropped or sized-and-discarded. It logs; it never changes the model
// input — the live redaction that reaches the model happens independently in
// renderChunk (Option A). Two separate things are reported:
//
//  1. Option A (structural, LIVE): re-derive the kinds scrub() redacts on each
//     chunk and emit one aggregate notice. Kinds only, never matched text —
//     same discipline as the typed-prompt redaction notice (server.go).
//
//  2. Warn-mode (entropy + keyword, LOG ONLY): run the deferred detectors on
//     the POST-scrub content, i.e. on what Option A does NOT already cover, to
//     measure how often they would fire on opaque/config-shaped secrets on real
//     repos. These do NOT redact and do NOT affect what is sent; they exist so
//     the founder can later decide, on evidence, whether entropy/keyword
//     redaction (Designs B/C) is worth its false-positive cost. They never log
//     raw suspected-secret content — only a fixed detector label, a secret-free
//     note, and a truncated SHA-256 indicator (see chunkscrub.go).
func (s *Server) logChunkScrub(chunks []Chunk) {
	var structuralKinds []string
	warnHits := 0
	for _, c := range chunks {
		cleaned, reds := scrub(c.Content, s.noScrub())
		structuralKinds = append(structuralKinds, redactionKinds(reds)...)

		ref := fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine)
		warns := detectWarnModeSecrets(cleaned)
		for _, d := range warns {
			s.logger.Printf("chunk-scrub warn-mode (LOG ONLY, not redacted, not sent to model): detector=%s file=%s class=%s shape=%s %s indicator=%s",
				d.Detector, ref, c.Class, d.Shape, d.Note, d.Indicator)
			// Durable twin of the stderr line above, so the fire-rate data
			// actually accumulates across restarts (warnsink.go). class is the
			// chunk's existing FileClass (same value logRetrieval reports);
			// shape is a fixed-label token-shape tag. Both are for later true-
			// vs-false-positive triage; still no raw secret material.
			s.warnSink.write(warnEvent{
				Detector:  d.Detector,
				File:      c.FilePath,
				StartLine: c.StartLine,
				EndLine:   c.EndLine,
				Class:     c.Class,
				Shape:     d.Shape,
				Note:      d.Note,
				Indicator: d.Indicator,
			})
		}
		warnHits += len(warns)
	}

	if len(structuralKinds) > 0 {
		s.logger.Printf("chunk-scrub: redacted %d structural secret(s) in retrieved chunk content before send: %v",
			len(structuralKinds), structuralKinds)
	}
	if warnHits > 0 || len(chunks) > 0 {
		s.logger.Printf("chunk-scrub warn-mode summary: chunks=%d warn_hits=%d (entropy/keyword, log-only fire-rate measurement, no redaction)",
			len(chunks), warnHits)
	}
}

// buildGroundingInfo translates outcome (already computed by gatherContext)
// plus this daemon's actual workspace and the client's stated expectation
// into the wire-level report sent back to the client. It only formats
// already-decided data — no retrieval decision is made here.
func buildGroundingInfo(o retrievalOutcome, daemonWorkspace, clientWorkspace string) *protocol.GroundingInfo {
	info := &protocol.GroundingInfo{
		Grounded:  !o.Skipped,
		Workspace: daemonWorkspace,
		Reason:    o.Reason,
		Chunks:    len(o.Chunks),
		Truncated: o.Truncated,
	}
	if clientWorkspace != "" && !sameWorkspaceDir(clientWorkspace, daemonWorkspace) {
		info.WorkspaceMismatch = true
	}
	return info
}

// sameWorkspaceDir reports whether two paths name the same directory.
//
// filepath.Clean alone -- which is what this used to be -- is LEXICAL. It
// removes "." and ".." and duplicate separators and nothing else, so two
// spellings of one directory that differ by a symlink compare unequal, and this
// daemon reports WORKSPACE MISMATCH about the very directory it is serving.
//
// Both sides need resolving, not just ours. The daemon's own root is already
// canonical (see groundedRoot in main.go), but no client is required to send a
// resolved path and neither of ours does: the VS Code extension sends
// workspaceFolders[0].uri.fsPath verbatim, and the TUI sends whatever it was
// given. On macOS that is enough on its own, because /tmp and /var are symlinks
// into /private, so ANY workspace under either has two spellings in ordinary
// use.
//
// Best effort, and it degrades to the old behaviour rather than to a wrong
// answer: a path that cannot be resolved -- because it does not exist on THIS
// machine, which is the normal case for a client path -- falls back to the
// lexical comparison. Mismatch is an advisory flag on a response that is being
// served either way, so a wrong "same" is a missed warning and a wrong
// "different" is a warning on every single turn. Failing towards the quieter
// one is deliberate.
func sameWorkspaceDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}

// logRetrieval writes one summary line per request describing what
// retrieval did, following the existing key=value Printf logging
// convention used everywhere else in the daemon. When debugContext is set,
// it additionally dumps each chunk's full (neutralized) content.
func (s *Server) logRetrieval(o retrievalOutcome) {
	if o.Skipped {
		s.logger.Printf("retrieval skipped: %s", o.Reason)
		return
	}

	refs := make([]string, len(o.Chunks))
	for i, c := range o.Chunks {
		refs[i] = fmt.Sprintf("%s:%d-%d(%s)", c.FilePath, c.StartLine, c.EndLine, c.Class)
	}
	s.logger.Printf("retrieval: chunks=%d truncated=%t rerank=%t sources=[%s]", len(o.Chunks), o.Truncated, !s.rerankDisabled, strings.Join(refs, ", "))
	if o.DirectRefSpans > 0 {
		s.logger.Printf("retrieval: resolved %d file:line reference(s) in the prompt directly to span(s), ranked first", o.DirectRefSpans)
	}
	if o.MergeSavedBytes > 0 {
		s.logger.Printf("retrieval: merged %d chunk(s)/span(s) into contiguous span(s), reclaiming %d rendered byte(s) of duplicated overlap",
			o.MergedFrom, o.MergeSavedBytes)
	}

	if s.debugContext {
		for i, c := range o.Chunks {
			// SCRUBBED, like the wire path, and through the same scrub() with the
			// same --no-scrub escape hatch.
			//
			// This line used to format c.Content directly. renderChunk's doc calls
			// itself "the single retrieval-time choke point" for chunk secret
			// scrubbing, and that was true of the prompt and false here: Content
			// reached two consumers and only one went through the choke point, so
			// --debug-context wrote secrets that scrub() would have caught into the
			// daemon log -- and --log-file makes that log durable on disk.
			//
			// Scrubbing here also makes the debug output MORE faithful, not less:
			// what an operator wants from this flag is what was SENT, and what is
			// sent is redacted. The file on disk is still the file on disk.
			cleaned, _ := scrub(c.Content, s.noScrub())
			s.logger.Printf("retrieval debug: chunk %d (%s:%d-%d) class=%s score=%.4f weighted=%.4f:\n%s",
				i+1, c.FilePath, c.StartLine, c.EndLine, c.Class, c.RawScore, c.Score, neutralizeDelimiters(cleaned))
		}
	}
}
