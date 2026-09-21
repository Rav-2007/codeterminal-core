package main

// Fuzzing the trust boundaries agent mode added.
//
// scripts/fuzz.sh states the rule this file exists to satisfy: "every target
// reads bytes this codebase did not author... nothing else is fuzzed, because
// nothing else is a trust boundary". When that was written the daemon had no
// such readers. Agent mode added four, and none of them had a target:
//
//   - renderToolResult reads the output of a THIRD-PARTY SUBPROCESS and puts
//     what survives on the wire to the model. It is the egress boundary for
//     bytes an unconfined program chose, which makes it the highest-value
//     target in this file.
//   - verifyApproval reads a client's answer to a security question. A false
//     positive RUNS A TOOL THE USER DID NOT APPROVE.
//   - toolCallAccumulator reads SSE fragments from the provider and assembles
//     the call that gets dispatched.
//   - SplitQualifiedName reads a tool name the MODEL supplied and it is the key
//     dispatch happens on.
//
// As in editapply/fuzz_test.go and proxy/fuzz_test.go, the invariants are the
// point -- this is not crash-hunting. Each target asserts a property whose
// violation is a specific, nameable harm, stated at the assertion.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// FuzzRenderToolResult — THE EGRESS BOUNDARY.
//
// Whatever a Lane B server returns passes through here on its way to the model,
// which means on its way off the machine. Three properties, each a real harm if
// broken:
//
//	the cap holds        -- a server returning 10 MB must not put 10 MB on the wire
//	valid UTF-8 out      -- truncation must not slice a multi-byte rune, and a
//	                        server returning arbitrary bytes must not corrupt the
//	                        JSON body that carries them
//	no half-redaction    -- the truncate-before-scrub order exists so a cap can
//	                        never cut a "[REDACTED:...]" placeholder in half and
//	                        leave the tail of a secret next to it
func FuzzRenderToolResult(f *testing.F) {
	f.Add("", 100)
	f.Add("plain tool output", 100)
	f.Add("sk-or-v1-0123456789abcdef0123456789abcdef0123456789abcdef", 100)
	f.Add("prefix sk-or-v1-0123456789abcdef0123456789abcdef0123456789abcdef suffix", 30)
	// Multi-byte runes straddling every plausible cut point.
	f.Add(strings.Repeat("é", 50), 25)
	f.Add(strings.Repeat("🔑", 50), 33)
	f.Add("AKIA1234567890ABCDEF", 10)
	f.Add("\x00\x01\x02\xff\xfe", 4)
	f.Add(strings.Repeat("a", 10000), 16)

	f.Fuzz(func(t *testing.T, content string, maxBytes int) {
		// The loop only ever passes a positive cap (see dispatchToolCall, which
		// derives it from two budgets), so anything else is out of contract.
		if maxBytes <= 0 || maxBytes > 1<<20 {
			t.Skip()
		}

		rendered, kinds, emitted := renderToolResult(content, maxBytes, false, false)

		// THE CAP HOLDS. emitted is what the caller charges against the turn's
		// byte budget, so a mismatch would let a tool spend budget it was not
		// charged for -- and the budget is what bounds egress.
		if emitted != len(rendered) {
			t.Fatalf("emitted %d but rendered %d bytes: the turn's egress budget "+
				"is charged the wrong amount", emitted, len(rendered))
		}
		// Scrubbing can only shrink or annotate; the truncation marker is the one
		// thing added after the cut, so allow a bounded overhead rather than an
		// exact bound. The harm being ruled out is UNBOUNDED output.
		if len(rendered) > maxBytes+512 {
			t.Fatalf("cap %d but rendered %d bytes: a tool's output is not bounded",
				maxBytes, len(rendered))
		}

		// VALID UTF-8 OUT. clipUTF8 exists so a cut never lands mid-rune; a
		// caller that put invalid UTF-8 in gets replacement characters, not a
		// corrupt body.
		if !utf8.ValidString(rendered) && utf8.ValidString(content) {
			t.Fatalf("valid UTF-8 in, invalid out: %q", rendered)
		}

		// NO HALF-REDACTION. If a placeholder was emitted at all, it must be
		// whole -- a truncated "[REDACTED:openai_k" is the exact shape the
		// truncate-before-scrub order was chosen to prevent.
		if i := strings.LastIndex(rendered, "[REDACTED"); i >= 0 {
			if !strings.Contains(rendered[i:], "]") {
				t.Fatalf("a redaction placeholder was cut in half: %q", rendered[i:])
			}
		}
		for _, k := range kinds {
			if k == "" {
				t.Fatal("an empty redaction kind would render as a blank notice to the user")
			}
		}
	})
}

