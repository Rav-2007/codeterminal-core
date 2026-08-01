package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// P2-2. The four request sniffers decoded into json-tagged structs, so Go's
// case-insensitive tag matching routed {"UNDO":true}, {"Undo":true},
// {"SEARCH":true} and {"EDIT":{...}} as typed requests -- the exact pattern
// 491e0f2 identified and replaced in the proxy, left unfixed in the daemon,
// where the misrouted destination restores files over the user's current work.
//
// Neuter check: restore any sniffer's `var peek struct { Undo *bool
// json:"undo" }` form and the case-variant rows for it fail.
func TestSniffers_MatchKeysExactly(t *testing.T) {
	const editBody = `{"file_path":"a.go","search":"x","replace":"y"}`

	tests := []struct {
		name  string
		raw   string
		sniff func(json.RawMessage) bool
		want  bool
	}{
		// The exact lowercase keys must still route -- this is the shipped
		// clients' path and the reason the sniffers exist at all.
		{"undo exact", `{"undo":true}`, isUndoRequest, true},
		{"search exact", `{"search":true}`, isSearchRequest, true},
		{"status exact", `{"status":true}`, isStatusRequest, true},
		{"edit exact", `{"edit":` + editBody + `}`, isApplyEditRequest, true},

		// Case variants must fall through to the prompt path.
		{"undo UPPERCASE", `{"UNDO":true}`, isUndoRequest, false},
		{"undo MixedCase", `{"Undo":true}`, isUndoRequest, false},
		{"undo uNdO", `{"uNdO":true}`, isUndoRequest, false},
		{"search UPPERCASE", `{"SEARCH":true}`, isSearchRequest, false},
		{"status UPPERCASE", `{"STATUS":true}`, isStatusRequest, false},
		{"edit UPPERCASE", `{"EDIT":` + editBody + `}`, isApplyEditRequest, false},

		// Ambiguous bodies are refused outright rather than resolved last-wins.
		{"undo duplicate null-then-true", `{"undo":null,"undo":true}`, isUndoRequest, false},
		{"undo duplicate true-then-true", `{"undo":true,"undo":true}`, isUndoRequest, false},
		{"search duplicate", `{"search":true,"search":false}`, isSearchRequest, false},
		{"edit duplicate", `{"edit":` + editBody + `,"edit":` + editBody + `}`, isApplyEditRequest, false},

		// Pre-existing semantics that must NOT change: a null or wrongly-typed
		// value falls through to the prompt path, exactly as the *bool /
		// *EditBlockWire form did. Presence alone must never route into undo.
		{"undo null", `{"undo":null}`, isUndoRequest, false},
		{"undo string", `{"undo":"yes"}`, isUndoRequest, false},
		{"undo false still routes", `{"undo":false}`, isUndoRequest, true},
		{"edit null", `{"edit":null}`, isApplyEditRequest, false},

		// A prompt is a prompt.
		{"plain prompt is not an undo", `{"prompt":"hello"}`, isUndoRequest, false},
		{"plain prompt is not an edit", `{"prompt":"hello"}`, isApplyEditRequest, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sniff(json.RawMessage(tt.raw)); got != tt.want {
				if got {
					t.Errorf("%s was accepted as a typed request (want fall-through to the "+
						"prompt path). A body whose meaning depends on which parser reads it "+
						"must never reach a handler, least of all a destructive one", tt.raw)
				} else {
					t.Errorf("%s was NOT routed (want routed) -- the exact-key discipline has "+
						"broken a shipped client's request shape", tt.raw)
				}
			}
		})
	}
}

// No field on PromptRequest may case-fold onto a sniffed key, or an ordinary
// prompt could misroute. Asserted rather than assumed, so adding a field named
// e.g. "Status" to PromptRequest fails here instead of in the field.
func TestSniffers_NoPromptFieldCollidesWithASniffedKey(t *testing.T) {
	sniffed := []string{"edit", "undo", "search", "status"}
	promptFields := []string{"protocol_version", "prompt", "workspace", "history", "reset", "prompt_kind"}

	for _, pf := range promptFields {
		for _, sk := range sniffed {
			if strings.EqualFold(pf, sk) {
				t.Errorf("PromptRequest field %q case-folds onto sniffed key %q -- an ordinary "+
					"prompt can be routed to a typed handler", pf, sk)
			}
		}
	}
}

func TestHasDuplicateKeys_Daemon(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"clean", `{"prompt":"hi","reset":false}`, false},
		{"top-level duplicate", `{"undo":null,"undo":true}`, true},
		{"nested duplicate", `{"edit":{"file_path":"a","file_path":"b"}}`, true},
		{"duplicate in an array element", `{"history":[{"role":"a","role":"b"}]}`, true},
		{"same key in sibling objects is legitimate", `{"a":{"x":1},"b":{"x":2}}`, false},
		{"empty object", `{}`, false},
		{"malformed", `{"a":`, false},
		{"excessive nesting is refused", strings.Repeat("[", 200) + strings.Repeat("]", 200), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDuplicateKeys([]byte(tt.body)); got != tt.want {
				t.Errorf("hasDuplicateKeys(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
