package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"mochiii/protocol"
)

// isHistoryRequest sniffs a protocol.HistoryRequest by its "chats" key, the
// way isSearchRequest sniffs "search". The key is matched exactly
// (requestfields.go).
func isHistoryRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "chats")
}

// handleHistory answers /history: list the chats, show one, resume one, or
// delete one. Only this daemon's own workspace is ever read, whatever the
// request says (HistoryRequest.Workspace is ignored, as SearchRequest's is).
//
// ERRORS SENT TO THE CLIENT NAME NO PATH. The history folder is under the
// user's home directory, and socketSafeError's rule -- no absolute path on the
// wire -- holds here too: the client gets a fixed sentence, the log gets the
// error.
func (s *Server) handleHistory(ctx context.Context, enc *json.Encoder, req protocol.HistoryRequest) {
	reply := func(resp protocol.HistoryResponse) {
		resp.ProtocolVersion = protocol.ProtocolVersion
		_ = enc.Encode(resp) // one-shot reply; a client that left cannot be told anything
	}
	fail := func(what string, err error) {
		s.logger.Printf("history %s: %v", req.Action, err)
		reply(protocol.HistoryResponse{Error: historyClientError(what, err)})
	}
	if s.memory == nil {
		reply(protocol.HistoryResponse{Error: "conversation memory is not available, so there is no history"})
		return
	}
	root, err := historyRoot()
	if err != nil {
		fail("finding the history folder", err)
		return
	}
	archive := newChatArchive(root, s.workspace)

	switch req.Action {
	case protocol.HistoryList:
		entries, err := s.listChats(ctx, archive, req.Spec)
		if err != nil {
			fail("listing saved chats", err)
			return
		}
		reply(protocol.HistoryResponse{Entries: entries})

	case protocol.HistorySave:
		s.historyMu.Lock()
		id, h, pruned, err := s.saveChatLocked(ctx, archive, req.Spec, req.Name)
		s.historyMu.Unlock()
		if err != nil {
			fail("saving this chat", err)
			return
		}
		entry := s.entryFor(id, h, false)
		reply(protocol.HistoryResponse{Entry: &entry, Pruned: pruned})

	case protocol.HistoryShow:
		h, turns, err := archive.load(req.ID)
		if err != nil {
			fail("reading that chat", err)
			return
		}
		entry := s.entryFor(req.ID, h, false)
		reply(protocol.HistoryResponse{Entry: &entry, Turns: turns})

	case protocol.HistoryResume:
		entry, turns, err := s.resumeChat(ctx, archive, req.ID)
		if err != nil {
			fail("resuming that chat", err)
			return
		}
		reply(protocol.HistoryResponse{Entry: &entry, Turns: turns})

	case protocol.HistoryDelete:
		// The link may still name the deleted chat. That is harmless by
		// construction: listChats counts a link as "saved" only while its chat
		// still exists, and a later save replaces a copy that is already gone.
		if err := archive.remove(req.ID); err != nil {
			fail("deleting that chat", err)
			return
		}
		reply(protocol.HistoryResponse{})

	default:
		reply(protocol.HistoryResponse{Error: "unknown history action; use list, save, show, resume or delete"})
	}
}

// historyClientError is what the client is told when an action failed.
func historyClientError(what string, err error) string {
	if errors.Is(err, errArchiveNotFound) {
		return "there is no saved chat with that id -- /history lists them"
	}
	if errors.Is(err, errNothingToSave) {
		return "nothing to save yet -- a chat is saved once it has a question and an answer"
	}
	return what + " failed (see the daemon log)"
}

// listChats returns the current chat, when it has any turns, then every saved
// chat, newest first. The current chat says whether it is saved: it is when the
// link names a saved copy that still exists and holds as many turns as it has.
func (s *Server) listChats(ctx context.Context, archive *chatArchive, spec string) ([]protocol.HistoryEntry, error) {
	var entries []protocol.HistoryEntry
	current, err := s.memory.LoadAllTurns(ctx, s.workspace)
	if err != nil {
		return nil, err
	}
	saved, err := archive.list()
	if err != nil {
		return nil, err
	}
	if len(current) > 0 {
		h, _ := headerFor(current, spec, time.Now())
		link := archive.readLink()
		savedTurns := -1
		for _, c := range saved {
			if c.ID == link.ID {
				h.Name = c.Header.Name
				savedTurns = link.Turns
			}
		}
		entry := s.entryFor("", h, true)
		entry.Ended = "" // still going
		if savedTurns >= 0 {
			entry.SavedAs = link.ID
		}
		entry.Unsaved = savedTurns != len(current)
		entries = append(entries, entry)
	}
	for _, c := range saved {
		entries = append(entries, s.entryFor(c.ID, c.Header, false))
	}
	return entries, nil
}

// entryFor describes one chat for a client, reading its spec's progress now:
// "half done" means the criteria that are STILL unticked, not the ones that
// were when the chat was saved.
func (s *Server) entryFor(id string, h archiveHeader, current bool) protocol.HistoryEntry {
	e := protocol.HistoryEntry{
		ID: id, Current: current, Title: h.displayTitle(), LastPrompt: h.LastPrompt,
		Started: h.Started, Ended: h.Ended, Turns: h.Turns, Incomplete: h.Incomplete, Spec: h.Spec,
	}
	if h.Spec == "" {
		return e
	}
	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return e
	}
	sp, err := loadSpec(realRoot, h.Spec)
	if err != nil {
		return e // a spec since deleted or renamed: named, with no progress
	}
	e.SpecTotal = len(sp.Criteria)
	for _, c := range sp.Criteria {
		if !c.Done {
			e.SpecOpen++
		}
	}
	return e
}

// resumeChat makes a saved chat the current one. NOTHING IS SAVED OR DELETED:
// the chat that was current is replaced (the client warns first when it is not
// saved), and the saved copy stays where it is -- the link records that the
// current chat is that copy, so /history save updates it rather than adding
// another. The chosen chat is read BEFORE anything changes, so an id that does
// not exist changes nothing.
func (s *Server) resumeChat(ctx context.Context, archive *chatArchive, id string) (protocol.HistoryEntry, []protocol.Turn, error) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	h, stored, err := archive.loadStored(id)
	if err != nil {
		return protocol.HistoryEntry{}, nil, err
	}
	if err := s.memory.ReplaceWorkspace(ctx, s.workspace, stored); err != nil {
		return protocol.HistoryEntry{}, nil, err
	}
	turns := make([]protocol.Turn, len(stored))
	for i, t := range stored {
		turns[i] = protocol.Turn{Role: t.Role, Content: t.Content}
	}
	if err := archive.writeLink(id, len(turns)); err != nil {
		// Resumed, but a later save will add a copy instead of updating this
		// one. Not a reason to report the resume as failed -- it did happen.
		s.logger.Printf("history resume: %v", err)
	}
	e := s.entryFor(id, h, true)
	e.SavedAs = id
	return e, turns, nil
}
