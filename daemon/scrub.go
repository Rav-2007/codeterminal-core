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
// Start/End are offsets into the text as scrub saw it at the moment of that
// pattern's match, never the matched text itself.
type Redaction struct {
	Kind       string
	Start, End int
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
// redactions slice — used by the --no-scrub escape hatch. Only ever scrub
// the user's own typed prompt; never RAG-retrieved chunk content, which is
// local workspace text, not user-pasted secret material, and must reach the
// model unmodified.
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
		for _, m := range matches {
			redactions = append(redactions, Redaction{Kind: p.kind, Start: m[0], End: m[1]})
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
