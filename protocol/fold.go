package protocol

// FOLDING THE CONVERSATION, ON THE WIRE (daemon/fold.go has the why).
//
// A conversation sent to the model is capped (daemon/history.go): past twelve
// messages, or 256 KB, the oldest were dropped and the model answered as if
// they had never been said. A daemon that folds writes the part that would be
// dropped into a summary instead, keeps the newest messages word for word, and
// tells the client so -- because the CLIENT sends the history with every
// question, and from then on it must send the summary in place of what it
// stands for. What the client SHOWS does not change: the person can still read
// every message they wrote.

// CapFold: the client takes a Fold and sends the summary from then on. A
// daemon folds only for a client that declared it -- one that did not would
// keep sending the whole conversation, and every later question would be cut
// again.
const CapFold = "fold"

// FeatureFold: this daemon folds for a client that declared CapFold.
const FeatureFold = "fold"

// FoldInfo is one fold -- or, with Skipped, a fold that could not be made, or,
// with only Usage set, the bill for a summary that was written and not used
// (the chat moved on before its question came).
type FoldInfo struct {
	// Summary is the two turns that now stand for the folded part: a user turn
	// that names the conversation and the assistant turn holding the summary.
	// The client puts them first in the history it sends from now on.
	Summary []Turn `json:"summary"`
	// Kept is how many of the turns the client sent with THIS question come
	// after the summary word for word: the newest ones.
	Kept int `json:"kept"`
	// Folded is how many earlier messages this fold put into the summary.
	Folded int `json:"folded"`
	// Revision is the stored chat's revision once it was folded. The client
	// adopts it, so its next question is level with the stored chat.
	Revision string `json:"revision,omitempty"`
	// Usage is what writing the summary cost, so the client's totals hold it.
	Usage *TurnUsage `json:"usage,omitempty"`
	// Pending marks the notice sent BEFORE a summary that is written while the
	// question waits: Folded says how many messages, and nothing else is set.
	// The Fold that follows it is the result.
	Pending bool `json:"pending,omitempty"`
	// Skipped says, in plain words, why the messages this question would have
	// folded were left out instead (a spending limit, the model failing, the
	// summary taking too long). Dropped is how many messages that is. Summary is
	// then empty, and the client keeps sending what it sent before.
	Skipped string `json:"skipped,omitempty"`
	Dropped int    `json:"dropped,omitempty"`
}
