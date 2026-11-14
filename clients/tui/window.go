package tui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// EACH TERMINAL WINDOW HAS ITS OWN CHAT (docs/CHAT_STORAGE_SPLIT_DESIGN_2026-11-14.md).
//
// The daemon keeps one chat per client kind, and two terminals open on one folder
// used to share the terminal's: a question typed in one was in the other's
// history. The owner's decision, 2026-11-14: each window gets its own.
//
// A WINDOW IS A PROCESS. runChat makes one id when it starts and the handshake
// carries it on every connection this process makes (connectToDaemon), so the
// daemon can tell this window's connections from another's. It is random, 16 hex
// digits, and never written to disk: a new launch is a new window with an empty
// chat, which is the fresh start the terminal already promises -- and a window
// that was closed leaves a chat nobody can reach, which the daemon sweeps once it
// has sat idle (daemon/chatsession.go).
//
// The one-shot path (`mochiii -prompt`) sets none, and so shares the terminal's
// default chat, which has no history to speak of.

// daemonWindowID is this process's window, "" until runChat sets it.
var daemonWindowID string

// setDaemonWindowID records the id for the rest of the process. Called from
// runChat once, BEFORE the first connection, for the reason setDaemonWorkspaceRoot
// is: a change mid-session would move this window to another chat.
func setDaemonWindowID(id string) { daemonWindowID = id }

// newWindowID is 8 random bytes as 16 lowercase hex digits, the form the daemon
// accepts (protocol.HandshakeRequest.WindowID). If the system's randomness fails
// -- which it does not -- the clock is unique enough for a label.
func newWindowID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
