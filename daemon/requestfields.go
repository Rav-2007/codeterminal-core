package main

import (
	"bytes"
	"encoding/json"

	"codeterminal/protocol"
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

// discriminatorKeys are the top-level keys that each SELECT a request type.
// Every sniffer in this file and in status.go tests exactly one of them, and
// every message protocol defines carries exactly one: ApplyEditRequest has
// "edit", UndoRequest "undo", StatusRequest "status", SearchRequest "search",
// ToolApprovalResponse "approval", and PromptRequest none at all -- its absence
// of a discriminator IS its discriminator.
//
// Named here rather than at the sniffers because the rule below is about the
// SET, which no individual sniffer can see. A sniffer knows whether its own key
// is present; only this list knows whether another one is too.
var discriminatorKeys = [...]string{"edit", "undo", "status", "search", "approval"}

// requestFields decodes raw's top-level object for exact-key inspection,
// reporting false when it is not a JSON object, when any object in it repeats a
// key, or when it selects more than one request type. Values stay opaque
// json.RawMessage and are never examined here, the same discipline the proxy's
// topLevelFields keeps.
//
// A false return sends the request down the prompt path, which is the
// fail-safe direction: the prompt path answers or refuses, and never writes to
// the user's files.
//
// TWO DISCRIMINATORS IS THE SAME DEFECT AS ONE KEY TWICE, and this file already
// said so before it checked for it. The rule written above -- "an ambiguous
// request has no single correct interpretation and must not be guessed at,
// least of all into a destructive handler" -- was enforced for duplicate keys
// and for case-folded keys and not for this, which produces the identical
// ambiguity by a different route.
//
// It was resolved by DECLARATION ORDER IN serveConn, which is not a decision
// anyone made. Measured against a workspace holding "ORIGINAL\n":
//
//	{"undo":true,"edit":{...}}                        -> file became "EDITED\n"
//	{"status":true,"edit":{...}}                      -> file became "EDITED\n"
//	{"status":true,"search":true,"undo":true,"edit":…} -> file became "EDITED\n"
//	{"edit":{...},"prompt":"please do nothing"}        -> file became "EDITED\n"
//
// In each of those the client named a second thing and got a write. Whichever
// sniffer serveConn happens to call first wins, so reordering the dispatch
// block -- a refactor with no visible risk -- silently changes which request a
// body means.
//
// SEVERITY, ON THE SAME TERMS AS THE CASE-FOLD DEFECT ABOVE. This is not
// privilege escalation: a caller who can send {"undo":true,"edit":{...}} can
// already send {"edit":{...}} on its own. It is fixed for the reasons that one
// was -- the misrouted destination is DESTRUCTIVE, and a wire contract that
// silently accepts ambiguous bodies is a bad contract regardless of who can
// reach it.
//
// The refusal is deliberately the SAME outcome a duplicate-key body gets rather
// than a new error of its own: both are "this body has no single meaning", one
// rule needs one answer, and a second refusal path is how the two drift apart.
func requestFields(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if hasDuplicateKeys(raw) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	if countDiscriminators(fields) > 1 {
		return nil, false
	}
	return fields, true
}

// countDiscriminators reports how many request types fields selects.
//
// It counts a key only when that key would actually SNIFF -- a present-but-null
// "undo" does not select the undo handler (see hasBoolKey), so it must not
// count as a second selector and turn an otherwise unambiguous apply-edit into
// a refusal. The predicates below are the same ones the sniffers use, called
// through the same helpers, so "would this sniff" has one implementation.
func countDiscriminators(fields map[string]json.RawMessage) int {
	n := 0
	for _, key := range discriminatorKeys {
		if key == "edit" {
			var edit protocol.EditBlockWire
			if hasObjectKey(fields, key, &edit) {
				n++
			}
			continue
		}
		if hasBoolKey(fields, key) {
			n++
		}
	}
	return n
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
