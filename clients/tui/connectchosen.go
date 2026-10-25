package tui

import (
	"fmt"

	"mochiii/protocol"
)

// THE USER'S OWN CHOICE OUTRANKS THE KEY'S DEFAULT (2026-10-25). After a new key
// the daemon goes on routing to the model picked with /model -- its log says
// "reason=user-selected tier" -- so "Ready — prompts go to <the key's default>"
// was false: the owner read "prompts go to minimaxai/minimax-m3.1-flash" while
// every prompt went to nvidia/nemotron-3-ultra-550b-a55b, as the line under the
// prompt said.
//
// In a file of its own, so that connect.go keeps its lines where they were:
// the retrieval eval reads the repository in 40-line windows.

// modelLineFor is connectModelLine for a report shown while chosen -- the model
// picked with /model, or "" -- is in force.
func modelLineFor(r protocol.ConnectResponse, chosen string) string {
	model, chosen := sanitizeText(r.Model), sanitizeText(chosen)
	if model == "" || chosen == "" || chosen == model {
		return connectModelLine(r)
	}
	answered := "answered a test request on"
	if !r.ModelTested {
		answered = "got NO answer to a test request to"
	}
	return fmt.Sprintf("\n\nPrompts still go to %s, the model you chose: /model switches. "+
		"This key %s %s; %d models are available: /model lists them.", chosen, answered, model, r.ModelCount)
}
