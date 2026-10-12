package main

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

// LITERAL CREDENTIAL SCRUBBING -- the exact-value defence the heuristic cannot be.
//
// scrub() (scrub.go) matches secret SHAPES: prefixed patterns, precision-first,
// and by design it misses a bare `sk-...` DeepSeek key, a novel format, or any
// ENCODING of a key (base64, hex, url-escaped, reversed). But the daemon KNOWS
// the exact credential values it is holding -- the key it was started with, the
// one `connect` stored, the one set live over the socket. Those exact values,
// and their common encodings, can be matched with certainty and NO false
// positives, which is a different and stronger guarantee than the heuristic's.
//
// This matters because web_search and web_fetch ship as "allow": a key that
// reaches the model's context can be put in a search query or a URL and leave
// the machine with no prompt. So the literal scrubber REFUSES an outbound web
// call that carries a known credential (credscrub_apply.go, point a), and
// redacts a known credential out of every tool result, chunk and prompt that
// enters model context (points b, c), and out of --debug-context (point d).
//
// NOT DISABLEABLE BY --no-scrub. That flag turns off the HEURISTIC, which a user
// may reasonably silence because it corrupts key-shaped identifiers in their
// code. Redacting the daemon's own live credential is not a heuristic and has no
// false positives to silence; it always runs. See credscrub_apply.go.
//
// WHAT IT STILL CANNOT CATCH, and why prompts A (read-tool denylist) and B
// (child-env allowlist) exist: a value transformed by arbitrary code -- XOR,
// gzip, a cipher, character-by-character arithmetic -- is not any of the forms
// below and cannot be, since the set of transforms is unbounded. The defences
// against that are keeping the key out of reach in the first place (it is not in
// any file the read tools will open, nor in any child's environment), not
// recognising it after an adversary has reshaped it.

const (
	// credMinLen is the shortest value registered as a credential. A real key is
	// far longer; a short value would make the fragment rule match common text,
	// and is not a credential worth protecting at the cost of that noise.
	credMinLen = 16

	// credFragmentWindow is the fragment rule: any contiguous run of this many
	// bytes of a raw credential, appearing anywhere in the text, is flagged.
	// This catches a key split across lines or fields to evade the exact-form
	// match ("...firsthalf\n secondhalf..."), because every window of the whole
	// key is registered, so any >= credFragmentWindow-byte piece of it hits one.
	//
	// 16 was chosen by measuring the false-positive rate of random 48-char keys'
	// windows against this repo's own test output corpus (see
	// TestCredFragment_FalsePositiveRateAtWindowSizes): 0 at 16, and the
	// birthday math agrees -- a specific 16-char base62 window has ~62^-16 ~=
	// 10^-29 chance per position, so even a gigabyte corpus is astronomically
	// clear, while 8 would start matching ordinary tokens.
	credFragmentWindow = 16

	credPlaceholder = "[REDACTED:mochiii-credential]"
)

// credForm is one precomputed way a credential value can appear on the wire,
// with a short label used only for logging the KIND of a match, never the value.
type credForm struct {
	pattern string
	label   string
}

// credFormsFor returns the exact-match forms of one raw credential value: the
// raw value and the encodings an exfiltration attempt would reach for. Each is
// in ONE place here with the attack it defends against; add a form here and
// every application point gains it.
//
// Fragment windows are added separately (see buildCredScrubber): they are the
// raw value sliced, not an encoding of it.
func credFormsFor(raw string) []credForm {
	reversed := func(s string) string {
		b := []byte(s)
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}
		return string(b)
	}
	return []credForm{
		{raw, "raw"}, // pasted as-is
		{base64.StdEncoding.EncodeToString([]byte(raw)), "b64"},    // base64(key) -- the commonest wrap
		{base64.RawStdEncoding.EncodeToString([]byte(raw)), "b64"}, // ... without "=" padding
		{base64.URLEncoding.EncodeToString([]byte(raw)), "b64url"}, // base64url, as used in URLs/JWTs
		{base64.RawURLEncoding.EncodeToString([]byte(raw)), "b64url"},
		{hex.EncodeToString([]byte(raw)), "hex"},                  // hex(key), lower
		{strings.ToUpper(hex.EncodeToString([]byte(raw))), "hex"}, // ... upper
		{url.QueryEscape(raw), "urlenc"},                          // %-escaped for a query parameter
		{url.PathEscape(raw), "urlenc"},                           // ... for a path segment (escapes a different set)
		{reversed(raw), "reversed"},                               // written backwards
	}
}

