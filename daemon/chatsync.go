package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mochiii/protocol"
)

// THE CURRENT CHAT IS SHARED, SO EVERY CLIENT HAS TO BE ABLE TO KEEP UP WITH IT.
//
// Conversation memory is one chat per workspace (memory.go), and one daemon
// serves every client of a workspace -- so a terminal and an editor open on the
// same folder are writing to the same chat. Until this file existed each client
// read that chat ONCE, at startup, and from then on showed and sent only what
// it had itself been part of. FOUND 2026-10-08 with both clients open: a
// question asked in the editor was missing from the terminal's screen, and from
// the history the terminal sent with its next question, so the model answering
// there had never heard of it -- while the stored chat held both clients' turns
// interleaved, which is what either client then showed after a restart.
//
// A REVISION says how far into the stored chat a client has seen. It is
// epoch.generation.last:
//
//   - last is the id of the newest stored turn the client was given. Ids only
//     grow within a workspace's chat, so "the turns after last" is exactly what
//     the client is missing while the chat has only been appended to.
//   - generation counts the times this daemon replaced the chat wholesale --
//     ctrl+n, /history resume, /compact. After one, "the turns after last" means
//     nothing (the rows were deleted and SQLite may reuse their ids), so the
//     client is sent the whole chat instead.
//   - epoch is random per daemon process, because generation lives in memory: a
//     revision from a daemon that has since restarted must never be mistaken for
//     one of this daemon's.
//
// THE ORDER OF READS IS THE CORRECTNESS ARGUMENT. Writers change the stored chat
// and only then, still under historyMu, bump the generation. A lock-free reader
// reads the generation FIRST and the turns second, and takes last from the same
// query as the turns. So a revision can understate what its turns contain (a
// replace landed between the two reads: the client is later sent the whole chat
// again, which is only redundant) but can never overstate it: a client is never
// told it has a turn it was not given. The reverse order would let a replace
// slip between the reads and leave a client appending the new chat to the old.
//
// Nothing here takes historyMu to READ. /compact holds it across a model call,
// and a client checking whether the chat moved -- which it does whenever its
// window gets focus -- must not hang for the length of someone else's summary.

// chatEpoch tells this daemon process's revisions apart from any other's.
var chatEpoch = newChatEpoch()

func newChatEpoch() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Not a secret, only a process label; the clock is unique enough.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

type chatRevision struct {
	epoch string
	gen   uint64
	last  int64
}

func (r chatRevision) String() string {
	return fmt.Sprintf("%s.%d.%d", r.epoch, r.gen, r.last)
}

// parseChatRevision reads a revision a client sent back. A client never builds
// one, so anything malformed is simply not this daemon's -- the caller treats it
// as "has seen nothing" and sends the whole chat.
func parseChatRevision(s string) (chatRevision, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] == "" {
		return chatRevision{}, false
	}
	gen, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return chatRevision{}, false
	}
	last, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || last < 0 {
		return chatRevision{}, false
	}
	return chatRevision{epoch: parts[0], gen: gen, last: last}, true
}

// chatRevisionNow is the current chat's revision, read without historyMu. It is
// what a request is compared against; anything a client is GIVEN carries the
// revision read with its turns instead (loadPersistedHistoryAt, currentChat).
func (s *Server) chatRevisionNow(ctx context.Context) (string, error) {
	gen := s.chatGen.Load()
	last, err := s.memory.lastTurnID(ctx, s.workspace)
	if err != nil {
		return "", err
	}
	return chatRevision{epoch: chatEpoch, gen: gen, last: last}.String(), nil
}

// chatRevisionLocked is the revision while the caller holds historyMu, so no
// write can land between the two reads.
func (s *Server) chatRevisionLocked(ctx context.Context) string {
	rev, err := s.chatRevisionNow(ctx)
	if err != nil {
		s.logger.Printf("chat revision: %v", err)
		return ""
	}
	return rev
}

// chatReplacedLocked records that the stored chat was just replaced or cleared.
// Caller holds historyMu and has finished the write: bumping first would let a
// reader pair the new generation with the old turns.
func (s *Server) chatReplacedLocked(ctx context.Context) string {
	s.chatGen.Add(1)
	return s.chatRevisionLocked(ctx)
}

// historyForTurn is the conversation a turn is answered from: what the client
// sent, unless the client's copy is behind the stored chat, in which case the
// stored chat. Reports which it chose.
//
// THE CLIENT'S COPY IS PREFERRED WHEN IT IS CURRENT, deliberately: it carries
// what the user typed rather than the expanded prompt a slash command sent, and
// it is what every client has always sent. The stored chat is used only when the
// client's copy is known to be missing something, which is precisely the case
// where sending it would ask the model to continue a conversation it was shown
// half of.
func (s *Server) historyForTurn(ctx context.Context, req protocol.PromptRequest) ([]protocol.Turn, bool) {
	if req.ChatRevision == "" || s.memory == nil {
		return req.History, false
	}
	now, err := s.chatRevisionNow(ctx)
	if err != nil {
		s.logger.Printf("chat revision: %v; answering from the history the client sent", err)
		return req.History, false
	}
	if now == req.ChatRevision {
		return req.History, false
	}
	stored, _ := s.loadPersistedHistoryAt(ctx)
	return stored, true
}

// markChatRevision puts a finished turn's chat revision on its Done: after is
// the revision once the turn was saved, before the one just ahead of it. The
// client is level with the stored chat only if before is the revision its
// request was built from; otherwise another client wrote in between, and the
// client is told to fetch the chat again.
func markChatRevision(done *protocol.TokenResponse, sent, before, after string) {
	if after == "" {
		return
	}
	done.ChatRevision = after
	done.ChatBehind = sent != "" && sent != before
}

// currentChat answers HistoryCurrent: what a client that shows the chat as of
// since is missing, and the revision that brings it level.
func (s *Server) currentChat(ctx context.Context, since string) (protocol.HistoryResponse, error) {
	gen := s.chatGen.Load() // before the turns -- see the note at the top of this file
	var after int64
	appendTo := false
	if r, ok := parseChatRevision(since); ok && r.epoch == chatEpoch && r.gen == gen {
		after, appendTo = r.last, true
	}
	stored, last, err := s.memory.turnsAfter(ctx, s.workspace, after)
	if err != nil {
		return protocol.HistoryResponse{}, err
	}
	turns := make([]protocol.Turn, 0, len(stored))
	for _, t := range stored {
		// The same filter hydration applies (loadPersistedHistory): a row
		// written before the write-side guard existed is not shown either.
		if pt := (protocol.Turn{Role: t.Role, Content: t.Content}); validTurn(pt) {
			turns = append(turns, pt)
		}
	}
	return protocol.HistoryResponse{
		ChatRevision: chatRevision{epoch: chatEpoch, gen: gen, last: last}.String(),
		Turns:        turns,
		Append:       appendTo,
	}, nil
}
