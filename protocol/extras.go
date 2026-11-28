package protocol

// WIRE FIELDS GROW HERE, NOT IN protocol.go (2026-11-27).
//
// The retrieval eval indexes this repository in fixed windows of lines, so a
// line added high in protocol.go moves every window under it -- measured twice
// (workspacetag.go has the story). So the messages that keep gaining fields
// each embed ONE struct defined in this file, written into protocol.go in the
// place of a line that was there before. encoding/json promotes an embedded
// struct's fields, so on the wire they are ordinary keys of the same object:
// nothing about the message's shape says where in the Go source a field lives.

// HistoryExtras is embedded in HistoryResponse.
type HistoryExtras struct {
	// Usage, on HistoryCompact's answer, is what writing the summary cost: a
	// model call like any other, so the client's /usage counts it. Additive.
	Usage *TurnUsage `json:"usage,omitempty"`
	// FoldedSaved, on HistorySave's answer, says the saved copy took the
	// earlier messages the client sent (HistoryRequestExtras.Folded) in place of
	// the summary standing in for them. Additive.
	FoldedSaved bool `json:"folded_saved,omitempty"`
}

// TokenExtras is embedded in TokenResponse.
type TokenExtras struct {
	// Fold, on a message of its own ahead of the answer, says the daemon
	// summarised the oldest part of the conversation instead of dropping it
	// (fold.go). Sent only to a client that declared CapFold. Additive.
	Fold *FoldInfo `json:"fold,omitempty"`
}

// HistoryRequestExtras is embedded in HistoryRequest.
type HistoryRequestExtras struct {
	// Folded, on HistorySave, is the conversation's earlier part as the client
	// still shows it: the messages a fold summarised and the daemon no longer
	// keeps. The saved copy then holds them in place of the summary. Used only
	// for a chat that was never saved and begins with a summary; ignored
	// otherwise. Additive.
	Folded []Turn `json:"folded,omitempty"`
}