// credScrubber matches a fixed set of credential values and their forms with an
// Aho-Corasick automaton -- one pass over the text however many patterns, which
// matters because a tool result can be 512 KiB per turn. Immutable once built;
// rebuilt (not mutated) when the daemon's credentials change, so a reader never
// needs a lock past fetching the pointer.
type credScrubber struct {
	ac *ahoCorasick
	// labelOf maps a pattern to its form label, for logging the kind of a match.
	labelOf map[string]string
}

// buildCredScrubber compiles the matcher for the given raw credential values.
// Returns nil when there is nothing to match (no credentials, or all too short),
// so the common no-credential path is a cheap nil check at every call site.
func buildCredScrubber(values []string) *credScrubber {
	patterns := map[string]string{} // pattern -> label (first wins; raw/encodings before fragments)
	add := func(pattern, label string) {
		if len(pattern) < credFragmentWindow {
			return // too short to match without inviting false positives
		}
		if _, ok := patterns[pattern]; !ok {
			patterns[pattern] = label
		}
	}

	seen := map[string]bool{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if len(raw) < credMinLen || seen[raw] {
			continue
		}
		seen[raw] = true
		for _, f := range credFormsFor(raw) {
			add(f.pattern, f.label)
		}
		// Fragment windows of the RAW value: every credFragmentWindow-byte slice,
		// so any split piece of the key of at least that length matches one.
		for i := 0; i+credFragmentWindow <= len(raw); i++ {
			add(raw[i:i+credFragmentWindow], "fragment")
		}
	}
	if len(patterns) == 0 {
		return nil
	}

	list := make([]string, 0, len(patterns))
	for p := range patterns {
		list = append(list, p)
	}
	return &credScrubber{ac: buildAhoCorasick(list), labelOf: patterns}
}

// scan reports whether any credential form appears in text, and the distinct
// form labels that matched (sorted, for a stable log line). No value, ever.
// Used by the outbound-refusal path, which needs only a verdict and a reason.
func (c *credScrubber) scan(text string) (hit bool, labels []string) {
	if c == nil {
		return false, nil
	}
	set := map[string]bool{}
	for _, m := range c.ac.matches(text) {
		set[c.labelOf[text[m.start:m.end]]] = true
	}
	if len(set) == 0 {
		return false, nil
	}
	for l := range set {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return true, labels
}

// redact replaces every credential-form occurrence in text with credPlaceholder
// and returns the count replaced. Overlapping and adjacent matches are merged
// into one placeholder so a key that matches as both an exact form and several
// fragment windows is not replaced many times over.
func (c *credScrubber) redact(text string) (string, int) {
	if c == nil {
		return text, 0
	}
	matches := c.ac.matches(text)
	if len(matches) == 0 {
		return text, 0
	}
	// Sort by start, then merge overlaps/adjacencies into spans.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].start != matches[j].start {
			return matches[i].start < matches[j].start
		}
		return matches[i].end > matches[j].end
	})
	// Merge overlapping/adjacent matches into spans; acMatch already carries the
	// (start, end) pair, so it doubles as a span here.
	var spans []acMatch
	for _, m := range matches {
		if n := len(spans); n > 0 && m.start <= spans[n-1].end {
			if m.end > spans[n-1].end {
				spans[n-1].end = m.end
			}
			continue
		}
		spans = append(spans, m)
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	for _, s := range spans {
		b.WriteString(text[prev:s.start])
		b.WriteString(credPlaceholder)
		prev = s.end
	}
	b.WriteString(text[prev:])
	return b.String(), len(spans)
}