// FuzzVerifyApproval — A FALSE POSITIVE RUNS A TOOL NOBODY APPROVED.
//
// This is the strongest invariant in the daemon and the cheapest to state: for
// an arbitrary body, verifyApproval may return an APPROVING decision only if
// that body carries the exact call id and the exact argument digest it was
// asked about. Everything else -- garbage, a different call, a stale digest, an
// invented verb -- must deny.
func FuzzVerifyApproval(f *testing.F) {
	const args = `{"path":"/etc/shadow"}`
	sum := sha256.Sum256([]byte(args))
	digest := hex.EncodeToString(sum[:])

	f.Add(`{"protocol_version":1,"approval":true,"call_id":"c1","arguments_sha256":"` + digest + `","decision":"approve"}`)
	f.Add(`{"approval":true,"call_id":"c1","arguments_sha256":"` + digest + `","decision":"approve_for_turn"}`)
	f.Add(`{"approval":false,"call_id":"c1","arguments_sha256":"` + digest + `","decision":"deny"}`)
	f.Add(`{"approval":true,"call_id":"other","arguments_sha256":"` + digest + `","decision":"approve"}`)
	f.Add(`{"APPROVAL":true,"call_id":"c1","decision":"approve"}`)
	f.Add(`{"approval":false,"approval":true,"call_id":"c1","decision":"approve"}`)
	f.Add(`{}`)
	f.Add(`null`)
	f.Add(`"approve"`)
	f.Add(``)

	req := protocol.ToolApprovalRequest{
		CallID:          "c1",
		Server:          "somebodys-server",
		Tool:            "read_anything",
		Arguments:       args,
		ArgumentsSHA256: digest,
		Lane:            protocol.LaneThirdParty,
	}

	f.Fuzz(func(t *testing.T, body string) {
		decision, _ := verifyApproval(req, json.RawMessage(body))

		if decision != protocol.ApprovalApprove && decision != protocol.ApprovalApproveForTurn {
			return // denied or cancelled: always safe
		}

		// It approved. Prove it had the right to, from the body alone -- an
		// independent oracle, not a re-run of the function under test.
		var resp protocol.ToolApprovalResponse
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("APPROVED a body that does not even decode: %q", body)
		}
		if !resp.Approval {
			t.Fatalf("APPROVED with approval=false: %q", body)
		}
		if resp.CallID != req.CallID {
			t.Fatalf("APPROVED an answer to a different call (%q): %q", resp.CallID, body)
		}
		if !strings.EqualFold(resp.ArgumentsSHA256, req.ArgumentsSHA256) {
			t.Fatalf("APPROVED with a digest that is not the one shown to the user: %q", body)
		}
		if resp.Decision != protocol.ApprovalApprove && resp.Decision != protocol.ApprovalApproveForTurn {
			t.Fatalf("APPROVED on an invented verb %q: %q", resp.Decision, body)
		}
	})
}

