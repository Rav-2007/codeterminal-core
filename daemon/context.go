package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
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
		return retrievalOutcome{Skipped: true, Reason: "retrieval disabled (no embedder/index configured for this daemon)"}
	}

	chunks, err := retrieveTopK(ctx, prompt, s.retrievalTopK, s.embedder, s.store)
	if err != nil {
		return retrievalOutcome{Skipped: true, Reason: fmt.Sprintf("retrieval error: %v", err)}
	}
	if len(chunks) == 0 {
		return retrievalOutcome{Skipped: true, Reason: "no relevant chunks found in index"}
	}

	kept, truncated := truncateToBudget(chunks, s.contextBudgetChars)
	return retrievalOutcome{Chunks: kept, Truncated: truncated}
}

// truncateToBudget keeps chunks (already ranked best-first by the vector
// store) in order while their rendered size stays within budget chars,
// dropping only lowest-ranked overflow — the top hit is never sacrificed to
// make room for a lower-ranked one. Reports whether anything was dropped.
func truncateToBudget(chunks []Chunk, budget int) ([]Chunk, bool) {
	var kept []Chunk
	used := 0
	for _, c := range chunks {
		size := len(renderChunk(len(kept)+1, c))
		if len(kept) > 0 && used+size > budget {
			return kept, true
		}
		kept = append(kept, c)
		used += size
	}
	return kept, false
}

// renderChunk formats one chunk as it will appear inside the
// <retrieved_context> block: an index, its source label, and its (safety-
// neutralized) content.
func renderChunk(index int, c Chunk) string {
	return fmt.Sprintf("[%d] %s:%d-%d\n%s\n", index, c.FilePath, c.StartLine, c.EndLine, neutralizeDelimiters(c.Content))
}

// buildAugmentedUserMessage renders retrieved chunks and the raw user
// prompt into a single user-role message, wrapped in explicit, labeled
// delimiters. This is the only place retrieved (untrusted) content is
// combined with a request; it never touches the system role (see
// buildChatMessages in provider.go, which places this string as-is into the
// "user" message). If there are no chunks, the prompt passes through
// unchanged — identical to today's behavior with retrieval off.
func buildAugmentedUserMessage(prompt string, chunks []Chunk) string {
	if len(chunks) == 0 {
		return prompt
	}

	var b strings.Builder
	b.WriteString(retrievedContextOpenTag)
	b.WriteString("\n")
	for i, c := range chunks {
		b.WriteString(renderChunk(i+1, c))
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
		refs[i] = fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine)
	}
	s.logger.Printf("retrieval: chunks=%d truncated=%t sources=[%s]", len(o.Chunks), o.Truncated, strings.Join(refs, ", "))

	if s.debugContext {
		for i, c := range o.Chunks {
			s.logger.Printf("retrieval debug: chunk %d (%s:%d-%d):\n%s", i+1, c.FilePath, c.StartLine, c.EndLine, neutralizeDelimiters(c.Content))
		}
	}
}
