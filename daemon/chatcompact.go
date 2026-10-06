package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mochiii/protocol"
)

// /compact, AS A SUMMARY. The clients' /compact used to drop all but the last
// eight turns from what they sent the model; on a short chat it said "already
// compact" and on a long one it threw context away with nothing in its place.
// This folds the older part of the chat into a summary written by the model,
// keeps the most recent exchanges word for word, and makes that the current
// chat -- the way Claude Code compacts.
//
// NOTHING IS LOST. The whole chat is saved to history first (saveChatLocked),
// and the link to that saved copy is then cleared, so the compacted chat is a
// NEW chat: saving it later adds an entry instead of overwriting the full one.

const (
	// compactKeepRecent turns stay verbatim after the summary; the cut is moved
	// back to a user turn so the kept part starts where an exchange starts.
	compactKeepRecent = 4
	// compactMinTurns: below this there is nothing worth summarising.
	compactMinTurns = 6
	// compactInputChars bounds what is sent to be summarised. The newest part
	// of the older turns is kept when it must be cut; it is closest to now.
	compactInputChars = 60_000
)

var errNothingToCompact = errors.New("nothing to compact yet")

const compactSystemPrompt = "You compress a conversation between a user and a coding assistant so it can " +
	"continue later from your summary alone. Write a concise summary, at most 250 words, as short bullet " +
	"points: the user's goals, decisions made, facts established (file names, functions, commands, numbers, " +
	"errors), what was done, and what is still open. Keep names and numbers exact. Do not invent anything, " +
	"do not give advice, and do not address the user."

func (s *Server) compactChat(ctx context.Context, archive *chatArchive, spec string) (protocol.HistoryEntry, []protocol.Turn, int, error) {
	// Held throughout, model call included: an exchange finishing meanwhile must
	// not be appended to a chat that is about to be replaced. The client holds
	// its input while this runs, and the call is one short completion.
	s.historyMu.Lock()
	defer s.historyMu.Unlock()

	turns, err := s.memory.LoadAllTurns(ctx, s.workspace)
	if err != nil {
		return protocol.HistoryEntry{}, nil, 0, err
	}
	cut := len(turns) - compactKeepRecent
	for cut > 0 && turns[cut].Role != "user" {
		cut--
	}
	if len(turns) < compactMinTurns || cut < 2 {
		return protocol.HistoryEntry{}, nil, 0, fmt.Errorf("%w: this chat has %d messages, and /compact summarises once there are more than %d",
			errNothingToCompact, len(turns), compactMinTurns-1)
	}

	id, h, _, err := s.saveChatLocked(ctx, archive, spec, "")
	if err != nil {
		return protocol.HistoryEntry{}, nil, 0, fmt.Errorf("saving the full chat before compacting: %w", err)
	}

	summary, err := s.summarizeTurns(ctx, turns[:cut])
	if err != nil {
		return protocol.HistoryEntry{}, nil, 0, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	title := h.displayTitle()
	compacted := []storedTurn{
		{Role: "user", CreatedAt: now, Content: "Continuing: " + title + "\n\n(The earlier part of this conversation was " +
			"summarised by /compact. The full chat is saved in history.)"},
		{Role: "assistant", CreatedAt: now, Content: "Summary of the conversation so far:\n\n" + summary},
	}
	compacted = append(compacted, turns[cut:]...)
	if err := s.memory.ReplaceWorkspace(ctx, s.workspace, compacted); err != nil {
		return protocol.HistoryEntry{}, nil, 0, fmt.Errorf("replacing the chat with its summary: %w", err)
	}
	archive.clearLink()

	out := make([]protocol.Turn, len(compacted))
	for i, t := range compacted {
		out[i] = protocol.Turn{Role: t.Role, Content: t.Content}
	}
	return s.entryFor(id, h, false), out, cut, nil
}

// summarizeTurns asks the configured default model for the summary, through
// the same provider path and retry policy as an ordinary turn.
func (s *Server) summarizeTurns(ctx context.Context, turns []storedTurn) (string, error) {
	var b strings.Builder
	for _, t := range turns {
		who := "User"
		if t.Role == "assistant" {
			who = "Assistant"
		}
		fmt.Fprintf(&b, "%s: %s\n\n", who, strings.TrimSpace(t.Content))
	}
	transcript := b.String()
	if len(transcript) > compactInputChars {
		transcript = "(earliest part omitted)\n\n" + transcript[len(transcript)-compactInputChars:]
	}

	cfg := s.tierConfig()
	model := s.modelOverride
	if model == "" {
		model = cfg.ResolvedSlug()
	}
	key, base := s.credentials()
	var out strings.Builder
	_, err := streamWithRetry(ctx, base, key, model,
		[]chatMessage{
			{Role: "system", Content: compactSystemPrompt},
			{Role: "user", Content: "Summarise this conversation:\n\n" + transcript},
		},
		nil, cfg.routingFor(cfg.DefaultTier),
		func(tok string) error { out.WriteString(tok); return nil },
		nil, nil, nil, s.logger)
	if err != nil {
		return "", fmt.Errorf("the model could not write the summary: %w", err)
	}
	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", errors.New("the model returned an empty summary")
	}
	return summary, nil
}
