package main

import (
	"bytes"
	"encoding/json"
)

// Exact-key request dispatch for the daemon socket.
//
// DELIBERATE DUPLICATION. proxy/main.go carries a near-identical topLevelFields
// and hasDuplicateKeys. They are not shared through the protocol module because
// the proxy must stay dependency-free: proxy/Dockerfile does `COPY go.mod ./`
// and `COPY *.go ./` with no go.sum and no sibling modules, so anything the
// proxy imports from this repo would have to be vendored into its image. Two
// copies with one stated rule is the trade that was chosen; each names the other
// so a change to one is a prompt to check its twin.
//
// WHY EXACT-KEY LOOKUP. Go's encoding/json "matches incoming object keys to the
// keys used by Marshal (either the struct field name or its tag), IGNORING
// CASE". Every sniffer here decoded into a json-tagged struct, so {"UNDO":true},
// {"Undo":true}, {"SEARCH":true} and {"EDIT":{...}} all sniffed true. That is
// the exact pattern 491e0f2 identified as defective and replaced in the proxy;
// the daemon was never updated, which is how a defect class regresses.
//
// SEVERITY, STATED HONESTLY. This is NOT privilege escalation: the socket is a
// local UNIX socket with owner-only permissions and SO_PEERCRED peer auth, so a
// caller who can send {"UNDO":true} can already send {"undo":true}. No field on
// protocol.PromptRequest (protocol_version, prompt, workspace, history, reset,
// prompt_kind) case-folds onto a sniffed key either, so the shipped clients
// cannot misroute by accident. It is fixed because the misrouted destination is
// DESTRUCTIVE -- undo restores files from backup over the user's current work --
// because a wire contract that silently accepts ambiguous bodies is a bad
// contract regardless of who can reach it, and because leaving a known-wrong
// pattern fixed in one twin only is how the class comes back.

// requestFields decodes raw's top-level object for exact-key inspection,
// reporting false when it is not a JSON object or when any object in it repeats
// a key. Values stay opaque json.RawMessage and are never examined here, the
// same discipline the proxy's topLevelFields keeps.
//
// A false return sends the request down the prompt path, which is the
// fail-safe direction: the prompt path answers or refuses, and never writes to
// the user's files.
func requestFields(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if hasDuplicateKeys(raw) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// hasDuplicateKeys reports whether raw contains any JSON object with the same
// key twice, at any depth. See proxy/main.go's copy for the full rationale: Go
// resolves duplicates last-wins and silently, so {"undo":null,"undo":true}
// sniffed as an undo with nothing rejecting it. An ambiguous request has no
// single correct interpretation and must not be guessed at, least of all into a
// destructive handler.
func hasDuplicateKeys(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return false // malformed: the decode below reports it
	}
	duplicate, _ := scanJSONValue(dec, tok, 0)
	return duplicate
}

// maxJSONNestingDepth bounds scanJSONValue's recursion. Requests are already
// capped by resolvedMaxRequestBytes, but a capped body can still be pathological
// nesting; exceeding this reads as a duplicate, i.e. refused, the fail-closed
// direction.
const maxJSONNestingDepth = 64

// scanJSONValue consumes the one JSON value whose first token is tok, descending
// into objects and arrays. It reports whether a duplicate key was found, and
// whether the scan itself completed.
func scanJSONValue(dec *json.Decoder, tok json.Token, depth int) (duplicate, ok bool) {
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return false, true // a scalar: nothing to descend into
	}
	if depth >= maxJSONNestingDepth {
		return true, false
	}

	switch delim {
	case '{':
		seen := make(map[string]bool)
		for {
			keyTok, err := dec.Token()
			if err != nil {
				return false, false
			}
			if d, isD := keyTok.(json.Delim); isD && d == '}' {
				return false, true
			}
			key, isString := keyTok.(string)
			if !isString {
				return false, false // unreachable for well-formed JSON
			}
			if seen[key] {
				return true, true
			}
			seen[key] = true

			valTok, err := dec.Token()
			if err != nil {
				return false, false
			}
			if dup, done := scanJSONValue(dec, valTok, depth+1); dup || !done {
				return dup, done
			}
		}
	case '[':
		for {
			elemTok, err := dec.Token()
			if err != nil {
				return false, false
			}
			if d, isD := elemTok.(json.Delim); isD && d == ']' {
				return false, true
			}
			if dup, done := scanJSONValue(dec, elemTok, depth+1); dup || !done {
				return dup, done
			}
		}
	}
	return false, false
}

// hasBoolKey reports whether fields carries key EXACTLY (never a case variant)
// with a non-null boolean value.
//
// The bool requirement preserves the previous sniffers' semantics precisely:
// they decoded into a *bool and tested non-nil, so {"undo":null} fell through to
// the prompt path and must continue to. Presence alone would route a null undo
// into the destructive handler -- a change in the wrong direction.
//
// The null test is explicit and NOT redundant with the Unmarshal below:
// unmarshalling JSON null into a bool is a documented no-op that returns a NIL
// error, so checking the error alone would accept {"undo":null}.
func hasBoolKey(fields map[string]json.RawMessage, key string) bool {
	raw, present := fields[key]
	if !present || isJSONNull(raw) {
		return false
	}
	var v bool
	return json.Unmarshal(raw, &v) == nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// isToolApprovalResponse sniffs whether raw is a protocol.ToolApprovalResponse,
// identified by its "approval" key (no omitempty, see its doc comment).
//
// Unlike every other sniffer here, this one is NOT consulted by serveConn's
// top-level dispatch. A tool approval is only ever read mid-turn, by the agent
// loop, at a point where an approval is the only message that makes sense --
// see readToolApproval. It lives here so the exact-key discipline is applied to
// it identically, and so a future maintainer adding it to the top-level
// dispatch finds the sniffer already written to the house rule rather than
// writing a case-folding one.
//
// A false return at the loop's call site is a DENIAL, not a fall-through: the
// loop asked a yes/no question, and a body that is not recognisably an answer
// is not a yes.
func isToolApprovalResponse(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "approval")
}

// hasObjectKey reports whether fields carries key EXACTLY with a value that
// decodes into target (a non-null JSON object). Mirrors hasBoolKey's reasoning
// for the one sniffed key whose payload is a struct rather than a flag.
func hasObjectKey(fields map[string]json.RawMessage, key string, target any) bool {
	raw, present := fields[key]
	if !present {
		return false
	}
	if isJSONNull(raw) {
		return false
	}
	return json.Unmarshal(raw, target) == nil
}
