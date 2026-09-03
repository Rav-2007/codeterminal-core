// This file implements the WARN-MODE (log-only, never-redact) deferred secret
// detectors for retrieved chunk content: high-entropy strings (§2b) and
// assignment/keyword heuristics (§2c) from CHUNK_SCRUB_DESIGN.md.
//
// They exist ONLY to measure how often each heuristic would fire on real
// repos, so the founder can later decide — on evidence, not guesswork —
// whether entropy/keyword *redaction* (Designs B/C) is worth its
// false-positive cost. Nothing here alters what is sent to the model: the only
// live redactor in this task is the structural scrub() run inside renderChunk
// (Option A). If you find yourself wiring these into the outbound prompt, stop
// — that is a future founder decision pending the data this mode gathers.
//
// Non-negotiable: never log raw suspected-secret content. Every detection
// carries a fixed detector label, a secret-free note, and a truncated SHA-256
// indicator of the suspected value — enough to recognize the same value across
// log lines and to measure fire rate, never enough to recover the value.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// warnDetection is one LOG-ONLY signal from a deferred detector. It is
// deliberately free of raw secret material — Detector, Note and Shape are
// fixed/computed labels, Indicator is a one-way hash prefix, never the value.
type warnDetection struct {
	Detector  string // "entropy" | "keyword"
	Note      string // secret-free, e.g. "len=44 bits_per_char=4.72" or "keyword=password value_len=18"
	Shape     string // fixed-label token shape for FP triage: hex|base64|uuid-like|mixed|unknown
	Indicator string // "sha256:xxxxxxxx" over the suspected value; never the value itself
}

// detectWarnModeSecrets runs every deferred (log-only) detector over already
// structurally-scrubbed content and returns their detections. Callers must
// treat the result as measurement only.
func detectWarnModeSecrets(text string) []warnDetection {
	var out []warnDetection
	out = append(out, detectHighEntropy(text)...)
	out = append(out, detectKeywordSecrets(text)...)
	return out
}

// entropyTokenPattern isolates the kind of long, unbroken token that an opaque
// secret would appear as (base64/hex/url-safe alphabets). Short tokens can't
// carry enough bits to be a credential and are skipped by the length floor.
var entropyTokenPattern = regexp.MustCompile(`[A-Za-z0-9+/=_\-]{20,}`)

// entropyWarnThresholdBitsPerChar is a deliberately provisional starting
// threshold. It is NOT tuned — the whole point of warn-mode is to gather the
// data needed to tune (or reject) it. bits_per_char is logged on every hit so
// the distribution on real repos can be inspected. Source code is full of
// legitimately high-entropy strings (SHAs, UUIDs, base64 assets, minified JS),
// so a real redacting threshold must be chosen from this data, not here.
const entropyWarnThresholdBitsPerChar = 4.0

// detectHighEntropy flags long, high-Shannon-entropy tokens. Log-only.
func detectHighEntropy(text string) []warnDetection {
	var out []warnDetection
	for _, tok := range entropyTokenPattern.FindAllString(text, -1) {
		bits := shannonEntropy(tok)
		if bits < entropyWarnThresholdBitsPerChar {
			continue
		}
		out = append(out, warnDetection{
			Detector:  "entropy",
			Note:      fmt.Sprintf("len=%d bits_per_char=%.2f", len(tok), bits),
			Shape:     classifyTokenShape(tok),
			Indicator: valueIndicator(tok),
		})
	}
	return out
}

// shannonEntropy returns the per-character Shannon entropy (bits/char) of s
// over its raw byte distribution. Range 0 (all one byte) to 8 (uniform).
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, count := range freq {
		if count == 0 {
			continue
		}
		p := count / n
		h -= p * math.Log2(p)
	}
	return h
}

