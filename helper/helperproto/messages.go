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
