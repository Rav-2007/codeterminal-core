package main

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"codeterminal/protocol"
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

var (
	retrievedContextTagPattern = regexp.MustCompile(buildTagVariantPattern("retrievedcontext"))
	userRequestTagPattern      = regexp.MustCompile(buildTagVariantPattern("userrequest"))
)

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
	s = retrievedContextTagPattern.ReplaceAllStringFunc(s, neutralizeMatch)
	s = userRequestTagPattern.ReplaceAllStringFunc(s, neutralizeMatch)
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
}

// gatherContext retrieves context for prompt using the exact same
// retrieveTopK the CLI `retrieve` command uses (no duplicated retrieval
// logic), then applies the server's configured char budget. It never
// returns an error: any failure — no embedder/store configured, a
// transient retrieval error, zero hits — becomes a Skipped outcome with a
// human-readable Reason, because retrieval must never prevent generation.
func (s *Server) gatherContext(ctx context.Context, prompt string) retrievalOutcome {
	if s.embedder == nil || s.store == nil {
		// Report the cause setupRetrieval actually recorded, not a guess at it
		// (Fix 8). The fallback only covers a Server built without going through
		// setupRetrieval at all, which in practice means a test.
		reason := s.retrievalDisabledReason
		if reason == "" {
			reason = "retrieval unavailable for this daemon"
		}
		return retrievalOutcome{Skipped: true, Reason: reason}
	}

	chunks, err := retrieveTopK(ctx, prompt, s.retrievalTopK, s.embedder, s.store, s.lexicalStore, !s.rerankDisabled)
	if err != nil {
		return retrievalOutcome{Skipped: true, Reason: fmt.Sprintf("retrieval error: %v", err)}
	}
	if len(chunks) == 0 {
		return retrievalOutcome{Skipped: true, Reason: "no relevant chunks found in index"}
	}

	kept, truncated := truncateToBudget(chunks, s.contextBudgetChars, s.cfg.NoScrub)
	return retrievalOutcome{Chunks: kept, Truncated: truncated}
}

// truncateToBudget keeps chunks (already ranked best-first by the vector
// store) in order while their rendered size stays within budget chars,
// dropping only lowest-ranked overflow — the top hit is never sacrificed to
// make room for a lower-ranked one. Reports whether anything was dropped.
func truncateToBudget(chunks []Chunk, budget int, scrubDisabled bool) ([]Chunk, bool) {
	var kept []Chunk
	used := 0
	for _, c := range chunks {
		size := len(renderChunk(len(kept)+1, c, scrubDisabled))
		if len(kept) > 0 && used+size > budget {
			return kept, true
		}
		kept = append(kept, c)
		used += size
	}
	return kept, false
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
		cleaned, reds := scrub(c.Content, s.cfg.NoScrub)
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
	if clientWorkspace != "" && filepath.Clean(clientWorkspace) != filepath.Clean(daemonWorkspace) {
		info.WorkspaceMismatch = true
	}
	return info
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

	if s.debugContext {
		for i, c := range o.Chunks {
			s.logger.Printf("retrieval debug: chunk %d (%s:%d-%d) class=%s score=%.4f weighted=%.4f:\n%s",
				i+1, c.FilePath, c.StartLine, c.EndLine, c.Class, c.RawScore, c.Score, neutralizeDelimiters(c.Content))
		}
	}
}
