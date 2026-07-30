package main

// Fuzzing the trust boundary (P3.1).
//
// Everything fuzzed here reads bytes the proxy did not author: a caller's
// request body, or an SSE line from OpenRouter. The repo had zero fuzz targets
// before this file.
//
// THREE INVARIANTS, applied throughout:
//
//   never panic     -- a panic in a predicate is a 500 at best; before P1.3 it
//                      also stranded the reservation, and it is still a request
//                      the caller did not get.
//   always terminate -- go test's fuzzer enforces this by timing out.
//   FAIL CLOSED     -- the important one. Every predicate below decides whether
//                      to forward a request or trust a value. Garbage in must
//                      mean "refuse", never "allow". The depth guard in
//                      scanJSONValue already establishes this convention.
//
// The fail-closed properties are asserted against json.Valid as an independent
// oracle: if the standard library says a body is not valid JSON, no gate here
// may report that it is satisfied. That is what makes these more than
// crash-hunting.

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzTopLevelFields covers the shared decode every other predicate is built
// on, including hasDuplicateKeys/scanJSONValue's recursive descent -- the one
// place in the request path with unbounded input-driven recursion.
func FuzzTopLevelFields(f *testing.F) {
	f.Add([]byte(`{"model":"x","stream":true}`))
	f.Add([]byte(`{"a":{"b":{"c":[1,2,{"d":null}]}}}`))
	f.Add([]byte(`{"dup":1,"dup":2}`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`{`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	// Deep nesting: the shape the depth guard exists for.
	f.Add([]byte(strings.Repeat(`{"a":`, 200) + `1` + strings.Repeat(`}`, 200)))

	f.Fuzz(func(t *testing.T, body []byte) {
		fields, ok := topLevelFields(body)

		if ok && !json.Valid(body) {
			t.Fatalf("topLevelFields accepted a body encoding/json rejects: %q", body)
		}
		if !ok && fields != nil {
			t.Fatalf("topLevelFields returned fields alongside ok=false, so a caller "+
				"checking only the map would act on a body that was refused: %q", body)
		}

		// hasDuplicateKeys walks the same bytes recursively; it must survive
		// whatever the decode did.
		_ = hasDuplicateKeys(body)
	})
}

// FuzzStreamRequested pins the fail-closed direction of the streaming gate.
// A body it cannot parse must NOT read as "streaming requested".
func FuzzStreamRequested(f *testing.F) {
	f.Add([]byte(`{"stream":true}`))
	f.Add([]byte(`{"stream":"true"}`))
	f.Add([]byte(`{"stream":1}`))
	f.Add([]byte(`{"STREAM":true}`))
	f.Add([]byte(`{"stream":null}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`garbage`))

	f.Fuzz(func(t *testing.T, body []byte) {
		if streamRequested(body) && !json.Valid(body) {
			t.Fatalf("streamRequested said yes for a body that is not valid JSON: %q", body)
		}
	})
}

// FuzzZDRRoutingEnforced is the most security-relevant target in this file:
// zdrRoutingEnforced is the F1 gate, and a false positive forwards a request
// that was NOT proven to carry zero-data-retention flags.
//
// Two properties, both fail-closed:
//   - an unparseable body can never satisfy the gate
//   - a body carrying no "provider" key at all can never satisfy it
func FuzzZDRRoutingEnforced(f *testing.F) {
	f.Add([]byte(`{"provider":{"zdr":true,"data_collection":"deny"}}`))
	f.Add([]byte(`{"provider":{"zdr":true,"data_collection":"allow"}}`))
	f.Add([]byte(`{"provider":{"zdr":false,"data_collection":"deny"}}`))
	f.Add([]byte(`{"PROVIDER":{"zdr":true,"data_collection":"deny"}}`))
	f.Add([]byte(`{"provider":"deny"}`))
	f.Add([]byte(`{"provider":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"provider":{"zdr":"true","data_collection":"deny"}}`))

	f.Fuzz(func(t *testing.T, body []byte) {
		enforced := zdrRoutingEnforced(body)
		if !enforced {
			return
		}

		if !json.Valid(body) {
			t.Fatalf("F1 gate satisfied by a body that is not valid JSON: %q", body)
		}
		// Exact-key discipline: satisfying the gate requires a literal "provider"
		// key. If the gate passed, that key must be decodable from the body.
		var top map[string]json.RawMessage
		if err := json.Unmarshal(body, &top); err != nil {
			t.Fatalf("F1 gate satisfied by a body that does not decode to an object: %q", body)
		}
		if _, ok := top["provider"]; !ok {
			t.Fatalf("F1 gate satisfied with no top-level \"provider\" key, so a request "+
				"with no routing object would be forwarded as if it had proven ZDR: %q", body)
		}
	})
}

// FuzzPeekMaxTokens: this value SIZES A RESERVATION, so a bogus one is a
// billing fault rather than a parse fault. The contract is that ok=true implies
// a usable positive number.
func FuzzPeekMaxTokens(f *testing.F) {
	f.Add([]byte(`{"max_tokens":100}`))
	f.Add([]byte(`{"max_tokens":0}`))
	f.Add([]byte(`{"max_tokens":-5}`))
	f.Add([]byte(`{"max_tokens":1e9}`))
	f.Add([]byte(`{"max_tokens":"100"}`))
	f.Add([]byte(`{"MAX_TOKENS":100}`))
	f.Add([]byte(`{"max_tokens":99999999999999999999}`))

	f.Fuzz(func(t *testing.T, body []byte) {
		v, ok := peekMaxTokens(body)
		if !ok {
			return
		}
		if v <= 0 {
			t.Fatalf("peekMaxTokens returned ok with a non-positive value %d, which would "+
				"size a reservation at or below zero: %q", v, body)
		}
		if !json.Valid(body) {
			t.Fatalf("peekMaxTokens read a value out of a body that is not valid JSON: %q", body)
		}
	})
}

// FuzzPeekUsageTotal reads the figure a request is BILLED on.
func FuzzPeekUsageTotal(f *testing.F) {
	f.Add([]byte(`{"usage":{"total_tokens":137}}`))
	f.Add([]byte(`{"usage":{"total_tokens":-1}}`))
	f.Add([]byte(`{"usage":{}}`))
	f.Add([]byte(`{"usage":null}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, body []byte) {
		v, ok := peekUsageTotal(body)
		if ok && !json.Valid(body) {
			t.Fatalf("peekUsageTotal read %d out of a body that is not valid JSON: %q", v, body)
		}
	})
}

// FuzzStripSSEAccountMetadata guards the ZDR-preserving rewrite on the response
// side. Two properties matter more than "does not crash":
//
//  1. If it rewrites, the metadata key is GONE. That is the entire purpose.
//  2. If the line is not a data: chunk it can safely rewrite, it is returned
//     BYTE FOR BYTE. Passing content through untouched is what lets this proxy
//     claim it never mangles or inspects message text.
func FuzzStripSSEAccountMetadata(f *testing.F) {
	f.Add(`data: {"user_id":"u1","choices":[{"delta":{"content":"hi"}}]}`)
	f.Add(`data: {"choices":[{"delta":{"content":"hi"}}]}`)
	f.Add(`data: [DONE]`)
	f.Add(`data: `)
	f.Add(`event: ping`)
	f.Add(``)
	f.Add(`data: {"user_id":null}`)
	f.Add(`data: not-json`)

	f.Fuzz(func(t *testing.T, line string) {
		out := stripSSEAccountMetadata(line)

		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			if out != line {
				t.Fatalf("a non-data line was rewritten, so the relay is no longer "+
					"byte-for-byte: in=%q out=%q", line, out)
			}
			return
		}

		if out == line {
			return // untouched, nothing to check
		}

		// It rewrote. The metadata fields must be gone from the top level.
		data := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "data:"))
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			t.Fatalf("rewrote a chunk into something that is no longer valid JSON: "+
				"in=%q out=%q", line, out)
		}
		for _, field := range accountMetadataFields {
			if _, present := parsed[field]; present {
				t.Fatalf("rewrote the chunk but %q survived, so account metadata would "+
					"still reach the client: in=%q out=%q", field, line, out)
			}
		}
	})
}

