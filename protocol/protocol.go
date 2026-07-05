// Package protocol defines the wire messages exchanged between the
// CodeTerminal CLI client and the local daemon over a Unix domain socket.
//
// Framing: newline-delimited JSON. Each message is one JSON object followed
// by "\n". This keeps both sides to stdlib bufio/json with no extra
// dependency, and supports streaming naturally (write-and-flush per line).
package protocol

import (
	"os"
	"path/filepath"
)

// ProtocolVersion is the version implemented by this build. Bump it whenever
// a message shape changes in a way that isn't backward compatible.
const ProtocolVersion = 1

// Discovery convention shared by the daemon (which creates these paths) and
// every client (which must derive the identical paths to find it). Defined
// once here so the two sides can't drift out of sync.
const (
	serviceDirName = "codeterminal"
	socketFileName = "daemon.sock"
	lockFileName   = "daemon.lock"
)

// RuntimeDir returns the per-user directory used for the daemon's socket and
// lockfile: $XDG_RUNTIME_DIR if set, otherwise the OS temp dir.
func RuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}

// SocketDir returns the directory holding the daemon's socket and lockfile,
// creating it (owner-only) if it doesn't already exist.
func SocketDir() (string, error) {
	dir := filepath.Join(RuntimeDir(), serviceDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// SocketPath and LockPath return the conventional paths for the daemon's
// Unix domain socket and lockfile under SocketDir(), without creating
// anything on disk.
func SocketPath() string { return filepath.Join(RuntimeDir(), serviceDirName, socketFileName) }
func LockPath() string   { return filepath.Join(RuntimeDir(), serviceDirName, lockFileName) }

// HandshakeRequest is the first message a client sends after connecting.
type HandshakeRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	ClientName      string `json:"client_name"`
}

// HandshakeResponse is the daemon's reply to a HandshakeRequest. If Ok is
// false, the daemon closes the connection immediately after sending this;
// the client must not send further messages.
type HandshakeResponse struct {
	ProtocolVersion int    `json:"protocol_version"`
	Ok              bool   `json:"ok"`
	Error           string `json:"error,omitempty"`
	DaemonVersion   string `json:"daemon_version,omitempty"`
}

// PromptRequest carries a single user prompt. Sent by the client only after
// a successful handshake.
type PromptRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	Prompt          string `json:"prompt"`
}

// TokenResponse is one chunk of a streamed reply. The daemon sends a
// sequence of these with Token set, followed by exactly one final message
// with Done set to true (Error set instead if the stream failed).
type TokenResponse struct {
	ProtocolVersion int    `json:"protocol_version"`
	Token           string `json:"token,omitempty"`
	Done            bool   `json:"done"`
	Error           string `json:"error,omitempty"`
}

// LockFile is the JSON document the daemon writes on startup so clients can
// discover its socket path without guessing.
type LockFile struct {
	SocketPath string `json:"socket_path"`
	PID        int    `json:"pid"`
}
