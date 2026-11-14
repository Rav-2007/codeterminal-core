package tui

import (
	"errors"
	"time"

	"mochiii/protocol"
)

// A LAUNCH STARTS A NEW CHAT.
//
// THE OWNER'S WORDS (2026-11-14): "Every time it is just opening again and
// again. It should be fresh, with a clean prompt section and opening. The
// previous stuff will be saved or cleared before closing; if the user failed to
// close it, then it should be erased."
//
// Until then a relaunch CONTINUED the chat the daemon was holding for the
// workspace (the handshake's PersistedHistory, drawn with a "continuing your
// chat" line), on the reasoning in continuedChatNote that a chat ends only when
// its user ends it. That is reversed here. Only what the user saves is kept
// (history.go), so a chat that was neither saved nor ended on purpose -- the
// terminal window was closed, the machine went down -- is erased by the next
// launch, and what WAS saved is one /resume away.
//
// Two halves, one meaning:
//
//   - a launch erases what the daemon still holds, here (startFresh);
//   - leaving on purpose with an unsaved chat asks first and erases on the way
//     out (quitchat.go), so the chat is not left lying on disk until the next
//     launch.
//
// The first half is the backstop for every exit that cannot ask: closing the
// window sends SIGHUP and a process cannot put a question to a window that is
// going away.
//
// THE CHAT IS ONE PER WORKSPACE AND THE VS CODE PANEL WRITES TO IT TOO
// (chatsync.go). Erasing it here erases it there, and a second Mochiii opened
// in the same folder blanks the first. ctrl+n has always done the same to the
// shared chat, and the daemon has no notion of whose chat it is, so no part of
// this can leave the editor's untouched.

// freshChatTimeout bounds how long a launch, or a quit, waits for the daemon to
// confirm the erase. Neither may hang on a daemon that does not answer.
const freshChatTimeout = 3 * time.Second

var errResetTimeout = errors.New("the daemon did not answer the request to start a new chat")

// requestChatReset asks the daemon to start a new chat -- exactly what
// clearConversation sends (PromptRequest.Reset) -- and waits for its answer. It
// returns the new chat's revision, which a client that keeps up with the shared
// chat needs to stay level (chatsync.go).
//
// resetHistoryOnDaemon (stream.go) is the same exchange for the UI's
// asynchronous /clear; it is kept apart because its failure paths are tied to a
// cancellable context and the channel a Bubble Tea Cmd is blocked on.
func requestChatReset(clientName string) (string, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Close() }() // see daemonSession.Close

	if err := sess.enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Reset:           true,
	}); err != nil {
		return "", err
	}
	var tok protocol.TokenResponse
	if err := sess.dec.Decode(&tok); err != nil {
		return "", err
	}
	if tok.Error != "" {
		return "", errors.New(tok.Error)
	}
	return tok.ChatRevision, nil
}

// withDeadline runs f and returns its answer, or errResetTimeout once d has
// passed. A call that outlives the deadline is left to finish on its own
// goroutine and its answer is dropped; the caller moves on.
func withDeadline(d time.Duration, f func() (string, error)) (string, error) {
	type result struct {
		rev string
		err error
	}
	done := make(chan result, 1)
	go func() {
		rev, err := f()
		done <- result{rev, err}
	}()
	select {
	case r := <-done:
		return r.rev, r.err
	case <-time.After(d):
		return "", errResetTimeout
	}
}

// resetWithin is requestChatReset with a deadline, so neither a launch nor a
// quit can hang on a daemon that does not answer.
func resetWithin(clientName string, d time.Duration) (string, error) {
	return withDeadline(d, func() (string, error) { return requestChatReset(clientName) })
}

// startFresh is what a launch does with the chat the daemon is holding: erase
// it, and hand back an empty one with the new chat's revision.
//
// NOTHING HELD, NOTHING ASKED. A daemon with no chat is not troubled.
//
// IF THE ERASE FAILS, THE CHAT IS SHOWN AS IT WAS (the caller's continuing-chat
// line says so). The daemon would answer from the stored chat whatever the
// screen showed (chatsync.go), so a blank screen over a full chat would be the
// worse lie. The failure costs the user a /clear, not a wrong picture.
func startFresh(clientName string, held []protocol.Turn, rev string) ([]protocol.Turn, string) {
	if len(held) == 0 {
		return held, rev
	}
	newRev, err := resetWithin(clientName, freshChatTimeout)
	if err != nil {
		return held, rev
	}
	if newRev != "" {
		rev = newRev
	}
	return nil, rev
}