// FuzzExtractUsageAndProvider covers the two SSE readers that feed billing and
// logging. Neither may accept a line that is not a data: chunk -- extractUsage's
// number is what a request is charged, and extractProvider's string is written
// to a log.
func FuzzExtractUsageAndProvider(f *testing.F) {
	f.Add(`data: {"usage":{"total_tokens":137}}`)
	f.Add(`data: {"provider":"DeepInfra"}`)
	f.Add(`data: [DONE]`)
	f.Add(`: comment`)
	f.Add(`data:{"provider":"x"}`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, line string) {
		usage, usageOK := extractUsage(line)
		provider, providerOK := extractProvider(line)

		isData := strings.HasPrefix(strings.TrimSpace(line), "data:")
		if !isData {
			if usageOK {
				t.Fatalf("extractUsage billed %d from a non-data SSE line: %q", usage, line)
			}
			if providerOK {
				t.Fatalf("extractProvider returned %q from a non-data SSE line: %q", provider, line)
			}
		}

		// A provider name is written into a log line. Newlines would let a
		// malicious upstream forge log records.
		if providerOK && strings.ContainsAny(provider, "\n\r") {
			t.Fatalf("extractProvider returned a value containing a newline, which would "+
				"forge log lines: %q from %q", provider, line)
		}
	})
}