// keywordAssignmentPattern matches an assignment/mapping whose KEY names a
// credential (password/secret/api_key/token/...) and captures the VALUE token
// only (group 2), so span-scoped measurement is possible without ever touching
// the surrounding line. Log-only; it never redacts.
//
// THE TRAILING \b USED TO MAKE THIS BLIND TO THE COMMONEST FORM THERE IS.
// "_" is a word character, so in SECRET_KEY there is no boundary between
// SECRET and _KEY, and the pattern did not match. Nor did stripe_secret_key,
// nor dbPassword. It DID match apiKey, authToken and clientSecret -- which is
// why the gap survived: every positive case in chunkscrub_test.go used a bare
// credential word as the whole identifier (`token =`, `password:`,
// `api_key =`, `client_secret:`), so the suite passed while the detector
// missed snake_case, SCREAMING_SNAKE and most camelCase compounds. A test
// that only asks the question the code already answers.
//
// The credential word may now sit anywhere inside the identifier, with the
// surrounding segments matched but not captured. GROUP 1 IS STILL THE FIXED
// VOCABULARY WORD, never the identifier, because the Note built from it is
// logged and this file's contract is that only a fixed label, a secret-free
// note and a one-way indicator ever reach a log line.
//
// Widening it lets through things like `my_token_bucket_size = 10`, which the
// old pattern's boundary excluded by accident. isNonSecretValue drops those on
// the value side, where the decision belongs: 10 is four characters short of
// the floor. Over-reporting and then filtering is what warn-mode is FOR -- it
// is a fire-rate measurement, and a detector that silently misses the dominant
// naming convention biases the very data D5 is to be decided from.
var keywordAssignmentPattern = regexp.MustCompile(
	`(?i)\b[A-Za-z0-9.\-]*_?(passwords?|passwd|pwd|secrets?|api[_-]?keys?|apikey|access[_-]?tokens?|auth[_-]?tokens?|tokens?|client[_-]?secret|private[_-]?keys?)[A-Za-z0-9_.\-]*\s*[:=]\s*["']?([^\s"',;)]+)`)

// detectKeywordSecrets flags credential-named assignments with a plausible
// literal value. Log-only.
func detectKeywordSecrets(text string) []warnDetection {
	var out []warnDetection
	for _, m := range keywordAssignmentPattern.FindAllStringSubmatch(text, -1) {
		keyword := strings.ToLower(m[1])
		value := m[2]
		if isNonSecretValue(value) {
			continue
		}
		out = append(out, warnDetection{
			Detector:  "keyword",
			Note:      fmt.Sprintf("keyword=%s value_len=%d", keyword, len(value)),
			Shape:     classifyTokenShape(value),
			Indicator: valueIndicator(value),
		})
	}
	return out
}

// isNonSecretValue filters the obvious non-secrets a bare keyword match would
// otherwise flag: too-short values, and indirections (env-var / config
// references) that are not themselves a secret. It is intentionally lenient —
// warn-mode is meant to over-report and be measured, but these are so clearly
// not literal credentials that counting them would only add noise to the
// fire-rate signal.
func isNonSecretValue(v string) bool {
	if len(v) < 6 {
		return true
	}
	switch {
	case strings.HasPrefix(v, "${"), strings.HasPrefix(v, "$("), strings.HasPrefix(v, "$"):
		return true // shell / template env reference
	case strings.HasPrefix(v, "os.environ"), strings.HasPrefix(v, "process.env"), strings.HasPrefix(v, "os.Getenv"):
		return true // programmatic env lookup
	case strings.HasPrefix(v, "[REDACTED:"):
		return true // already redacted by Option A upstream
	}
	return false
}

// valueIndicator returns a short, one-way indicator for a suspected value: a
// truncated SHA-256, prefixed so it is unmistakably a hash and never the value.
// Same value -> same indicator (recognizable across log lines); the value is
// not recoverable from it.
func valueIndicator(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "sha256:" + hex.EncodeToString(sum[:])[:8]
}

var (
	uuidLikePattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	allHexPattern    = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	base64ishPattern = regexp.MustCompile(`^[A-Za-z0-9+/=_\-]+$`)
)

// classifyTokenShape returns a COARSE, FIXED-LABEL description of a suspected
// value's character shape — a triage aid so a later human review can separate
// true secrets from the dominant false positives (git SHAs, content hashes,
// UUIDs, base64 assets/lockfile integrity strings; see CHUNK_SCRUB_DESIGN §2b)
// without re-opening every source file.
//
// It is NOT a detector and makes NO secret/not-secret decision — it never
// feeds redaction. Non-negotiable (preserves Gate 3): it returns ONLY one of a
// fixed set of labels and NEVER any substring of tok, so no raw value material
// can ride out on the shape field. Order matters: uuid (most specific) before
// hex before base64 (hex's alphabet is a subset of base64's).
func classifyTokenShape(tok string) string {
	switch {
	case tok == "":
		return "unknown"
	case uuidLikePattern.MatchString(tok):
		return "uuid-like"
	case allHexPattern.MatchString(tok):
		return "hex"
	case base64ishPattern.MatchString(tok):
		return "base64"
	default:
		return "mixed"
	}
}
