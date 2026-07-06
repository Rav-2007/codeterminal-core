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
// a successful handshake. Workspace is optional and additive: when set,
// it's the client's own absolute path for the workspace it expects
// grounding against, sent purely so the daemon can report back whether
// that matches its own configured grounding workspace (see
// GroundingInfo.WorkspaceMismatch below). It never changes what the daemon
// actually retrieves from — that's fixed at daemon startup by the daemon's
// own --workspace flag. Older daemons that don't know this field simply
// ignore it.
type PromptRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	Prompt          string `json:"prompt"`
	Workspace       string `json:"workspace,omitempty"`
}

// TokenResponse is one message in a streamed reply. The daemon sends, in
// order: at most one message carrying Grounding (before any tokens, once
// retrieval has been decided for this request), a sequence of messages
// with Token set, and exactly one final message with Done set to true
// (Error set instead if the stream failed). Grounding is additive: older
// clients that don't know this field simply ignore it.
type TokenResponse struct {
	ProtocolVersion int            `json:"protocol_version"`
	Token           string         `json:"token,omitempty"`
	Done            bool           `json:"done"`
	Error           string         `json:"error,omitempty"`
	Grounding       *GroundingInfo `json:"grounding,omitempty"`
}

// GroundingInfo reports whether the daemon augmented THIS request with
// retrieved local context, and from where. It's purely a report of a
// decision already made server-side (see daemon/context.go's
// gatherContext) — it never carries retrieved content itself; that stays
// confined to the existing <retrieved_context> user-role delimiter.
type GroundingInfo struct {
	Grounded  bool   `json:"grounded"`
	Workspace string `json:"workspace,omitempty"` // this daemon's actual grounding workspace (absolute path)
	Reason    string `json:"reason,omitempty"`    // set when !Grounded, e.g. "no index found"
	Chunks    int    `json:"chunks,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// WorkspaceMismatch is set when PromptRequest.Workspace was non-empty
	// and didn't match Workspace above — the client expected grounding
	// against a different repo than this daemon instance actually uses.
	WorkspaceMismatch bool `json:"workspace_mismatch,omitempty"`
}

// LockFile is the JSON document the daemon writes on startup so clients can
// discover its socket path without guessing.
type LockFile struct {
	SocketPath string `json:"socket_path"`
	PID        int    `json:"pid"`
}
