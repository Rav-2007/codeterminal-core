package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// A SECRET THE MODEL WRITES INTO A THIRD-PARTY TOOL'S ARGUMENTS IS SCRUBBED
// before anyone reads them (owner decision, 2026-11-20).
//
// FOUND 2026-11-19, live: a recognisable key the model put into a Lane B call
// was shown verbatim in the approval prompt -- under "LEAVES YOUR MACHINE" --
// and sent as-is when approved. web_search and web_fetch already scrub what
// they send (websearch.go scrubbedQuery), for the reason given there: a loop
// that has read a .env can hand its contents to any host. A server the user
// added is such a host, and a person skimming JSON in a prompt is not a
// scrubber.
//
// It runs where the arguments are coerced to the tool's types (argcoerce.go),
// because both rewrite the arguments BEFORE anything reads them: the approval
// prompt, the ArgumentsSHA256 that binds what was shown to what runs, the audit
// record and the dispatch all see the same scrubbed text. A tool that needs a
// key gets it from its configured env (mcp-secrets.json), never from the model.
// --no-scrub turns this off, as it does on every other path.
//
// The same scrub() as everywhere else, so the same limits: precision-first
// shapes, not opaque high-entropy strings.

// scrubLaneBCall rewrites call's arguments in place when its tool is a third
// party's, and logs the kinds it redacted (never the values).
func (s *Server) scrubLaneBCall(spec mcp.Tool, call *toolCall) {
	if spec.Lane != protocol.LaneThirdParty {
		return
	}
	cleaned, kinds := scrubbedLaneBArguments(call.Function.Arguments, s.noScrub())
	if len(kinds) == 0 {
		return
	}
	s.logger.Printf("agent: %d secret(s) the model wrote into %q's arguments were redacted before the approval and the call: %s",
		len(kinds), call.Function.Name, strings.Join(kinds, ", "))
	call.Function.Arguments = cleaned
}

// scrubbedLaneBArguments returns args with every recognisable secret in a JSON
// string (values and keys) redacted, and the kinds it found. Nothing found
// returns args unchanged, byte for byte.
func scrubbedLaneBArguments(args string, disabled bool) (string, []string) {
	if disabled {
		return args, nil
	}
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber() // a number is sent as the model wrote it, not as a float64
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		// Not one JSON value: scrubbed as text, so a malformed call cannot
		// carry a key past this. The redaction adds no quote or backslash.
		cleaned, reds := scrub(args, false)
		cleaned, named := redactNamedAssignments(cleaned)
		return cleaned, append(redactionKinds(reds), named...)
	}
	var kinds []string
	v = scrubJSONStrings(v, &kinds)
	if len(kinds) == 0 {
		return args, nil
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		cleaned, reds := scrub(args, false)
		return cleaned, redactionKinds(reds)
	}
	return strings.TrimSuffix(out.String(), "\n"), kinds
}

func scrubJSONStrings(v any, kinds *[]string) any {
	switch t := v.(type) {
	case string:
		cleaned, reds := scrub(t, false)
		cleaned, named := redactNamedAssignments(cleaned)
		*kinds = append(append(*kinds, redactionKinds(reds)...), named...)
		return cleaned
	case []any:
		for i := range t {
			t[i] = scrubJSONStrings(t[i], kinds)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			key, reds := scrub(k, false)
			*kinds = append(*kinds, redactionKinds(reds)...)
			if str, ok := val.(string); ok && namedSecretKey(k) && secretValue(str, false) {
				out[key] = namedSecretRedaction
				*kinds = append(*kinds, namedSecretKind)
				continue
			}
			out[key] = scrubJSONStrings(val, kinds)
		}
		return out
	}
	return v
}

// THE NAME RULE: a value whose own name says it is a secret, for third-party
// arguments only (owner decision, 2026-11-20).
//
// scrub() needs a recognisable SHAPE, and laneBCredentialRefusal needs a value
// this machine holds. A database password the model read from a config file
// has neither. What it often has is a NAME: `"password": "..."`,
// `DB_PASSWORD=...`. That is the signal this rule reads, and nothing else:
// entropy was measured on this repository's chunks and rejected (it fired on
// a third of them, with no true positive at any threshold;
// docs/CHUNK_SCRUB_FIRE_RATE.md).
//
// It is scoped to where a false positive costs least and a true one most:
// text leaving for a server the user added, never code the model reads. And
// it is kept narrow:
//   - names ENDING in password, passwd, secret, api key, access token, auth
//     token, private key or credentials, compared without case or separators.
//     A pagination `page_token`, a `cursor` or a `token_count` is never one;
//   - a value of at least 8 characters that is not a placeholder (<...>,
//     ${...}, {{...}}, ****, your-..., example, changeme);
//   - in free text, only `NAME=value` or `NAME: value`, and only a value
//     with a digit in it or 16 characters long, so a sentence such as "the
//     secret: implementation" is left alone.
// What it cannot see -- an unnamed opaque value in free text -- is recorded in
// docs/RESIDUAL_RISKS.md; the approval prompt is the control there.

const (
	namedSecretKind      = "named_secret"
	namedSecretRedaction = "[REDACTED:named_secret]"
)

var namedSecretSuffixes = []string{"password", "passwd", "secret", "apikey", "accesstoken", "authtoken", "privatekey", "credentials"}

func namedSecretKey(name string) bool {
	n := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(name))
	for _, suf := range namedSecretSuffixes {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return false
}

// secretValue reports whether v is a value worth redacting rather than a
// placeholder. inText asks for more, because a name in prose is weaker
// evidence than a JSON key: a digit, or 16 characters.
func secretValue(v string, inText bool) bool {
	t := strings.TrimSpace(v)
	l := strings.ToLower(t)
	switch {
	case len(t) < 8,
		strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">"),
		strings.HasPrefix(t, "${") && strings.HasSuffix(t, "}"),
		strings.HasPrefix(t, "{{") && strings.HasSuffix(t, "}}"),
		strings.Trim(t, "*xX.") == "",
		strings.HasPrefix(l, "your-"), strings.HasPrefix(l, "your_"),
		strings.Contains(l, "example"), strings.Contains(l, "changeme"),
		strings.HasPrefix(t, "[REDACTED"):
		return false
	}
	return !inText || len(t) >= 16 || strings.ContainsAny(t, "0123456789")
}

var namedAssignment = regexp.MustCompile(`(?i)\b[\w.-]*?(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|private[_-]?key|credentials) ?[:=] ?["']?([^\s"'\x60,;&)]{8,})`)

// redactNamedAssignments applies the name rule to free text, returning the
// text and one kind per value it redacted.
func redactNamedAssignments(text string) (string, []string) {
	locs := namedAssignment.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text, nil
	}
	var b strings.Builder
	var kinds []string
	last := 0
	for _, m := range locs {
		vs, ve := m[2], m[3]
		if !secretValue(text[vs:ve], true) {
			continue
		}
		b.WriteString(text[last:vs])
		b.WriteString(namedSecretRedaction)
		last = ve
		kinds = append(kinds, namedSecretKind)
	}
	if kinds == nil {
		return text, nil
	}
	b.WriteString(text[last:])
	return b.String(), kinds
}
