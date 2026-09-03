// This file implements HEURISTIC, best-effort scrubbing of common secret
// shapes out of a user's typed prompt before it leaves this machine. It is
// defense-in-depth, NOT a guarantee: it matches a fixed set of high-
// confidence, prefixed patterns (OpenAI-style, AWS, GitHub, Slack, Google,
// Supabase, Mochiii keys, and PEM private-key blocks) and nothing else. It
// WILL miss secrets that don't match one of these shapes — novel key
// formats, obfuscated/split/base64-wrapped secrets, or anything pasted
// without its normal prefix. Do not present this as, or rely on it as,
// something that guarantees no secret ever reaches the model API.
package main

import "regexp"

// Redaction records that a match was found and replaced. It deliberately
// carries no secret material — Kind is a fixed label (e.g. "openai_key"),
// never the matched text itself.
//
// IT NO LONGER CARRIES OFFSETS, and their removal is the fix rather than a
// simplification (L6). Start/End were recorded against `cleaned` as it stood
// during that pattern's pass — but scrub runs the patterns in sequence, each
// rewriting the string for the next, so every offset from a pattern after the
// first indexed a string that no longer existed by the time scrub returned.
// Nothing consumed them (redactionKinds reads Kind alone), so nothing was ever
// wrong on the wire; what existed was accurate-looking data that was not
// accurate, waiting for the first consumer to trust it. Wrong offsets are worse
// than no offsets. If a caller ever needs positions, they have to be computed
// against the returned string, which is a different piece of work.
type Redaction struct {
	Kind string
}

// scrubPatterns are compiled once at package init. Every pattern here is
// deliberately a high-confidence, prefixed shape (precision-first: accept
// false negatives over false positives, since this is a coding assistant
// and users legitimately paste key-shaped identifiers, e.g. variable names,
// that must never be silently corrupted). No generic KEY=value or bare-
// base64 pattern is included on purpose — those are a false-positive
// minefield and out of scope for this heuristic pass.
var scrubPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"openai_key", regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
	// Stripe. THE UNDERSCORE IS THE WHOLE REASON THIS LINE IS SEPARATE from the
	// openai_key pattern above: that one is `sk-`, this one is `sk_`, and a
	// Stripe live key therefore matched nothing here at all. Caught end to end --
	// a canary `sk_live_...` in an indexed file travelled verbatim inside
	// <retrieved_context> to the provider, and warn-mode's entropy detector did
	// not catch it either, because a key with low character diversity is
	// low-entropy. Two layers, both blind to one of the most common secrets a
	// repository actually contains.
	//
	// This is NOT the Design B/C question D5 ruled on. That ruling rejected
	// ENTROPY/KEYWORD redaction, measured at 33% of chunks with zero precision.
	// This is a prefixed structural shape of exactly the kind the eight patterns
	// around it already are, and it carries their false-positive cost: none
	// measured, because `sk_live_` followed by 16+ base62 characters is not a
	// shape that occurs by accident.
	//
	// rk_ (restricted keys) is included with sk_ because it is equally secret.
	// pk_ (publishable) is deliberately NOT: it is public by design, and
	// redacting it would corrupt working code for no gain.
	{"stripe_secret_key", regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`)},
	{"aws_access_key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"github_token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"slack_token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"google_key", regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`)},
	{"supabase_secret", regexp.MustCompile(`sb_secret_[A-Za-z0-9]{20,}`)},
	{"supabase_publishable", regexp.MustCompile(`sb_publishable_[A-Za-z0-9]{20,}`)},
	{"mochiii_key", regexp.MustCompile(`mochi_[a-f0-9]{20,}`)},
	{"private_key_block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)},
}

// scrub replaces every high-confidence secret-shaped match in text with a
// labeled placeholder ([REDACTED:<kind>]) and reports what it found. If
// disabled is true, it's a pure no-op — returns text unchanged and a nil
// redactions slice — used by the --no-scrub escape hatch. It is called on two
// inputs: the user's own typed prompt (server.go) AND, since Option A
// (daemon/CHUNK_SCRUB_DESIGN.md §4), RAG-retrieved chunk content at the
// renderChunk choke point (context.go). Chunk content is scrubbed because it
// is NOT local-only: the query embedding is computed locally (ONNX), but the
// retrieved chunk text itself IS POSTed to the hosted completion provider on
// every grounded turn, so it needs the same structural protection. This
// covers structural signatures only — opaque/novel secrets with no
// recognizable prefix are not caught here and remain open (see the warn-mode
// measurement in chunkscrub.go, gathering data for that future decision).
func scrub(text string, disabled bool) (cleaned string, redactions []Redaction) {
	if disabled {
		return text, nil
	}

	cleaned = text
	for _, p := range scrubPatterns {
		matches := p.re.FindAllStringIndex(cleaned, -1)
		if matches == nil {
			continue
		}
		for range matches {
			redactions = append(redactions, Redaction{Kind: p.kind})
		}
		cleaned = p.re.ReplaceAllString(cleaned, "[REDACTED:"+p.kind+"]")
	}
	return cleaned, redactions
}

// redactionKinds returns the Kind of each redaction, in order, for a
// compact log/notice line -- never the matched text.
func redactionKinds(redactions []Redaction) []string {
	kinds := make([]string, len(redactions))
	for i, r := range redactions {
		kinds[i] = r.Kind
	}
	return kinds
}
