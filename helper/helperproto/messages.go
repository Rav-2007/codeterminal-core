// Package helperproto defines the wire messages exchanged between the
// CodeTerminal daemon and its embedder helper subprocess, plus the
// convention for locating the helper's socket.
//
// Framing: one JSON object per Unix domain socket connection. The daemon
// dials, encodes exactly one Request, reads exactly one Response, and
// closes the connection — there is no persistent multiplexed session, which
// keeps both sides simple since embedding calls are already batched
// (Request.Texts is a slice).
//
// This package must never depend on anything that could grow a CGO build
// tag: the daemon imports it directly, and the daemon must stay pure Go
// even after the helper itself gains a CGO ONNX Runtime binding in a later
// step.
package helperproto

import (
	"fmt"
	"path/filepath"

	"codeterminal/protocol"
)

// Request is sent by the daemon to the helper. Method selects the
// operation; Texts is only meaningful for "embed".
type Request struct {
	Method string   `json:"method"` // "health" or "embed"
	Texts  []string `json:"texts,omitempty"`
}

// Response is the helper's reply. OK is false only when Error is set.
type Response struct {
	OK      bool        `json:"ok"`
	Vectors [][]float32 `json:"vectors,omitempty"`
	Error   string      `json:"error,omitempty"`
}

const (
	MethodHealth = "health"
	MethodEmbed  = "embed"
)

// MaxSequenceLength bounds tokenized input length for the embedding model.
//
// IT LIVES HERE, IN THE SHARED PACKAGE, because it is not an implementation
// detail of the helper: it changes what a vector MEANS, so the daemon has to
// fold it into the embedder identity it stamps into an index (see
// bgeEmbedderID). Two indexes built with different caps hold vectors of
// genuinely different things, and the stamp is the only thing standing between
// a user and silently querying one with the other.
//
// 512, WHICH IS BGE's OWN model_max_length, and the number was measured rather
// than chosen. It was 256, under a comment asserting that "code chunks (~40
// lines) rarely need anywhere close to" the model's limit. Run over 1,197 real
// 40-line chunks from this repository with the actual BGE tokenizer, the median
// chunk is 467 tokens and 94% exceed 256 -- so the assertion was backwards, and
// roughly the back half of nearly every chunk was being dropped by
// truncateKeepingFinalToken before it was ever embedded. Silently: truncation
// preserves [SEP], so nothing errors and nothing is logged.
//
// What that cost, measured end to end: with 40-line windows on a 30-line stride,
// 28.3% of all source lines (4,557 of 16,083, across 50 real files) fell inside
// NO chunk's embedded prefix -- unreachable by vector search at any k, in an
// index that reported itself healthy. At 512 that is 0.4%.
//
// The price is paid at INDEX time only: 37ms -> 94ms per chunk (2.54x, measured
// on this machine over real chunks at the production batch size of 40). Query
// embedding is unaffected, because Embed sizes its tensor to the batch's actual
// longest input and a search query is nowhere near either cap.
const MaxSequenceLength = 512

// SocketPath returns the Unix domain socket path the embedder helper spawned
// by the daemon process daemonPID should bind. It lives in the same runtime
// directory as the daemon's own client-facing socket (protocol.SocketDir),
// and is scoped by daemonPID so a socket left behind by an unrelated or
// previous daemon process can never collide with it.
func SocketPath(daemonPID int) (string, error) {
	dir, err := protocol.SocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("embedder-helper-%d.sock", daemonPID)), nil
}