// FuzzToolCallAccumulator — never repair, only refuse.
//
// The accumulator assembles tool calls from SSE fragments the provider chose to
// split however it liked. A stream cut mid-fragment leaves a call with no name
// or with arguments that are not valid JSON, and the dangerous behaviour is
// GUESSING: dispatching a half-built call is dispatching something the model
// never finished asking for.
func FuzzToolCallAccumulator(f *testing.F) {
	// Newline-separated chunk JSONs: a whole streamed response, fragmented
	// however the provider felt like fragmenting it. Fuzzing the wire shape
	// rather than the assembled struct is deliberate -- the fragmenting IS the
	// hazard, and a target that took pre-joined fragments would skip it.
	f.Add(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"builtin__read_file","arguments":"{\"path\":\"a\"}"}}]}}]}`)
	f.Add(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"a__b","arguments":"{\"p"}}]}}]}` + "\n" +
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ath\":\"a\"}"}}]}}]}`)
	// Cut mid-arguments: the classic truncated stream. Must be refused.
	f.Add(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"a__b","arguments":"{\"pa"}}]}}]}`)
	// A fragment that never carried a name.
	f.Add(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`)
	f.Add(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"a__b","arguments":"{}"}},{"index":1,"id":"c2","function":{"name":"c__d","arguments":"[]"}}]}}]}`)
	f.Add(`{"choices":[{"delta":{"content":"just text"}}]}`)
	f.Add(`{"choices":[]}`)
	f.Add(`{}`)

	f.Fuzz(func(t *testing.T, stream string) {
		acc := newToolCallAccumulator()
		any := false
		for _, line := range strings.Split(stream, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var chunk chatCompletionChunk
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				continue // not a shape the provider could send
			}
			any = true
			acc.ingest(chunk)
		}
		if !any {
			t.Skip()
		}

		calls, err := acc.finish()
		if err != nil {
			return // refused: the safe outcome
		}

		for _, c := range calls {
			// Every field dispatch depends on must be present. An empty name
			// resolves to nothing; an empty id means the approval prompt cannot
			// be bound to a call at all.
			if c.ID == "" {
				t.Fatalf("assembled a call with no id from %q -- an approval could not be bound to it", stream)
			}
			if c.Function.Name == "" {
				t.Fatalf("assembled a call with no name from %q", stream)
			}
			// The arguments are shown to a human and handed to a tool. Invalid
			// JSON here means the user is shown something the tool will not
			// receive, which breaks the what-you-saw-is-what-runs promise.
			if !json.Valid([]byte(c.Function.Arguments)) {
				t.Fatalf("assembled a call whose arguments are not valid JSON (%q) from %q",
					c.Function.Arguments, stream)
			}
		}
	})
}

// FuzzSplitQualifiedName — the model supplies this string, and it is the key
// dispatch happens on.
//
// The harm being ruled out is a name that parses into a server the user did not
// mean. QualifiedName joins on "__", so a tool whose own name contains "__" is
// exactly the ambiguous case, and the requirement is that anything accepted
// round-trips: whatever comes out must rebuild the input, or the pair that was
// dispatched is not the pair that was named.
func FuzzSplitQualifiedName(f *testing.F) {
	f.Add("builtin__read_file")
	f.Add("builtin__read__file")
	f.Add("__read_file")
	f.Add("builtin__")
	f.Add("builtin")
	f.Add("")
	f.Add("__")
	f.Add("a__b__c")
	f.Add("builtin\x00__read_file")

	f.Fuzz(func(t *testing.T, qualified string) {
		server, tool, err := mcp.SplitQualifiedName(qualified)
		if err != nil {
			// Refused. Nothing may be usable on this path.
			if server != "" || tool != "" {
				t.Fatalf("refused %q but still returned server=%q tool=%q", qualified, server, tool)
			}
			return
		}
		if server == "" || tool == "" {
			t.Fatalf("accepted %q with an empty half: server=%q tool=%q", qualified, server, tool)
		}
		// ROUND-TRIP. If this fails, the daemon looked up a different tool from
		// the one the model named, and the approval prompt showed a name that is
		// not what runs.
		if got := (mcp.Tool{Server: server, Name: tool}).QualifiedName(); got != qualified {
			t.Fatalf("split %q into (%q, %q) which rejoins as %q", qualified, server, tool, got)
		}
	})
}
