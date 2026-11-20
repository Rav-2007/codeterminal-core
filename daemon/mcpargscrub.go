package main

import (
	"bytes"
	"encoding/json"
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
		return cleaned, redactionKinds(reds)
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
		*kinds = append(*kinds, redactionKinds(reds)...)
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
			out[key] = scrubJSONStrings(val, kinds)
		}
		return out
	}
	return v
}
